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

	// MoveMessages relocates the given messages (by source-folder uid) from the
	// source folder to the destination folder, returning how many were moved.
	MoveMessages(user, srcFolder, dstFolder string, uids []uint32) (moved int, err error)

	// CopyMessages copies the given messages (by source-folder uid) into the
	// destination folder, returning how many were copied.
	CopyMessages(user, srcFolder, dstFolder string, uids []uint32) (copied int, err error)
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

// ropMoveCopyMessages handles RopMoveCopyMessages (MS-OXCFOLD 2.2.1.7 / MS-OXCROPS
// 2.2.4.6): it moves or copies the listed messages from the source folder named by
// the request handle to the destination folder named by the destination handle
// index, routing through the canonical mailstore mutator so the result lands in the
// one store every surface reads. want_copy selects copy (non-zero) over move
// (zero); want_asynchronous is honored synchronously. The response reports partial
// completion when not every message could be relocated. A null destination handle
// is rejected with the generic failure rather than the dedicated null-destination
// response shape, which is an error-path refinement.
func ropMoveCopyMessages(c *ropCtx, _ uint8, hindex uint8) {
	dhindex := c.in.Uint8()
	mids := pullEntryIDArray(c.in)
	_ = c.in.Uint8() // want_asynchronous: handled synchronously
	wantCopy := c.in.Uint8()
	if c.in.Err() != nil {
		writeRopError(c.out, RopMoveCopyMessages, hindex, ecError)
		return
	}
	if c.mutator == nil {
		writeRopError(c.out, RopMoveCopyMessages, hindex, ecNotImplemented)
		return
	}
	src, ok := c.objectAt(hindex).(*folderObject)
	if !ok || src.mailbox == "" {
		writeRopError(c.out, RopMoveCopyMessages, hindex, ecNullObject)
		return
	}
	dst, ok := c.objectAt(dhindex).(*folderObject)
	if !ok || dst.mailbox == "" {
		writeRopError(c.out, RopMoveCopyMessages, hindex, ecNullObject)
		return
	}
	uids := make([]uint32, 0, len(mids))
	for _, mid := range mids {
		uids = append(uids, messageUID(mid))
	}
	var done int
	var err error
	if wantCopy != 0 {
		done, err = c.mutator.CopyMessages(c.email, src.mailbox, dst.mailbox, uids)
	} else {
		done, err = c.mutator.MoveMessages(c.email, src.mailbox, dst.mailbox, uids)
	}
	if err != nil {
		writeRopError(c.out, RopMoveCopyMessages, hindex, ecError)
		return
	}
	var partial uint8
	if done < len(uids) {
		partial = 1
	}

	out := c.out
	out.Uint8(RopMoveCopyMessages)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
}
