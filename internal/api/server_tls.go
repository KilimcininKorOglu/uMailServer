package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// TLSCertificateStatus is the admin-facing summary of one domain's certificate.
// It mirrors the TLS manager's status without coupling the api package to the
// tls package; the orchestrator adapts between them when wiring the provider.
type TLSCertificateStatus struct {
	Domain    string    `json:"domain"`
	Valid     bool      `json:"valid"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	Issuer    string    `json:"issuer,omitempty"`
	Warning   string    `json:"warning,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// SetCertificateStatusFunc injects the provider that lists the current TLS
// certificate status for every authoritative domain. Without it the endpoint
// reports an empty set rather than failing.
func (s *Server) SetCertificateStatusFunc(fn func() []TLSCertificateStatus) {
	s.certificateStatusFunc = fn
}

// handleTLSCertificates serves the admin-gated TLS certificate inventory:
// GET /api/v1/admin/tls/certificates.
func (s *Server) handleTLSCertificates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method_not_allowed", "Only GET is supported", http.StatusMethodNotAllowed)
		return
	}

	statuses := []TLSCertificateStatus{}
	if s.certificateStatusFunc != nil {
		statuses = s.certificateStatusFunc()
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"certificates": statuses}); err != nil {
		s.logger.Error("failed to encode TLS certificate status", "error", err)
	}
}
