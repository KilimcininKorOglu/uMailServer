package server

import (
	"slices"
	"strconv"
	"strings"

	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/mapi/emsmdb"
)

// emsmdbMutator backs the binary MAPI/HTTP (emsmdb) content-mutation ROPs with the
// canonical mailstore the IMAP and EWS surfaces already converge on, so a delete or
// move authored over MAPI/HTTP removes or relocates the message in the one store
// every surface reads. It also fires the cross-surface refresh notifications the
// mailstore methods do not raise themselves (those live in the IMAP command layer
// and the EWS mirror), so connected IMAP IDLE sessions and the webmail SSE stream
// stay current and a pending scheduled send is canceled when its projection is
// removed — the same behavior an EWS DeleteItem or an IMAP EXPUNGE produces.
//
// Note: the deletion is recorded in the canonical data stores (index, blob,
// semantic identity, QRESYNC tombstone, soft-delete dumpster), exactly as an IMAP
// EXPUNGE is. It does not additionally emit a semcore MutateDelete lifecycle event
// (the tombstone feed consumed by incremental-sync clients); IMAP EXPUNGE does not
// either, so emsmdb is as convergent as IMAP. Routing every surface's delete/move
// through one shared mutation core is a separate, deferred convergence effort.
type emsmdbMutator struct {
	srv *Server
}

var _ emsmdb.Mutator = emsmdbMutator{}

// DeleteMessages flags the target messages \Deleted and runs the canonical expunge
// the IMAP path uses (index + blob + semantic identity + QRESYNC tombstone +
// soft-delete dumpster), then notifies the other surfaces. It returns how many of
// the requested messages were actually removed so the ROP can report partial
// completion. The expunge is scoped to the requested uids, so a message marked
// \Deleted by another session but not in this request is left untouched.
func (m emsmdbMutator) DeleteMessages(user, folder string, uids []uint32) (int, error) {
	if len(uids) == 0 {
		return 0, nil
	}
	for _, uid := range uids {
		meta, err := m.srv.storageDB.GetMessageMetadata(user, folder, uid)
		if err != nil {
			continue // already gone or never existed; the expunge simply skips it
		}
		if slices.Contains(meta.Flags, "\\Deleted") {
			continue
		}
		meta.Flags = append(meta.Flags, "\\Deleted")
		if err := m.srv.storageDB.StoreMessageMetadata(user, folder, uid, meta); err != nil {
			return 0, err
		}
	}
	ranges := make([]imap.SeqRange, 0, len(uids))
	for _, uid := range uids {
		ranges = append(ranges, imap.SeqRange{Start: uid, End: uid})
	}
	seqs, removed, err := m.srv.mailstore.ExpungeUIDs(user, folder, ranges)
	if err != nil {
		return 0, err
	}
	hub := imap.GetNotificationHub()
	for i, uid := range removed {
		var seq uint32
		if i < len(seqs) {
			seq = seqs[i]
		}
		hub.NotifyExpunge(user, folder, uid, seq)
		if strings.EqualFold(folder, scheduledFolder) {
			m.srv.cancelScheduledOnExpunge(user, scheduledFolder, uid)
		}
	}
	return len(removed), nil
}

// MoveMessages relocates the given source-folder messages to the destination via
// the mailstore's copy-then-expunge move (which re-indexes the same blob under a
// fresh destination uid and drops the source entry), then notifies both folders:
// an expunge on the source and a new-message on the destination, matching an EWS
// MoveItem and an IMAP MOVE. It returns the number of messages removed from the
// source so the ROP can report partial completion.
func (m emsmdbMutator) MoveMessages(user, srcFolder, dstFolder string, uids []uint32) (int, error) {
	seqSet, want := m.uidsToSeqSet(user, srcFolder, uids)
	if want == 0 {
		return 0, nil
	}
	copied, seqs, expunged, err := m.srv.mailstore.MoveMessages(user, srcFolder, dstFolder, seqSet)
	if err != nil {
		return 0, err
	}
	hub := imap.GetNotificationHub()
	for i, uid := range expunged {
		var seq uint32
		if i < len(seqs) {
			seq = seqs[i]
		}
		hub.NotifyExpunge(user, srcFolder, uid, seq)
		if strings.EqualFold(srcFolder, scheduledFolder) {
			m.srv.cancelScheduledOnExpunge(user, scheduledFolder, uid)
		}
	}
	for _, duid := range copied.DstUIDs {
		hub.NotifyNewMessage(user, dstFolder, duid, duid)
	}
	return len(expunged), nil
}

// CopyMessages copies the given source-folder messages into the destination (the
// blob is re-stored under a fresh destination uid; the source is left intact), then
// notifies the destination with a new-message event. It returns the number copied.
func (m emsmdbMutator) CopyMessages(user, srcFolder, dstFolder string, uids []uint32) (int, error) {
	seqSet, want := m.uidsToSeqSet(user, srcFolder, uids)
	if want == 0 {
		return 0, nil
	}
	copied, err := m.srv.mailstore.CopyMessages(user, srcFolder, dstFolder, seqSet)
	if err != nil {
		return 0, err
	}
	hub := imap.GetNotificationHub()
	for _, duid := range copied.DstUIDs {
		hub.NotifyNewMessage(user, dstFolder, duid, duid)
	}
	return len(copied.DstUIDs), nil
}

// uidsToSeqSet maps source-folder uids to a 1-based sequence-number set string, the
// form the mailstore Move/Copy methods expect (they resolve the set against
// sequence positions, not uids). Uids no longer present in the folder are dropped,
// so a stale id simply does not contribute to the moved/copied set.
func (m emsmdbMutator) uidsToSeqSet(user, folder string, uids []uint32) (string, int) {
	all, err := m.srv.storageDB.GetMessageUIDs(user, folder)
	if err != nil {
		return "", 0
	}
	pos := make(map[uint32]int, len(all))
	for i, u := range all {
		pos[u] = i + 1 // IMAP sequence numbers are 1-based
	}
	seqs := make([]string, 0, len(uids))
	for _, u := range uids {
		if p, ok := pos[u]; ok {
			seqs = append(seqs, strconv.Itoa(p))
		}
	}
	return strings.Join(seqs, ","), len(seqs)
}
