package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/umailserver/umailserver/internal/auditreader"
)

// logEventDTO mirrors auditreader.Event on the wire. The DTO exists
// only to decouple the API contract from the reader's internal type so
// future changes to the reader (renames, filter-only fields) cannot
// silently leak into the admin surface. No masking, no extras — the
// admin operator sees every field exactly as the audit writer wrote
// it.
type logEventDTO struct {
	Timestamp string            `json:"timestamp"`
	Type      string            `json:"type"`
	User      string            `json:"user,omitempty"`
	IP        string            `json:"ip,omitempty"`
	Success   bool              `json:"success"`
	Service   string            `json:"service"`
	Tenant    string            `json:"tenant,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// logFilterDTO echoes the filter actually applied to the current
// page. The admin UI renders the active filter chips from this object
// so the operator can confirm what they are looking at; nil fields
// (zero values, including the success tri-state) are omitted so the
// rendered chips stay tight.
type logFilterDTO struct {
	Type    string `json:"type,omitempty"`
	User    string `json:"user,omitempty"`
	IP      string `json:"ip,omitempty"`
	Service string `json:"service,omitempty"`
	Success *bool  `json:"success,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}

// logPageDTO is the response shape for both /api/v1/admin/logs and
// /api/v1/admin/logs/tail. Events are always in chronological order
// (oldest first) so the operator's forensic view walks naturally.
// Next is the opaque cursor (empty when exhausted) and HasMore mirrors
// Next for clients that prefer a boolean.
type logPageDTO struct {
	Events  []logEventDTO `json:"events"`
	Next    string        `json:"next"`
	HasMore bool          `json:"has_more"`
	Filters logFilterDTO  `json:"filters"`
}

// logTailDefaultLimit and logTailMaxLimit bound the tail endpoint.
// Tail is intended for a "refresh" button, not a stream; 50 is the
// typical operator sweet spot and 500 mirrors the paged endpoint's
// hard cap.
const (
	logTailDefaultLimit = 50
	logTailMaxLimit     = 500
)

// handleAdminLogs handles GET /api/v1/admin/logs. It returns one page
// of audit events matching the supplied filter, scanning the
// configured active log file followed by rotated archives in
// chronological order.
//
// Audit is disabled (path empty) → 503. The log viewer is meaningless
// without a backing log file; a 200 with an empty list would mask the
// configuration error from the operator. The reader also returns the
// sentinel ErrAuditDisabled for the same condition; the handler treats
// it as 503 to be race-safe (path cleared between the check above and
// the read).
func (s *Server) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	logPath := s.config.AuditLog.Path
	if logPath == "" {
		s.sendError(w, http.StatusServiceUnavailable, "audit logging is disabled")
		return
	}

	q := r.URL.Query()
	filter, ferr := parseLogFilter(q)
	if ferr != nil {
		s.sendError(w, http.StatusBadRequest, ferr.Error())
		return
	}

	limit := auditreader.DefaultLimit
	if raw := q.Get("limit"); raw != "" {
		v, perr := strconv.Atoi(raw)
		if perr != nil || v < auditreader.MinLimit || v > auditreader.MaxLimit {
			s.sendError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = v
	}

	page, err := auditreader.Read(logPath, filter, q.Get("cursor"), limit)
	if err != nil {
		if errors.Is(err, auditreader.ErrAuditDisabled) {
			s.sendError(w, http.StatusServiceUnavailable, "audit logging is disabled")
			return
		}
		s.logger.Error("Failed to read audit log", "error", err, "path", logPath)
		s.sendError(w, http.StatusInternalServerError, "failed to read audit log")
		return
	}

	s.sendJSON(w, http.StatusOK, logPageDTO{
		Events:  mapLogEvents(page.Events),
		Next:    page.Next,
		HasMore: page.HasMore,
		Filters: buildLogFilterDTO(filter),
	})
}

// handleAdminLogsTail handles GET /api/v1/admin/logs/tail. It returns
// the last N events (chronological order, oldest first within the
// window) so the admin "refresh tail" button can catch up after a
// pause. No filter parameters are honored — this endpoint is the
// streaming quick view, not the analysis view.
func (s *Server) handleAdminLogsTail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	logPath := s.config.AuditLog.Path
	if logPath == "" {
		s.sendError(w, http.StatusServiceUnavailable, "audit logging is disabled")
		return
	}

	limit := logTailDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, perr := strconv.Atoi(raw)
		if perr != nil || v < 1 || v > logTailMaxLimit {
			s.sendError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = v
	}

	all, err := auditreader.Tail(logPath, limit)
	if err != nil {
		if errors.Is(err, auditreader.ErrAuditDisabled) {
			s.sendError(w, http.StatusServiceUnavailable, "audit logging is disabled")
			return
		}
		s.logger.Error("Failed to tail audit log", "error", err, "path", logPath)
		s.sendError(w, http.StatusInternalServerError, "failed to read audit log")
		return
	}

	s.sendJSON(w, http.StatusOK, logPageDTO{
		Events:  mapLogEvents(all),
		Next:    "",
		HasMore: false,
		// Filters intentionally empty — the tail endpoint has no
		// query-string filter inputs.
		Filters: logFilterDTO{},
	})
}

// parseLogFilter reads the filter parameters into an auditreader.Filter.
// The only error path is a malformed timestamp or success value;
// empty fields mean "no constraint".
func parseLogFilter(q map[string][]string) (auditreader.Filter, error) {
	f := auditreader.Filter{
		Type:    firstOrEmpty(q, "type"),
		User:    firstOrEmpty(q, "user"),
		IP:      firstOrEmpty(q, "ip"),
		Service: firstOrEmpty(q, "service"),
	}
	if raw := firstOrEmpty(q, "success"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return f, errors.New("success must be true or false")
		}
		f.Success = &b
	}
	if raw := firstOrEmpty(q, "from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, errors.New("from must be an RFC3339 timestamp")
		}
		f.FromTime = t
	}
	if raw := firstOrEmpty(q, "to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, errors.New("to must be an RFC3339 timestamp")
		}
		f.ToTime = t
	}
	return f, nil
}

// firstOrEmpty returns the first value of a query key or "" when the
// key is absent. url.Values.Get already does this, but operating on
// the raw map lets parseLogFilter be exercised from a unit test
// without a *http.Request.
func firstOrEmpty(q map[string][]string, key string) string {
	if vs, ok := q[key]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// mapLogEvents converts a slice of auditreader.Event into the DTO
// shape, always returning a non-nil slice so the admin UI does not
// have to distinguish "no events" from "absent key".
func mapLogEvents(in []auditreader.Event) []logEventDTO {
	out := make([]logEventDTO, 0, len(in))
	for _, ev := range in {
		out = append(out, logEventDTO{
			Timestamp: ev.Timestamp,
			Type:      ev.Type,
			User:      ev.User,
			IP:        ev.IP,
			Success:   ev.Success,
			Service:   ev.Service,
			Tenant:    ev.Tenant,
			Details:   ev.Details,
		})
	}
	return out
}

// buildLogFilterDTO renders the active filter as a wire DTO. The
// success tri-state pointer survives the round-trip; a zero-value
// time is omitted so the rendered chips stay tight.
func buildLogFilterDTO(f auditreader.Filter) logFilterDTO {
	return logFilterDTO{
		Type:    f.Type,
		User:    f.User,
		IP:      f.IP,
		Service: f.Service,
		Success: f.Success,
		From:    timeToString(f.FromTime),
		To:      timeToString(f.ToTime),
	}
}

// timeToString renders a time.Time as an RFC3339 string, or "" for a
// zero value (no bound applied).
func timeToString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
