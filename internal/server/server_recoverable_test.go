package server

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// newRecoverableTestServer builds a bare Server backed by a throwaway bbolt
// store, sufficient for the record-decision logic (the expunge-cleanup folder
// gating) that does not touch the storage/blob stack. The capture happy path and
// the retention purge are covered by the Docker probe, not in-process.
func newRecoverableTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("db close: %v", err)
		}
	})
	return &Server{database: d, logger: slog.Default()}, d
}

// TestDropRecoverableOnExpunge_FolderGating encodes the cross-protocol cleanup
// rule: expunging a message from the Recoverable Items folder drops its canonical
// retention record (so a manually emptied dumpster leaves nothing for the cleaner
// to chase), while expunging from ANY other folder must leave records untouched.
// The folder name is matched case-insensitively because IMAP/EWS may present
// "recoverable items" rather than "Recoverable Items".
func TestDropRecoverableOnExpunge_FolderGating(t *testing.T) {
	srv, d := newRecoverableTestServer(t)
	const owner = "u@example.com"
	mk := func(id string, uid uint32) {
		if err := d.CreateRecoverableItem(&db.RecoverableItem{
			ID: id, Owner: owner, OriginalFolder: "INBOX",
			BlobKey: "blob-" + id, FolderUID: uid, DeletedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	// Expunge from a non-dumpster folder is a no-op: the record survives.
	mk("keep", 11)
	srv.dropRecoverableOnExpunge(owner, "INBOX", 11)
	if _, err := d.GetRecoverableItem("keep"); err != nil {
		t.Fatalf("expunge from INBOX must not drop the record; got err %v", err)
	}

	// Expunge from the Recoverable Items folder drops the matching record.
	mk("drop", 22)
	srv.dropRecoverableOnExpunge(owner, "Recoverable Items", 22)
	if _, err := d.GetRecoverableItem("drop"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("expunge from Recoverable Items must drop the record; got err %v", err)
	}

	// Case-insensitive folder match (a client may present "recoverable items").
	mk("ci", 33)
	srv.dropRecoverableOnExpunge(owner, "recoverable items", 33)
	if _, err := d.GetRecoverableItem("ci"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("case-insensitive dumpster match must drop the record; got err %v", err)
	}

	// A different owner's record with the same uid is never touched (owner-scoped).
	if err := d.CreateRecoverableItem(&db.RecoverableItem{
		ID: "other", Owner: "v@example.com", OriginalFolder: "INBOX", FolderUID: 22, DeletedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create other: %v", err)
	}
	srv.dropRecoverableOnExpunge(owner, "Recoverable Items", 22)
	if _, err := d.GetRecoverableItem("other"); err != nil {
		t.Fatalf("another owner's record must survive; got err %v", err)
	}
}
