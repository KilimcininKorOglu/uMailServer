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
	gotCreate string
	gotDelete string
	gotEmpty  string
	created   bool // CreateFolder returns this as "existed"
	removed   int
	relocated int
	emptyLeft int
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

func (f *fakeMutator) CreateFolder(user, mailbox string) (bool, error) {
	f.gotUser, f.gotCreate = user, mailbox
	return f.created, f.err
}

func (f *fakeMutator) DeleteFolder(user, mailbox string) error {
	f.gotUser, f.gotDelete = user, mailbox
	return f.err
}

func (f *fakeMutator) EmptyFolder(user, folder string) (int, error) {
	f.gotUser, f.gotEmpty = user, folder
	if f.err != nil {
		return 0, f.err
	}
	return f.emptyLeft, nil
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

// TestCreateFolderFlow drives RopCreateFolder under the IPM subtree and verifies the
// folder is created top-level, bound at the output handle, and — critically — that
// the returned folder id resolves back to the folder so a later RopOpenFolder can
// reopen it.
func TestCreateFolderFlow(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openSpecialFolder(t, store, 0x09) // IPM subtree (parent) at handle 1
	fm := &fakeMutator{created: false}
	p.SetMutator(fm)

	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(2) // output handle index
	body.Uint8(1) // folder_type: generic
	body.Uint8(1) // use_unicode
	body.Uint8(0) // open_existing
	body.Uint8(0) // reserved
	body.WStr("Projects")
	body.WStr("") // comment
	resp, handles := p.Dispatch(sess, ropRequest(RopCreateFolder, 1, body.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopCreateFolder {
		t.Fatalf("rop id = %#x, want RopCreateFolder", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("CreateFolder return value = %#x, want success", rv)
	}
	fid := q.Uint64()
	if isExisting := q.Uint8(); isExisting != 0 {
		t.Errorf("is_existing = %d, want 0 (a new folder)", isExisting)
	}
	if fm.gotCreate != "Projects" {
		t.Errorf("created mailbox = %q, want Projects (top-level under the IPM subtree)", fm.gotCreate)
	}
	fo, ok := stateFor(sess).objects[handles[2]].(*folderObject)
	if !ok || fo.mailbox != "Projects" {
		t.Fatalf("new folder object not bound at the output handle: %+v", fo)
	}
	if mb, _, rok := fo.logon.resolveFolder(fid); !rok || mb != "Projects" {
		t.Errorf("resolveFolder(returned id) = (%q, %v), want (Projects, true) so RopOpenFolder can reopen it", mb, rok)
	}
}

// TestDeleteFolderRejectsSpecial verifies a special/distinguished folder (the Inbox)
// cannot be deleted, and that such a request never reaches the mutator.
func TestDeleteFolderRejectsSpecial(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store) // folder at handle 1 carries the logon
	fm := &fakeMutator{}
	p.SetMutator(fm)

	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(0)                         // flags
	body.Uint64(makeFID(fidReplID, 0x0d)) // the Inbox — a special folder
	resp, _ := p.Dispatch(sess, ropRequest(RopDeleteFolder, 1, body.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecAccessDenied {
		t.Errorf("delete of a special folder = %#x, want ecAccessDenied", rv)
	}
	if fm.gotDelete != "" {
		t.Error("a special-folder delete must not reach the mutator")
	}
}

// TestEmptyFolderFlow drives RopEmptyFolder over the Inbox and verifies it routes to
// the mutator's empty path with no partial completion when the folder is fully
// emptied.
func TestEmptyFolderFlow(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	fm := &fakeMutator{emptyLeft: 0}
	p.SetMutator(fm)

	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(0) // want_asynchronous
	body.Uint8(0) // want_delete_associated
	resp, _ := p.Dispatch(sess, ropRequest(RopEmptyFolder, 1, body.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopEmptyFolder {
		t.Fatalf("rop id = %#x, want RopEmptyFolder", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("EmptyFolder return value = %#x, want success", rv)
	}
	if pc := q.Uint8(); pc != 0 {
		t.Errorf("partial completion = %d, want 0 (fully emptied)", pc)
	}
	if fm.gotEmpty != "INBOX" {
		t.Errorf("emptied folder = %q, want INBOX", fm.gotEmpty)
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
