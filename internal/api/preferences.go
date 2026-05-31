package api

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/db"
)

// handlePreferences stores and returns per-user UI preferences (the settings
// toggles), so they persist across reloads instead of living only in the SPA.
func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.db == nil {
		s.sendError(w, http.StatusInternalServerError, "database not available")
		return
	}

	switch r.Method {
	case http.MethodGet:
		prefs := map[string]bool{}
		if err := s.db.Get(db.BucketPreferences, user, &prefs); err != nil {
			prefs = map[string]bool{}
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"preferences": prefs})
	case http.MethodPut, http.MethodPost:
		var prefs map[string]bool
		if err := decodeJSON(r, &prefs); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := s.db.Put(db.BucketPreferences, user, prefs); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to save preferences")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"preferences": prefs})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
