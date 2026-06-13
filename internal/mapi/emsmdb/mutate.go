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

	// CreateFolder creates a folder with the given IMAP-canonical mailbox name,
	// reporting whether it already existed.
	CreateFolder(user, mailbox string) (existed bool, err error)

	// DeleteFolder removes the folder with the given mailbox name (and its contents).
	DeleteFolder(user, mailbox string) error

	// EmptyFolder removes every message in the folder, returning how many remained
	// (0 means the folder was fully emptied), so the caller can report partial
	// completion.
	EmptyFolder(user, folder string) (remaining int, err error)
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

// ropCreateFolder handles RopCreateFolder (MS-OXCFOLD 2.2.1.2 / MS-OXCROPS 2.2.4.2):
// it creates a folder under the parent folder named by the request handle and binds
// the new folder to the output handle index for the ROPs that follow. The folder's
// IMAP-canonical name is the parent's name joined with the display name, and its id
// is registered on the logon (the read-path custom-folder scheme) so a later
// RopOpenFolder resolves it. The response carries the new folder id; a collision is
// reported as an existing folder when the request allows opening one, else failed.
func ropCreateFolder(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	_ = c.in.Uint8() // folder_type: only generic mail folders are created
	useUnicode := c.in.Uint8()
	openExisting := c.in.Uint8()
	_ = c.in.Uint8() // reserved
	var name string
	if useUnicode != 0 {
		name = c.in.WStr()
		_ = c.in.WStr() // folder comment (PidTagComment); not persisted in this model
	} else {
		name = c.in.Str()
		_ = c.in.Str()
	}
	if c.in.Err() != nil {
		writeRopError(c.out, RopCreateFolder, ohindex, ecError)
		return
	}
	if c.mutator == nil {
		writeRopError(c.out, RopCreateFolder, ohindex, ecNotImplemented)
		return
	}
	parent, ok := c.objectAt(hindex).(*folderObject)
	if !ok || parent.logon == nil || name == "" {
		writeRopError(c.out, RopCreateFolder, ohindex, ecNullObject)
		return
	}
	mailbox := name
	if parent.mailbox != "" {
		mailbox = parent.mailbox + "/" + name // IMAP hierarchy separator
	}
	existed, err := c.mutator.CreateFolder(c.email, mailbox)
	if err != nil {
		writeRopError(c.out, RopCreateFolder, ohindex, ecError)
		return
	}
	if existed && openExisting == 0 {
		writeRopError(c.out, RopCreateFolder, ohindex, ecError) // collision, open-existing not allowed
		return
	}
	lo := parent.logon
	fid := lo.folderIDForName(mailbox)
	c.setHandle(ohindex, c.state.alloc(&folderObject{folderID: fid, mailbox: mailbox, special: sfNone, logon: lo}))

	out := c.out
	out.Uint8(RopCreateFolder)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint64(fid)
	if !existed {
		out.Uint8(0) // IsExisting: a new folder; no further fields follow
		return
	}
	out.Uint8(1) // IsExisting
	out.Uint8(0) // HasRules: none
	out.Uint8(0) // IsGhosted: not a ghosted (replicated) folder
}

// ropDeleteFolder handles RopDeleteFolder (MS-OXCFOLD 2.2.1.3 / MS-OXCROPS 2.2.4.3):
// it removes the folder named by the request (a child of the folder at the request
// handle), routing through the canonical mailstore. A special/distinguished folder
// (Inbox, Sent, Trash) or a structural folder cannot be deleted. The response
// reports partial completion, set when the delete did not fully succeed.
func ropDeleteFolder(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint8() // flags: DEL_FOLDERS/DEL_MESSAGES/DELETE_HARD_DELETE; contents go with the folder
	folderID := c.in.Uint64()
	if c.in.Err() != nil {
		writeRopError(c.out, RopDeleteFolder, hindex, ecError)
		return
	}
	if c.mutator == nil {
		writeRopError(c.out, RopDeleteFolder, hindex, ecNotImplemented)
		return
	}
	lo := logonFromHandle(c.objectAt(hindex))
	if lo == nil {
		writeRopError(c.out, RopDeleteFolder, hindex, ecNullObject)
		return
	}
	mailbox, special, ok := lo.resolveFolder(folderID)
	if !ok || mailbox == "" {
		writeRopError(c.out, RopDeleteFolder, hindex, ecNotFound)
		return
	}
	if special != sfNone {
		writeRopError(c.out, RopDeleteFolder, hindex, ecAccessDenied) // a well-known folder is undeletable
		return
	}
	var partial uint8
	if err := c.mutator.DeleteFolder(c.email, mailbox); err != nil {
		partial = 1
	}

	out := c.out
	out.Uint8(RopDeleteFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
}

// ropEmptyFolder handles RopEmptyFolder (MS-OXCFOLD 2.2.1.9 / MS-OXCROPS 2.2.4.9):
// it removes every message in the folder named by the request handle, routing each
// through the canonical delete so the empty converges on every surface. want_async
// is honored synchronously. The response reports partial completion when messages
// remain.
func ropEmptyFolder(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint8() // want_asynchronous: handled synchronously
	_ = c.in.Uint8() // want_delete_associated: folder-associated items are not modeled
	if c.in.Err() != nil {
		writeRopError(c.out, RopEmptyFolder, hindex, ecError)
		return
	}
	if c.mutator == nil {
		writeRopError(c.out, RopEmptyFolder, hindex, ecNotImplemented)
		return
	}
	fo, ok := c.objectAt(hindex).(*folderObject)
	if !ok || fo.mailbox == "" {
		writeRopError(c.out, RopEmptyFolder, hindex, ecNullObject)
		return
	}
	remaining, err := c.mutator.EmptyFolder(c.email, fo.mailbox)
	if err != nil {
		writeRopError(c.out, RopEmptyFolder, hindex, ecError)
		return
	}
	var partial uint8
	if remaining > 0 {
		partial = 1
	}

	out := c.out
	out.Uint8(RopEmptyFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(partial) // PartialCompletion
}
