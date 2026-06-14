package emsmdb

import (
	"bytes"
	"net/mail"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

// openCollector drives RopOpenFolder(Inbox) -> RopSynchronizationOpenCollector and
// returns the handle table with the contents collector bound at index 2.
func openCollector(t *testing.T, store Store) (*Processor, *Session, []uint32) {
	t.Helper()
	p, sess, handles := openInbox(t, store) // folder at handle index 1
	oc := wire.NewPush(0)
	oc.Uint8(2) // output handle index for the collector
	oc.Uint8(1) // is_content_collector
	_, handles = p.Dispatch(sess, ropRequest(RopSyncOpenCollector, 1, oc.Bytes()), handles, 0x10000)
	return p, sess, handles
}

// importMessageChangeRequest builds a RopSynchronizationImportMessageChange body: the
// output handle index, import flags, then a property array carrying the source key.
func importMessageChangeRequest(t *testing.T, ohindex uint8, sourceKey []byte) []byte {
	t.Helper()
	arr := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(arr, []wire.TaggedPropertyValue{
		{Tag: wire.PidTagSourceKey, Value: sourceKey},
	}); err != nil {
		t.Fatalf("push source-key array: %v", err)
	}
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(ohindex)
	body.Uint8(0) // import flags
	body.Raw(arr.Bytes())
	return body.Bytes()
}

// openHierarchyCollector drives RopOpenFolder(Inbox) -> RopSynchronizationOpenCollector
// with is_content_collector=0 and returns the handle table with the hierarchy collector
// bound at index 2.
func openHierarchyCollector(t *testing.T, store Store) (*Processor, *Session, []uint32) {
	t.Helper()
	p, sess, handles := openInbox(t, store) // folder at handle index 1
	oc := wire.NewPush(0)
	oc.Uint8(2) // output handle index
	oc.Uint8(0) // is_content_collector=0: a hierarchy collector
	_, handles = p.Dispatch(sess, ropRequest(RopSyncOpenCollector, 1, oc.Bytes()), handles, 0x10000)
	return p, sess, handles
}

// importHierarchyChangeRequest builds a RopSynchronizationImportHierarchyChange body:
// the hierarchy-values array (parent source key + display name) then a trailing
// (empty) property array.
func importHierarchyChangeRequest(t *testing.T, parentKey []byte, name string) []byte {
	t.Helper()
	hichy := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(hichy, []wire.TaggedPropertyValue{
		{Tag: wire.PidTagParentSourceKey, Value: parentKey},
		{Tag: wire.PidTagDisplayName, Value: name},
	}); err != nil {
		t.Fatalf("push hierarchy values: %v", err)
	}
	extra := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(extra, nil); err != nil {
		t.Fatalf("push extra properties: %v", err)
	}
	body := wire.NewPush(wire.FlagUTF16)
	body.Raw(hichy.Bytes())
	body.Raw(extra.Bytes())
	return body.Bytes()
}

// TestImportHierarchyChangeCreatesFolder verifies an empty parent source key creates a
// folder directly under the collector's folder, routed to the canonical folder path,
// and the response carries a resolvable folder id.
func TestImportHierarchyChangeCreatesFolder(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openHierarchyCollector(t, store)
	fm := &fakeMutator{}
	p.SetMutator(fm)

	resp, _ := p.Dispatch(sess, ropRequest(RopSyncImportHierarchyChange, 2, importHierarchyChangeRequest(t, nil, "Projects")), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopSyncImportHierarchyChange {
		t.Fatalf("rop id = %#x, want RopSyncImportHierarchyChange", got)
	}
	q.Uint8()
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("ImportHierarchyChange return value = %#x, want success", rv)
	}
	if fid := q.Uint64(); fid == 0 {
		t.Error("response folder id = 0, want a resolvable id")
	}
	if fm.gotCreate != "INBOX/Projects" {
		t.Errorf("created folder = %q, want INBOX/Projects", fm.gotCreate)
	}
}

// TestImportHierarchyChangeArbitraryParentDeferred verifies a non-empty parent source
// key (an arbitrary parent) is deferred rather than mis-resolved.
func TestImportHierarchyChangeArbitraryParentDeferred(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openHierarchyCollector(t, store)
	fm := &fakeMutator{}
	p.SetMutator(fm)

	parentKey := wire.SerializeXID(derivedGUID("replica", "qa.bob@local.test"), 0x0d)
	resp, _ := p.Dispatch(sess, ropRequest(RopSyncImportHierarchyChange, 2, importHierarchyChangeRequest(t, parentKey, "Sub")), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8()
	q.Uint8()
	if rv := q.Uint32(); rv != ecNotSupported {
		t.Errorf("ImportHierarchyChange with an arbitrary parent = %#x, want ecNotSupported", rv)
	}
	if fm.gotCreate != "" {
		t.Errorf("a folder was created for a deferred arbitrary-parent import: %q", fm.gotCreate)
	}
}

// importDeletesRequest builds a RopSynchronizationImportDeletes body: flags, then a
// property array carrying the deletion set as a single MV_BINARY of source-key XIDs.
func importDeletesRequest(t *testing.T, xids ...[]byte) []byte {
	t.Helper()
	arr := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(arr, []wire.TaggedPropertyValue{
		{Tag: wire.MakeTag(0, wire.PtMvBinary), Value: [][]byte(xids)},
	}); err != nil {
		t.Fatalf("push deletion array: %v", err)
	}
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(0) // flags
	body.Raw(arr.Bytes())
	return body.Bytes()
}

// TestSyncOpenCollectorBindsContentsCollector drives RopOpenFolder ->
// RopSynchronizationOpenCollector and verifies a contents collector is bound at the
// output handle, ready for the import ROPs.
func TestSyncOpenCollectorBindsContentsCollector(t *testing.T) {
	store := newFakeStore()
	store.addMailbox("INBOX")
	p, sess, handles := openInbox(t, store) // folder at handle index 1

	oc := wire.NewPush(0)
	oc.Uint8(2) // output handle index for the collector
	oc.Uint8(1) // is_content_collector: a contents (message) collector
	resp, handles := p.Dispatch(sess, ropRequest(RopSyncOpenCollector, 1, oc.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, 0)
	if got := q.Uint8(); got != RopSyncOpenCollector {
		t.Fatalf("rop id = %#x, want RopSyncOpenCollector", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("OpenCollector return value = %#x, want success", rv)
	}
	col, ok := stateFor(sess).objects[handles[2]].(*syncCollectorObject)
	if !ok {
		t.Fatal("no sync collector bound at the output handle")
	}
	if col.mailbox != "INBOX" {
		t.Errorf("collector mailbox = %q, want INBOX", col.mailbox)
	}
	if !col.contents {
		t.Error("collector is not a contents collector")
	}
}

// TestImportMessageChangeForeignGUIDCreatesMessage drives the cached-mode upload of a
// client-composed message: OpenCollector -> ImportMessageChange (a source key in a
// FOREIGN replica GUID, the offline-composed case) -> DestinationConfigure -> PutBuffer
// -> SaveChanges. The server must create a fresh message and the streamed subject/body
// must land in the committed RFC 5322 message — the branch a download-only round-trip
// never exercises.
func TestImportMessageChangeForeignGUIDCreatesMessage(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openCollector(t, store) // collector at handle index 2
	app, blob, _ := newWriteAppender()
	p.SetAppender(app)

	// A source key whose replica GUID is NOT this store's (a message composed elsewhere).
	foreign := wire.GUID{TimeLow: 0xDEADBEEF, TimeMid: 0x1234, TimeHiAndVersion: 0x5678, Node: [6]byte{1, 2, 3, 4, 5, 6}}
	sk := wire.SerializeXID(foreign, 0x4242)
	resp, handles := p.Dispatch(sess, ropRequest(RopSyncImportMessageChange, 2, importMessageChangeRequest(t, 3, sk)), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopSyncImportMessageChange {
		t.Fatalf("rop id = %#x, want RopSyncImportMessageChange", got)
	}
	q.Uint8()
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("ImportMessageChange (foreign GUID) = %#x, want success", rv)
	}
	if mid := q.Uint64(); mid != 0 {
		t.Errorf("import response message id = %#x, want 0 (identity assigned at save)", mid)
	}
	if _, ok := stateFor(sess).objects[handles[3]].(*messageObject); !ok {
		t.Fatal("no message object bound at the import output handle")
	}

	// Stream the content into the imported message, then save.
	dc := wire.NewPush(0)
	dc.Uint8(4)
	dc.Uint8(fastSourceOpCopyTo)
	dc.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopFastTransferDestConfigure, 3, dc.Bytes()), handles, 0x10000)

	stream := wire.NewPush(0)
	if err := wire.PushFastTransferPropval(stream, wire.PidTagSubject, "imported subject"); err != nil {
		t.Fatalf("push subject: %v", err)
	}
	if err := wire.PushFastTransferPropval(stream, wire.PidTagBody, "imported body"); err != nil {
		t.Fatalf("push body: %v", err)
	}
	handles = putBuffer(t, p, sess, handles, 4, stream.Bytes())

	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(3)
	sc.Uint8(0)
	resp2, _ := p.Dispatch(sess, ropRequest(RopSaveChangesMessage, 3, sc.Bytes()), handles, 0x10000)
	q2 := wire.NewPull(resp2, wire.FlagUTF16)
	q2.Uint8()
	q2.Uint8()
	if rv := q2.Uint32(); rv != ecSuccess {
		t.Fatalf("SaveChanges return value = %#x, want success", rv)
	}

	if len(blob.msgs) != 1 {
		t.Fatalf("blob store holds %d messages, want 1", len(blob.msgs))
	}
	m, err := mail.ReadMessage(bytes.NewReader(blob.msgs[0]))
	if err != nil {
		t.Fatalf("committed blob is not a valid RFC 5322 message: %v", err)
	}
	if subj := m.Header.Get("Subject"); subj != "imported subject" {
		t.Errorf("Subject = %q, want %q", subj, "imported subject")
	}
	if !bytes.Contains(blob.msgs[0], []byte("imported body")) {
		t.Error("the imported body did not reach the committed message")
	}
}

// TestImportMessageChangeServerGUIDExistingDeferred verifies that importing a source
// key in THIS store's namespace whose message still exists is deferred (an in-place
// update is not yet supported) rather than inverting the GLOBCNT to a local uid and
// corrupting that message. No object is bound on the defer path.
func TestImportMessageChangeServerGUIDExistingDeferred(t *testing.T) {
	store := newFakeStore()
	store.put("INBOX", &storage.MessageMetadata{UID: 5, ModSeq: 3, Subject: "existing", MessageID: "m5"})
	p, sess, handles := openCollector(t, store)
	app, _, _ := newWriteAppender()
	p.SetAppender(app)

	// A source key in this store's replica GUID, GLOBCNT 5 — the existing message's uid.
	serverGUID := derivedGUID("replica", "qa.bob@local.test")
	sk := wire.SerializeXID(serverGUID, 5)
	before := len(stateFor(sess).objects)
	resp, _ := p.Dispatch(sess, ropRequest(RopSyncImportMessageChange, 2, importMessageChangeRequest(t, 3, sk)), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8()
	q.Uint8()
	if rv := q.Uint32(); rv != ecNotSupported {
		t.Errorf("ImportMessageChange (server GUID, existing message) = %#x, want ecNotSupported", rv)
	}
	// The defer path early-returns before allocating, so no object is bound.
	if after := len(stateFor(sess).objects); after != before {
		t.Errorf("an object was allocated on the deferred-update path: %d -> %d", before, after)
	}
}

// TestImportDeletesServerGUIDDeletes verifies a deletion whose source key is in this
// store's namespace routes the message's uid (the XID's GLOBCNT) to the canonical
// delete path.
func TestImportDeletesServerGUIDDeletes(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openCollector(t, store)
	fm := &fakeMutator{removed: 1}
	p.SetMutator(fm)

	serverGUID := derivedGUID("replica", "qa.bob@local.test")
	sk := wire.SerializeXID(serverGUID, 7)
	resp, _ := p.Dispatch(sess, ropRequest(RopSyncImportDeletes, 2, importDeletesRequest(t, sk)), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopSyncImportDeletes {
		t.Fatalf("rop id = %#x, want RopSyncImportDeletes", got)
	}
	q.Uint8()
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("ImportDeletes return value = %#x, want success", rv)
	}
	if fm.gotFolder != "INBOX" {
		t.Errorf("delete folder = %q, want INBOX", fm.gotFolder)
	}
	if len(fm.gotUIDs) != 1 || fm.gotUIDs[0] != 7 {
		t.Errorf("deleted uids = %v, want [7] (the source key's GLOBCNT)", fm.gotUIDs)
	}
}

// TestImportDeletesForeignGUIDSkipped verifies a deletion whose source key belongs to
// another replica names nothing in this store and is skipped — the canonical delete is
// not invoked, and the ROP still succeeds.
func TestImportDeletesForeignGUIDSkipped(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openCollector(t, store)
	fm := &fakeMutator{}
	p.SetMutator(fm)

	foreign := wire.GUID{TimeLow: 0xFEEDFACE, Node: [6]byte{9, 8, 7, 6, 5, 4}}
	sk := wire.SerializeXID(foreign, 99)
	resp, _ := p.Dispatch(sess, ropRequest(RopSyncImportDeletes, 2, importDeletesRequest(t, sk)), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8()
	q.Uint8()
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("ImportDeletes (foreign GUID) = %#x, want success", rv)
	}
	if fm.gotUIDs != nil {
		t.Errorf("a foreign-replica deletion invoked the canonical delete with %v, want none", fm.gotUIDs)
	}
}
