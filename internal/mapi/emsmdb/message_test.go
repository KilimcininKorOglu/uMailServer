package emsmdb

import (
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

// TestMessageIDRoundTrip verifies a uid survives the trip through a message id.
func TestMessageIDRoundTrip(t *testing.T) {
	for _, uid := range []uint32{1, 7, 0x0d, 0xFFFF, 0x12345678} {
		if got := messageUID(messageID(uid)); got != uid {
			t.Errorf("messageUID(messageID(%d)) = %d", uid, got)
		}
	}
}

// TestOpenMessageAndGetProperties verifies a message can be opened by id and its
// scalar properties read back from the canonical store.
func TestOpenMessageAndGetProperties(t *testing.T) {
	store := newFakeStore()
	store.put("INBOX", &storage.MessageMetadata{UID: 7, Subject: "hi", Size: 42, Flags: []string{"\\Seen"}})
	p, sess, handles := openInbox(t, store) // folder bound at handle index 1

	om := wire.NewPush(wire.FlagUTF16)
	om.Uint8(2)                         // output handle index for the message
	om.Uint16(1252)                     // code page id
	om.Uint64(makeFID(fidReplID, 0x0d)) // Inbox folder id
	om.Uint8(0)                         // open mode flags
	om.Uint64(messageID(7))             // message id
	resp, handles := p.Dispatch(sess, ropRequest(RopOpenMessage, 1, om.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopOpenMessage {
		t.Fatalf("rop id = %#x, want RopOpenMessage", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("open return value = %#x, want success", rv)
	}
	q.Uint8() // has named properties
	if pt := q.Uint8(); pt != stringTypeNone {
		t.Errorf("subject prefix type = %#x, want none", pt)
	}
	if nt := q.Uint8(); nt != stringTypeNone {
		t.Errorf("normalized subject type = %#x, want none", nt)
	}
	if rc := q.Uint16(); rc != 0 {
		t.Errorf("recipient count = %d, want 0", rc)
	}
	if cols := q.Uint16(); cols != 0 {
		t.Errorf("recipient columns = %d, want 0", cols)
	}
	if rows := q.Uint8(); rows != 0 {
		t.Errorf("recipient rows = %d, want 0", rows)
	}
	if q.Err() != nil {
		t.Fatalf("open response parse error: %v", q.Err())
	}

	// The message object must be bound at the output handle.
	if _, ok := stateFor(sess).objects[handles[2]].(*messageObject); !ok {
		t.Fatal("no message object bound at the output handle")
	}

	// Read the subject and size back through RopGetPropertiesSpecific.
	cols := []wire.PropTag{wire.PidTagSubject, wire.PidTagMessageSize}
	gp := wire.NewPush(wire.FlagUTF16)
	gp.Uint16(0) // size limit
	gp.Uint16(1) // want unicode
	wire.PushPropertyTagArray(gp, cols)
	resp2, _ := p.Dispatch(sess, ropRequest(RopGetPropertiesSpecific, 2, gp.Bytes()), handles, 0x10000)

	q2 := wire.NewPull(resp2, wire.FlagUTF16)
	if got := q2.Uint8(); got != RopGetPropertiesSpecific {
		t.Fatalf("rop id = %#x, want RopGetPropertiesSpecific", got)
	}
	q2.Uint8() // handle index
	if rv := q2.Uint32(); rv != ecSuccess {
		t.Fatalf("get properties return value = %#x, want success", rv)
	}
	row, err := wire.PullPropertyRow(q2, cols)
	if err != nil {
		t.Fatalf("property row decode: %v", err)
	}
	if subj, ok := row.Values[0].(string); !ok || subj != "hi" {
		t.Errorf("subject = %v, want hi", row.Values[0])
	}
	if sz, ok := row.Values[1].(uint32); !ok || sz != 42 {
		t.Errorf("size = %v, want 42", row.Values[1])
	}
}

// TestOpenMessageUnknownIDFails verifies opening a message id with no backing
// store record fails with ecNotFound.
func TestOpenMessageUnknownIDFails(t *testing.T) {
	store := newFakeStore()
	store.put("INBOX", &storage.MessageMetadata{UID: 1})
	p, sess, handles := openInbox(t, store)

	om := wire.NewPush(wire.FlagUTF16)
	om.Uint8(2)
	om.Uint16(1252)
	om.Uint64(makeFID(fidReplID, 0x0d))
	om.Uint8(0)
	om.Uint64(messageID(999)) // no such message
	resp, _ := p.Dispatch(sess, ropRequest(RopOpenMessage, 1, om.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecNotFound {
		t.Errorf("return value = %#x, want ecNotFound", rv)
	}
}
