package emsmdb

import (
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

// openInbox logs on and opens the Inbox, returning the processor, session, and
// handle table with the folder object bound at index 1, backed by store.
func openInbox(t *testing.T, store Store) (*Processor, *Session, []uint32) {
	t.Helper()
	p := NewProcessor(store)
	sess := &Session{ID: "s", Email: "qa.bob@local.test"}
	logon := append([]byte{RopLogon, 0x00, 0x00}, encodeLogonRequest()...)
	_, handles := p.Dispatch(sess, logon, []uint32{0xFFFFFFFF}, 0x10000)

	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(1)                         // output handle index for the folder
	body.Uint64(makeFID(fidReplID, 0x0d)) // Inbox
	body.Uint8(0)                         // open flags
	_, handles = p.Dispatch(sess, ropRequest(RopOpenFolder, 0, body.Bytes()), handles, 0x10000)
	return p, sess, handles
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
