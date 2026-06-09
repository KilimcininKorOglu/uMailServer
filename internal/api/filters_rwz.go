package api

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/umailserver/umailserver/internal/rwz"
	"github.com/umailserver/umailserver/internal/semcore"
)

// maxRwzUpload bounds an uploaded .rwz file. Real rule sets are a few KiB; this
// is generous while still rejecting abuse (the global body limit is 4 MiB).
const maxRwzUpload = 1 << 20 // 1 MiB

// handleFiltersExport serves the caller's inbox filters as an Outlook .rwz file.
//
// GET /api/v1/filters/export
func (s *Server) handleFiltersExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	mbid, err := semcore.NewMailboxId(user)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid mailbox")
		return
	}
	filters, err := s.getUserFilters(user)
	if err != nil {
		s.logger.Error("rwz export: failed to load filters", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to load filters")
		return
	}

	rules := make([]*semcore.Rule, 0, len(filters))
	for _, f := range filters {
		rule, err := filterToRule(f, mbid)
		if err != nil {
			s.logger.Warn("rwz export: skipping unconvertible filter", "error", err, "filter", f.ID)
			continue
		}
		rules = append(rules, rule)
	}

	data, rep, err := rwz.Write(rules)
	if err != nil {
		s.logger.Error("rwz export: encode failed", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to encode rules")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="rules.rwz"`)
	if rep.SkippedRules > 0 || rep.SkippedElements > 0 {
		w.Header().Set("X-Rwz-Skipped", fmt.Sprintf("rules=%d elements=%d", rep.SkippedRules, rep.SkippedElements))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		s.logger.Warn("rwz export: write failed", "error", err, "user", user)
	}
}

// handleFiltersImport creates inbox filters from an uploaded Outlook .rwz file.
//
// POST /api/v1/filters/import  (multipart/form-data, field "file")
func (s *Server) handleFiltersImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := r.ParseMultipartForm(maxRwzUpload); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid upload")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer func() { _ = file.Close() }() //nolint:errcheck // best-effort close of an upload

	data, err := io.ReadAll(io.LimitReader(file, maxRwzUpload+1))
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "failed to read upload")
		return
	}
	if len(data) > maxRwzUpload {
		s.sendError(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}

	rules, rep, err := rwz.Parse(data)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "not a supported .rwz file: "+err.Error())
		return
	}

	existing, _ := s.getUserFilters(user) //nolint:errcheck // empty on error is fine for ordering
	base := len(existing)

	imported, skippedRules := 0, rep.SkippedRules
	now := time.Now()
	for _, rule := range rules {
		f := ruleToFilter(rule)
		// A webmail filter needs at least one condition and one action; a parsed
		// rule that maps to neither (e.g. all elements were outside the subset)
		// cannot be represented, so skip it rather than persist an invalid rule.
		if _, ok := validateFilterPayload(f.Conditions, f.Actions); !ok {
			skippedRules++
			continue
		}
		f.ID = uuid.New().String()
		f.UserID = user
		if f.Name == "" {
			f.Name = fmt.Sprintf("Imported rule %d", base+imported+1)
		}
		f.Priority = base + imported + 1
		f.CreatedAt = now
		f.UpdatedAt = now
		if err := s.saveFilter(f); err != nil {
			s.logger.Error("rwz import: failed to save filter", "error", err, "user", user)
			s.sendError(w, http.StatusInternalServerError, "failed to save imported rule")
			return
		}
		imported++
	}
	if imported > 0 {
		s.recompileUserSieve(user)
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"imported":        imported,
		"skippedRules":    skippedRules,
		"skippedElements": rep.SkippedElements,
		"notes":           rep.Notes,
	})
}
