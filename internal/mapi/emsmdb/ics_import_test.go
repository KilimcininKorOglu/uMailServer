package emsmdb

import (
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

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
