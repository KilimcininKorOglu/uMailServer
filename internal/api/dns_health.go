package api

import (
	"io"
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/cli"
)

// dnsCheckResultDTO mirrors internal/cli.DNSCheckResult so the admin UI
// (web/admin/src/pages/Domains.tsx) can render the per-record status without
// leaking the cli's internal field names.
type dnsCheckResultDTO struct {
	RecordType string `json:"record_type"`
	RecordName string `json:"record_name"`
	Expected   string `json:"expected"`
	Found      string `json:"found"`
	Status     string `json:"status"` // pass | fail | warning
	Message    string `json:"message"`
}

// handleAdminDNSHealth handles GET /api/v1/admin/domains/{domain}/dns-check.
// It reuses the same DNS probing logic as the `umailserver` CLI's
// `diagnose dns` command (MX/SPF/DKIM/DMARC/PTR) but exposes it as JSON for
// the admin Domains page. CLI-style stdout chatter is silenced via
// io.Discard; all output flows through the structured DNSCheckResult slice.
func (s *Server) handleAdminDNSHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// /api/v1/admin/domains/{domain}/dns-check[?...]
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/domains/")
	if i := strings.Index(trimmed, "/"); i >= 0 {
		trimmed = trimmed[:i]
	}
	domain := strings.ToLower(strings.TrimSpace(trimmed))
	if domain == "" {
		s.sendError(w, http.StatusBadRequest, "domain required")
		return
	}

	diag := cli.NewDiagnosticsWithWriter(s.liveConfig, io.Discard)
	results, err := diag.CheckDNS(domain)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "dns check failed: "+err.Error())
		return
	}

	out := make([]dnsCheckResultDTO, 0, len(results))
	for _, r := range results {
		out = append(out, dnsCheckResultDTO{
			RecordType: r.RecordType,
			RecordName: r.RecordName,
			Expected:   r.Expected,
			Found:      r.Found,
			Status:     r.Status,
			Message:    r.Message,
		})
	}
	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"domain":  domain,
		"results": out,
	})
}
