package emsmdb

import (
	"math/bits"
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
	p := NewProcessor(newFakeStore())
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
	// The Inbox id must decode to the spec wire layout: a 2-byte little-endian
	// replica id then a 6-byte big-endian global counter (MS-OXCDATA 2.2.1.1),
	// carrying the canonical Inbox counter 0x0d (MS-OXCSTOR 2.2.1.1.3).
	inbox := folderIDs[sfInbox]
	if got := uint16(inbox & 0xFFFF); got != fidReplID {
		t.Errorf("Inbox replica id = %d, want %d", got, fidReplID)
	}
	if got := bits.ReverseBytes64(inbox &^ 0xFFFF); got != 0x0d {
		t.Errorf("Inbox global counter = %#x, want 0x0d", got)
	}
	if folderIDs[sfIPMSubtree] == 0 {
		t.Error("IPM subtree fid should be non-zero")
	}
	q.Uint8() // response flags
	q.GUID()  // mailbox guid
	if replID := q.Uint16(); replID != logonReplID {
		t.Errorf("response replica id = %d, want %d", replID, logonReplID)
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
