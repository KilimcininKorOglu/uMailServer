package emsmdb

import (
	"bytes"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

// le32 returns the little-endian bytes of a FastTransfer marker, the form it has on
// the wire.
func le32(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

// TestBuildContentsSyncStream asserts the produced FastTransfer download stream has
// the MS-OXCFXICS contents-sync shape: one change block per message (INCRSYNCCHG +
// header + INCRSYNCMESSAGE) carrying each message's source/change XIDs, then a state
// block (INCRSYNCSTATEBEGIN + IdsetGiven over the uids + CnsetSeen over the modseqs +
// INCRSYNCSTATEEND) and a terminating INCRSYNCEND, in that order. This pins the wire
// structure, not a round-trip of the producer against itself.
func TestBuildContentsSyncStream(t *testing.T) {
	guid := wire.GUID{
		TimeLow:          0x11223344,
		TimeMid:          0x5566,
		TimeHiAndVersion: 0x7788,
		ClockSeq:         [2]byte{0x99, 0xAA},
		Node:             [6]byte{0xBB, 0xCC, 0xDD, 0xEE, 0x12, 0x34},
	}
	when := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	msgs := []syncMessage{
		{uid: 1, modseq: 5, lastMod: when, props: []wire.TaggedPropertyValue{{Tag: wire.PidTagSubject, Value: "first"}}},
		{uid: 2, modseq: 7, lastMod: when, props: []wire.TaggedPropertyValue{{Tag: wire.PidTagSubject, Value: "second"}}},
	}
	stream, err := buildContentsSyncStream(msgs, guid)
	if err != nil {
		t.Fatalf("buildContentsSyncStream: %v", err)
	}

	// One change block per message.
	if n := bytes.Count(stream, le32(markerIncrSyncChg)); n != 2 {
		t.Errorf("INCRSYNCCHG markers = %d, want 2 (one per message)", n)
	}
	if n := bytes.Count(stream, le32(markerIncrSyncMessage)); n != 2 {
		t.Errorf("INCRSYNCMESSAGE markers = %d, want 2", n)
	}
	if !bytes.HasPrefix(stream, le32(markerIncrSyncChg)) {
		t.Error("stream does not begin with an INCRSYNCCHG marker")
	}

	// Marker ordering: the state block follows the messages and precedes the end.
	begin := bytes.Index(stream, le32(markerIncrSyncStateBegin))
	end := bytes.Index(stream, le32(markerIncrSyncStateEnd))
	syncEnd := bytes.LastIndex(stream, le32(markerIncrSyncEnd))
	if begin <= 0 || end <= begin || syncEnd <= end {
		t.Fatalf("state/end markers out of order: stateBegin=%d stateEnd=%d syncEnd=%d", begin, end, syncEnd)
	}

	// Each message's source key (replica GUID + uid GLOBCNT) and change key (replica
	// GUID + modseq GLOBCNT) appear in the change region (before the state block).
	changeRegion := stream[:begin]
	for _, uid := range []uint64{1, 2} {
		if !bytes.Contains(changeRegion, wire.SerializeXID(guid, uid)) {
			t.Errorf("source-key XID for uid %d not found in the change region", uid)
		}
	}
	for _, cn := range []uint64{5, 7} {
		if !bytes.Contains(changeRegion, wire.SerializeXID(guid, cn)) {
			t.Errorf("change-key XID for modseq %d not found in the change region", cn)
		}
	}

	// The state block carries IdsetGiven over the coalesced uids [1,2] and CnsetSeen
	// over the modseqs [1,7].
	stateRegion := stream[begin:]
	if !bytes.Contains(stateRegion, wire.SerializeIDSET(guid, []wire.GlobcntRange{{Lo: 1, Hi: 2}})) {
		t.Error("IdsetGiven over uids [1,2] not found in the state block")
	}
	if !bytes.Contains(stateRegion, wire.SerializeIDSET(guid, []wire.GlobcntRange{{Lo: 1, Hi: 7}})) {
		t.Error("CnsetSeen over modseqs [1,7] not found in the state block")
	}

	// The message proplist carries the requested Subject ("first" as UTF-16LE).
	if !bytes.Contains(changeRegion, []byte{0x66, 0x00, 0x69, 0x00, 0x72, 0x00, 0x73, 0x00, 0x74, 0x00}) {
		t.Error("the Subject value \"first\" (UTF-16LE) not found in the message proplist")
	}
}

// TestFastTransferGetBufferFlow drives the live ROP chain RopOpenFolder ->
// RopSyncConfigure -> repeated RopFastTransferSourceGetBuffer with a small buffer and
// verifies the stream is chunked (PARTIAL until the last chunk reports DONE) and the
// reassembled bytes are a well-formed contents-sync stream covering both messages.
func TestFastTransferGetBufferFlow(t *testing.T) {
	store := newFakeStore()
	store.addMailbox("INBOX")
	when := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	store.put("INBOX", &storage.MessageMetadata{UID: 1, ModSeq: 5, Subject: "first", MessageID: "m1", InternalDate: when})
	store.put("INBOX", &storage.MessageMetadata{UID: 2, ModSeq: 7, Subject: "second", MessageID: "m2", InternalDate: when})

	p := NewProcessor(store)
	p.SetBodyStore(store)
	sess := &Session{ID: "s", Email: "qa.bob@local.test"}
	logon := append([]byte{RopLogon, 0x00, 0x00}, encodeLogonRequest()...)
	_, handles := p.Dispatch(sess, logon, []uint32{0xFFFFFFFF}, 0x10000)

	of := wire.NewPush(wire.FlagUTF16)
	of.Uint8(1)
	of.Uint64(makeFID(fidReplID, 0x0d))
	of.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopOpenFolder, 0, of.Bytes()), handles, 0x10000)

	cfg := wire.NewPush(wire.FlagUTF16)
	cfg.Uint8(2)
	cfg.Uint8(syncTypeContents)
	cfg.Uint8(0x01)
	cfg.Uint16(0)
	cfg.Uint16(0)
	cfg.Uint32(0)
	wire.PushPropertyTagArray(cfg, []wire.PropTag{wire.PidTagSubject})
	_, handles = p.Dispatch(sess, ropRequest(RopSyncConfigure, 1, cfg.Bytes()), handles, 0x10000)

	var full []byte
	sawPartial := false
	done := false
	for range 1000 {
		gb := wire.NewPush(wire.FlagUTF16)
		gb.Uint16(16) // a deliberately small buffer to force chunking
		resp, _ := p.Dispatch(sess, ropRequest(RopFastTransferSourceGetBuffer, 2, gb.Bytes()), handles, 0x10000)
		q := wire.NewPull(resp, wire.FlagUTF16)
		if got := q.Uint8(); got != RopFastTransferSourceGetBuffer {
			t.Fatalf("rop id = %#x, want RopFastTransferSourceGetBuffer", got)
		}
		q.Uint8() // handle index
		if rv := q.Uint32(); rv != ecSuccess {
			t.Fatalf("GetBuffer return value = %#x, want success", rv)
		}
		status := q.Uint16()
		q.Uint16() // in_progress_count
		q.Uint16() // total_step_count
		q.Uint8()  // reserved
		chunk := q.Bin()
		if q.Err() != nil {
			t.Fatalf("GetBuffer response parse error: %v", q.Err())
		}
		if len(chunk) > 16 {
			t.Errorf("chunk = %d bytes, want <= the requested 16", len(chunk))
		}
		full = append(full, chunk...)
		if status == transferStatusPartial {
			sawPartial = true
		}
		if status == transferStatusDone {
			done = true
			break
		}
	}
	if !done {
		t.Fatal("GetBuffer never reported DONE")
	}
	if !sawPartial {
		t.Error("the stream fit in one chunk; expected PARTIAL chunks before DONE with a 16-byte buffer")
	}

	// The reassembled stream is well formed and covers both messages.
	if !bytes.HasPrefix(full, le32(markerIncrSyncChg)) {
		t.Error("reassembled stream does not begin with INCRSYNCCHG")
	}
	if n := bytes.Count(full, le32(markerIncrSyncChg)); n != 2 {
		t.Errorf("reassembled INCRSYNCCHG markers = %d, want 2", n)
	}
	if !bytes.HasSuffix(full, le32(markerIncrSyncEnd)) {
		t.Error("reassembled stream does not end with INCRSYNCEND")
	}
}
