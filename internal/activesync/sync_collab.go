package activesync

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"sort"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// This file holds the enumerate-and-diff engine shared by the journal-less PIM
// data classes (calendar, contacts, tasks). None of those collections has a
// change journal, so every Sync re-enumerates the folder and diffs it against a
// compact serverId->etag cursor. The diff, the cursor digest and the Sync
// response envelope are identical across the classes — only the canonical
// source, the up-sync mutator and the per-class WBXML projection differ — so the
// shared parts live here and each per-class handler (sync_calendar/sync_contacts/
// sync_tasks) supplies the projection. This is not the mail Sync model: mail has
// a change journal and blob-key ServerIds, so it keeps its own path (Rule 10 —
// the two data-class families do not share a sync model).

// collabEntry is one enumerated item's identity for the diff: its stable EAS
// ServerId and the canonical ETag that advances whenever the item changes.
type collabEntry struct {
	serverID string
	etag     string
}

// collabDiffOp is one diff outcome, neutral to the data class so the same diff
// serves every projection: an Add/Change references the source item by index
// (the caller projects it), and a Delete carries only the gone ServerId.
type collabDiffOp struct {
	op  string // "Add", "Change", "Delete"
	idx int    // index into the enumerated entries (Add/Change)
	id  string // gone ServerId (Delete)
}

// diffCollab compares the current entries against the previously synced state
// (serverId->etag) and returns the next window of changes, the advanced cursor
// reflecting only the emitted changes, and whether more remain. It is the single
// diff implementation every journal-less class uses: carrying the emitted
// changes into the cursor lets a large folder drain across several syncs and
// still converge. The caller maps each op back to its own command/projection.
func diffCollab(prev map[string]string, entries []collabEntry, window int) (ops []collabDiffOp, nextCursor string, more bool) {
	seen := make(map[string]bool, len(entries))
	var all []collabDiffOp
	for i := range entries {
		seen[entries[i].serverID] = true
		etag, known := prev[entries[i].serverID]
		switch {
		case !known:
			all = append(all, collabDiffOp{op: "Add", idx: i})
		case etag != entries[i].etag:
			all = append(all, collabDiffOp{op: "Change", idx: i})
		}
	}
	for id := range prev {
		if !seen[id] {
			all = append(all, collabDiffOp{op: "Delete", id: id})
		}
	}
	// Stable order so a windowed drain is deterministic across syncs.
	sort.Slice(all, func(i, j int) bool { return diffOpKey(all[i], entries) < diffOpKey(all[j], entries) })

	end := len(all)
	if window > 0 && window < end {
		end = window
	}
	ops = all[:end]
	more = end < len(all)

	next := make(map[string]string, len(prev))
	maps.Copy(next, prev)
	for _, o := range ops {
		if o.op == "Delete" {
			delete(next, o.id)
		} else {
			next[entries[o.idx].serverID] = entries[o.idx].etag
		}
	}
	return ops, encodeCursor(next), more
}

// diffOpKey is the stable sort key for a diff op: the deleted id, or the
// referenced entry's ServerId.
func diffOpKey(o collabDiffOp, entries []collabEntry) string {
	if o.op == "Delete" {
		return o.id
	}
	return entries[o.idx].serverID
}

// encodeCursor serializes the synced-state digest into the opaque watermark
// (base64 JSON, free of the "|" the watermark splits on); an empty map is "".
func encodeCursor(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decodeCursor reverses encodeCursor, returning an empty map for the empty or
// malformed cursor (a malformed cursor degrades to a full re-add, which is safe).
func decodeCursor(s string) map[string]string {
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

// collabOp is one projected change to emit on the wire: an Add/Change carries
// the item's ServerId and its projected ApplicationData; a Delete carries only
// the ServerId. Each per-class handler builds these from a diffCollab op plus
// its own projection.
type collabOp struct {
	op       string // "Add", "Change", "Delete"
	serverID string
	appData  []*wbxml.Element
}

// collabClientResponse is one entry in a Sync Responses block for an applied
// client up-sync command: an Add echoes ClientId + the assigned ServerId; a
// Change/Delete is echoed only on failure (success is acknowledged by the
// advanced SyncKey).
type collabClientResponse struct {
	op       string // "Add", "Change", "Delete"
	clientID string // for Add
	serverID string
	status   string
}

// marshalCollabSync builds a Sync response for a journal-less collection: the
// SyncKey/CollectionId/Status envelope (shared AirSync page 0), an optional
// Responses block, an optional MoreAvailable, and the Commands block of
// Add/Change/Delete. A non-success status resets the SyncKey to "0" so the
// client restarts. The per-class ApplicationData rides inside each op.
func marshalCollabSync(collectionID, status, syncKey string, responses []collabClientResponse, ops []collabOp, more bool) ([]byte, error) {
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
			block.Children = append(block.Children, encodeCollabClientResponse(r))
		}
		collection.Children = append(collection.Children, block)
	}
	if more {
		collection.Children = append(collection.Children, &wbxml.Element{Page: wbxml.PageAirSync, Name: "MoreAvailable"})
	}
	if len(ops) > 0 {
		commands := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Commands"}
		for _, o := range ops {
			commands.Children = append(commands.Children, encodeCollabOp(o))
		}
		collection.Children = append(collection.Children, commands)
	}
	root := &wbxml.Element{Page: wbxml.PageAirSync, Name: "Sync", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "Collections", Children: []*wbxml.Element{collection}},
	}}
	return wbxml.Marshal(root)
}

// encodeCollabOp encodes one change: a Delete carries only the ServerId, while
// Add/Change carry the ServerId and the item's ApplicationData.
func encodeCollabOp(o collabOp) *wbxml.Element {
	if o.op == "Delete" {
		return &wbxml.Element{Page: wbxml.PageAirSync, Name: "Delete", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSync, Name: "ServerId", Text: o.serverID},
		}}
	}
	return &wbxml.Element{Page: wbxml.PageAirSync, Name: o.op, Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "ServerId", Text: o.serverID},
		{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: o.appData},
	}}
}

// encodeCollabClientResponse encodes one Responses entry for an applied client
// command: an Add carries ClientId, the assigned ServerId and Status; a
// Change/Delete carries the ServerId and its failure Status.
func encodeCollabClientResponse(r collabClientResponse) *wbxml.Element {
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
