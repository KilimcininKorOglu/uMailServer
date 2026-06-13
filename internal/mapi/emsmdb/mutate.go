package emsmdb

import "github.com/umailserver/umailserver/internal/mapi/wire"

// Mutator is the canonical mailbox-mutation surface the content-changing write
// ROPs (delete/move/folder) bind to. It is backed by the same mailstore the IMAP
// and EWS surfaces converge on, so a mutation over MAPI/HTTP lands in the one
// canonical store every surface reads and refreshes connected clients. A nil
// Mutator makes the content-mutation ROPs report the operation as unsupported.
type Mutator interface {
	// DeleteMessages removes the given messages (by IMAP uid) from the folder and
	// returns how many were actually removed, so the caller can report partial
	// completion. The implementation performs the canonical hard removal (index,
	// blob, and semantic identity) and notifies the other surfaces.
	DeleteMessages(user, folder string, uids []uint32) (removed int, err error)
}

// pullEntryIDArray reads an EntryID array whose count is a 16-bit value followed by
// that many 64-bit ids (the form RopDeleteMessages and RopMoveCopyMessages carry
// their message ids in; MS-OXCDATA 2.3.1, the reference g_eid_a with a 2-byte
// count).
func pullEntryIDArray(p *wire.Pull) []uint64 {
	n := int(p.Uint16())
	ids := make([]uint64, 0, n)
	for range n {
		id := p.Uint64()
		if p.Err() != nil {
			break
		}
		ids = append(ids, id)
	}
	return ids
}

// ropDeleteMessages handles RopDeleteMessages (MS-OXCFOLD 2.2.1.11 / MS-OXCROPS
// 2.2.4.11): it removes the listed messages from the folder named by the request
// handle, routing through the canonical mailstore mutator so the deletion lands in
// the one store every surface reads (and connected IMAP/webmail clients refresh).
// want_asynchronous is honored synchronously — the delete completes before the
// response — so the asynchronous/progress path is not used. The response reports
// partial completion when not every requested message could be removed.
func ropDeleteMessages(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint8() // want_asynchronous: handled synchronously
	_ = c.in.Uint8() // notify_non_read: non-read receipts are not generated
	mids := pullEntryIDArray(c.in)
	if c.in.Err() != nil {
		writeRopError(c.out, RopDeleteMessages, hindex, ecError)
		return
	}
	if c.mutator == nil {
		writeRopError(c.out, RopDeleteMessages, hindex, ecNotImplemented)
		return
	}
	fo, ok := c.objectAt(hindex).(*folderObject)
	if !ok || fo.mailbox == "" {
		writeRopError(c.out, RopDeleteMessages, hindex, ecNullObject)
		return
	}
	uids := make([]uint32, 0, len(mids))
	for _, mid := range mids {
		uids = append(uids, messageUID(mid))
	}
	removed, err := c.mutator.DeleteMessages(c.email, fo.mailbox, uids)
	if err != nil {
		writeRopError(c.out, RopDeleteMessages, hindex, ecError)
		return
	}
	var partial uint8
	if removed < len(uids) {
		partial = 1
	}

	out := c.out
	out.Uint8(RopDeleteMessages)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
}
