package emsmdb

import (
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// encodeLogonRequest builds a private-mailbox RopLogon request body.
func encodeLogonRequest() []byte {
	p := wire.NewPush(0)
	p.Uint8(0x01)        // logon flags (private)
	p.Uint32(0x01000000) // open flags
	p.Uint32(0)          // store state
	p.Uint16(0)          // essdn length (we identify by session email)
	return p.Bytes()
}

// TestRopLogon verifies a private-mailbox logon returns success, the documented
// special-folder ids, and the store identity, and registers a logon object.
func TestRopLogon(t *testing.T) {
	p := NewProcessor()
	sess := &Session{ID: "s", Email: "qa.bob@local.test"}

	rop := append([]byte{RopLogon, 0x00, 0x00}, encodeLogonRequest()...)
	resp, handles := p.Dispatch(sess, rop, []uint32{0xFFFFFFFF}, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopLogon {
		t.Fatalf("response rop id = %#x, want RopLogon", got)
	}
	if got := q.Uint8(); got != 0x00 {
		t.Errorf("handle index = %#x, want 0", got)
	}
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("return value = %#x, want success", rv)
	}
	q.Uint8() // logon flags echo
	var folderIDs [numSpecialFolders]uint64
	for i := range folderIDs {
		folderIDs[i] = q.Uint64()
	}
	if folderIDs[sfInbox] != makeFID(privateReplID, sfInbox+1) {
		t.Errorf("Inbox fid = %#x, want %#x", folderIDs[sfInbox], makeFID(privateReplID, sfInbox+1))
	}
	if folderIDs[sfIPMSubtree] == 0 {
		t.Error("IPM subtree fid should be non-zero")
	}
	q.Uint8() // response flags
	q.GUID()  // mailbox guid
	replID := q.Uint16()
	if replID != privateReplID {
		t.Errorf("replid = %d, want %d", replID, privateReplID)
	}
	q.GUID()   // repl guid
	q.Skip(8)  // logon time
	q.Uint64() // gwart time
	q.Uint32() // store state
	if q.Err() != nil {
		t.Fatalf("response parse error: %v", q.Err())
	}

	// The logon object must be registered and bound to the output handle slot.
	if len(handles) == 0 || handles[0] == 0xFFFFFFFF {
		t.Errorf("handle[0] = %#x, want an allocated handle", handles[0])
	}
	if _, ok := stateFor(sess).objects[handles[0]].(*logonObject); !ok {
		t.Error("no logon object registered at the output handle")
	}
}
