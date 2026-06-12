package emsmdb

import (
	"sort"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// tableStatusComplete is the synchronous table status: the operation finished and
// the table is ready to read (MS-OXCTABL 2.2.2.1.3, TBLSTAT_COMPLETE).
const tableStatusComplete uint8 = 0x00

// tableKind selects what a table enumerates: a folder's messages or its child
// folders.
type tableKind uint8

const (
	contentsKind tableKind = iota
	hierarchyKind
)

// folderEntry is one snapshotted child folder of a hierarchy table.
type folderEntry struct {
	name string // IMAP-canonical mailbox name
	fid  uint64
}

// tableObject is a server object opened on a folder's contents or hierarchy
// (RopGetContentsTable / RopGetHierarchyTable). It snapshots the row identities
// (message uids, or child folders) at open time; the table ROPs that follow read
// columns and walk rows over this snapshot.
type tableObject struct {
	kind    tableKind
	mailbox string         // IMAP-canonical folder name; "" when the folder has no content
	uids    []uint32       // contents rows: message uids in store order
	folders []folderEntry  // hierarchy rows: child folders in list order
	columns []wire.PropTag // columns selected by RopSetColumns
	cursor  int            // next row index RopQueryRows reads
}

// rowCount returns the number of rows in the table's snapshot.
func (t *tableObject) rowCount() int {
	if t.kind == hierarchyKind {
		return len(t.folders)
	}
	return len(t.uids)
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
	tbl := &tableObject{kind: contentsKind, mailbox: fo.mailbox}
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

// ropGetHierarchyTable handles RopGetHierarchyTable (MS-OXCFOLD 2.2.1.13): it
// opens a table over a folder's child folders, binds it to the output handle
// index, and reports the row count. Only the IPM subtree exposes the mailbox's
// visible folders on the online path; the same client-hidden folders the IMAP and
// EWS surfaces suppress (the Recoverable Items dumpster) are excluded here too, so
// every surface presents one folder tree.
func ropGetHierarchyTable(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	_ = c.in.Uint8() // table flags: depth/associated options are not used online
	if c.in.Err() != nil {
		writeRopError(c.out, RopGetHierarchyTable, ohindex, ecError)
		return
	}
	fo, ok := c.objectAt(hindex).(*folderObject)
	if !ok {
		writeRopError(c.out, RopGetHierarchyTable, ohindex, ecNullObject)
		return
	}
	tbl := &tableObject{kind: hierarchyKind}
	if fo.special == sfIPMSubtree && fo.logon != nil {
		names, err := c.store.ListMailboxes(c.email)
		if err != nil {
			writeRopError(c.out, RopGetHierarchyTable, ohindex, ecError)
			return
		}
		for _, name := range names {
			if semcore.IsClientHiddenFolderName(name) {
				continue
			}
			tbl.folders = append(tbl.folders, folderEntry{name: name, fid: fo.logon.folderIDForName(name)})
		}
	}
	c.setHandle(ohindex, c.state.alloc(tbl))

	out := c.out
	out.Uint8(RopGetHierarchyTable)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint32(uint32(len(tbl.folders)))
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

// tableSortDescend is the descending sort order (MS-OXCTABL 2.2.1.3,
// TABLE_SORT_DESCEND); the default 0x00 is ascending.
const tableSortDescend uint8 = 0x01

// ropSortTable handles RopSortTable (MS-OXCTABL 2.2.2.3): it orders a contents
// table by the primary sort key, fetching the rows' sort property once. Only the
// first sort key drives the order; categorized/expanded sorting is not offered.
func ropSortTable(c *ropCtx, _ uint8, hindex uint8) {
	_ = c.in.Uint8() // table flags
	count := int(c.in.Uint16())
	_ = c.in.Uint16() // category count: categorized views not offered
	_ = c.in.Uint16() // expanded count
	var primaryTag wire.PropTag
	var primaryOrder uint8
	for i := range count {
		tag := wire.PropTag(c.in.Uint32())
		order := c.in.Uint8()
		if i == 0 {
			primaryTag, primaryOrder = tag, order
		}
	}
	if c.in.Err() != nil {
		writeRopError(c.out, RopSortTable, hindex, ecError)
		return
	}
	tbl, ok := c.objectAt(hindex).(*tableObject)
	if !ok {
		writeRopError(c.out, RopSortTable, hindex, ecNullObject)
		return
	}
	if tbl.kind == contentsKind && count > 0 {
		c.sortContents(tbl, primaryTag, primaryOrder == tableSortDescend)
	}
	tbl.cursor = 0 // a re-sort restarts the table at the first row

	out := c.out
	out.Uint8(RopSortTable)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint8(tableStatusComplete)
}

// sortContents reorders a contents table's uids by the given property, fetching
// each row's metadata once. Rows whose message vanished from the snapshot sort
// last; an unsupported sort property leaves the order unchanged.
func (c *ropCtx) sortContents(tbl *tableObject, tag wire.PropTag, desc bool) {
	meta := make(map[uint32]*storage.MessageMetadata, len(tbl.uids))
	for _, uid := range tbl.uids {
		if m, err := c.store.GetMessageMetadata(c.email, tbl.mailbox, uid); err == nil {
			meta[uid] = m
		}
	}
	sort.SliceStable(tbl.uids, func(i, j int) bool {
		a, b := meta[tbl.uids[i]], meta[tbl.uids[j]]
		if a == nil || b == nil {
			return a != nil // present rows before vanished ones
		}
		if desc {
			return lessByTag(tag, b, a)
		}
		return lessByTag(tag, a, b)
	})
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
	var (
		count uint16
		err   error
	)
	if tbl.kind == hierarchyKind {
		count, err = appendHierarchyRows(c, tbl, want, rows)
	} else {
		count, err = appendContentsRows(c, tbl, want, rows)
	}
	if err != nil {
		writeRopError(c.out, RopQueryRows, hindex, ecError)
		return
	}
	seek := bookmarkCurrent
	if tbl.cursor >= tbl.rowCount() {
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

// appendContentsRows emits up to want message rows from the cursor forward,
// advancing the cursor. A row whose message vanished from the snapshot is skipped.
func appendContentsRows(c *ropCtx, tbl *tableObject, want int, rows *wire.Push) (uint16, error) {
	var count uint16
	for int(count) < want && tbl.cursor < len(tbl.uids) {
		uid := tbl.uids[tbl.cursor]
		tbl.cursor++
		m, err := c.store.GetMessageMetadata(c.email, tbl.mailbox, uid)
		if err != nil {
			continue // row vanished from the snapshot; skip it
		}
		if err := pushRow(rows, tbl.columns, func(t wire.PropTag) (any, bool) {
			return messageProperty(t, m)
		}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// appendHierarchyRows emits up to want folder rows from the cursor forward,
// fetching each folder's live counts, and advancing the cursor.
func appendHierarchyRows(c *ropCtx, tbl *tableObject, want int, rows *wire.Push) (uint16, error) {
	var count uint16
	for int(count) < want && tbl.cursor < len(tbl.folders) {
		fe := tbl.folders[tbl.cursor]
		tbl.cursor++
		exists, _, unseen, err := c.store.GetMailboxCounts(c.email, fe.name)
		if err != nil {
			return count, err
		}
		fi := folderInfo{
			fid:     fe.fid,
			display: semcore.DisplayNameFromStorageName(fe.name),
			exists:  exists,
			unseen:  unseen,
		}
		if err := pushRow(rows, tbl.columns, func(t wire.PropTag) (any, bool) {
			return folderProperty(t, fi)
		}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// pushRow serializes one row over cols (MS-OXCDATA 2.8.1), resolving each column
// through value. A row whose every column is available uses the compact untagged
// form; if any column is missing it uses the flagged form, marking the absent
// columns with an error code.
func pushRow(p *wire.Push, cols []wire.PropTag, value func(wire.PropTag) (any, bool)) error {
	values := make([]any, len(cols))
	allPresent := true
	for i, col := range cols {
		v, ok := value(col)
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
