package activesync

import (
	"strings"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// contactsCollectionPrefix tags a Sync CollectionId as a contacts collection.
// FolderSync emits the contacts folder's ServerId as this prefix plus the
// canonical folder id; the Sync handler routes on the prefix (a mail folder's
// ServerId is its bare name and a calendar's is the "cal:" prefix, so the
// namespaces never collide) and strips it to recover the folder id for the
// ContactSource. Keeping contacts on the shared journal-less Sync path leaves the
// mail machinery untouched (Rule 10 — the data-class families do not share a
// sync model).
const contactsCollectionPrefix = "con:"

// ContactCollectionID tags a canonical contacts folder id as an EAS contacts
// collection ServerId so the Sync router recognizes it and routes it to the
// ContactSource path; the prefix convention stays encapsulated here.
func ContactCollectionID(folderID string) string {
	return contactsCollectionPrefix + folderID
}

// ContactMutator applies a mobile client's contacts up-sync changes to the
// canonical collaboration store — the one store EWS/CardDAV/webmail read, so a
// card created, edited or deleted on a phone converges with them. CreateItem
// returns the new card's stable server id (the vCard UID, echoed to the client
// for its temporary ClientId). A change to a card already gone is not an error;
// only a real store failure returns one.
type ContactMutator interface {
	CreateItem(email, folderID string, it ContactItem) (serverID string, err error)
	UpdateItem(email, folderID, serverID string, it ContactItem) error
	DeleteItem(email, folderID, serverID string) error
}

// handleContactsSync answers the Sync command for a contacts collection. SyncKey
// 0 primes (returns key 1, empty cursor); a real sync applies the client's
// up-sync changes, enumerates the folder's cards and diffs them against the
// cursor through the shared journal-less engine, emitting Adds/Changes/Deletes.
// A SyncKey that is not the last one issued is rejected with Status 3, forcing a
// fresh sync.
func (s *Server) handleContactsSync(ctx *Context, collection *wbxml.Element, collectionID, folderID, reqKey string, window int, deviceID string) ([]byte, error) {
	if reqKey == "0" {
		if err := s.sync.PutSyncState(ctx.Email, collectionID, deviceID, "1|"); err != nil {
			return nil, err
		}
		return marshalCollabSync(collectionID, syncStatusSuccess, "1", nil, nil, false)
	}

	stored, err := s.sync.GetSyncState(ctx.Email, collectionID, deviceID)
	if err != nil || stored == "" {
		return marshalCollabSync(collectionID, syncStatusInvalidKey, "", nil, nil, false)
	}
	lastKey, cursor, ok := strings.Cut(stored, "|")
	if !ok || reqKey != lastKey {
		return marshalCollabSync(collectionID, syncStatusInvalidKey, "", nil, nil, false)
	}

	// Apply the client's up-sync changes first, then reconcile the cursor with
	// them so the client's own writes are not echoed back as server-side changes.
	responses, touched, deleted := s.applyContactCommands(ctx.Email, folderID, collection)

	items, err := s.contacts.ListItems(ctx.Email, folderID)
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

	entries := make([]collabEntry, len(items))
	for i, it := range items {
		entries[i] = collabEntry{serverID: it.ServerID, etag: it.ETag}
	}
	ops, nextCursor, more := diffCollab(prev, entries, window)
	cmds := make([]collabOp, len(ops))
	for i, o := range ops {
		if o.op == "Delete" {
			cmds[i] = collabOp{op: "Delete", serverID: o.id}
		} else {
			cmds[i] = collabOp{op: o.op, serverID: items[o.idx].ServerID, appData: contactAppData(items[o.idx])}
		}
	}

	nextKey := bumpKey(reqKey)
	if err := s.sync.PutSyncState(ctx.Email, collectionID, deviceID, nextKey+"|"+nextCursor); err != nil {
		return nil, err
	}
	return marshalCollabSync(collectionID, syncStatusSuccess, nextKey, responses, cmds, more)
}

// applyContactCommands applies the client's contacts up-sync Commands to the
// canonical store and reports them: the Responses to echo (every Add maps its
// ClientId to the assigned ServerId; a Change/Delete only on failure), the set
// of server ids the client created or changed, and the ids it deleted. The
// caller reconciles the sync cursor with these so they are not re-emitted.
func (s *Server) applyContactCommands(email, folderID string, collection *wbxml.Element) (responses []collabClientResponse, touched map[string]bool, deleted []string) {
	cmds := collection.Sub("Commands")
	if cmds == nil || s.conMutator == nil {
		return nil, nil, nil
	}
	touched = map[string]bool{}
	for _, c := range cmds.Children {
		switch c.Name {
		case "Add":
			clientID := textOf(c.Sub("ClientId"))
			serverID, err := s.conMutator.CreateItem(email, folderID, contactItemFromAppData(c.Sub("ApplicationData")))
			status := syncStatusSuccess
			if err != nil {
				status, serverID = syncStatusProtocolError, ""
			} else {
				touched[serverID] = true
			}
			responses = append(responses, collabClientResponse{op: "Add", clientID: clientID, serverID: serverID, status: status})
		case "Change":
			serverID := textOf(c.Sub("ServerId"))
			if serverID == "" {
				continue
			}
			if err := s.conMutator.UpdateItem(email, folderID, serverID, contactItemFromAppData(c.Sub("ApplicationData"))); err != nil {
				responses = append(responses, collabClientResponse{op: "Change", serverID: serverID, status: syncStatusProtocolError})
			} else {
				touched[serverID] = true
			}
		case "Delete":
			serverID := textOf(c.Sub("ServerId"))
			if serverID == "" {
				continue
			}
			if err := s.conMutator.DeleteItem(email, folderID, serverID); err != nil {
				responses = append(responses, collabClientResponse{op: "Delete", serverID: serverID, status: syncStatusProtocolError})
			} else {
				deleted = append(deleted, serverID)
			}
		}
	}
	return responses, touched, deleted
}
