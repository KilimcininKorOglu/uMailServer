package emsmdb

// syncCollectorObject is a server object bound by RopSynchronizationOpenCollector: the
// ICS upload counterpart of syncContextObject (the download configuration). It names the
// folder the import ROPs (RopSyncImportMessageChange and friends) write into, and is a
// distinct type so a download context can never be mistaken for an upload target.
// contents distinguishes a contents collector (messages) from a hierarchy collector
// (folders).
type syncCollectorObject struct {
	mailbox  string
	contents bool
}

// ropSyncOpenCollector handles RopSynchronizationOpenCollector (MS-OXCFXICS 2.2.3.2.3.1;
// MS-OXCROPS 2.2.13.7): it opens an ICS upload collector on the folder named by the
// input handle and binds it to the output handle index for the import ROPs that follow.
// The is_content_collector byte selects a contents (message) or hierarchy (folder)
// collector. The response carries no body (the no-body push group); the collector lives
// at the output handle.
func ropSyncOpenCollector(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	isContent := c.in.Uint8()
	if c.in.Err() != nil {
		writeRopError(c.out, RopSyncOpenCollector, ohindex, ecError)
		return
	}
	fo, ok := c.objectAt(hindex).(*folderObject)
	if !ok {
		writeRopError(c.out, RopSyncOpenCollector, ohindex, ecNullObject)
		return
	}
	c.setHandle(ohindex, c.state.alloc(&syncCollectorObject{
		mailbox:  fo.mailbox,
		contents: isContent != 0,
	}))

	out := c.out
	out.Uint8(RopSyncOpenCollector)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
}
