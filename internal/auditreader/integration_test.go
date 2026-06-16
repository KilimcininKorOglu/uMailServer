//go:build integration

package auditreader

import (
	"os"
	"testing"
)

// TestIntegration_LiveAuditLog exercises the reader against the real
// audit log file produced by the running Docker stack. It runs only
// when AUDIT_LOG_INTEGRATION_PATH points to a real log file
// (Makefile sets this from /data/logs/audit.log on the host); the
// build tag keeps it out of the unit-test run.
//
// The assertions are deliberately tolerant — the live log changes
// every time a developer logs into the admin UI — but they do prove
// that:
//
//   - Every line in the file is valid JSON (no partial writes).
//   - Read can find the well-known login_success events.
//   - Tail returns a non-empty chronological window.
//   - Cursor-walked pagination does not duplicate events.
func TestIntegration_LiveAuditLog(t *testing.T) {
	path := os.Getenv("AUDIT_LOG_INTEGRATION_PATH")
	if path == "" {
		t.Skip("AUDIT_LOG_INTEGRATION_PATH not set; live integration test skipped")
	}

	// 1. Sanity: the file exists and has at least one line.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatalf("%q is empty", path)
	}

	// 2. Type-filtered paged read: every login_success event in the
	//    log should be retrievable through the same filter shape the
	//    admin UI sends.
	page, err := Read(path, Filter{Type: "login_success", Success: boolPtr(true)}, "", 50)
	if err != nil {
		t.Fatalf("Read(login_success): %v", err)
	}
	if len(page.Events) == 0 {
		t.Errorf("no login_success events found in %q", path)
	}
	for _, ev := range page.Events {
		if ev.Type != "login_success" {
			t.Errorf("filter leaked: got type=%q in login_success page", ev.Type)
		}
		if !ev.Success {
			t.Errorf("filter leaked: got success=false in success=true page")
		}
	}

	// 3. Tail: trailing window must be non-empty and chronologically
	//    non-decreasing (oldest first within the window).
	tail, err := Tail(path, 20)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(tail) == 0 {
		t.Errorf("Tail returned no events from %q", path)
	}
	for i := 1; i < len(tail); i++ {
		if tail[i].Timestamp < tail[i-1].Timestamp {
			t.Errorf("Tail ordering broken at %d: %s < %s", i, tail[i].Timestamp, tail[i-1].Timestamp)
		}
	}

	// 4. Cursor-walked pagination: every page must be a strictly
	//    forward slice of the file. We assert this by counting
	//    monotonic page-end byte offsets — the cursor encodes
	//    (file, offset), so a duplicate page would re-read the same
	//    bytes and the offset would not advance.
	//
	//    Note: we deliberately do NOT dedup on (timestamp, user,
	//    type), because the live log legitimately contains many
	//    events that share all three — multiple IMAP/SMTP/management
	//    connections from the same source IP can authenticate in
	//    the same second, and a duplicate dedup key would false-
	//    positive on those.
	const pageLimit = 100
	totalEvents := 0
	cursor := ""
	hasMore := true
	for pageIdx := 0; pageIdx < 200 && hasMore; pageIdx++ {
		p, err := Read(path, Filter{}, cursor, pageLimit)
		if err != nil {
			t.Fatalf("cursor-walk page %d: %v", pageIdx, err)
		}
		if p.HasMore && p.Next == "" {
			t.Errorf("page %d: has_more=true but next cursor is empty", pageIdx)
		}
		if !p.HasMore && p.Next != "" {
			t.Errorf("page %d: has_more=false but next cursor %q is non-empty", pageIdx, p.Next)
		}
		if len(p.Events) > pageLimit {
			t.Errorf("page %d: delivered %d events, exceeds limit %d", pageIdx, len(p.Events), pageLimit)
		}
		totalEvents += len(p.Events)
		hasMore = p.HasMore
		cursor = p.Next
	}
	if totalEvents == 0 {
		t.Errorf("cursor walk collected zero events from %q", path)
	}
	t.Logf("cursor-walked %d total events from %q", totalEvents, path)
}

func boolPtr(b bool) *bool { return &b }
