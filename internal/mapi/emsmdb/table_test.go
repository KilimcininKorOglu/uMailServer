package emsmdb

import (
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

// openSpecialFolder logs on and opens the special folder with global counter gc,
// returning the processor, session, and handle table with the folder bound at
// index 1, backed by store.
func openSpecialFolder(t *testing.T, store Store, gc uint64) (*Processor, *Session, []uint32) {
	t.Helper()
	p := NewProcessor(store)
	sess := &Session{ID: "s", Email: "qa.bob@local.test"}
	logon := append([]byte{RopLogon, 0x00, 0x00}, encodeLogonRequest()...)
	_, handles := p.Dispatch(sess, logon, []uint32{0xFFFFFFFF}, 0x10000)

	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(1) // output handle index for the folder
	body.Uint64(makeFID(fidReplID, gc))
	body.Uint8(0) // open flags
	_, handles = p.Dispatch(sess, ropRequest(RopOpenFolder, 0, body.Bytes()), handles, 0x10000)
	return p, sess, handles
}

// openInbox logs on and opens the Inbox (global counter 0x0d).
func openInbox(t *testing.T, store Store) (*Processor, *Session, []uint32) {
	t.Helper()
	return openSpecialFolder(t, store, 0x0d)
}

// openContentsTable opens the Inbox contents table at handle index 2.
func openContentsTable(t *testing.T, store Store) (*Processor, *Session, []uint32) {
	t.Helper()
	p, sess, handles := openInbox(t, store)
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(2) // output handle index for the table
	body.Uint8(0) // table flags
	_, handles = p.Dispatch(sess, ropRequest(RopGetContentsTable, 1, body.Bytes()), handles, 0x10000)
	return p, sess, handles
}

// TestGetContentsTableReportsRowCount verifies the contents table over the Inbox
// reports the message count from the canonical store and snapshots the uids.
func TestGetContentsTableReportsRowCount(t *testing.T) {
	store := newFakeStore()
	store.put("INBOX", &storage.MessageMetadata{UID: 1, Subject: "one", InternalDate: time.Unix(1700000000, 0), Size: 100})
	store.put("INBOX", &storage.MessageMetadata{UID: 2, Subject: "two", InternalDate: time.Unix(1700000100, 0), Size: 200})
	p, sess, handles := openInbox(t, store)

	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(2) // output handle index for the table
	body.Uint8(0) // table flags
	resp, handles := p.Dispatch(sess, ropRequest(RopGetContentsTable, 1, body.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopGetContentsTable {
		t.Fatalf("rop id = %#x, want RopGetContentsTable", got)
	}
	if hi := q.Uint8(); hi != 2 {
		t.Errorf("output handle index = %d, want 2", hi)
	}
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("return value = %#x, want success", rv)
	}
	if rc := q.Uint32(); rc != 2 {
		t.Errorf("row count = %d, want 2", rc)
	}

	tbl, ok := stateFor(sess).objects[handles[2]].(*tableObject)
	if !ok {
		t.Fatal("no table object bound at the output handle")
	}
	if tbl.mailbox != "INBOX" || len(tbl.uids) != 2 {
		t.Errorf("table = {%q, %d uids}, want {INBOX, 2}", tbl.mailbox, len(tbl.uids))
	}
}

// TestSetColumnsAppliesColumns verifies RopSetColumns records the column set on
// the table and reports a complete, ready-to-read table.
func TestSetColumnsAppliesColumns(t *testing.T) {
	store := newFakeStore()
	store.put("INBOX", &storage.MessageMetadata{UID: 1})
	p, sess, handles := openContentsTable(t, store)

	cols := []wire.PropTag{wire.PidTagMid, wire.PidTagSubject, wire.PidTagMessageDeliveryTime}
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(0) // set-columns flags
	wire.PushPropertyTagArray(sc, cols)
	resp, _ := p.Dispatch(sess, ropRequest(RopSetColumns, 2, sc.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopSetColumns {
		t.Fatalf("rop id = %#x, want RopSetColumns", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("return value = %#x, want success", rv)
	}
	if ts := q.Uint8(); ts != tableStatusComplete {
		t.Errorf("table status = %#x, want complete", ts)
	}

	tbl, ok := stateFor(sess).objects[handles[2]].(*tableObject)
	if !ok {
		t.Fatal("no table object at handle 2")
	}
	if len(tbl.columns) != len(cols) {
		t.Fatalf("columns = %d, want %d", len(tbl.columns), len(cols))
	}
	for i, c := range cols {
		if tbl.columns[i] != c {
			t.Errorf("column[%d] = %#x, want %#x", i, tbl.columns[i], c)
		}
	}
}

// setColumns selects cols on the table at handle index 2.
func setColumns(t *testing.T, p *Processor, sess *Session, handles []uint32, cols []wire.PropTag) {
	t.Helper()
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(0)
	wire.PushPropertyTagArray(sc, cols)
	p.Dispatch(sess, ropRequest(RopSetColumns, 2, sc.Bytes()), handles, 0x10000)
}

// queryRows reads up to want rows from the table at handle index 2.
func queryRows(p *Processor, sess *Session, handles []uint32, want uint16) []byte {
	qr := wire.NewPush(wire.FlagUTF16)
	qr.Uint8(0)     // flags
	qr.Uint8(1)     // forward read
	qr.Uint16(want) // rows wanted
	resp, _ := p.Dispatch(sess, ropRequest(RopQueryRows, 2, qr.Bytes()), handles, 0x10000)
	return resp
}

// TestQueryRowsReturnsMessageRows verifies RopQueryRows maps stored message
// metadata onto the selected columns and returns compact untagged rows when every
// column is present.
func TestQueryRowsReturnsMessageRows(t *testing.T) {
	store := newFakeStore()
	store.put("INBOX", &storage.MessageMetadata{UID: 7, Subject: "hello", InternalDate: time.Unix(1700000000, 0).UTC(), Size: 1234, Flags: []string{"\\Seen"}})
	store.put("INBOX", &storage.MessageMetadata{UID: 9, Subject: "world", InternalDate: time.Unix(1700000500, 0).UTC(), Size: 5678})
	p, sess, handles := openContentsTable(t, store)

	cols := []wire.PropTag{wire.PidTagMid, wire.PidTagSubject, wire.PidTagMessageSize, wire.PidTagMessageFlags}
	setColumns(t, p, sess, handles, cols)
	resp := queryRows(p, sess, handles, 100)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopQueryRows {
		t.Fatalf("rop id = %#x, want RopQueryRows", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("return value = %#x, want success", rv)
	}
	if seek := q.Uint8(); seek != bookmarkEnd {
		t.Errorf("seek pos = %#x, want bookmarkEnd", seek)
	}
	if count := q.Uint16(); count != 2 {
		t.Fatalf("row count = %d, want 2", count)
	}

	row, err := wire.PullPropertyRow(q, cols)
	if err != nil {
		t.Fatalf("row 0 decode: %v", err)
	}
	if row.Flag != wire.RowFlagNone {
		t.Errorf("row 0 flag = %#x, want RowFlagNone", row.Flag)
	}
	if mid, ok := row.Values[0].(uint64); !ok || mid != messageID(7) {
		t.Errorf("row 0 MID = %v, want %#x", row.Values[0], messageID(7))
	}
	if subj, ok := row.Values[1].(string); !ok || subj != "hello" {
		t.Errorf("row 0 subject = %v, want hello", row.Values[1])
	}
	if sz, ok := row.Values[2].(uint32); !ok || sz != 1234 {
		t.Errorf("row 0 size = %v, want 1234", row.Values[2])
	}
	if fl, ok := row.Values[3].(uint32); !ok || fl != msgFlagRead {
		t.Errorf("row 0 flags = %v, want read", row.Values[3])
	}

	row2, err := wire.PullPropertyRow(q, cols)
	if err != nil {
		t.Fatalf("row 1 decode: %v", err)
	}
	if subj, ok := row2.Values[1].(string); !ok || subj != "world" {
		t.Errorf("row 1 subject = %v, want world", row2.Values[1])
	}
	if fl, ok := row2.Values[3].(uint32); !ok || fl != 0 {
		t.Errorf("row 1 flags = %v, want 0 (unread)", row2.Values[3])
	}
	if q.Err() != nil {
		t.Fatalf("trailing parse error: %v", q.Err())
	}
}

// TestQueryRowsFlagsMissingColumn verifies a row with an unmapped column uses the
// flagged form, marking the present column available and the missing one in error.
func TestQueryRowsFlagsMissingColumn(t *testing.T) {
	store := newFakeStore()
	store.put("INBOX", &storage.MessageMetadata{UID: 1, Subject: "x"})
	p, sess, handles := openContentsTable(t, store)

	body := wire.MakeTag(0x1000, wire.PtUnicode) // PidTagBody: not mapped on the online path
	cols := []wire.PropTag{wire.PidTagSubject, body}
	setColumns(t, p, sess, handles, cols)
	resp := queryRows(p, sess, handles, 100)

	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8()  // rop id
	q.Uint8()  // handle index
	q.Uint32() // return value
	q.Uint8()  // seek pos
	if count := q.Uint16(); count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
	row, err := wire.PullPropertyRow(q, cols)
	if err != nil {
		t.Fatalf("row decode: %v", err)
	}
	if row.Flag != wire.RowFlagFlagged {
		t.Fatalf("row flag = %#x, want RowFlagFlagged", row.Flag)
	}
	present, ok := row.Values[0].(wire.FlaggedPropertyValue)
	if !ok || present.Flag != wire.FlaggedAvailable {
		t.Fatalf("subject column = %+v, want available", row.Values[0])
	}
	if s, sok := present.Value.(string); !sok || s != "x" {
		t.Errorf("subject value = %v, want \"x\"", present.Value)
	}
	missing, ok := row.Values[1].(wire.FlaggedPropertyValue)
	if !ok || missing.Flag != wire.FlaggedError {
		t.Errorf("body column = %+v, want error flag", row.Values[1])
	}
}

// TestSortTableOrdersByDeliveryTime verifies RopSortTable reorders the contents
// table so a descending delivery-time sort returns the newest message first.
func TestSortTableOrdersByDeliveryTime(t *testing.T) {
	store := newFakeStore()
	store.put("INBOX", &storage.MessageMetadata{UID: 1, InternalDate: time.Unix(1000, 0)})
	store.put("INBOX", &storage.MessageMetadata{UID: 2, InternalDate: time.Unix(3000, 0)})
	store.put("INBOX", &storage.MessageMetadata{UID: 3, InternalDate: time.Unix(2000, 0)})
	p, sess, handles := openContentsTable(t, store)

	st := wire.NewPush(wire.FlagUTF16)
	st.Uint8(0)  // table flags
	st.Uint16(1) // one sort key
	st.Uint16(0) // categories
	st.Uint16(0) // expanded
	st.Uint32(uint32(wire.PidTagMessageDeliveryTime))
	st.Uint8(tableSortDescend)
	resp := mustDispatch(p, sess, ropRequest(RopSortTable, 2, st.Bytes()), handles)
	sq := wire.NewPull(resp, wire.FlagUTF16)
	if got := sq.Uint8(); got != RopSortTable {
		t.Fatalf("rop id = %#x, want RopSortTable", got)
	}
	sq.Uint8() // handle index
	if rv := sq.Uint32(); rv != ecSuccess {
		t.Fatalf("sort return value = %#x, want success", rv)
	}
	if ts := sq.Uint8(); ts != tableStatusComplete {
		t.Errorf("table status = %#x, want complete", ts)
	}

	setColumns(t, p, sess, handles, []wire.PropTag{wire.PidTagMid})
	rows := queryRows(p, sess, handles, 100)
	q := wire.NewPull(rows, wire.FlagUTF16)
	q.Uint8()
	q.Uint8()
	q.Uint32()
	q.Uint8()
	if count := q.Uint16(); count != 3 {
		t.Fatalf("row count = %d, want 3", count)
	}
	want := []uint32{2, 3, 1} // delivery times 3000 > 2000 > 1000
	for i, uid := range want {
		row, err := wire.PullPropertyRow(q, []wire.PropTag{wire.PidTagMid})
		if err != nil {
			t.Fatalf("row %d decode: %v", i, err)
		}
		if mid, ok := row.Values[0].(uint64); !ok || mid != messageID(uid) {
			t.Errorf("row %d MID = %v, want uid %d", i, row.Values[0], uid)
		}
	}
}

// mustDispatch runs one request and returns the response bytes.
func mustDispatch(p *Processor, sess *Session, req []byte, handles []uint32) []byte {
	resp, _ := p.Dispatch(sess, req, handles, 0x10000)
	return resp
}

// openHierarchyTable opens the IPM subtree hierarchy table at handle index 2.
func openHierarchyTable(t *testing.T, store Store) (*Processor, *Session, []uint32) {
	t.Helper()
	p, sess, handles := openSpecialFolder(t, store, 0x09) // IPM subtree
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(2) // output handle index for the table
	body.Uint8(0) // table flags
	resp, handles := p.Dispatch(sess, ropRequest(RopGetHierarchyTable, 1, body.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopGetHierarchyTable {
		t.Fatalf("rop id = %#x, want RopGetHierarchyTable", got)
	}
	q.Uint8() // output handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("hierarchy return value = %#x, want success", rv)
	}
	if rc := q.Uint32(); rc != 3 {
		t.Fatalf("hierarchy row count = %d, want 3 (INBOX, Sent, Drafts; Recoverable Items hidden)", rc)
	}
	return p, sess, handles
}

// hierarchyStore builds a store whose visible folders are INBOX (2 messages, one
// unread), Sent, and Drafts, plus the client-hidden Recoverable Items dumpster.
func hierarchyStore() *fakeStore {
	store := newFakeStore()
	store.addMailbox("INBOX")
	store.addMailbox("Sent")
	store.addMailbox("Drafts")
	store.addMailbox("Recoverable Items") // client-hidden: must not appear
	store.put("INBOX", &storage.MessageMetadata{UID: 1})
	store.put("INBOX", &storage.MessageMetadata{UID: 2, Flags: []string{"\\Seen"}})
	return store
}

// TestGetHierarchyTableHidesRecoverableItems verifies the IPM subtree hierarchy
// lists the visible folders with their counts and excludes client-hidden folders
// (Recoverable Items), matching the IMAP and EWS surfaces.
func TestGetHierarchyTableHidesRecoverableItems(t *testing.T) {
	p, sess, handles := openHierarchyTable(t, hierarchyStore())

	cols := []wire.PropTag{wire.PidTagFolderId, wire.PidTagContentCount, wire.PidTagContentUnreadCount}
	setColumns(t, p, sess, handles, cols)
	resp := queryRows(p, sess, handles, 100)

	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8()  // rop id
	q.Uint8()  // handle index
	q.Uint32() // return value
	q.Uint8()  // seek pos
	count := q.Uint16()
	if count != 3 {
		t.Fatalf("queried rows = %d, want 3", count)
	}

	type counts struct{ cc, uc uint32 }
	byFID := map[uint64]counts{}
	for i := 0; i < int(count); i++ {
		row, err := wire.PullPropertyRow(q, cols)
		if err != nil {
			t.Fatalf("row %d decode: %v", i, err)
		}
		fid, okf := row.Values[0].(uint64)
		cc, okc := row.Values[1].(uint32)
		uc, oku := row.Values[2].(uint32)
		if !okf || !okc || !oku {
			t.Fatalf("row %d has unexpected value types: %+v", i, row.Values)
		}
		byFID[fid] = counts{cc, uc}
	}

	// The Inbox keeps its special id and reports live counts (2 total, 1 unread).
	if got, ok := byFID[makeFID(fidReplID, 0x0d)]; !ok || got.cc != 2 || got.uc != 1 {
		t.Errorf("Inbox row = %+v (present=%v), want {cc:2 uc:1}", got, ok)
	}
	// Sent keeps its special id; Drafts gets the first custom counter.
	if _, ok := byFID[makeFID(fidReplID, 0x0a)]; !ok {
		t.Error("Sent folder missing from hierarchy")
	}
	if _, ok := byFID[makeFID(fidReplID, customFolderGCBase)]; !ok {
		t.Error("Drafts folder missing its custom folder id")
	}
}

// TestOpenCustomFolderAfterEnumeration verifies a custom folder learned from the
// hierarchy table can be opened by its id and its contents read, proving the
// folder registry round-trips a synthesized id back to its mailbox.
func TestOpenCustomFolderAfterEnumeration(t *testing.T) {
	store := hierarchyStore()
	store.put("Drafts", &storage.MessageMetadata{UID: 5, Subject: "draft"})
	p, sess, handles := openHierarchyTable(t, store)

	// Open the custom Drafts folder by the id the hierarchy assigned it.
	draftsFID := makeFID(fidReplID, customFolderGCBase)
	of := wire.NewPush(wire.FlagUTF16)
	of.Uint8(3) // output handle index for the folder
	of.Uint64(draftsFID)
	of.Uint8(0)
	resp, handles := p.Dispatch(sess, ropRequest(RopOpenFolder, 0, of.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopOpenFolder {
		t.Fatalf("rop id = %#x, want RopOpenFolder", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("open custom folder = %#x, want success", rv)
	}

	// Its contents table must read the Drafts mailbox.
	gct := wire.NewPush(wire.FlagUTF16)
	gct.Uint8(4) // output handle index for the table
	gct.Uint8(0)
	resp, _ = p.Dispatch(sess, ropRequest(RopGetContentsTable, 3, gct.Bytes()), handles, 0x10000)
	q = wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8()  // rop id
	q.Uint8()  // handle index
	q.Uint32() // return value
	if rc := q.Uint32(); rc != 1 {
		t.Errorf("Drafts content count = %d, want 1", rc)
	}
}
