package emsmdb

import (
	"bytes"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestBuildHierarchySyncStream asserts the hierarchy-download stream shape: one
// folder-change block per folder (INCRSYNCCHG + folder proplist, and NO
// INCRSYNCMESSAGE, since these are folders not messages) carrying each folder's
// source key and the shared parent (IPM subtree) source key, then a state block with
// IdsetGiven over the folder ids and a terminating INCRSYNCEND.
func TestBuildHierarchySyncStream(t *testing.T) {
	guid := wire.GUID{
		TimeLow:          0x01020304,
		TimeMid:          0x0506,
		TimeHiAndVersion: 0x0708,
		ClockSeq:         [2]byte{0x09, 0x0A},
		Node:             [6]byte{0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10},
	}
	folders := []folderEntry{
		{name: "INBOX", fid: makeFID(fidReplID, 0x0d)},
		{name: "Archive", fid: makeFID(fidReplID, 0x100000)},
	}
	stream, err := buildHierarchySyncStream(folders, guid)
	if err != nil {
		t.Fatalf("buildHierarchySyncStream: %v", err)
	}

	if n := bytes.Count(stream, le32(markerIncrSyncChg)); n != 2 {
		t.Errorf("folder-change blocks = %d, want 2", n)
	}
	if n := bytes.Count(stream, le32(markerIncrSyncMessage)); n != 0 {
		t.Errorf("INCRSYNCMESSAGE markers = %d, want 0 (folders, not messages)", n)
	}
	begin := bytes.Index(stream, le32(markerIncrSyncStateBegin))
	if begin <= 0 {
		t.Fatal("no state block")
	}
	changeRegion := stream[:begin]
	for _, gc := range []uint64{0x0d, 0x100000} {
		if !bytes.Contains(changeRegion, wire.SerializeXID(guid, gc)) {
			t.Errorf("source key for folder gc %#x not found", gc)
		}
	}
	if !bytes.Contains(changeRegion, wire.SerializeXID(guid, ipmSubtreeGC)) {
		t.Error("parent (IPM subtree) source key not found in a folder change")
	}
	if !bytes.Contains(stream[begin:], wire.SerializeIDSET(guid, coalesceGCs([]uint64{0x0d, 0x100000}))) {
		t.Error("IdsetGiven over the folder ids not found in the state block")
	}
	if !bytes.HasSuffix(stream, le32(markerIncrSyncEnd)) {
		t.Error("hierarchy stream does not end with INCRSYNCEND")
	}
}

// TestHierarchyGetBufferFlow drives RopOpenFolder(IPM subtree) -> RopSyncConfigure
// (hierarchy) -> RopFastTransferSourceGetBuffer and verifies the live dispatch
// produces a folder list covering the mailbox's folders.
func TestHierarchyGetBufferFlow(t *testing.T) {
	store := newFakeStore()
	store.addMailbox("INBOX")
	store.addMailbox("Sent")
	store.addMailbox("Archive")

	p := NewProcessor(store)
	sess := &Session{ID: "s", Email: "qa.bob@local.test"}
	logon := append([]byte{RopLogon, 0x00, 0x00}, encodeLogonRequest()...)
	_, handles := p.Dispatch(sess, logon, []uint32{0xFFFFFFFF}, 0x10000)

	// Open the IPM subtree (gc 0x09), the folder a hierarchy sync configures on.
	of := wire.NewPush(wire.FlagUTF16)
	of.Uint8(1)
	of.Uint64(makeFID(fidReplID, 0x09))
	of.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopOpenFolder, 0, of.Bytes()), handles, 0x10000)

	cfg := wire.NewPush(wire.FlagUTF16)
	cfg.Uint8(2)
	cfg.Uint8(syncTypeHierarchy)
	cfg.Uint8(0x01)
	cfg.Uint16(0)
	cfg.Uint16(0)
	cfg.Uint32(0)
	wire.PushPropertyTagArray(cfg, nil)
	_, handles = p.Dispatch(sess, ropRequest(RopSyncConfigure, 1, cfg.Bytes()), handles, 0x10000)

	gb := wire.NewPush(wire.FlagUTF16)
	gb.Uint16(0xFFFF)
	resp, _ := p.Dispatch(sess, ropRequest(RopFastTransferSourceGetBuffer, 2, gb.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8()
	q.Uint8()
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("hierarchy GetBuffer return value = %#x, want success", rv)
	}
	if status := q.Uint16(); status != transferStatusDone {
		t.Fatalf("hierarchy GetBuffer status = %#x, want DONE", status)
	}
	q.Uint16()
	q.Uint16()
	q.Uint8()
	full := q.Bin()

	// One folder-change block per mailbox folder, ending with INCRSYNCEND.
	if n := bytes.Count(full, le32(markerIncrSyncChg)); n != 3 {
		t.Errorf("hierarchy streamed %d folder changes, want 3 (INBOX, Sent, Archive)", n)
	}
	guid := derivedGUID("replica", "qa.bob@local.test")
	for _, gc := range []uint64{0x0d, 0x0a} { // INBOX and Sent keep their special gcs
		if !bytes.Contains(full, wire.SerializeXID(guid, gc)) {
			t.Errorf("source key for special folder gc %#x not streamed", gc)
		}
	}
	if !bytes.HasSuffix(full, le32(markerIncrSyncEnd)) {
		t.Error("hierarchy stream does not end with INCRSYNCEND")
	}
}
