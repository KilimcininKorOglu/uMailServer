package emsmdb

import (
	"slices"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// fakeMutator records the canonical-mutation calls the content ROPs make and
// returns a configurable outcome, so the ROP handlers can be exercised without a
// real mailstore (the mailstore-backed mutator lives in the server package).
type fakeMutator struct {
	gotUser   string
	gotFolder string
	gotUIDs   []uint32
	removed   int
	err       error
}

func (f *fakeMutator) DeleteMessages(user, folder string, uids []uint32) (int, error) {
	f.gotUser, f.gotFolder, f.gotUIDs = user, folder, uids
	if f.err != nil {
		return 0, f.err
	}
	return f.removed, nil
}

// deleteMessagesBody builds a RopDeleteMessages request body: want_asynchronous,
// notify_non_read, then the message-id array (u16 count + 64-bit ids).
func deleteMessagesBody(uids []uint32) []byte {
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(0)                  // want_asynchronous: synchronous
	body.Uint8(0)                  // notify_non_read
	body.Uint16(uint16(len(uids))) // EntryID count
	for _, uid := range uids {
		body.Uint64(messageID(uid))
	}
	return body.Bytes()
}

// TestDeleteMessagesFlow verifies RopDeleteMessages parses the message-id array,
// routes the canonical delete through the mutator with the folder's mailbox and the
// uids inverted from the MIDs, and reports no partial completion when every message
// is removed.
func TestDeleteMessagesFlow(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store) // Inbox folder bound at handle index 1
	fm := &fakeMutator{removed: 2}
	p.SetMutator(fm)

	uids := []uint32{5, 7}
	resp, _ := p.Dispatch(sess, ropRequest(RopDeleteMessages, 1, deleteMessagesBody(uids)), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopDeleteMessages {
		t.Fatalf("rop id = %#x, want RopDeleteMessages", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("DeleteMessages return value = %#x, want success", rv)
	}
	if pc := q.Uint8(); pc != 0 {
		t.Errorf("partial completion = %d, want 0 (every message removed)", pc)
	}
	if fm.gotFolder != "INBOX" {
		t.Errorf("mutator folder = %q, want INBOX", fm.gotFolder)
	}
	if !slices.Equal(fm.gotUIDs, uids) {
		t.Errorf("mutator uids = %v, want %v (inverted from the MIDs)", fm.gotUIDs, uids)
	}
}

// TestDeleteMessagesPartial verifies the response reports partial completion when
// the mutator removed fewer messages than were requested (e.g. one had already been
// deleted on another surface).
func TestDeleteMessagesPartial(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	fm := &fakeMutator{removed: 1} // only one of the two requested removed
	p.SetMutator(fm)

	resp, _ := p.Dispatch(sess, ropRequest(RopDeleteMessages, 1, deleteMessagesBody([]uint32{5, 7})), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("DeleteMessages return value = %#x, want success", rv)
	}
	if pc := q.Uint8(); pc != 1 {
		t.Errorf("partial completion = %d, want 1 (not every message removed)", pc)
	}
}

// TestDeleteMessagesWithoutMutator verifies the ROP reports the operation as
// unsupported when no mutation core is wired, rather than panicking or silently
// dropping the request.
func TestDeleteMessagesWithoutMutator(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store) // no SetMutator

	resp, _ := p.Dispatch(sess, ropRequest(RopDeleteMessages, 1, deleteMessagesBody([]uint32{5})), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecNotImplemented {
		t.Errorf("DeleteMessages without mutator = %#x, want ecNotImplemented", rv)
	}
}
