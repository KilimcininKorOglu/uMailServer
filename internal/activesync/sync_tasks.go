package activesync

import (
	"strings"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// tasksCollectionPrefix tags a Sync CollectionId as a tasks collection.
// FolderSync emits the tasks folder's ServerId as this prefix plus the canonical
// folder id; the Sync handler routes on the prefix (a mail folder's ServerId is
// its bare name, a calendar's is "cal:" and a contacts' is "con:", so the
// namespaces never collide) and strips it to recover the folder id for the
// TaskSource. Keeping tasks on the shared journal-less Sync path leaves the mail
// machinery untouched (Rule 10 — the data-class families do not share a sync
// model).
const tasksCollectionPrefix = "tsk:"

// TaskCollectionID tags a canonical tasks folder id as an EAS tasks collection
// ServerId so the Sync router recognizes it and routes it to the TaskSource path;
// the prefix convention stays encapsulated here.
func TaskCollectionID(folderID string) string {
	return tasksCollectionPrefix + folderID
}

// TaskMutator applies a mobile client's tasks up-sync changes to the canonical
// collaboration store — the one store EWS/CalDAV/webmail read, so a to-do
// created, edited or deleted on a phone converges with them. CreateItem returns
// the new to-do's stable server id (the VTODO UID, echoed to the client for its
// temporary ClientId). A change to a to-do already gone is not an error; only a
// real store failure returns one.
type TaskMutator interface {
	CreateItem(email, folderID string, it TaskItem) (serverID string, err error)
	UpdateItem(email, folderID, serverID string, it TaskItem) error
	DeleteItem(email, folderID, serverID string) error
}

// handleTasksSync answers the Sync command for a tasks collection. SyncKey 0
// primes (returns key 1, empty cursor); a real sync applies the client's up-sync
// changes, enumerates the folder's to-dos and diffs them against the cursor
// through the shared journal-less engine, emitting Adds/Changes/Deletes. A
// SyncKey that is not the last one issued is rejected with Status 3, forcing a
// fresh sync.
func (s *Server) handleTasksSync(ctx *Context, collection *wbxml.Element, collectionID, folderID, reqKey string, window int, deviceID string) ([]byte, error) {
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
	responses, touched, deleted := s.applyTaskCommands(ctx.Email, folderID, collection)

	items, err := s.tasks.ListItems(ctx.Email, folderID)
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
			cmds[i] = collabOp{op: o.op, serverID: items[o.idx].ServerID, appData: taskAppData(items[o.idx])}
		}
	}

	nextKey := bumpKey(reqKey)
	if err := s.sync.PutSyncState(ctx.Email, collectionID, deviceID, nextKey+"|"+nextCursor); err != nil {
		return nil, err
	}
	return marshalCollabSync(collectionID, syncStatusSuccess, nextKey, responses, cmds, more)
}

// applyTaskCommands applies the client's tasks up-sync Commands to the canonical
// store and reports them: the Responses to echo (every Add maps its ClientId to
// the assigned ServerId; a Change/Delete only on failure), the set of server ids
// the client created or changed, and the ids it deleted. The caller reconciles
// the sync cursor with these so they are not re-emitted.
func (s *Server) applyTaskCommands(email, folderID string, collection *wbxml.Element) (responses []collabClientResponse, touched map[string]bool, deleted []string) {
	cmds := collection.Sub("Commands")
	if cmds == nil || s.taskMutator == nil {
		return nil, nil, nil
	}
	touched = map[string]bool{}
	for _, c := range cmds.Children {
		switch c.Name {
		case "Add":
			clientID := textOf(c.Sub("ClientId"))
			serverID, err := s.taskMutator.CreateItem(email, folderID, taskItemFromAppData(c.Sub("ApplicationData")))
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
			if err := s.taskMutator.UpdateItem(email, folderID, serverID, taskItemFromAppData(c.Sub("ApplicationData"))); err != nil {
				responses = append(responses, collabClientResponse{op: "Change", serverID: serverID, status: syncStatusProtocolError})
			} else {
				touched[serverID] = true
			}
		case "Delete":
			serverID := textOf(c.Sub("ServerId"))
			if serverID == "" {
				continue
			}
			if err := s.taskMutator.DeleteItem(email, folderID, serverID); err != nil {
				responses = append(responses, collabClientResponse{op: "Delete", serverID: serverID, status: syncStatusProtocolError})
			} else {
				deleted = append(deleted, serverID)
			}
		}
	}
	return responses, touched, deleted
}
