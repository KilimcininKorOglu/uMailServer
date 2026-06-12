package emsmdb

import (
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// logonSession runs RopLogon and returns the processor, session, and the handle
// table with the logon object bound at index 0, ready for folder ROPs.
func logonSession(t *testing.T) (*Processor, *Session, []uint32) {
	t.Helper()
	p := NewProcessor()
	sess := &Session{ID: "s", Email: "qa.bob@local.test"}
	rop := append([]byte{RopLogon, 0x00, 0x00}, encodeLogonRequest()...)
	_, handles := p.Dispatch(sess, rop, []uint32{0xFFFFFFFF}, 0x10000)
	if len(handles) == 0 || handles[0] == 0xFFFFFFFF {
		t.Fatal("logon did not bind a handle at index 0")
	}
	return p, sess, handles
}

// ropRequest frames a ROP header (id, logon id, input handle index) and body.
func ropRequest(ropID, hindex uint8, body []byte) []byte {
	return append([]byte{ropID, 0x00, hindex}, body...)
}

// TestGetReceiveFolderReturnsInbox verifies the receive folder for any class is
// the Inbox, reported with the default (all-class) empty message class.
func TestGetReceiveFolderReturnsInbox(t *testing.T) {
	p, sess, handles := logonSession(t)

	body := wire.NewPush(wire.FlagUTF16)
	body.Str("IPM.Note")
	resp, _ := p.Dispatch(sess, ropRequest(RopGetReceiveFolder, 0, body.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopGetReceiveFolder {
		t.Fatalf("rop id = %#x, want RopGetReceiveFolder", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("return value = %#x, want success", rv)
	}
	if fid := q.Uint64(); fid != makeFID(fidReplID, 0x0d) {
		t.Errorf("receive folder id = %#x, want the Inbox", fid)
	}
	if class := q.Str(); class != "" {
		t.Errorf("message class = %q, want empty (all classes)", class)
	}
	if q.Err() != nil {
		t.Fatalf("response parse error: %v", q.Err())
	}
}

// TestOpenFolderBindsInbox verifies opening the Inbox by id binds a folder object
// at the output handle index and reports a hosted, rule-free folder.
func TestOpenFolderBindsInbox(t *testing.T) {
	p, sess, handles := logonSession(t)

	inbox := makeFID(fidReplID, 0x0d)
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(1) // output handle index
	body.Uint64(inbox)
	body.Uint8(0) // open flags
	resp, handles := p.Dispatch(sess, ropRequest(RopOpenFolder, 0, body.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopOpenFolder {
		t.Fatalf("rop id = %#x, want RopOpenFolder", got)
	}
	if hi := q.Uint8(); hi != 1 {
		t.Errorf("output handle index = %d, want 1", hi)
	}
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("return value = %#x, want success", rv)
	}
	if hr := q.Uint8(); hr != 0 {
		t.Errorf("has rules = %d, want 0", hr)
	}
	if gh := q.Uint8(); gh != 0 {
		t.Errorf("is ghosted = %d, want 0", gh)
	}

	fo, ok := stateFor(sess).objects[handles[1]].(*folderObject)
	if !ok {
		t.Fatal("no folder object bound at the output handle")
	}
	if fo.folderID != inbox || fo.special != sfInbox {
		t.Errorf("folder object = {%#x, %d}, want {Inbox, sfInbox}", fo.folderID, fo.special)
	}
}

// TestOpenFolderUnknownIDFails verifies opening an id that is not one of the
// mailbox's folders fails with ecNotFound and binds no object.
func TestOpenFolderUnknownIDFails(t *testing.T) {
	p, sess, handles := logonSession(t)

	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(2) // output handle index
	body.Uint64(0xDEADBEEF)
	body.Uint8(0)
	resp, handles := p.Dispatch(sess, ropRequest(RopOpenFolder, 0, body.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopOpenFolder {
		t.Fatalf("rop id = %#x, want RopOpenFolder", got)
	}
	if hi := q.Uint8(); hi != 2 {
		t.Errorf("handle index = %d, want the output index 2", hi)
	}
	if rv := q.Uint32(); rv != ecNotFound {
		t.Errorf("return value = %#x, want ecNotFound", rv)
	}
	if len(handles) > 2 && handles[2] != 0xFFFFFFFF {
		t.Error("a folder object was bound despite the failure")
	}
}
