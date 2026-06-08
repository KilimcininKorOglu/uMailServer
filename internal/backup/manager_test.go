package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRestore_PerUserDifferentUser_NoNesting verifies a per-user archive
// restored to a DIFFERENT user lands directly under <messagesRoot>/<target>/,
// not the buggy <messagesRoot>/<target>/<origUser>/ that no protocol serves.
func TestRestore_PerUserDifferentUser_NoNesting(t *testing.T) {
	dataDir := t.TempDir()
	m := NewManager(dataDir)

	orig := "alice@local.test"
	mustWrite(t, filepath.Join(m.messagesRoot(), orig, "INBOX", "cur", "1.eml"), "hello")

	archive := filepath.Join(dataDir, "alice.tar.gz")
	if err := m.BackupUser(orig, archive, BackupOptions{}); err != nil {
		t.Fatalf("BackupUser: %v", err)
	}

	dst := t.TempDir()
	m2 := NewManager(dst)
	target := "bob@local.test"
	if err := m2.Restore(archive, RestoreOptions{
		Mode:       RestoreModeDifferent,
		SourceType: BackupTypeUser,
		TargetUser: target,
		Overwrite:  true,
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	want := filepath.Join(m2.messagesRoot(), target, "INBOX", "cur", "1.eml")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("restored file not at %s: %v", want, err)
	}
	// The original-user segment must NOT survive as a nested directory.
	bad := filepath.Join(m2.messagesRoot(), target, orig)
	if _, err := os.Stat(bad); err == nil {
		t.Errorf("restored tree wrongly nested under %s", bad)
	}
}

// TestRestore_FullBackup_NoMessagesPrefix verifies a full archive restored to
// the canonical location lands at <messagesRoot>/<user>/..., not the buggy
// <messagesRoot>/messages/<user>/....
func TestRestore_FullBackup_NoMessagesPrefix(t *testing.T) {
	dataDir := t.TempDir()
	m := NewManager(dataDir)

	user := "carol@local.test"
	mustWrite(t, filepath.Join(m.messagesRoot(), user, "INBOX", "cur", "1.eml"), "hi")

	archive := filepath.Join(dataDir, "full.tar.gz")
	if err := m.BackupFull(archive, BackupOptions{}); err != nil {
		t.Fatalf("BackupFull: %v", err)
	}

	dst := t.TempDir()
	m2 := NewManager(dst)
	if err := m2.Restore(archive, RestoreOptions{
		Mode:       RestoreModeOverwrite,
		SourceType: BackupTypeFull,
		Overwrite:  true,
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	want := filepath.Join(m2.messagesRoot(), user, "INBOX", "cur", "1.eml")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("restored file not at %s: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(m2.messagesRoot(), "messages")); err == nil {
		t.Errorf("restored tree wrongly carries the messages/ prefix")
	}
}

// TestRestore_PerUserSameUser_RoundTrips verifies the common case — restoring a
// user's own backup back to their location — is unchanged: no segment stripped.
func TestRestore_PerUserSameUser_RoundTrips(t *testing.T) {
	dataDir := t.TempDir()
	m := NewManager(dataDir)

	user := "dave@local.test"
	mustWrite(t, filepath.Join(m.messagesRoot(), user, "INBOX", "cur", "1.eml"), "x")

	archive := filepath.Join(dataDir, "dave.tar.gz")
	if err := m.BackupUser(user, archive, BackupOptions{}); err != nil {
		t.Fatalf("BackupUser: %v", err)
	}

	dst := t.TempDir()
	m2 := NewManager(dst)
	if err := m2.Restore(archive, RestoreOptions{
		Mode:       RestoreModeMerge,
		SourceType: BackupTypeUser,
		Overwrite:  true,
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	want := filepath.Join(m2.messagesRoot(), user, "INBOX", "cur", "1.eml")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("restored file not at %s: %v", want, err)
	}
}
