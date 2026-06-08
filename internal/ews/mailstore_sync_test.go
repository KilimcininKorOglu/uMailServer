package ews

import "testing"

// TestMailboxNameForFolder_Lineage proves the EWS→IMAP mirror resolves a user
// folder to its full hierarchy path, so two folders that share a display name
// under different parents map to DISTINCT IMAP mailboxes (the wrong-mailbox
// mirror bug that a parent-scoped CopyFolder would otherwise trigger). A
// top-level folder still resolves to its flat name, and a distinguished folder
// resolves through its canonical role name.
func TestMailboxNameForFolder_Lineage(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	const mbox = "user@x.com"
	id := srv.identity

	// A top-level user folder resolves to its flat name (pre-existing behavior).
	misc, err := id.EnsureFolderId(mbox, "Misc", "")
	if err != nil {
		t.Fatalf("EnsureFolderId(Misc): %v", err)
	}
	if got := srv.mailboxNameForFolder(mbox, misc); got != "Misc" {
		t.Errorf("top-level mailbox name = %q, want Misc", got)
	}

	// Two parents, one user-created and one distinguished.
	projects, err := id.EnsureFolderId(mbox, "Projects", "")
	if err != nil {
		t.Fatalf("EnsureFolderId(Projects): %v", err)
	}
	archive, err := id.EnsureFolderId(mbox, "Archive", "archive")
	if err != nil {
		t.Fatalf("EnsureFolderId(Archive): %v", err)
	}

	// Same display name "Reports" under each parent must resolve to distinct
	// paths so their mirrored items never collide.
	repA, err := id.EnsureChildFolderId(mbox, projects, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId(Projects/Reports): %v", err)
	}
	repB, err := id.EnsureChildFolderId(mbox, archive, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId(Archive/Reports): %v", err)
	}
	if repA.Equal(repB) {
		t.Fatalf("same-name children under different parents share id %v", repA)
	}

	if got := srv.mailboxNameForFolder(mbox, repA); got != "Projects/Reports" {
		t.Errorf("child A mailbox name = %q, want Projects/Reports", got)
	}
	if got := srv.mailboxNameForFolder(mbox, repB); got != "Archive/Reports" {
		t.Errorf("child B mailbox name = %q, want Archive/Reports", got)
	}

	// A collaboration folder is not mail and resolves to "" so the mirror skips it.
	cal, err := id.EnsureFolderId(mbox, "Calendar", "calendar")
	if err != nil {
		t.Fatalf("EnsureFolderId(Calendar): %v", err)
	}
	if got := srv.mailboxNameForFolder(mbox, cal); got != "" {
		t.Errorf("calendar mailbox name = %q, want empty", got)
	}
}
