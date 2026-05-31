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

// signaturePref holds the user's outgoing-mail signature. It is persisted under
// its own key in the preferences bucket, separate from the boolean UI toggles
// in handlePreferences (which store a map[string]bool under the bare user key).
type signaturePref struct {
	Signature string `json:"signature"`
}

const maxSignatureLength = 10000

// handleSignature stores and returns the authenticated user's email signature,
// which the webmail composer appends to outgoing messages.
func (s *Server) handleSignature(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.db == nil {
		s.sendError(w, http.StatusInternalServerError, "database not available")
		return
	}

	key := user + ":signature"
	switch r.Method {
	case http.MethodGet:
		var pref signaturePref
		if err := s.db.Get(db.BucketPreferences, key, &pref); err != nil {
			pref = signaturePref{}
		}
		s.sendJSON(w, http.StatusOK, pref)
	case http.MethodPut, http.MethodPost:
		var pref signaturePref
		if err := decodeJSON(r, &pref); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(pref.Signature) > maxSignatureLength {
			s.sendError(w, http.StatusBadRequest, "signature exceeds maximum length of 10000")
			return
		}
		if err := s.db.Put(db.BucketPreferences, key, pref); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to save signature")
			return
		}
		s.sendJSON(w, http.StatusOK, pref)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
