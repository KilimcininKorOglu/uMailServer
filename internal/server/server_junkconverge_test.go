package server

import (
	"log/slog"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestConvergeLegacyJunkFolders verifies the startup convergence re-homes a
// legacy spam-role junk folder onto the canonical "junk" role: items move into
// the existing Junk folder (the "both folders exist" merge case), the spam-role
// folder is deleted, and a second run is a no-op (idempotent).
func TestConvergeLegacyJunkFolders(t *testing.T) {
	store, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})

	const email = "victim@local.test"
	id := store.Identity()
	mboxID, err := id.EnsureMailboxId(email)
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	mp := semcore.NewMutationPipeline(id, store.Lifecycle())

	// A canonical Junk folder that already holds one item (e.g. junk filed over
	// IMAP), so convergence must MERGE rather than relabel.
	junkID, err := id.EnsureFolderId(email, "Junk", "junk")
	if err != nil {
		t.Fatalf("EnsureFolderId junk: %v", err)
	}
	if _, err := mp.MutateItem(&semcore.MutationInput{
		MailboxID: mboxID, FolderID: junkID, RawMessage: []byte("Subject: already-junk\r\n\r\nbody A\r\n"),
		InternalDate: time.Now(), Actor: email, Email: email, Source: semcore.MutationSourceIMAP,
	}); err != nil {
		t.Fatalf("seed junk item: %v", err)
	}

	// A legacy spam-role folder with one stranded item (old EWS mark-as-junk).
	spamID, err := id.EnsureFolderId(email, legacyJunkFolderName, "spam")
	if err != nil {
		t.Fatalf("EnsureFolderId spam: %v", err)
	}
	if _, err := mp.MutateItem(&semcore.MutationInput{
		MailboxID: mboxID, FolderID: spamID, RawMessage: []byte("Subject: stranded\r\n\r\nbody B\r\n"),
		InternalDate: time.Now(), Actor: email, Email: email, Source: semcore.MutationSourceEWS,
	}); err != nil {
		t.Fatalf("seed spam item: %v", err)
	}

	// storageDB is nil here: the semcore re-home is the assertion; the mirrored
	// storage mailbox is covered by the end-to-end probe.
	if !convergeMailboxJunk(email, id, nil, slog.Default()) {
		t.Fatal("convergeMailboxJunk: expected it to find and re-home the spam folder")
	}

	folders, err := id.ListFolderIdentitiesForMailbox(email)
	if err != nil {
		t.Fatalf("ListFolderIdentitiesForMailbox: %v", err)
	}
	for _, f := range folders {
		if f.Role == "spam" {
			t.Errorf("spam-role folder still present after convergence")
		}
	}

	items, err := id.ListItemIdentitiesByFolder(junkID)
	if err != nil {
		t.Fatalf("ListItemIdentitiesByFolder: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("expected both items merged into the junk folder, got %d", len(items))
	}

	// Idempotent: nothing left to converge.
	if convergeMailboxJunk(email, id, nil, slog.Default()) {
		t.Errorf("second convergence should be a no-op (no spam folder remains)")
	}
}
