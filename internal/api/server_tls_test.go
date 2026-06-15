package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

func newTLSTestServer(t *testing.T) *Server {
	t.Helper()
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})
	return NewServer(database, nil, Config{JWTSecret: "test-secret", TokenExpiry: time.Hour})
}

// TestHandleTLSCertificates verifies the admin endpoint serializes the injected
// certificate status, including the expiry the alert and operator rely on.
func TestHandleTLSCertificates(t *testing.T) {
	s := newTLSTestServer(t)
	expiry := time.Now().Add(20 * 24 * time.Hour).UTC().Truncate(time.Second)
	s.SetCertificateStatusFunc(func() []TLSCertificateStatus {
		return []TLSCertificateStatus{
			{Domain: "mail.example.com", Valid: true, ExpiresAt: expiry, Issuer: "Test CA"},
			{Domain: "tenant.test", Valid: false, Error: "acme/autocert: cache miss"},
		}
	})

	rec := httptest.NewRecorder()
	s.handleTLSCertificates(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/tls/certificates", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Certificates []TLSCertificateStatus `json:"certificates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Certificates) != 2 {
		t.Fatalf("certificates = %d, want 2", len(body.Certificates))
	}
	if c := body.Certificates[0]; c.Domain != "mail.example.com" || !c.Valid || !c.ExpiresAt.Equal(expiry) {
		t.Fatalf("first cert = %+v, want mail.example.com valid expiry %v", c, expiry)
	}
	if c := body.Certificates[1]; c.Valid || c.Error == "" {
		t.Fatalf("second cert = %+v, want invalid with an error", c)
	}
}

// TestHandleTLSCertificatesNoProvider verifies the endpoint reports an empty set
// (not a 500) when no status provider is wired.
func TestHandleTLSCertificatesNoProvider(t *testing.T) {
	s := newTLSTestServer(t)

	rec := httptest.NewRecorder()
	s.handleTLSCertificates(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/tls/certificates", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Certificates []TLSCertificateStatus `json:"certificates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Certificates) != 0 {
		t.Fatalf("certificates = %d, want 0", len(body.Certificates))
	}
}

// TestHandleTLSCertificatesMethodNotAllowed rejects non-GET methods.
func TestHandleTLSCertificatesMethodNotAllowed(t *testing.T) {
	s := newTLSTestServer(t)

	rec := httptest.NewRecorder()
	s.handleTLSCertificates(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/tls/certificates", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
