package activesync

import (
	"strings"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// calendarCollectionPrefix tags a Sync CollectionId as a calendar collection.
// FolderSync emits the calendar folder's ServerId as this prefix plus the
// canonical folder id; the Sync handler routes on the prefix (a mail folder's
// ServerId is its bare name, so the two namespaces never collide) and strips it
// to recover the folder id for the CalendarSource. Keeping calendar on its own
// Sync path leaves the mail machinery (journal cursor, blob-key ServerIds)
// untouched — the two data classes do not share a sync model.
const calendarCollectionPrefix = "cal:"

// CalendarCollectionID tags a canonical calendar folder id as an EAS calendar
// collection ServerId. FolderSync adapters call it so the Sync router recognizes
// the collection as a calendar and routes it to the CalendarSource path; the
// prefix convention stays encapsulated here rather than hard-coded in adapters.
func CalendarCollectionID(folderID string) string {
	return calendarCollectionPrefix + folderID
}

// CalendarMutator applies a mobile client's calendar up-sync changes to the
// canonical collaboration store — the one store EWS/CalDAV/JMAP/webmail read, so
// an event created, edited or deleted on a phone converges with them. CreateItem
// returns the new event's stable server id (echoed to the client so it learns
// the permanent id for its temporary ClientId). A change to an item already gone
// is not an error; only a real store failure returns one.
type CalendarMutator interface {
	CreateItem(email, folderID string, it CalendarItem) (serverID string, err error)
	UpdateItem(email, folderID, serverID string, it CalendarItem) error
	DeleteItem(email, folderID, serverID string) error
}

// calCommand is one calendar change to emit: an Add/Change carries the event, a
// Delete carries only the server id.
type calCommand struct {
	op   string // "Add", "Change", "Delete"
	item CalendarItem
	id   string // for Delete
}

// calClientResponse is one entry in the calendar Sync Responses block. An Add is
// always echoed (the client needs the assigned ServerId for its ClientId); a
// Change/Delete is echoed only on failure (success is acknowledged by the
// advanced SyncKey).
type calClientResponse struct {
	op       string // "Add", "Change", "Delete"
	clientID string // for Add
	serverID string
	status   string
}

// handleCalendarSync answers the Sync command for a calendar collection. SyncKey
// 0 primes (returns key 1, empty cursor); a real sync enumerates the folder's
// events and diffs them against the cursor (a compact serverId->etag digest of
// the last-synced state), emitting Adds for new events, Changes for ones whose
// ETag advanced and Deletes for ones gone. A calendar has no change journal, so
// each sync re-enumerates and diffs rather than reading a feed. A SyncKey that
// is not the last one issued is rejected with Status 3, forcing a fresh sync.
func (s *Server) handleCalendarSync(ctx *Context, collection *wbxml.Element, collectionID, folderID, reqKey string, window int, deviceID string) ([]byte, error) {
	if reqKey == "0" {
		if err := s.sync.PutSyncState(ctx.Email, collectionID, deviceID, "1|"); err != nil {
			return nil, err
		}
		return marshalCalendarSync(collectionID, syncStatusSuccess, "1", nil, nil, false)
	}

	stored, err := s.sync.GetSyncState(ctx.Email, collectionID, deviceID)
	if err != nil || stored == "" {
		return marshalCalendarSync(collectionID, syncStatusInvalidKey, "", nil, nil, false)
	}
	lastKey, cursor, ok := strings.Cut(stored, "|")
	if !ok || reqKey != lastKey {
		return marshalCalendarSync(collectionID, syncStatusInvalidKey, "", nil, nil, false)
	}

	// Apply the client's up-sync changes (Add/Change/Delete) first, then reconcile
	// the cursor with them so the client's own writes are not echoed back as
	// server-side changes in this same response.
	responses, touched, deleted := s.applyCalendarCommands(ctx.Email, folderID, collection)

	items, err := s.calendar.ListItems(ctx.Email, folderID)
	if err != nil {
		return nil, err
	}
	prev := decodeCursor(cursor)
	for _, it := range items {
		if touched[it.ServerID] {
			prev[it.ServerID] = it.ETag
		}
	}
	for _, id := range deleted {
		delete(prev, id)
	}
	cmds, nextCursor, more := diffCalendar(prev, items, window)
	nextKey := bumpKey(reqKey)
	if err := s.sync.PutSyncState(ctx.Email, collectionID, deviceID, nextKey+"|"+nextCursor); err != nil {
		return nil, err
	}
	return marshalCalendarSync(collectionID, syncStatusSuccess, nextKey, responses, cmds, more)
}

// applyCalendarCommands applies the client's calendar up-sync Commands to the
// canonical store and reports them: the Responses to echo (every Add maps its
// ClientId to the assigned ServerId; a Change/Delete only on failure), the set
// of server ids the client created or changed, and the ids it deleted. The
// caller reconciles the sync cursor with these so they are not re-emitted.
func (s *Server) applyCalendarCommands(email, folderID string, collection *wbxml.Element) (responses []calClientResponse, touched map[string]bool, deleted []string) {
	cmds := collection.Sub("Commands")
	if cmds == nil || s.calMutator == nil {
		return nil, nil, nil
	}
	touched = map[string]bool{}
	for _, c := range cmds.Children {
		switch c.Name {
		case "Add":
			clientID := textOf(c.Sub("ClientId"))
			serverID, err := s.calMutator.CreateItem(email, folderID, calendarItemFromAppData(c.Sub("ApplicationData")))
			status := syncStatusSuccess
			if err != nil {
				status, serverID = syncStatusProtocolError, ""
			} else {
				touched[serverID] = true
			}
			responses = append(responses, calClientResponse{op: "Add", clientID: clientID, serverID: serverID, status: status})
		case "Change":
			serverID := textOf(c.Sub("ServerId"))
			if serverID == "" {
				continue
			}
			if err := s.calMutator.UpdateItem(email, folderID, serverID, calendarItemFromAppData(c.Sub("ApplicationData"))); err != nil {
				responses = append(responses, calClientResponse{op: "Change", serverID: serverID, status: syncStatusProtocolError})
			} else {
				touched[serverID] = true
			}
		case "Delete":
			serverID := textOf(c.Sub("ServerId"))
			if serverID == "" {
				continue
			}
			if err := s.calMutator.DeleteItem(email, folderID, serverID); err != nil {
				responses = append(responses, calClientResponse{op: "Delete", serverID: serverID, status: syncStatusProtocolError})
			} else {
				deleted = append(deleted, serverID)
			}
		}
	}
	return responses, touched, deleted
}

// diffCalendar compares the current event set against the previous synced state
// (serverId->etag) and returns the next window of calendar commands, the
// advanced cursor and whether more remain. It is a thin projection over the
// shared diffCollab engine: the diff/window/cursor logic is identical for every
// journal-less class, so calendar only maps each neutral op back to a calCommand
// carrying the event (Add/Change) or the gone ServerId (Delete).
func diffCalendar(prev map[string]string, items []CalendarItem, window int) (cmds []calCommand, nextCursor string, more bool) {
	entries := make([]collabEntry, len(items))
	for i, it := range items {
		entries[i] = collabEntry{serverID: it.ServerID, etag: it.ETag}
	}
	ops, nextCursor, more := diffCollab(prev, entries, window)
	cmds = make([]calCommand, len(ops))
	for i, o := range ops {
		if o.op == "Delete" {
			cmds[i] = calCommand{op: "Delete", id: o.id}
		} else {
			cmds[i] = calCommand{op: o.op, item: items[o.idx]}
		}
	}
	return cmds, nextCursor, more
}

// marshalCalendarSync builds a Sync response for a calendar collection: the
// SyncKey/CollectionId/Status envelope (shared AirSync page 0), an optional
// MoreAvailable, and the Commands block of calendar Add/Change/Delete. A
// non-success status resets the SyncKey to "0" so the client restarts.
func marshalCalendarSync(collectionID, status, syncKey string, responses []calClientResponse, cmds []calCommand, more bool) ([]byte, error) {
	if status != syncStatusSuccess {
		syncKey = "0"
	}
	collection := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Collection", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: collectionID},
		{Page: wbxml.PageAirSync, Name: "Status", Text: status},
	}}
	if len(responses) > 0 {
		block := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Responses"}
		for _, r := range responses {
			block.Children = append(block.Children, encodeCalClientResponse(r))
		}
		collection.Children = append(collection.Children, block)
	}
	if more {
		collection.Children = append(collection.Children, &wbxml.Element{Page: wbxml.PageAirSync, Name: "MoreAvailable"})
	}
	if len(cmds) > 0 {
		commands := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Commands"}
		for _, c := range cmds {
			commands.Children = append(commands.Children, encodeCalCommand(c))
		}
		collection.Children = append(collection.Children, commands)
	}
	root := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Sync", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "Collections", Children: []*wbxml.Element{collection}},
	}}
	return wbxml.Marshal(root)
}

