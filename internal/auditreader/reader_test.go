package auditreader

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeNDJSON writes one event per line to path in NDJSON form. The
// timestamp is incremented by 1 minute per event so chronological
// ordering is unambiguous and time-range tests can rely on it.
func writeNDJSON(t *testing.T, path string, events []Event) {
	t.Helper()
	lines := make([]string, 0, len(events))
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i, ev := range events {
		ev.Timestamp = base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		if ev.Type == "" {
			ev.Type = "login_success"
		}
		if ev.Service == "" {
			ev.Service = "api"
		}
		js, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		lines = append(lines, string(js))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

// TestRead_EmptyPath covers the audit-disabled gate: the handler
// translates this to 503, the reader must return ErrAuditDisabled.
func TestRead_EmptyPath(t *testing.T) {
	_, err := Read("", Filter{}, "", 0)
	if err != ErrAuditDisabled {
		t.Fatalf("err = %v, want ErrAuditDisabled", err)
	}
}

// TestRead_NoFiles covers the case where neither the active log nor
// any rotated archive exist yet — fresh deployment, or just-emptied
// directory.
func TestRead_NoFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	page, err := Read(path, Filter{}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 0 {
		t.Errorf("len(events) = %d, want 0", len(page.Events))
	}
	if page.Next != "" {
		t.Errorf("next = %q, want empty", page.Next)
	}
	if page.HasMore {
		t.Errorf("hasMore = true, want false")
	}
}

// TestRead_BasicRead feeds 5 events and asks for them all.
func TestRead_BasicRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{
		{Type: "login_success", User: "alice@x.test", IP: "10.0.0.1", Success: true},
		{Type: "login_failure", User: "mallory@x.test", IP: "10.0.0.2", Success: false, Details: map[string]string{"reason": "bad_password"}},
		{Type: "login_success", User: "bob@x.test", IP: "10.0.0.3", Success: true},
		{Type: "account_create", User: "admin@x.test", IP: "10.0.0.4", Success: true, Details: map[string]string{"target": "carol@x.test"}},
		{Type: "eas_remote_wipe", User: "admin@x.test", IP: "10.0.0.4", Success: true, Details: map[string]string{"target": "device-1"}},
	})
	page, err := Read(path, Filter{}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 5 {
		t.Fatalf("len(events) = %d, want 5", len(page.Events))
	}
	if page.HasMore {
		t.Errorf("hasMore = true, want false on full read")
	}
	if page.Events[0].Type != "login_success" || page.Events[4].Type != "eas_remote_wipe" {
		t.Errorf("events not in expected order: first=%q last=%q", page.Events[0].Type, page.Events[4].Type)
	}
}

// TestRead_Pagination splits 5 events across two pages of size 2.
func TestRead_Pagination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{
		{User: "u1@x.test"},
		{User: "u2@x.test"},
		{User: "u3@x.test"},
		{User: "u4@x.test"},
		{User: "u5@x.test"},
	})

	page1, err := Read(path, Filter{}, "", 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Events) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1.Events))
	}
	if !page1.HasMore {
		t.Errorf("page1 hasMore = false, want true")
	}
	if page1.Next == "" {
		t.Fatalf("page1 next empty, want cursor")
	}
	if page1.Events[0].User != "u1@x.test" || page1.Events[1].User != "u2@x.test" {
		t.Errorf("page1 events = %+v, want [u1, u2]", page1.Events)
	}

	page2, err := Read(path, Filter{}, page1.Next, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Events) != 2 {
		t.Fatalf("page2 len = %d, want 2", len(page2.Events))
	}
	if !page2.HasMore {
		t.Errorf("page2 hasMore = false, want true")
	}
	if page2.Events[0].User != "u3@x.test" || page2.Events[1].User != "u4@x.test" {
		t.Errorf("page2 events = %+v, want [u3, u4]", page2.Events)
	}

	page3, err := Read(path, Filter{}, page2.Next, 2)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3.Events) != 1 {
		t.Fatalf("page3 len = %d, want 1", len(page3.Events))
	}
	if page3.HasMore {
		t.Errorf("page3 hasMore = true, want false (end of stream)")
	}
	if page3.Events[0].User != "u5@x.test" {
		t.Errorf("page3 events[0] = %q, want u5", page3.Events[0].User)
	}
}

// TestRead_FilterByType exercises the type filter, exact match.
func TestRead_FilterByType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{
		{Type: "login_success"},
		{Type: "login_failure"},
		{Type: "login_success"},
		{Type: "account_create"},
	})
	page, err := Read(path, Filter{Type: "login_success"}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 2 {
		t.Errorf("len = %d, want 2", len(page.Events))
	}
	for _, ev := range page.Events {
		if ev.Type != "login_success" {
			t.Errorf("event type = %q, want login_success", ev.Type)
		}
	}
}

