package emsmdb

import "github.com/umailserver/umailserver/internal/mapi/wire"

// syncCollectorObject is a server object bound by RopSynchronizationOpenCollector: the
// ICS upload counterpart of syncContextObject (the download configuration). It names the
// folder the import ROPs (RopSyncImportMessageChange and friends) write into, and is a
// distinct type so a download context can never be mistaken for an upload target.
// contents distinguishes a contents collector (messages) from a hierarchy collector
// (folders); logon backs the store identity (the replica GUID an import source key is
// matched against).
type syncCollectorObject struct {
	mailbox  string
	contents bool
	logon    *logonObject
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
		logon:    fo.logon,
	}))

	out := c.out
	out.Uint8(RopSyncOpenCollector)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
}

// importSourceKey returns the PidTagSourceKey value (the XID) from an import property
// array, matched by property id so a String8/binary type variant still resolves.
func importSourceKey(propvals []wire.TaggedPropertyValue) ([]byte, bool) {
	for _, pv := range propvals {
		if pv.Tag.ID() == wire.PidTagSourceKey.ID() {
			if b, ok := pv.Value.([]byte); ok {
				return b, true
			}
		}
	}
	return nil, false
}

// ropSyncImportMessageChange handles RopSynchronizationImportMessageChange (MS-OXCFXICS
// 2.2.3.2.4.2; MS-OXCROPS 2.2.13.2): the client uploads a message change by its source
// key, and the server opens a message object to receive the content the FastTransfer
// destination ROPs then stream. The source key's replica GUID decides the path: a key
// in THIS store's namespace whose message still exists is an in-place update (not yet
// supported — read/unread changes, the common mutation, use a separate ROP); any other
// key (a message composed on the client, or a vanished local id) is a new message. A new
// message is created in the collector's folder as a fresh in-flight object that acquires
// its server identity (uid, source key, change key) on RopSaveChangesMessage, exactly
// like RopCreateMessage; the client streams the content next and saves. The response's
// message id is 0 — the server assigns identity at save, which RopSaveChangesMessage
// returns.
func ropSyncImportMessageChange(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	_ = c.in.Uint8() // import_flags: associated/fail-on-conflict; the store carries only mail
	propvals, err := wire.PullTPropValArray(c.in)
	if err != nil || c.in.Err() != nil {
		writeRopError(c.out, RopSyncImportMessageChange, ohindex, ecError)
		return
	}
	if c.appender == nil {
		writeRopError(c.out, RopSyncImportMessageChange, ohindex, ecNotImplemented)
		return
	}
	col, ok := c.objectAt(hindex).(*syncCollectorObject)
	if !ok || !col.contents {
		writeRopError(c.out, RopSyncImportMessageChange, ohindex, ecNullObject)
		return
	}
	sourceKey, ok := importSourceKey(propvals)
	if !ok {
		writeRopError(c.out, RopSyncImportMessageChange, ohindex, ecError)
		return
	}
	guid, globcnt, perr := wire.ParseXID(sourceKey)
	if perr != nil {
		writeRopError(c.out, RopSyncImportMessageChange, ohindex, ecError)
		return
	}
	if guid == col.logon.replGUID {
		// A source key in this store's namespace: the GLOBCNT is the message uid. If
		// that message still exists, this is an in-place update — deferred (early-return
		// before binding any object), so no half-built message lands.
		if _, merr := c.store.GetMessageMetadata(c.email, col.mailbox, uint32(globcnt)); merr == nil {
			writeRopError(c.out, RopSyncImportMessageChange, ohindex, ecNotSupported)
			return
		}
	}
	c.setHandle(ohindex, c.state.alloc(&messageObject{
		mailbox: col.mailbox,
		write:   &messageWriteState{props: map[wire.PropTag]any{}},
	}))

	out := c.out
	out.Uint8(RopSyncImportMessageChange)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint64(0) // message id: the server assigns identity at save (returned by RopSaveChangesMessage)
}
