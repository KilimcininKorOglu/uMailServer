package emsmdb

import (
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// openInboxFolderForSync logs on and opens the Inbox at handle index 1, the folder
// an ICS sync is configured on.
func openInboxFolderForSync(t *testing.T) (*Processor, *Session, []uint32) {
	t.Helper()
	p, sess, handles := logonSession(t)
	of := wire.NewPush(wire.FlagUTF16)
	of.Uint8(1) // output handle index for the folder
	of.Uint64(makeFID(fidReplID, 0x0d))
	of.Uint8(0) // open flags
	_, handles = p.Dispatch(sess, ropRequest(RopOpenFolder, 0, of.Bytes()), handles, 0x10000)
	return p, sess, handles
}

// TestSyncConfigureBindsContext drives RopOpenFolder(Inbox) -> RopSyncConfigure and
// verifies the sync-context object is bound at the output handle with the folder,
// sync type, flags, send options, and property tags captured from the request — the
// configuration RopFastTransferSourceGetBuffer will later stream against. The
// response is the bare envelope (no body).
func TestSyncConfigureBindsContext(t *testing.T) {
	p, sess, handles := openInboxFolderForSync(t)

	cols := []wire.PropTag{wire.PidTagSubject, wire.PidTagMessageDeliveryTime}
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)                // output handle index for the sync context
	sc.Uint8(syncTypeContents) // sync type
	sc.Uint8(0x01)             // send options (Unicode)
	sc.Uint16(0x000C)          // sync flags
	sc.Uint16(0)               // restriction size: none
	sc.Uint32(0)               // extra flags
	wire.PushPropertyTagArray(sc, cols)
	resp, handles := p.Dispatch(sess, ropRequest(RopSyncConfigure, 1, sc.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopSyncConfigure {
		t.Fatalf("rop id = %#x, want RopSyncConfigure", got)
	}
	if hi := q.Uint8(); hi != 2 {
		t.Errorf("output handle index = %d, want 2", hi)
	}
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("SyncConfigure return value = %#x, want success", rv)
	}
	if q.Remaining() != 0 {
		t.Errorf("response carries %d trailing bytes, want none", q.Remaining())
	}

	sctx, ok := stateFor(sess).objects[handles[2]].(*syncContextObject)
	if !ok {
		t.Fatal("no sync-context object bound at the output handle")
	}
	if sctx.mailbox != "INBOX" {
		t.Errorf("sync context mailbox = %q, want INBOX", sctx.mailbox)
	}
	if sctx.syncType != syncTypeContents {
		t.Errorf("sync type = %#x, want contents (%#x)", sctx.syncType, syncTypeContents)
	}
	if sctx.syncFlags != 0x000C {
		t.Errorf("sync flags = %#x, want 0x000C", sctx.syncFlags)
	}
	if sctx.sendOptions != 0x01 {
		t.Errorf("send options = %#x, want 0x01", sctx.sendOptions)
	}
	if len(sctx.proptags) != 2 || sctx.proptags[0] != wire.PidTagSubject || sctx.proptags[1] != wire.PidTagMessageDeliveryTime {
		t.Errorf("proptags = %v, want [PidTagSubject, PidTagMessageDeliveryTime]", sctx.proptags)
	}
}

// TestSyncConfigureSkipsRestriction verifies the optional restriction blob is read
// past as a whole so the trailing extra_flags and property-tag array stay aligned:
// the property tags after a non-empty restriction must still parse correctly.
func TestSyncConfigureSkipsRestriction(t *testing.T) {
	p, sess, handles := openInboxFolderForSync(t)

	restriction := []byte{0x01, 0x02, 0x03, 0x04, 0x05} // opaque restriction bytes, skipped whole
	cols := []wire.PropTag{wire.PidTagSubject}
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)
	sc.Uint8(syncTypeContents)
	sc.Uint8(0)
	sc.Uint16(0)
	sc.Uint16(uint16(len(restriction))) // restriction size
	sc.Raw(restriction)
	sc.Uint32(0)
	wire.PushPropertyTagArray(sc, cols)
	resp, handles := p.Dispatch(sess, ropRequest(RopSyncConfigure, 1, sc.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("SyncConfigure with restriction = %#x, want success", rv)
	}
	sctx, ok := stateFor(sess).objects[handles[2]].(*syncContextObject)
	if !ok {
		t.Fatal("no sync-context object bound after a restriction")
	}
	if len(sctx.proptags) != 1 || sctx.proptags[0] != wire.PidTagSubject {
		t.Errorf("proptags after restriction skip = %v, want [PidTagSubject] (the skip kept alignment)", sctx.proptags)
	}
}

// TestSyncConfigureRejectsUnknownType verifies a sync type that is neither contents
// nor hierarchy is rejected rather than binding a context that cannot be served.
func TestSyncConfigureRejectsUnknownType(t *testing.T) {
	p, sess, handles := openInboxFolderForSync(t)

	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)
	sc.Uint8(0x09) // invalid sync type
	sc.Uint8(0)
	sc.Uint16(0)
	sc.Uint16(0)
	sc.Uint32(0)
	wire.PushPropertyTagArray(sc, nil)
	resp, _ := p.Dispatch(sess, ropRequest(RopSyncConfigure, 1, sc.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecError {
		t.Errorf("unknown sync type = %#x, want ecError", rv)
	}
}

// TestSyncConfigureRejectsNonFolder verifies configuring a sync on a handle that is
// not a folder (here the logon itself) is rejected.
func TestSyncConfigureRejectsNonFolder(t *testing.T) {
	p, sess, handles := logonSession(t)

	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)
	sc.Uint8(syncTypeContents)
	sc.Uint8(0)
	sc.Uint16(0)
	sc.Uint16(0)
	sc.Uint32(0)
	wire.PushPropertyTagArray(sc, nil)
	resp, _ := p.Dispatch(sess, ropRequest(RopSyncConfigure, 0, sc.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecNullObject {
		t.Errorf("SyncConfigure on a non-folder handle = %#x, want ecNullObject", rv)
	}
}
