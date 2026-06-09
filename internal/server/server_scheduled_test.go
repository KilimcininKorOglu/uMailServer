package server

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// newScheduledTestServer builds a bare Server backed by a throwaway bbolt store,
// sufficient for the schedule-record decision logic (cancel gating, retry/give-up)
// that does not touch the delivery stack. The full release happy path (submit +
// Sent filing) is covered by the Docker probe, not in-process.
func newScheduledTestServer(t *testing.T) (*Server, *db.DB) {
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

// TestCancelScheduledOnExpunge_FolderGating encodes the user's cross-protocol
// cancel rule: expunging a Scheduled-folder projection cancels the pending send,
// while expunging from ANY other folder must leave the schedule untouched. The
// folder name is matched case-insensitively because IMAP/EWS may present
// "scheduled" rather than "Scheduled".
func TestCancelScheduledOnExpunge_FolderGating(t *testing.T) {
	srv, d := newScheduledTestServer(t)
	const owner = "u@example.com"
	mk := func(id string, uid uint32) {
		if err := d.CreateScheduledMessageWithLimit(&db.ScheduledMessage{
			ID: id, Owner: owner, From: owner, To: []string{"x@y.com"},
			SendAt: time.Now().Add(time.Hour), Status: "pending", Source: "webmail",
			FolderUID: uid, BlobKey: "blob-" + id,
		}, 100); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	// Expunge from a non-Scheduled folder is a no-op: the schedule survives.
	mk("keep", 11)
	srv.cancelScheduledOnExpunge(owner, "INBOX", 11)
	if _, err := d.GetScheduledMessage("keep"); err != nil {
		t.Fatalf("expunge from INBOX must not cancel the send; got err %v", err)
	}

	// Expunge from the Scheduled folder cancels: the record is removed.
	mk("drop", 22)
	srv.cancelScheduledOnExpunge(owner, "Scheduled", 22)
	if _, err := d.GetScheduledMessage("drop"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("expunge from Scheduled must cancel (delete) the record; got err %v", err)
	}

	// Case-insensitive folder match.
	mk("drop2", 33)
	srv.cancelScheduledOnExpunge(owner, "scheduled", 33)
	if _, err := d.GetScheduledMessage("drop2"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("case-insensitive Scheduled match must cancel; got err %v", err)
	}
}

// TestRetryScheduled_BacksOffThenGivesUp verifies a failed release is retried
// with a forward-pushed SendAt until the attempt cap, after which it is marked
// failed and LEFT VISIBLE — a give-up must never silently drop the message.
func TestRetryScheduled_BacksOffThenGivesUp(t *testing.T) {
	srv, d := newScheduledTestServer(t)
	now := time.Now().UTC()
	mk := func(id string, retries int) *db.ScheduledMessage {
		m := &db.ScheduledMessage{
			ID: id, Owner: "u@example.com", From: "u@example.com", To: []string{"x@y.com"},
			SendAt: now, Status: "pending", Source: "webmail", RetryCount: retries, BlobKey: "b" + id,
		}
		if err := d.CreateScheduledMessageWithLimit(m, 100); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		return m
	}

	// Below the cap: stays pending, SendAt pushed into the future, error recorded.
	early := mk("early", 0)
	srv.retryScheduled(early, now, fmt.Errorf("smtp 451 transient"))
	got, err := d.GetScheduledMessage("early")
	if err != nil {
		t.Fatalf("reload early: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("below cap: status = %q, want pending", got.Status)
	}
	if got.RetryCount != 1 {
		t.Errorf("below cap: RetryCount = %d, want 1", got.RetryCount)
	}
	if !got.SendAt.After(now) {
		t.Errorf("below cap: SendAt %v not pushed past %v", got.SendAt, now)
	}
	if got.LastError == "" {
		t.Error("below cap: LastError not recorded")
	}

	// At the cap (RetryCount scheduledMaxRetries-1 → ++ reaches the cap): give up,
	// mark failed, and the record MUST remain (fail loud, not a silent drop).
	last := mk("last", scheduledMaxRetries-1)
	srv.retryScheduled(last, now, fmt.Errorf("smtp 550 permanent"))
	got2, err := d.GetScheduledMessage("last")
	if err != nil {
		t.Fatalf("failed record must remain visible; got err %v", err)
	}
	if got2.Status != "failed" {
		t.Errorf("at cap: status = %q, want failed", got2.Status)
	}
}