// TestRead_FilterByUser exercises the substring, case-insensitive
// user filter. Operationally this lets the admin search "BOB" and hit
// "bob@x.test".
func TestRead_FilterByUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{
		{User: "alice@x.test"},
		{User: "bob@x.test"},
		{User: "carol@x.test"},
		{User: "Bob@OTHER.test"},
	})
	page, err := Read(path, Filter{User: "BOB"}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 2 {
		t.Errorf("len = %d, want 2 (case-insensitive match)", len(page.Events))
	}
}

// TestRead_FilterBySuccess exercises the tri-state success filter,
// including the nil case (any) used by the default "all" UI control.
func TestRead_FilterBySuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{
		{Success: true},
		{Success: false},
		{Success: true},
	})
	tr := true
	page, err := Read(path, Filter{Success: &tr}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 2 {
		t.Errorf("len = %d, want 2", len(page.Events))
	}
	fl := false
	page, err = Read(path, Filter{Success: &fl}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 1 {
		t.Errorf("len = %d, want 1 (false only)", len(page.Events))
	}
	// nil = no filter.
	page, err = Read(path, Filter{}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 3 {
		t.Errorf("len = %d, want 3 (any)", len(page.Events))
	}
}

// TestRead_FilterByService verifies the service filter (api / smtp /
// imap / pop3).
func TestRead_FilterByService(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	evs := []Event{
		{Service: "api"},
		{Service: "smtp"},
		{Service: "imap"},
		{Service: "api"},
	}
	// writeNDJSON defaults Service to "api" if empty — set explicitly.
	lines := make([]string, 0, len(evs))
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i, ev := range evs {
		ev.Timestamp = base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		if ev.Type == "" {
			ev.Type = "login_success"
		}
		js, _ := json.Marshal(ev) //nolint:errcheck // test helper; marshaling valid Event cannot fail //nolint:errcheck // test helper; marshaling valid Event cannot fail
		lines = append(lines, string(js))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := Read(path, Filter{Service: "smtp"}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 1 {
		t.Errorf("len = %d, want 1", len(page.Events))
	}
	if page.Events[0].Service != "smtp" {
		t.Errorf("event service = %q, want smtp", page.Events[0].Service)
	}
}

// TestRead_FilterByTimeRange verifies the from/to timestamp filter.
// Events at 12:00, 12:01, 12:02, 12:03, 12:04; we ask for 12:01..12:03
// and expect the middle three.
func TestRead_FilterByTimeRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{
		{User: "e1"},
		{User: "e2"},
		{User: "e3"},
		{User: "e4"},
		{User: "e5"},
	})
	from := time.Date(2026, 1, 1, 12, 1, 0, 0, time.UTC)
	to := time.Date(2026, 1, 1, 12, 3, 0, 0, time.UTC)
	page, err := Read(path, Filter{FromTime: from, ToTime: to}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 3 {
		t.Errorf("len = %d, want 3", len(page.Events))
	}
}

// TestRead_LimitClamp verifies the [MinLimit, MaxLimit] enforcement.
func TestRead_LimitClamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{
		{User: "e1"}, {User: "e2"}, {User: "e3"}, {User: "e4"}, {User: "e5"},
	})
	// limit=0 → DefaultLimit
	page, err := Read(path, Filter{}, "", 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 5 {
		t.Errorf("default limit: len = %d, want 5", len(page.Events))
	}
	// limit=MaxLimit+10 → MaxLimit
	page, err = Read(path, Filter{}, "", MaxLimit+10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) > MaxLimit {
		t.Errorf("limit clamp: len = %d, want <= MaxLimit=%d", len(page.Events), MaxLimit)
	}
	// limit < 0 → 1
	page, err = Read(path, Filter{}, "", -5)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 1 {
		t.Errorf("negative limit: len = %d, want 1", len(page.Events))
	}
}

// TestRead_SkipsMalformedLines verifies the reader does not abort on
// a corrupt line (partial write at rotation).
func TestRead_SkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	ev := Event{Type: "login_success", User: "ok@x.test", Service: "api"}
	js, _ := json.Marshal(ev) //nolint:errcheck // test helper; marshaling valid Event cannot fail
	bad := "{this is not valid json"
	content := string(js) + "\n" + bad + "\n" + string(js) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := Read(path, Filter{}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 2 {
		t.Errorf("len = %d, want 2 (skip the malformed line)", len(page.Events))
	}
}

// TestRead_RotatedFiles ensures the reader walks active + rotated
// archives in chronological order. Three rotated archives plus the
// active file contain a total of 8 events; we ask for them all.
func TestRead_RotatedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	// Active file: 2 events.
	writeNDJSON(t, path, []Event{{User: "a1"}, {User: "a2"}})
	// Rotated archives — note the timestamp suffix format that
	// internal/audit.rotatingWriter uses (YYYYMMDD-HHMMSS).
	writeNDJSON(t, path+".20260615-100000", []Event{{User: "r1"}, {User: "r2"}, {User: "r3"}})
	writeNDJSON(t, path+".20260614-100000", []Event{{User: "r4"}, {User: "r5"}})
	writeNDJSON(t, path+".20260613-100000", []Event{{User: "r6"}})

	page, err := Read(path, Filter{}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 8 {
		t.Errorf("len = %d, want 8", len(page.Events))
	}
}

// TestRead_CursorAcrossFiles verifies the cursor points to the right
// place when pagination spans rotated archives.
func TestRead_CursorAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{{User: "a1"}, {User: "a2"}})
	writeNDJSON(t, path+".20260615-100000", []Event{{User: "r1"}, {User: "r2"}, {User: "r3"}, {User: "r4"}})
	writeNDJSON(t, path+".20260614-100000", []Event{{User: "r5"}})

	page1, err := Read(path, Filter{}, "", 3)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Events) != 3 {
		t.Fatalf("page1 len = %d, want 3", len(page1.Events))
	}
	// First three events live in the active file (a1, a2) and the
	// first line of the most-recent rotated archive (r1).
	if !page1.HasMore {
		t.Fatalf("page1 hasMore = false, want true")
	}

	page2, err := Read(path, Filter{}, page1.Next, 100)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Events) != 4 {
		t.Errorf("page2 len = %d, want 4 (r2, r3, r4 from the same rotated file + r5 from the older one)", len(page2.Events))
	}
}

