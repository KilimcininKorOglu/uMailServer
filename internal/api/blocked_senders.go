package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/db"
)

// handleBlockedSenders handles GET/POST /api/v1/blocked-senders
func (s *Server) handleBlockedSenders(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		entries, err := s.db.ListBlockedSenders(user)
		if err != nil {
			s.logger.Error("failed to list blocked senders", "error", err, "user", user)
			s.sendError(w, http.StatusInternalServerError, "failed to get blocked senders")
			return
		}
		if entries == nil {
			entries = []db.BlockedSender{}
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{
			"blocked_senders": entries,
		})

	case http.MethodPost:
		var entry db.BlockedSender
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		entry.Address = strings.ToLower(strings.TrimSpace(entry.Address))
		if entry.Address == "" {
			s.sendError(w, http.StatusBadRequest, "address is required")
			return
		}
		if !strings.HasPrefix(entry.Address, "@") && !strings.Contains(entry.Address, "@") {
			s.sendError(w, http.StatusBadRequest, "address must be an email or @domain")
			return
		}

		entries, err := s.db.ListBlockedSenders(user)
		if err != nil {
			s.logger.Error("failed to list blocked senders", "error", err, "user", user)
			s.sendError(w, http.StatusInternalServerError, "failed to add blocked sender")
			return
		}
		if entries == nil {
			entries = []db.BlockedSender{}
		}
		for _, e := range entries {
			if strings.EqualFold(e.Address, entry.Address) {
				s.sendJSON(w, http.StatusOK, map[string]bool{"added": false})
				return
			}
		}
		entries = append(entries, entry)
		if err := s.db.PutBlockedSenders(user, entries); err != nil {
			s.logger.Error("failed to save blocked senders", "error", err, "user", user)
			s.sendError(w, http.StatusInternalServerError, "failed to add blocked sender")
			return
		}
		s.recompileUserSieve(user)
		s.sendJSON(w, http.StatusOK, map[string]bool{"added": true})

	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleBlockedSenderDelete handles DELETE /api/v1/blocked-senders?address=...
func (s *Server) handleBlockedSenderDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if r.Method != http.MethodDelete {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	address := r.URL.Query().Get("address")
	if address == "" {
		s.sendError(w, http.StatusBadRequest, "address query parameter required")
		return
	}
	address = strings.ToLower(strings.TrimSpace(address))

	entries, err := s.db.ListBlockedSenders(user)
	if err != nil {
		s.logger.Error("failed to list blocked senders", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to delete blocked sender")
		return
	}

	found := false
	filtered := entries[:0]
	for _, e := range entries {
		if !strings.EqualFold(e.Address, address) {
			filtered = append(filtered, e)
		} else {
			found = true
		}
	}
	if !found {
		s.sendError(w, http.StatusNotFound, "address not found in blocked list")
		return
	}

	if err := s.db.PutBlockedSenders(user, filtered); err != nil {
		s.logger.Error("failed to save blocked senders after delete", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to delete blocked sender")
		return
	}
	s.recompileUserSieve(user)
	s.sendJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
