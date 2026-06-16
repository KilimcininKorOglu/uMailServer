package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestServerWithAuditLog builds a minimal *Server pointed at a
// fresh temp directory. The audit log path inside the temp dir is
// returned alongside the server so the test can write fixture
// NDJSON directly. t.Cleanup removes the dir.
func newTestServerWithAuditLog(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.log")
	srv := &Server{
		config: Config{AuditLog: AuditLogConfig{Path: logPath}},
		logger: slogTestLogger(),
	}
	return srv, logPath
}

// writeNDJSON writes each event as one JSON line to path, replacing
// any prior content. Used to seed the test audit log with
// deterministic fixtures; each test owns its tempdir, so the
// overwrite is safe.
func writeNDJSON(t *testing.T, path string, evs ...map[string]any) {
	t.Helper()
	var sb strings.Builder
	for _, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

// newGET returns a *http.Request for the given path with method GET.
// The path may include a query string.
func newGET(path string) *http.Request {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return httptest.NewRequest(http.MethodGet, path, nil)
}

// decodeLogPage unmarshals a JSON body into a logPageDTO. Test
// helpers that don't care about the filters echo decode with an
// anonymous struct instead — this one is for the assertions that do.
func decodeLogPage(t *testing.T, body []byte) logPageDTO {
	t.Helper()
	var page logPageDTO
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode log page: %v; body=%s", err, body)
	}
	return page
}

