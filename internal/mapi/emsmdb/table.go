package emsmdb

import (
	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

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
	cursor  int            // next row index RopQueryRows reads
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

// Bookmark positions reported by RopQueryRows (MS-OXCTABL 2.2.2.1.2): where the
// table cursor came to rest after the read.
const (
	bookmarkCurrent uint8 = 0x01 // the cursor stopped in the middle of the table
	bookmarkEnd     uint8 = 0x02 // the cursor reached the end of the table
)

// ropQueryRows handles RopQueryRows (MS-OXCTABL 2.2.2.4): it returns up to the
// requested number of rows from the table's cursor forward, each row carrying the
// columns selected by RopSetColumns, and advances the cursor.
func ropQueryRows(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint8() // query flags
	_ = c.in.Uint8() // direction: only forward reads are served
	want := int(c.in.Uint16())
	if c.in.Err() != nil {
		writeRopError(c.out, RopQueryRows, hindex, ecError)
		return
	}
	tbl, ok := c.objectAt(hindex).(*tableObject)
	if !ok {
		writeRopError(c.out, RopQueryRows, hindex, ecNullObject)
		return
	}

	rows := wire.NewPush(wire.FlagUTF16)
	var count uint16
	for int(count) < want && tbl.cursor < len(tbl.uids) {
		uid := tbl.uids[tbl.cursor]
		tbl.cursor++
		m, err := c.store.GetMessageMetadata(c.email, tbl.mailbox, uid)
		if err != nil {
			continue // row vanished from the snapshot; skip it
		}
		if err := pushMessageRow(rows, tbl.columns, m); err != nil {
			writeRopError(c.out, RopQueryRows, hindex, ecError)
			return
		}
		count++
	}
	seek := bookmarkCurrent
	if tbl.cursor >= len(tbl.uids) {
		seek = bookmarkEnd
	}

	out := c.out
	out.Uint8(RopQueryRows)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(seek)
	out.Uint16(count)
	out.Raw(rows.Bytes())
}

// pushMessageRow serializes one message as a PropertyRow over the given columns
// (MS-OXCDATA 2.8.1). A row whose every column is available uses the compact
// untagged form; if any column is missing it uses the flagged form, marking the
// absent columns with an error code.
func pushMessageRow(p *wire.Push, cols []wire.PropTag, m *storage.MessageMetadata) error {
	values := make([]any, len(cols))
	allPresent := true
	for i, col := range cols {
		v, ok := messageProperty(col, m)
		values[i] = v
		if !ok {
			allPresent = false
		}
	}
	if allPresent {
		return wire.PushPropertyRow(p, cols, wire.PropertyRow{Flag: wire.RowFlagNone, Values: values})
	}
	flagged := make([]any, len(cols))
	for i := range cols {
		if values[i] != nil {
			flagged[i] = wire.FlaggedPropertyValue{Flag: wire.FlaggedAvailable, Value: values[i]}
		} else {
			flagged[i] = wire.FlaggedPropertyValue{Flag: wire.FlaggedError, Value: ecNotFound}
		}
	}
	return wire.PushPropertyRow(p, cols, wire.PropertyRow{Flag: wire.RowFlagFlagged, Values: flagged})
}
