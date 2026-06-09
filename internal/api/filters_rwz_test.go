package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rwzImportRequest builds a JSON import request carrying data as base64, matching
// the application/json contract enforced by the API CSRF guard.
func rwzImportRequest(user string, data []byte) *http.Request {
	body, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(data)}) //nolint:errcheck // marshaling a string map never fails
	req := httptest.NewRequest("POST", "/api/v1/filters/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req = req.WithContext(withUser(req.Context(), user))
	}
	return req
}

func exportRwz(t *testing.T, server *Server, user string) []byte {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/filters/export", nil)
	req = req.WithContext(withUser(req.Context(), user))
	w := httptest.NewRecorder()
	server.handleFiltersExport(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "rules.rwz") {
		t.Errorf("Content-Disposition = %q, want it to name rules.rwz", cd)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	return w.Body.Bytes()
}

// TestFiltersExportImportRoundTrip creates filters for one user, exports them to
// .rwz, and imports the result for a second user, asserting the conditions and
// actions survive the binary round-trip end to end.
func TestFiltersExportImportRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	server := newFilterTestServer(t, tmpDir)

	const srcUser = "src@example.com"
	createTestFilter(t, server, srcUser) // From contains test@example.com -> move Junk
	createTestFilter(t, server, srcUser)

	data := exportRwz(t, server, srcUser)
	if len(data) == 0 {
		t.Fatal("export produced no bytes")
	}

	const dstUser = "dst@example.com"
	req := rwzImportRequest(dstUser, data)
	w := httptest.NewRecorder()
	server.handleFiltersImport(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Imported        int `json:"imported"`
		SkippedRules    int `json:"skippedRules"`
		SkippedElements int `json:"skippedElements"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if resp.Imported != 2 {
		t.Errorf("imported = %d, want 2 (skippedRules=%d skippedElements=%d)", resp.Imported, resp.SkippedRules, resp.SkippedElements)
	}

	got, err := server.getUserFilters(dstUser)
	if err != nil {
		t.Fatalf("getUserFilters: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("imported filter count = %d, want 2", len(got))
	}
	for _, f := range got {
		if len(f.Conditions) != 1 || f.Conditions[0].Field != "from" || f.Conditions[0].Value != "test@example.com" {
			t.Errorf("imported condition = %+v, want from contains test@example.com", f.Conditions)
		}
		if len(f.Actions) != 1 || f.Actions[0].Type != "moveToFolder" || f.Actions[0].Target != "Junk" {
			t.Errorf("imported action = %+v, want moveToFolder Junk", f.Actions)
		}
	}
}

func TestFiltersExportUnauthorized(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())
	req := httptest.NewRequest("GET", "/api/v1/filters/export", nil)
	w := httptest.NewRecorder()
	server.handleFiltersExport(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestFiltersExportMethodNotAllowed(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())
	req := httptest.NewRequest("POST", "/api/v1/filters/export", nil)
	req = req.WithContext(withUser(req.Context(), "u@example.com"))
	w := httptest.NewRecorder()
	server.handleFiltersExport(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestFiltersImportUnauthorized(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())
	req := rwzImportRequest("", []byte{0})
	w := httptest.NewRecorder()
	server.handleFiltersImport(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestFiltersImportRejectsBadFile(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())
	req := rwzImportRequest("u@example.com", []byte("this is not an rwz file"))
	w := httptest.NewRecorder()
	server.handleFiltersImport(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestFiltersImportRejectsOversize(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())
	req := rwzImportRequest("u@example.com", bytes.Repeat([]byte{0}, maxRwzUpload+128))
	w := httptest.NewRecorder()
	server.handleFiltersImport(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", w.Code)
	}
}

func TestFiltersImportMethodNotAllowed(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())
	req := httptest.NewRequest("GET", "/api/v1/filters/import", nil)
	req = req.WithContext(withUser(req.Context(), "u@example.com"))
	w := httptest.NewRecorder()
	server.handleFiltersImport(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}
