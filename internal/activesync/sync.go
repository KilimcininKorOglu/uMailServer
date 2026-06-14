package activesync

import (
	"errors"
	"strconv"
	"strings"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// Sync status codes (MS-ASCMD 2.2.3.177.4): 1 = success, 3 = invalid SyncKey
// (the client must restart this collection's sync from 0), 4 = a protocol/server
// error applying a client change (reported per item in the Responses block).
const (
	syncStatusSuccess       = "1"
	syncStatusInvalidKey    = "3"
	syncStatusProtocolError = "4"
)

// defaultWindowSize bounds a Sync response when the client sends no WindowSize.
const defaultWindowSize = 100

// SyncMessage is one mail item projected for an EAS Sync Add/Change.
type SyncMessage struct {
	ServerID     string // "<collectionID>:<uid>"
	Subject      string
	From         string
	To           string
	DateReceived string // ISO 8601, e.g. 2026-06-14T12:00:00.000Z
	Read         bool
	Importance   string // "0" low, "1" normal, "2" high
	BodyType     string // AirSyncBase Type: "1" plain text, "2" HTML
	Body         string
	Truncated    bool   // set when Body was clamped to the client's TruncationSize
}

// MailSource supplies a folder's mail for the Sync command: a current snapshot
// for the initial enumeration, and the change feed for incremental syncs.
type MailSource interface {
	// ListMessages returns the folder's current messages, oldest first.
	ListMessages(email, collectionID string) ([]SyncMessage, error)
	// ChangesSince returns the folder's adds, flag-changes and deletes since the
	// journal sequence, plus the new sequence head.
	ChangesSince(email, collectionID string, since uint64) (adds, changes []SyncMessage, deletes []string, newSeq uint64, err error)
	// CurrentSeq returns the journal head — the baseline a full enumeration
	// advances to, after which incremental syncs run from the change feed.
	CurrentSeq(email string) (uint64, error)
}

// Mutator applies a mobile client's up-sync changes to the canonical mailstore:
// a Change updates a message's read state, a Delete removes it. Implementations
// converge the mutation on the one store every surface reads, so a flag set or a
// deletion authored on a phone is reflected over IMAP/JMAP/webmail too. A change
// to an item that is already gone is not an error (the client's view is merely
// stale); only a real store failure returns one.
type Mutator interface {
	SetRead(email, collectionID, serverID string, read bool) error
	Delete(email, collectionID, serverID string) error
}

// clientResponse is one entry in the Sync Responses block: the per-item status of
// a client command the server applied. It is emitted only on failure — a success
// is acknowledged by the advanced SyncKey, not echoed.
type clientResponse struct {
	op       string // "Change" or "Delete"
	serverID string
	status   string
}

// handleSync answers the Sync command for one mail collection. SyncKey 0 primes
// (returns key 1 with no items); the first real sync streams the folder's
// current messages as Adds, windowed by WindowSize; once the snapshot is
// exhausted the cursor switches to the change feed and later syncs report only
// adds/flag-changes/deletes. A SyncKey that is not the last one issued is
// rejected with Status 3, prompting a fresh sync from 0.
//
// The per-collection cursor is stored in the sync watermark as
// "<lastIssuedKey>|<cursor>", where <cursor> is "e:<offset>" while enumerating
// the snapshot and "j:<seq>" once on the change feed.
func (s *Server) handleSync(ctx *Context) ([]byte, error) {
	if s.mail == nil || s.sync == nil {
		return nil, errors.New("activesync: mail source or sync state not configured")
	}
	deviceID := ctx.Request.URL.Query().Get("DeviceId")

	root, err := wbxml.Unmarshal(ctx.Body)
	if err != nil {
		return nil, err
	}
	collection := collectionOf(root)
	if collection == nil {
		return marshalSync("", syncStatusInvalidKey, "", nil, nil, false)
	}
	collectionID := textOf(collection.Sub("CollectionId"))
	reqKey := textOf(collection.Sub("SyncKey"))
	window := windowSize(root, collection)
	trunc := truncationSize(collection)

	if reqKey == "0" {
		if err := s.sync.PutSyncState(ctx.Email, collectionID, deviceID, "1|e:0"); err != nil {
			return nil, err
		}
		return marshalSync(collectionID, syncStatusSuccess, "1", nil, nil, false)
	}

	stored, err := s.sync.GetSyncState(ctx.Email, collectionID, deviceID)
	if err != nil || stored == "" {
		return marshalSync(collectionID, syncStatusInvalidKey, "", nil, nil, false)
	}
	lastKey, cursor, ok := strings.Cut(stored, "|")
	if !ok || reqKey != lastKey {
		return marshalSync(collectionID, syncStatusInvalidKey, "", nil, nil, false)
	}

	// Apply the client's up-sync changes (read-flag Changes, Deletes) before
	// enumerating server-side changes, so a just-read or just-deleted item is
	// reflected in both directions of this one exchange.
	responses := s.applyClientCommands(ctx.Email, collectionID, collection)

	cmds, nextCursor, more, err := s.nextBatch(ctx.Email, collectionID, cursor, window, trunc)
	if err != nil {
		return nil, err
	}
	nextKey := bumpKey(reqKey)
	if err := s.sync.PutSyncState(ctx.Email, collectionID, deviceID, nextKey+"|"+nextCursor); err != nil {
		return nil, err
	}
	return marshalSync(collectionID, syncStatusSuccess, nextKey, responses, cmds, more)
}

// applyClientCommands applies the client's up-sync changes for a collection — a
// Change updates read state, a Delete removes the item — and returns the failure
// responses. Successful commands are acknowledged by the advanced SyncKey, not
// echoed. It is a no-op when no Mutator is wired or the request carries no
// Commands. Only the read flag is honored on a Change in this phase; other
// property writes are left untouched rather than guessed.
func (s *Server) applyClientCommands(email, collectionID string, collection *wbxml.Element) []clientResponse {
	cmds := collection.Sub("Commands")
	if cmds == nil || s.mutator == nil {
		return nil
	}
	var responses []clientResponse
	for _, c := range cmds.Children {
		serverID := textOf(c.Sub("ServerId"))
		if serverID == "" {
			continue
		}
		switch c.Name {
		case "Change":
			appData := c.Sub("ApplicationData")
			if appData == nil {
				continue
			}
			readEl := appData.Sub("Read")
			if readEl == nil {
				continue // only the read flag is honored in this phase
			}
			if err := s.mutator.SetRead(email, collectionID, serverID, readEl.Text == "1"); err != nil {
				responses = append(responses, clientResponse{op: "Change", serverID: serverID, status: syncStatusProtocolError})
			}
		case "Delete":
			if err := s.mutator.Delete(email, collectionID, serverID); err != nil {
				responses = append(responses, clientResponse{op: "Delete", serverID: serverID, status: syncStatusProtocolError})
			}
		}
	}
	return responses
}

// syncCommand is one decoded change to emit (an Add/Change carries a message; a
// Delete carries only the server id).
type syncCommand struct {
	op  string // "Add", "Change", "Delete"
	msg SyncMessage
	id  string // for Delete
}

// nextBatch produces the next window of changes for a cursor and returns the
// advanced cursor and whether more remain. While the cursor is "e:<offset>" it
// walks the current snapshot a window at a time; once exhausted it switches to
// "j:<seq>" and thereafter reads the change feed.
func (s *Server) nextBatch(email, collectionID, cursor string, window, trunc int) ([]syncCommand, string, bool, error) {
	kind, val, _ := strings.Cut(cursor, ":")
	if kind == "e" {
		offset := atoiOr(val, 0)
		all, err := s.mail.ListMessages(email, collectionID)
		if err != nil {
			return nil, "", false, err
		}
		end := min(offset+window, len(all))
		var cmds []syncCommand
		for _, m := range all[offset:end] {
			cmds = append(cmds, syncCommand{op: "Add", msg: truncateBody(m, trunc)})
		}
		if end < len(all) {
			return cmds, "e:" + strconv.Itoa(end), true, nil
		}
		seq, err := s.mail.CurrentSeq(email)
		if err != nil {
			return nil, "", false, err
		}
		return cmds, "j:" + strconv.FormatUint(seq, 10), false, nil
	}

	var since uint64
	if v, err := strconv.ParseUint(val, 10, 64); err == nil {
		since = v
	}
	adds, changes, deletes, newSeq, err := s.mail.ChangesSince(email, collectionID, since)
	if err != nil {
		return nil, "", false, err
	}
	var cmds []syncCommand
	for _, m := range adds {
		cmds = append(cmds, syncCommand{op: "Add", msg: truncateBody(m, trunc)})
	}
	for _, m := range changes {
		cmds = append(cmds, syncCommand{op: "Change", msg: truncateBody(m, trunc)})
	}
	for _, id := range deletes {
		cmds = append(cmds, syncCommand{op: "Delete", id: id})
	}
	return cmds, "j:" + strconv.FormatUint(newSeq, 10), false, nil
}

// truncateBody clamps a message body to trunc bytes (0 = no limit) and reports
// truncation via a marker the encoder reads.
func truncateBody(m SyncMessage, trunc int) SyncMessage {
	if trunc > 0 && len(m.Body) > trunc {
		m.Body = m.Body[:trunc]
		m.Truncated = true
	}
	return m
}

// collectionOf returns the single Collection element of a Sync request.
func collectionOf(root *wbxml.Element) *wbxml.Element {
	if root == nil {
		return nil
	}
	if cols := root.Sub("Collections"); cols != nil {
		return cols.Sub("Collection")
	}
	return nil
}

func textOf(e *wbxml.Element) string {
	if e == nil {
		return ""
	}
	return e.Text
}

// windowSize reads the client's WindowSize (collection- or request-level),
// clamped to the default when absent or invalid.
func windowSize(root, collection *wbxml.Element) int {
	for _, e := range []*wbxml.Element{collection.Sub("WindowSize"), root.Sub("WindowSize")} {
		if e != nil {
			if n, err := strconv.Atoi(e.Text); err == nil && n > 0 {
				return n
			}
		}
	}
	return defaultWindowSize
}

// truncationSize reads Options/BodyPreference/TruncationSize, or 0 for no limit.
func truncationSize(collection *wbxml.Element) int {
	opts := collection.Sub("Options")
	if opts == nil {
		return 0
	}
	bp := opts.Sub("BodyPreference")
	if bp == nil {
		return 0
	}
	if ts := bp.Sub("TruncationSize"); ts != nil {
		if n, err := strconv.Atoi(ts.Text); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func bumpKey(key string) string {
	return strconv.Itoa(atoiOr(key, 0) + 1)
}

// atoiOr parses s as an int, returning def when it is not a valid integer.
func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