// TestRead_MalformedCursor ensures a garbage cursor is treated as
// "start fresh" (empty result) rather than crashing or starting from
// the beginning of the file — the latter would silently resurface
// already-seen events.
func TestRead_MalformedCursor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{{User: "u1"}, {User: "u2"}})
	page, err := Read(path, Filter{}, "not-a-real-cursor", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 0 {
		t.Errorf("len = %d, want 0 (malformed cursor → empty page)", len(page.Events))
	}
}

// TestRead_FilterByIP covers the IP substring filter.
func TestRead_FilterByIP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{
		{IP: "192.168.1.1"},
		{IP: "10.0.0.5"},
		{IP: "192.168.1.42"},
		{IP: "10.0.0.5"},
	})
	page, err := Read(path, Filter{IP: "192.168"}, "", 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(page.Events) != 2 {
		t.Errorf("len = %d, want 2", len(page.Events))
	}
}

// TestEncodeDecodeCursor round-trips the opaque cursor format.
func TestEncodeDecodeCursor(t *testing.T) {
	cases := []struct {
		file   string
		offset int64
	}{
		{"audit.log", 0},
		{"audit.log", 12345},
		{"/data/logs/audit.log", 9876543210},
		{"audit.log.20260615-100000", 4096},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			cur := encodeCursor(tc.file, tc.offset)
			if cur == "" {
				t.Fatalf("encode returned empty for file=%q offset=%d", tc.file, tc.offset)
			}
			gotFile, gotOff, ok := decodeCursor(cur)
			if !ok {
				t.Fatalf("decode failed for %q", cur)
			}
			if gotFile != tc.file || gotOff != tc.offset {
				t.Errorf("round-trip: got (%q, %d), want (%q, %d)", gotFile, gotOff, tc.file, tc.offset)
			}
		})
	}
	// Round-trip the empty cursor (no resume).
	if gotFile, _, ok := decodeCursor(""); !ok || gotFile != "" {
		t.Errorf("empty cursor: ok=%v file=%q", ok, gotFile)
	}
	// Garbage in → ok=false.
	if _, _, ok := decodeCursor("!!!not-base64!!!"); ok {
		t.Errorf("garbage cursor decoded as ok=true")
	}
	// Base64 but not a cursor → ok=false.
	notCursor := base64.StdEncoding.EncodeToString([]byte("no-delimiter-here"))
	if _, _, ok := decodeCursor(notCursor); ok {
		t.Errorf("non-cursor base64 decoded as ok=true")
	}
	// Cursor with non-numeric offset → ok=false.
	wrongOff := base64.StdEncoding.EncodeToString([]byte("file\x1fNaN"))
	if _, _, ok := decodeCursor(wrongOff); ok {
		t.Errorf("non-numeric offset decoded as ok=true")
	}
}

// TestTail returns the last n events of a multi-file log.
func TestTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{{User: "a1"}, {User: "a2"}})
	writeNDJSON(t, path+".20260615-100000", []Event{{User: "r1"}, {User: "r2"}, {User: "r3"}})

	got, err := Tail(path, 3)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].User != "r1" || got[1].User != "r2" || got[2].User != "r3" {
		t.Errorf("tail = %+v, want [r1, r2, r3]", got)
	}
}

// TestTail_EmptyPath covers the audit-disabled gate.
func TestTail_EmptyPath(t *testing.T) {
	_, err := Tail("", 5)
	if err != ErrAuditDisabled {
		t.Fatalf("err = %v, want ErrAuditDisabled", err)
	}
}

// TestTail_NZeroOrNegative returns no events.
func TestTail_NZeroOrNegative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeNDJSON(t, path, []Event{{User: "u1"}})
	for _, n := range []int{0, -1, -100} {
		got, err := Tail(path, n)
		if err != nil {
			t.Errorf("n=%d: Tail: %v", n, err)
		}
		if len(got) != 0 {
			t.Errorf("n=%d: len = %d, want 0", n, len(got))
		}
	}
}
