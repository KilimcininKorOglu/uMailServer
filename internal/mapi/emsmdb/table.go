package emsmdb

import "github.com/umailserver/umailserver/internal/mapi/wire"

// tableStatusComplete is the synchronous table status: the operation finished and
// the table is ready to read (MS-OXCTABL 2.2.2.1.3, TBLSTAT_COMPLETE).
const tableStatusComplete uint8 = 0x00

// tableObject is a server object opened on a folder's contents
// (RopGetContentsTable). It snapshots the row identities (message uids) at open
// time; the table ROPs that follow read columns and walk rows over this
// snapshot.
type tableObject struct {
	mailbox string         // IMAP-canonical folder name; "" when the folder has no content
	uids    []uint32       // message uids in store order
	columns []wire.PropTag // columns selected by RopSetColumns
}

// ropGetContentsTable handles RopGetContentsTable (MS-OXCFOLD 2.2.1.14): it opens
// a table over the folder's messages, binds it to the request's output handle
// index, and reports the row count. A structural folder with no message content
// yields an empty table.
func ropGetContentsTable(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	_ = c.in.Uint8() // table flags: associated/depth options are not used online
	if c.in.Err() != nil {
		writeRopError(c.out, RopGetContentsTable, ohindex, ecError)
		return
	}
	fo, ok := c.objectAt(hindex).(*folderObject)
	if !ok {
		writeRopError(c.out, RopGetContentsTable, ohindex, ecNullObject)
		return
	}
	tbl := &tableObject{mailbox: storageFolderName(fo.special)}
	if tbl.mailbox != "" {
		uids, err := c.store.GetMessageUIDs(c.email, tbl.mailbox)
		if err != nil {
			writeRopError(c.out, RopGetContentsTable, ohindex, ecError)
			return
		}
		tbl.uids = uids
	}
	c.setHandle(ohindex, c.state.alloc(tbl))

	out := c.out
	out.Uint8(RopGetContentsTable)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint32(uint32(len(tbl.uids)))
}

// ropSetColumns handles RopSetColumns (MS-OXCTABL 2.2.2.2): it selects the
// property columns that later RopQueryRows responses carry for the table. The
// columns are applied synchronously, so the response reports a complete table.
func ropSetColumns(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint8() // set-columns flags: async apply is not offered
	cols := wire.PullPropertyTagArray(c.in)
	if c.in.Err() != nil {
		writeRopError(c.out, RopSetColumns, hindex, ecError)
		return
	}
	tbl, ok := c.objectAt(hindex).(*tableObject)
	if !ok {
		writeRopError(c.out, RopSetColumns, hindex, ecNullObject)
		return
	}
	tbl.columns = cols

	out := c.out
	out.Uint8(RopSetColumns)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(tableStatusComplete)
}
