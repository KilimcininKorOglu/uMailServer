package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestICalUID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"BEGIN:VEVENT\r\nUID:abc-123\r\nEND:VEVENT", "abc-123"},
		{"uid:lower@x\r\n", "lower@x"}, // property name is case-insensitive
		{"SUMMARY:x\nNO-UID-HERE", ""},
		{"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:card-9\r\nEND:VCARD", "card-9"},
	}
	for _, c := range cases {
		if got := icalUID(c.in); got != c.want {
			t.Errorf("icalUID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestMigrateDAVTree verifies the filesystem walker derives the mailbox from the
// top-level directory (un-sanitizing "_at_"), reads the UID from each payload,
// skips payloads without a UID, ignores non-matching extensions, and reports
// accurate migrated/skipped counts.
func TestMigrateDAVTree(t *testing.T) {
	root := t.TempDir()
	userDir := filepath.Join(root, "bob_at_ex.test", "default")
	if err := os.MkdirAll(userDir, 0o750); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(name, body string) {
		if err := os.WriteFile(filepath.Join(userDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("e1.ics", "BEGIN:VEVENT\r\nUID:e1\r\nEND:VEVENT")
	mustWrite("nouid.ics", "BEGIN:VEVENT\r\nEND:VEVENT")
	mustWrite("ignore.txt", "not an ics")

	var saved []string
	migrated, skipped, err := migrateDAVTree(root, ".ics", func(user, uid, raw string) error {
		if user != "bob@ex.test" {
			t.Errorf("user = %q, want bob@ex.test", user)
		}
		saved = append(saved, uid)
		return nil
	})
	if err != nil {
		t.Fatalf("migrateDAVTree: %v", err)
	}
	if migrated != 1 {
		t.Errorf("migrated = %d, want 1", migrated)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the no-UID payload)", skipped)
	}
	if len(saved) != 1 || saved[0] != "e1" {
		t.Errorf("saved = %v, want [e1]", saved)
	}
}

// TestMigrateDAVTreeMissingRoot verifies a missing root is a no-op, not an error.
func TestMigrateDAVTreeMissingRoot(t *testing.T) {
	migrated, skipped, err := migrateDAVTree(filepath.Join(t.TempDir(), "does-not-exist"), ".ics", func(string, string, string) error { return nil })
	if err != nil || migrated != 0 || skipped != 0 {
		t.Errorf("missing root: got (%d,%d,%v), want (0,0,nil)", migrated, skipped, err)
	}
}
