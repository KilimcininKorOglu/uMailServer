package activesync

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"sort"
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

// calCommand is one calendar change to emit: an Add/Change carries the event, a
// Delete carries only the server id.
type calCommand struct {
	op   string // "Add", "Change", "Delete"
	item CalendarItem
	id   string // for Delete
}

// handleCalendarSync answers the Sync command for a calendar collection. SyncKey
// 0 primes (returns key 1, empty cursor); a real sync enumerates the folder's
// events and diffs them against the cursor (a compact serverId->etag digest of
// the last-synced state), emitting Adds for new events, Changes for ones whose
// ETag advanced and Deletes for ones gone. A calendar has no change journal, so
// each sync re-enumerates and diffs rather than reading a feed. A SyncKey that
// is not the last one issued is rejected with Status 3, forcing a fresh sync.
func (s *Server) handleCalendarSync(ctx *Context, collectionID, folderID, reqKey string, window int, deviceID string) ([]byte, error) {
	if reqKey == "0" {
		if err := s.sync.PutSyncState(ctx.Email, collectionID, deviceID, "1|"); err != nil {
			return nil, err
		}
		return marshalCalendarSync(collectionID, syncStatusSuccess, "1", nil, false)
	}

	stored, err := s.sync.GetSyncState(ctx.Email, collectionID, deviceID)
	if err != nil || stored == "" {
		return marshalCalendarSync(collectionID, syncStatusInvalidKey, "", nil, false)
	}
	lastKey, cursor, ok := strings.Cut(stored, "|")
	if !ok || reqKey != lastKey {
		return marshalCalendarSync(collectionID, syncStatusInvalidKey, "", nil, false)
	}

	items, err := s.calendar.ListItems(ctx.Email, folderID)
	if err != nil {
		return nil, err
	}
	cmds, nextCursor, more := diffCalendar(decodeCalCursor(cursor), items, window)
	nextKey := bumpKey(reqKey)
	if err := s.sync.PutSyncState(ctx.Email, collectionID, deviceID, nextKey+"|"+nextCursor); err != nil {
		return nil, err
	}
	return marshalCalendarSync(collectionID, syncStatusSuccess, nextKey, cmds, more)
}

// diffCalendar compares the current event set against the previous synced state
// (serverId->etag) and returns the next window of changes, the advanced cursor
// reflecting only the emitted changes, and whether more remain. Carrying the
// emitted changes into the cursor lets a later sync resume the remainder, so a
// large folder drains across several syncs and still converges.
func diffCalendar(prev map[string]string, items []CalendarItem, window int) (cmds []calCommand, nextCursor string, more bool) {
	seen := make(map[string]bool, len(items))
	var all []calCommand
	for _, it := range items {
		seen[it.ServerID] = true
		etag, known := prev[it.ServerID]
		switch {
		case !known:
			all = append(all, calCommand{op: "Add", item: it})
		case etag != it.ETag:
			all = append(all, calCommand{op: "Change", item: it})
		}
	}
	for id := range prev {
		if !seen[id] {
			all = append(all, calCommand{op: "Delete", id: id})
		}
	}
	// Stable order so a windowed drain is deterministic across syncs.
	sort.Slice(all, func(i, j int) bool { return calCommandKey(all[i]) < calCommandKey(all[j]) })

	end := len(all)
	if window > 0 && window < end {
		end = window
	}
	emitted := all[:end]
	more = end < len(all)

	next := make(map[string]string, len(prev))
	maps.Copy(next, prev)
	for _, c := range emitted {
		if c.op == "Delete" {
			delete(next, c.id)
		} else {
			next[c.item.ServerID] = c.item.ETag
		}
	}
	return emitted, encodeCalCursor(next), more
}

// calCommandKey is the sort key for a calendar command (its item or delete id).
func calCommandKey(c calCommand) string {
	if c.op == "Delete" {
		return c.id
	}
	return c.item.ServerID
}

// encodeCalCursor serializes the synced-state digest into the opaque watermark
// (base64 JSON, free of the "|" the watermark splits on); an empty map is "".
func encodeCalCursor(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decodeCalCursor reverses encodeCalCursor, returning an empty map for the
// empty or malformed cursor (a malformed cursor degrades to a full re-add,
// which is safe).
func decodeCalCursor(s string) map[string]string {
	if s == "" {
		return map[string]string{}
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return map[string]string{}
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]string{}
	}
	return m
}

// marshalCalendarSync builds a Sync response for a calendar collection: the
// SyncKey/CollectionId/Status envelope (shared AirSync page 0), an optional
// MoreAvailable, and the Commands block of calendar Add/Change/Delete. A
// non-success status resets the SyncKey to "0" so the client restarts.
func marshalCalendarSync(collectionID, status, syncKey string, cmds []calCommand, more bool) ([]byte, error) {
	if status != syncStatusSuccess {
		syncKey = "0"
	}
	collection := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Collection", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: collectionID},
		{Page: wbxml.PageAirSync, Name: "Status", Text: status},
	}}
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