// encodeCalCommand encodes one calendar change: a Delete carries only the
// ServerId, while Add/Change carry the ServerId and the event's ApplicationData.
func encodeCalCommand(c calCommand) *wbxml.Element {
	if c.op == "Delete" {
		return &wbxml.Element{Page: wbxml.PageAirSync, Name: "Delete", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSync, Name: "ServerId", Text: c.id},
		}}
	}
	return &wbxml.Element{Page: wbxml.PageAirSync, Name: c.op, Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "ServerId", Text: c.item.ServerID},
		{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: calendarAppData(c.item)},
	}}
}

// encodeCalClientResponse encodes one Responses entry for an applied client
// command: an Add carries ClientId, the assigned ServerId and Status; a
// Change/Delete carries the ServerId and its failure Status.
func encodeCalClientResponse(r calClientResponse) *wbxml.Element {
	var children []*wbxml.Element
	if r.op == "Add" && r.clientID != "" {
		children = append(children, &wbxml.Element{Page: wbxml.PageAirSync, Name: "ClientId", Text: r.clientID})
	}
	if r.serverID != "" {
		children = append(children, &wbxml.Element{Page: wbxml.PageAirSync, Name: "ServerId", Text: r.serverID})
	}
	children = append(children, &wbxml.Element{Page: wbxml.PageAirSync, Name: "Status", Text: r.status})
	return &wbxml.Element{Page: wbxml.PageAirSync, Name: r.op, Children: children}
}
