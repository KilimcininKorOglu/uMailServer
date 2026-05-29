package semcore

import (
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestBoltLifecycleStorePoll(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer store.Close() //nolint:errcheck

	lifecycleDB, err := bbolt.Open(filepath.Join(tmpDir, "lifecycle.db"), 0o600, nil)
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	defer lifecycleDB.Close() //nolint:errcheck

	lifecycle, err := NewBoltLifecycleStore(lifecycleDB)
	if err != nil {
		t.Fatalf("NewBoltLifecycleStore: %v", err)
	}

	ownerID := MustMailboxId("mbx-test-poll")
	err = store.PutMailboxIdentity("e:owner@local.test", ownerID, 1)
	if err != nil {
		t.Fatalf("PutMailboxIdentity: %v", err)
	}

	// Append a test event.
	testLC := Lifecycle{
		MailboxID: ownerID,
		Kind:     LifecycleKindCreated,
		At:       time.Now(),
		Actor:    "test-actor",
	}
	appendErr := lifecycle.AppendLifecycle(testLC)
	t.Logf("append err=%v", appendErr)

	// Poll.
	events, _, pollErr := lifecycle.PollEvents(ownerID, 0, 10)
	t.Logf("poll err=%v eventsCount=%d ownerID=%s first32=%s", pollErr, len(events), ownerID.String(), ownerID.String()[:min(32, len(ownerID.String()))])
	
	if len(events) == 0 {
		t.Errorf("PollEvents returned 0 events after append; expected 1")
	}
}
