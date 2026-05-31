package storage

import (
	"testing"

	bbolt "go.etcd.io/bbolt"
)

// forceUIDNextForTest writes UIDNEXT directly, simulating the legacy drift where
// the counter fell below the highest stored UID.
func (db *Database) forceUIDNextForTest(user, mbox string, v uint32) error {
	return db.bolt.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(mailboxKey(user, mbox))).Put([]byte("uidnext"), itob(v))
	})
}

// TestReconcileUIDNext repairs a mailbox whose UIDNEXT counter has drifted below
// the highest stored UID (RFC 3501 requires UIDNEXT to exceed every current
// UID). It must also leave an already-consistent mailbox untouched.
func TestReconcileUIDNext(t *testing.T) {
	db, err := OpenDatabase(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }() //nolint:errcheck // test cleanup

	const user, mbox = "u@example.test", "INBOX"
	if err := db.CreateMailbox(user, mbox); err != nil {
		t.Fatalf("create mailbox: %v", err)
	}

	// Store messages with UIDs 1..10 directly (simulating delivery), then force
	// the counter to drift backwards as the legacy data did.
	for i := uint32(1); i <= 10; i++ {
		if err := db.StoreMessageMetadata(user, mbox, i, &MessageMetadata{MessageID: "m", UID: i}); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	if err := db.forceUIDNextForTest(user, mbox, 4); err != nil {
		t.Fatalf("force uidnext: %v", err)
	}

	if err := db.ReconcileUIDNext(user, mbox); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	mb, err := db.GetMailbox(user, mbox)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mb.UIDNext != 11 {
		t.Fatalf("expected UIDNEXT repaired to 11 (maxUID+1), got %d", mb.UIDNext)
	}

	// A correct counter must be preserved (no spurious change).
	if err := db.ReconcileUIDNext(user, mbox); err != nil {
		t.Fatalf("reconcile (idempotent): %v", err)
	}
	mb, err = db.GetMailbox(user, mbox)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mb.UIDNext != 11 {
		t.Fatalf("reconcile must be idempotent, got UIDNEXT %d", mb.UIDNext)
	}
}
