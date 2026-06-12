package emsmdb

// folderObject is a server object opened on a mailbox folder (RopOpenFolder). It
// carries the folder id and the special-folder slot it resolves to, which the
// later table and property ROPs use to reach the canonical store.
type folderObject struct {
	folderID uint64
	special  int // index into the logon's special folders
}

// receiveFolderClass is the message class the default receive folder is
// configured for. An empty string means the folder receives every class, which
// is how a simple mailbox routes all delivery to the Inbox (MS-OXCSTOR 2.2.1.2).
const receiveFolderClass = ""

// ropGetReceiveFolder handles RopGetReceiveFolder (MS-OXCSTOR 2.2.1.2): it maps a
// message class to the folder that receives it. Every class routes to the Inbox
// in this mailbox model, so the response always names the Inbox.
func ropGetReceiveFolder(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Str() // requested message class; all classes route to the Inbox
	lo, ok := c.objectAt(hindex).(*logonObject)
	if !ok {
		writeRopError(c.out, RopGetReceiveFolder, hindex, ecNullObject)
		return
	}
	out := c.out
	out.Uint8(RopGetReceiveFolder)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint64(lo.folderIDs[sfInbox])
	out.Str(receiveFolderClass)
}

// ropOpenFolder handles RopOpenFolder (MS-OXCFOLD 2.2.1.1): it opens a folder by
// id under the logon and binds the new folder object to the request's output
// handle index. Only the mailbox's well-known folders are openable on the online
// path.
func ropOpenFolder(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	folderID := c.in.Uint64()
	_ = c.in.Uint8() // open mode flags: no ghosted/soft-deleted handling online
	if c.in.Err() != nil {
		writeRopError(c.out, RopOpenFolder, ohindex, ecError)
		return
	}
	lo, ok := c.objectAt(hindex).(*logonObject)
	if !ok {
		writeRopError(c.out, RopOpenFolder, ohindex, ecNullObject)
		return
	}
	special := lo.specialIndex(folderID)
	if special < 0 {
		writeRopError(c.out, RopOpenFolder, ohindex, ecNotFound)
		return
	}
	c.setHandle(ohindex, c.state.alloc(&folderObject{folderID: folderID, special: special}))

	out := c.out
	out.Uint8(RopOpenFolder)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint8(0) // HasRules: no delivery rules on this folder
	out.Uint8(0) // IsGhosted: the folder is hosted on this server
}
