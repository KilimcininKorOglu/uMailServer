package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// handleAntispamStats returns spam statistics and recent history.
func (s *Server) handleAntispamStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	verdict := r.URL.Query().Get("verdict")
	domain := r.URL.Query().Get("domain")
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	opts := db.SpamHistoryListOptions{
		Domain:  domain,
		Verdict: verdict,
		Limit:   50,
		Offset:  0,
	}

	if startStr != "" {
		if t, err := time.Parse(time.RFC3339, startStr); err == nil {
			opts.Start = t
		}
	}
	if endStr != "" {
		if t, err := time.Parse(time.RFC3339, endStr); err == nil {
			opts.End = t
		}
	}
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	if offsetStr != "" {
		if n, err := strconv.Atoi(offsetStr); err == nil && n >= 0 {
			opts.Offset = n
		}
	}

	entries, total, err := s.db.ListSpamHistory(opts)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to fetch spam history")
		return
	}

	result := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		result = append(result, map[string]interface{}{
			"id":          e.ID,
			"mail_from":   e.MailFrom,
			"rcpt_to":     e.RcptTo,
			"from_header": e.FromHeader,
			"subject":     e.Subject,
			"score":       e.Score,
			"verdict":     e.Verdict,
			"reasons":     e.Reasons,
			"client_ip":   e.ClientIP,
			"helo":        e.Helo,
			"message_id":  e.MessageID,
			"size":        e.Size,
			"timestamp":   e.Timestamp.Format(time.RFC3339),
		})
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"entries": result,
		"total":   total,
		"limit":   opts.Limit,
		"offset":  opts.Offset,
	})
}

// handleAntispamLog manually records a spam event (for testing/webhook integration).
func (s *Server) handleAntispamLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var entry db.SpamHistoryEntry
	if err := decodeJSON(r, &entry); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if entry.Verdict == "" {
		s.sendError(w, http.StatusBadRequest, "verdict is required")
		return
	}

	if err := s.db.LogSpamEvent(&entry); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to log spam event")
		return
	}
	s.sendJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}