// TestHandleAdminLogs_PostNotAllowed covers the method gate on the
// paged endpoint.
func TestHandleAdminLogs_PostNotAllowed(t *testing.T) {
	srv := &Server{
		config: Config{AuditLog: AuditLogConfig{Path: "/tmp/x"}},
		logger: slogTestLogger(),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/logs", nil)
	w := httptest.NewRecorder()
	srv.handleAdminLogs(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// TestHandleAdminLogsTail_PostNotAllowed covers the method gate on the
// tail endpoint.
func TestHandleAdminLogsTail_PostNotAllowed(t *testing.T) {
	srv := &Server{
		config: Config{AuditLog: AuditLogConfig{Path: "/tmp/x"}},
		logger: slogTestLogger(),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/logs/tail", nil)
	w := httptest.NewRecorder()
	srv.handleAdminLogsTail(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// TestHandleAdminLogs_AuditDisabled covers the path-empty branch:
// the handler must surface 503 (not 200-with-empty), so a misconfigured
// deployment does not silently render an empty log viewer.
func TestHandleAdminLogs_AuditDisabled(t *testing.T) {
	srv := &Server{
		config: Config{}, // AuditLog.Path is "" → audit disabled
		logger: slogTestLogger(),
	}
	req := newGET("/api/v1/admin/logs")
	w := httptest.NewRecorder()
	srv.handleAdminLogs(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

// TestHandleAdminLogsTail_AuditDisabled covers the path-empty branch
// on the tail endpoint.
func TestHandleAdminLogsTail_AuditDisabled(t *testing.T) {
	srv := &Server{
		config: Config{},
		logger: slogTestLogger(),
	}
	req := newGET("/api/v1/admin/logs/tail")
	w := httptest.NewRecorder()
	srv.handleAdminLogsTail(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

// TestHandleAdminLogs_EmptyLog feeds an empty log file and asserts the
// handler returns 200 with an empty (non-nil) events slice and a
// has_more=false cursor.
func TestHandleAdminLogs_EmptyLog(t *testing.T) {
	srv, logPath := newTestServerWithAuditLog(t)
	// Touch the file so the reader's "no files" branch is bypassed.
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("touch: %v", err)
	}
	req := newGET("/api/v1/admin/logs")
	w := httptest.NewRecorder()
	srv.handleAdminLogs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	page := decodeLogPage(t, w.Body.Bytes())
	if page.Events == nil {
		t.Errorf("events = nil, want []")
	}
	if len(page.Events) != 0 {
		t.Errorf("len(events) = %d, want 0", len(page.Events))
	}
	if page.HasMore {
		t.Errorf("has_more = true, want false")
	}
	if page.Next != "" {
		t.Errorf("next = %q, want empty", page.Next)
	}
}

// TestHandleAdminLogs_BasicReadAndFilter seeds three events, then
// exercises type / user / service / success filters. The test asserts
// only the matching event survives each filter and that the filters
// echo carries the value the operator applied.
func TestHandleAdminLogs_BasicReadAndFilter(t *testing.T) {
	srv, logPath := newTestServerWithAuditLog(t)
	writeNDJSON(t, logPath,
		map[string]any{
			"timestamp": "2026-01-01T10:00:00Z",
			"type":      "login_success",
			"user":      "alice@example.test",
			"ip":        "10.0.0.1",
			"success":   true,
			"service":   "api",
			"details":   map[string]string{"method": "password"},
		},
		map[string]any{
			"timestamp": "2026-01-01T11:00:00Z",
			"type":      "login_failure",
			"user":      "bob@example.test",
			"ip":        "10.0.0.2",
			"success":   false,
			"service":   "api",
			"details":   map[string]string{"reason": "bad_password"},
		},
		map[string]any{
			"timestamp": "2026-01-01T12:00:00Z",
			"type":      "account_create",
			"user":      "carol@example.test",
			"ip":        "10.0.0.3",
			"success":   true,
			"service":   "api",
		},
	)

	type tcase struct {
		name       string
		query      string
		wantCount  int
		wantType   string
		wantUser   string
		wantFilter logFilterDTO
	}
	successTrue := true
	cases := []tcase{
		{
			name:      "type=login_success keeps only matches",
			query:     "type=login_success",
			wantCount: 1,
			wantType:  "login_success",
			wantFilter: logFilterDTO{
				Type: "login_success",
			},
		},
		{
			name:      "user=bob substring match",
			query:     "user=bob",
			wantCount: 1,
			wantUser:  "bob@example.test",
			wantFilter: logFilterDTO{
				User: "bob",
			},
		},
		{
			name:      "service=api matches all three",
			query:     "service=api",
			wantCount: 3,
		},
		{
			name:      "success=false keeps only the failure event",
			query:     "success=false",
			wantCount: 1,
			wantType:  "login_failure",
			wantFilter: logFilterDTO{
				Success: &successTrue, // overwriting: see note below
			},
		},
		{
			name:      "from bounds drop the 10:00 event",
			query:     "from=2026-01-01T10%3A30%3A00Z",
			wantCount: 2,
		},
	}
	// success=false echoes false. Replace the placeholder above.
	cases[3].wantFilter.Success = boolPtr(false)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := newGET("/api/v1/admin/logs?" + tc.query)
			w := httptest.NewRecorder()
			srv.handleAdminLogs(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
			}
			page := decodeLogPage(t, w.Body.Bytes())
			if len(page.Events) != tc.wantCount {
				t.Errorf("len(events) = %d, want %d; events=%+v", len(page.Events), tc.wantCount, page.Events)
			}
			if tc.wantType != "" && len(page.Events) > 0 && page.Events[0].Type != tc.wantType {
				t.Errorf("events[0].type = %q, want %q", page.Events[0].Type, tc.wantType)
			}
			if tc.wantUser != "" && len(page.Events) > 0 && page.Events[0].User != tc.wantUser {
				t.Errorf("events[0].user = %q, want %q", page.Events[0].User, tc.wantUser)
			}
			if tc.wantFilter.Type != "" && page.Filters.Type != tc.wantFilter.Type {
				t.Errorf("filters.type = %q, want %q", page.Filters.Type, tc.wantFilter.Type)
			}
			if tc.wantFilter.User != "" && page.Filters.User != tc.wantFilter.User {
				t.Errorf("filters.user = %q, want %q", page.Filters.User, tc.wantFilter.User)
			}
			if tc.wantFilter.Success != nil {
				if page.Filters.Success == nil {
					t.Errorf("filters.success = nil, want %v", *tc.wantFilter.Success)
				} else if *page.Filters.Success != *tc.wantFilter.Success {
					t.Errorf("filters.success = %v, want %v", *page.Filters.Success, *tc.wantFilter.Success)
				}
			}
			if tc.wantFilter.From != "" && page.Filters.From != tc.wantFilter.From {
				t.Errorf("filters.from = %q, want %q", page.Filters.From, tc.wantFilter.From)
			}
		})
	}
}

// TestHandleAdminLogs_BadQuery asserts the handler returns 400 for
// malformed filter parameters rather than silently ignoring them.
func TestHandleAdminLogs_BadQuery(t *testing.T) {
	srv, _ := newTestServerWithAuditLog(t)
	cases := []string{
		"success=maybe",                  // not a bool
		"from=not-a-timestamp",           // not RFC3339
		"to=2026-01-01",                  // date only is not RFC3339
		"limit=0",                        // below min
		"limit=501",                      // above max
		"limit=abc",                      // not an int
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			req := newGET("/api/v1/admin/logs?" + q)
			w := httptest.NewRecorder()
			srv.handleAdminLogs(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// TestHandleAdminLogs_Pagination seeds five events, walks the cursor
// across two pages of limit=2, and verifies every event is delivered
// exactly once with no extras.
func TestHandleAdminLogs_Pagination(t *testing.T) {
	srv, logPath := newTestServerWithAuditLog(t)
	evs := []map[string]any{}
	for i := 0; i < 5; i++ {
		evs = append(evs, map[string]any{
			"timestamp": fmt.Sprintf("2026-01-01T10:%02d:00Z", i),
			"type":      "login_success",
			"user":      fmt.Sprintf("u%d@example.test", i),
			"success":   true,
			"service":   "api",
		})
	}
	writeNDJSON(t, logPath, evs...)

	seen := map[string]bool{}
	cursor := ""
	for pageIdx := 0; pageIdx < 5; pageIdx++ {
		path := "/api/v1/admin/logs?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		req := newGET(path)
		w := httptest.NewRecorder()
		srv.handleAdminLogs(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("page %d: status = %d, want %d; body=%s", pageIdx, w.Code, http.StatusOK, w.Body.String())
		}
		page := decodeLogPage(t, w.Body.Bytes())
		for _, ev := range page.Events {
			if seen[ev.User] {
				t.Errorf("user %q returned twice across pages", ev.User)
			}
			seen[ev.User] = true
		}
		if !page.HasMore {
			if len(page.Events) == 0 {
				break
			}
			// Last page should be ≤ limit and have has_more=false.
			if len(page.Events) > 2 {
				t.Errorf("last page delivered %d events with has_more=false", len(page.Events))
			}
			break
		}
		if page.Next == "" {
			t.Fatalf("page %d: has_more=true but next is empty", pageIdx)
		}
		cursor = page.Next
	}
	if len(seen) != 5 {
		t.Errorf("total distinct events seen = %d, want 5; seen=%+v", len(seen), seen)
	}
}

// TestHandleAdminLogsTail_LastN seeds six events and asks for the
// trailing three. The reader returns chronological order (oldest
// first within the window), so the last three written should be the
// first three in the response.
func TestHandleAdminLogsTail_LastN(t *testing.T) {
	srv, logPath := newTestServerWithAuditLog(t)
	evs := []map[string]any{}
	for i := 0; i < 6; i++ {
		evs = append(evs, map[string]any{
			"timestamp": fmt.Sprintf("2026-01-01T10:%02d:00Z", i),
			"type":      "login_success",
			"user":      fmt.Sprintf("u%d@example.test", i),
			"success":   true,
			"service":   "api",
		})
	}
	writeNDJSON(t, logPath, evs...)

	req := newGET("/api/v1/admin/logs/tail?limit=3")
	w := httptest.NewRecorder()
	srv.handleAdminLogsTail(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	page := decodeLogPage(t, w.Body.Bytes())
	if len(page.Events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(page.Events))
	}
	// Expect u3, u4, u5 in chronological order.
	for i, want := range []string{"u3@example.test", "u4@example.test", "u5@example.test"} {
		if page.Events[i].User != want {
			t.Errorf("events[%d].user = %q, want %q", i, page.Events[i].User, want)
		}
	}
	if page.Next != "" || page.HasMore {
		t.Errorf("tail: next=%q has_more=%v, want empty/false", page.Next, page.HasMore)
	}
}

// TestHandleAdminLogsTail_BadLimit covers the validation path on the
// tail endpoint.
func TestHandleAdminLogsTail_BadLimit(t *testing.T) {
	srv, _ := newTestServerWithAuditLog(t)
	for _, q := range []string{"limit=0", "limit=501", "limit=abc"} {
		t.Run(q, func(t *testing.T) {
			req := newGET("/api/v1/admin/logs/tail?" + q)
			w := httptest.NewRecorder()
			srv.handleAdminLogsTail(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// TestParseLogFilter_AcceptsValues exercises the helper directly so
// the wire-format expectations stay coupled to the parser.
func TestParseLogFilter_AcceptsValues(t *testing.T) {
	in := url.Values{}
	in.Set("type", "login_success")
	in.Set("user", "alice")
	in.Set("ip", "10.0.0.1")
	in.Set("service", "api")
	in.Set("success", "true")
	in.Set("from", "2026-01-01T00:00:00Z")
	in.Set("to", "2026-01-02T00:00:00Z")
	f, err := parseLogFilter(in)
	if err != nil {
		t.Fatalf("parseLogFilter: %v", err)
	}
	if f.Type != "login_success" || f.User != "alice" || f.IP != "10.0.0.1" || f.Service != "api" {
		t.Errorf("scalar fields wrong: %+v", f)
	}
	if f.Success == nil || !*f.Success {
		t.Errorf("success = %v, want pointer to true", f.Success)
	}
	if f.FromTime.IsZero() || f.ToTime.IsZero() {
		t.Errorf("from/to not parsed: from=%v to=%v", f.FromTime, f.ToTime)
	}
	if !f.FromTime.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("from = %v, want 2026-01-01T00:00:00Z", f.FromTime)
	}
}

// TestParseLogFilter_RejectsMalformed covers the error paths.
func TestParseLogFilter_RejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"bad success": "success=foo",
		"bad from":    "from=yesterday",
		"bad to":      "to=now",
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLogFilter(urlValues(t, q)); err == nil {
				t.Errorf("expected error for %q, got nil", q)
			}
		})
	}
}

// urlValues parses a "k=v&k=v" string into url.Values for tests
// that don't otherwise need a *http.Request.
func urlValues(t *testing.T, q string) map[string][]string {
	t.Helper()
	v, err := url.ParseQuery(q)
	if err != nil {
		t.Fatalf("url.ParseQuery(%q): %v", q, err)
	}
	return v
}

// boolPtr returns &b. Defined as a small named helper so the test
// table can be a struct literal.
func boolPtr(b bool) *bool { return &b }

// slogTestLogger returns a *slog.Logger that discards output, suitable
// for unit tests. The handler chain writes to io.Discard so noisy
// error paths in the SUT (which call logger.Error on read failures)
// do not pollute the test output.
func slogTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
