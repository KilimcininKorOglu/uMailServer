package server

import (
	"slices"
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
