package emsmdb

import (
	"bytes"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/storage"
)

// TestSyncUploadStateDelta drives the full incremental flow: configure a contents
// sync, upload a prior CnsetSeen covering change numbers up to 5 via the
// RopSynchronizationUploadStateStream ROPs, then GetBuffer. Only the message whose
// ModSeq exceeds 5 must be streamed (the delta), while the state block still reports
// the FULL membership (IdsetGiven over both uids) and the full change high-water
// (CnsetSeen up to 7) so the client's post-sync state is complete.
func TestSyncUploadStateDelta(t *testing.T) {
	store := newFakeStore()
	store.addMailbox("INBOX")
	when := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	store.put("INBOX", &storage.MessageMetadata{UID: 1, ModSeq: 5, Subject: "oldmsg", MessageID: "m1", InternalDate: when})
	store.put("INBOX", &storage.MessageMetadata{UID: 2, ModSeq: 7, Subject: "newmsg", MessageID: "m2", InternalDate: when})

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

	// Upload CnsetSeen covering change numbers [1,5]. The GUID is irrelevant to the
	// baseline (only the ranges' high-water is used), so any GUID serializes it.
	cnset := wire.SerializeIDSET(wire.GUID{}, []wire.GlobcntRange{{Lo: 1, Hi: 5}})
	beg := wire.NewPush(wire.FlagUTF16)
	beg.Uint32(uint32(metaTagCnsetSeen))
	beg.Uint32(uint32(len(cnset)))
	_, handles = p.Dispatch(sess, ropRequest(RopSyncUploadStateStreamBegin, 2, beg.Bytes()), handles, 0x10000)

	cont := wire.NewPush(wire.FlagUTF16)
	cont.Uint32(uint32(len(cnset)))
	cont.Raw(cnset)
	_, handles = p.Dispatch(sess, ropRequest(RopSyncUploadStateStreamContinue, 2, cont.Bytes()), handles, 0x10000)

	endResp, handles := p.Dispatch(sess, ropRequest(RopSyncUploadStateStreamEnd, 2, nil), handles, 0x10000)
	eq := wire.NewPull(endResp, wire.FlagUTF16)
	eq.Uint8()
	eq.Uint8()
	if rv := eq.Uint32(); rv != ecSuccess {
		t.Fatalf("UploadStateStreamEnd return value = %#x, want success", rv)
	}

	// GetBuffer in one shot (a large buffer); the delta is small.
	gb := wire.NewPush(wire.FlagUTF16)
	gb.Uint16(0xFFFF)
	resp, _ := p.Dispatch(sess, ropRequest(RopFastTransferSourceGetBuffer, 2, gb.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8()
	q.Uint8()
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("GetBuffer return value = %#x, want success", rv)
	}
	if status := q.Uint16(); status != transferStatusDone {
		t.Fatalf("GetBuffer status = %#x, want DONE (the delta fits one buffer)", status)
	}
	q.Uint16()
	q.Uint16()
	q.Uint8()
	full := q.Bin()

	// The delta streams only the message whose ModSeq (7) exceeds the seen high-water
	// (5): one change block, the new message present, the already-seen one absent.
	if n := bytes.Count(full, le32(markerIncrSyncChg)); n != 1 {
		t.Errorf("delta streamed %d change blocks, want 1 (only ModSeq > 5)", n)
	}
	newUTF16 := []byte{0x6E, 0, 0x65, 0, 0x77, 0, 0x6D, 0, 0x73, 0, 0x67, 0} // "newmsg"
	oldUTF16 := []byte{0x6F, 0, 0x6C, 0, 0x64, 0, 0x6D, 0, 0x73, 0, 0x67, 0} // "oldmsg"
	if !bytes.Contains(full, newUTF16) {
		t.Error("the changed message (ModSeq 7) was not streamed")
	}
	if bytes.Contains(full, oldUTF16) {
		t.Error("the already-seen message (ModSeq 5) was streamed in the delta")
	}

	// The state block still reports the full membership and the full change high-water.
	guid := derivedGUID("replica", "qa.bob@local.test")
	if !bytes.Contains(full, wire.SerializeIDSET(guid, []wire.GlobcntRange{{Lo: 1, Hi: 2}})) {
		t.Error("IdsetGiven does not cover the full membership [1,2] after a delta")
	}
	if !bytes.Contains(full, wire.SerializeIDSET(guid, []wire.GlobcntRange{{Lo: 1, Hi: 7}})) {
		t.Error("CnsetSeen does not cover the full change high-water [1,7] after a delta")
	}
	if !bytes.HasSuffix(full, le32(markerIncrSyncEnd)) {
		t.Error("delta stream does not end with INCRSYNCEND")
	}
}
