package storage

import (
	"testing"
)

// Every account must get the full standard folder set so all protocols
// (IMAP/JMAP/EWS/webmail) expose a consistent view; ListMailboxes must report
// all of DefaultMailboxes after provisioning, and the call must be idempotent.
func TestEnsureDefaultMailboxes(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDatabase(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }() //nolint:errcheck

	const user = "newuser@example.com"
	if err := db.EnsureDefaultMailboxes(user); err != nil {
		t.Fatalf("EnsureDefaultMailboxes: %v", err)
	}

	got, err := db.ListMailboxes(user)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	gotSet := make(map[string]bool, len(got))
	for _, m := range got {
		gotSet[m] = true
	}
	for _, want := range DefaultMailboxes {
		if !gotSet[want] {
			t.Errorf("default mailbox %q missing after provisioning; got %v", want, got)
		}
	}

	// Idempotent: a second call must not error or duplicate.
	if err := db.EnsureDefaultMailboxes(user); err != nil {
		t.Fatalf("EnsureDefaultMailboxes (second call): %v", err)
	}
	got2, err := db.ListMailboxes(user)
	if err != nil {
		t.Fatalf("ListMailboxes (second): %v", err)
	}
	if len(got2) != len(got) {
		t.Errorf("mailbox count changed on repeat call: %d -> %d", len(got), len(got2))
	}
}
