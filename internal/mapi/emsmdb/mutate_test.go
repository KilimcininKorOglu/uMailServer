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
	gotSrc    string
	gotDst    string
	gotUIDs   []uint32
	gotCopy   bool
	removed   int
	relocated int
	err       error
}

func (f *fakeMutator) DeleteMessages(user, folder string, uids []uint32) (int, error) {
	f.gotUser, f.gotFolder, f.gotUIDs = user, folder, uids
	if f.err != nil {
		return 0, f.err
	}
	return f.removed, nil
}

func (f *fakeMutator) MoveMessages(user, srcFolder, dstFolder string, uids []uint32) (int, error) {
	f.gotUser, f.gotSrc, f.gotDst, f.gotUIDs, f.gotCopy = user, srcFolder, dstFolder, uids, false
	if f.err != nil {
		return 0, f.err
	}
	return f.relocated, nil
}

func (f *fakeMutator) CopyMessages(user, srcFolder, dstFolder string, uids []uint32) (int, error) {
	f.gotUser, f.gotSrc, f.gotDst, f.gotUIDs, f.gotCopy = user, srcFolder, dstFolder, uids, true
	if f.err != nil {
		return 0, f.err
	}
	return f.relocated, nil
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

// moveCopyBody builds a RopMoveCopyMessages request body: the destination handle
// index, the message-id array (u16 count + 64-bit ids), want_asynchronous, and
// want_copy.
func moveCopyBody(dhindex uint8, uids []uint32, wantCopy uint8) []byte {
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(dhindex)            // destination folder handle index
	body.Uint16(uint16(len(uids))) // EntryID count
	for _, uid := range uids {
		body.Uint64(messageID(uid))
	}
	body.Uint8(0)        // want_asynchronous: synchronous
	body.Uint8(wantCopy) // want_copy
	return body.Bytes()
}

// openSentFolderHandle opens the Sent folder (global counter 0x0a) at the given
// output handle index, so a move/copy test has a real destination folder object.
func openSentFolderHandle(t *testing.T, p *Processor, sess *Session, handles []uint32, ohindex uint8) []uint32 {
	t.Helper()
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(ohindex)
	body.Uint64(makeFID(fidReplID, 0x0a))
	body.Uint8(0) // open flags
	_, handles = p.Dispatch(sess, ropRequest(RopOpenFolder, 0, body.Bytes()), handles, 0x10000)
	return handles
}

// TestMoveCopyMessagesMove drives RopMoveCopyMessages with want_copy=0 and verifies
// the source folder, destination folder, and uids reach the mutator's move path,
// with no partial completion when every message is relocated.
func TestMoveCopyMessagesMove(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store) // Inbox (source) at handle 1
	fm := &fakeMutator{relocated: 2}
	p.SetMutator(fm)
	handles = openSentFolderHandle(t, p, sess, handles, 2) // Sent (destination) at handle 2

	uids := []uint32{5, 7}
	resp, _ := p.Dispatch(sess, ropRequest(RopMoveCopyMessages, 1, moveCopyBody(2, uids, 0)), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopMoveCopyMessages {
		t.Fatalf("rop id = %#x, want RopMoveCopyMessages", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("MoveCopyMessages return value = %#x, want success", rv)
	}
	if pc := q.Uint8(); pc != 0 {
		t.Errorf("partial completion = %d, want 0 (every message moved)", pc)
	}
	if fm.gotCopy {
		t.Error("want_copy=0 must call the move path, not the copy path")
	}
	if fm.gotSrc != "INBOX" || fm.gotDst != "Sent" {
		t.Errorf("move src/dst = %q/%q, want INBOX/Sent", fm.gotSrc, fm.gotDst)
	}
	if !slices.Equal(fm.gotUIDs, uids) {
		t.Errorf("move uids = %v, want %v (inverted from the MIDs)", fm.gotUIDs, uids)
	}
}

// TestMoveCopyMessagesCopy verifies want_copy=1 routes to the mutator's copy path
// (the source is left intact) with the source and destination folders resolved.
func TestMoveCopyMessagesCopy(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	fm := &fakeMutator{relocated: 1}
	p.SetMutator(fm)
	handles = openSentFolderHandle(t, p, sess, handles, 2)

	resp, _ := p.Dispatch(sess, ropRequest(RopMoveCopyMessages, 1, moveCopyBody(2, []uint32{9}, 1)), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("MoveCopyMessages return value = %#x, want success", rv)
	}
	if !fm.gotCopy {
		t.Error("want_copy=1 must call the copy path, not the move path")
	}
	if fm.gotSrc != "INBOX" || fm.gotDst != "Sent" {
		t.Errorf("copy src/dst = %q/%q, want INBOX/Sent", fm.gotSrc, fm.gotDst)
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
