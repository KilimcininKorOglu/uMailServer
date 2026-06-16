package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/config"
)

// TestHandleAdminDNSHealth_PostNotAllowed verifies the handler rejects
// non-GET methods (the route is documented as a read-only diagnostic).
func TestHandleAdminDNSHealth_PostNotAllowed(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/domains/example.com/dns-check", nil)
	w := httptest.NewRecorder()

	s.handleAdminDNSHealth(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// TestHandleAdminDNSHealth_EmptyDomain verifies an empty domain segment
// (which would otherwise look like a bare prefix match) is rejected.
func TestHandleAdminDNSHealth_EmptyDomain(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/domains//dns-check", nil)
	w := httptest.NewRecorder()

	s.handleAdminDNSHealth(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestHandleAdminDNSHealth_Success exercises the happy path against a real
// domain. The probe runs against live DNS, so we only assert the response
// shape: HTTP 200, JSON with domain + non-empty results slice. Per-record
// status content depends on the live DNS answers for the domain and is not
// pinned by this test.
func TestHandleAdminDNSHealth_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live DNS test in short mode")
	}

	s := &Server{}
	s.liveConfig = &config.Config{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/domains/example.com/dns-check", nil)
	w := httptest.NewRecorder()

	s.handleAdminDNSHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	var body struct {
		Domain  string              `json:"domain"`
		Results []dnsCheckResultDTO `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v; body = %s", err, w.Body.String())
	}
	if body.Domain != "example.com" {
		t.Errorf("domain = %q, want example.com", body.Domain)
	}
	if len(body.Results) == 0 {
		t.Errorf("results slice is empty; want at least one record (MX/SPF/DKIM/DMARC/PTR)")
	}
}
