package api

import (
	"net/http"
	"strings"

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

// category is one named master category with a display color, the Exchange-style
// colored classification a user can apply to messages as a label.
type category struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type categoriesPref struct {
	Categories []category `json:"categories"`
}

const (
	maxCategories     = 50
	maxCategoryName   = 100
	maxCategoryColor  = 32
	categoriesPrefKey = ":categories"
)

// handleCategories stores and returns the user's master category list (name +
// color), used to render message labels with a consistent color. Stored under
// its own preferences key, separate from the per-message Labels themselves.
func (s *Server) handleCategories(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.db == nil {
		s.sendError(w, http.StatusInternalServerError, "database not available")
		return
	}

	key := user + categoriesPrefKey
	switch r.Method {
	case http.MethodGet:
		var pref categoriesPref
		if err := s.db.Get(db.BucketPreferences, key, &pref); err != nil {
			pref = categoriesPref{}
		}
		if pref.Categories == nil {
			pref.Categories = []category{}
		}
		s.sendJSON(w, http.StatusOK, pref)
	case http.MethodPut, http.MethodPost:
		var pref categoriesPref
		if err := decodeJSON(r, &pref); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// Normalize: trim, drop empties, dedupe by name (case-insensitive), cap.
		seen := map[string]bool{}
		normalized := make([]category, 0, len(pref.Categories))
		for _, c := range pref.Categories {
			name := strings.TrimSpace(c.Name)
			if name == "" || len(name) > maxCategoryName || len(c.Color) > maxCategoryColor {
				continue
			}
			lc := strings.ToLower(name)
			if seen[lc] {
				continue
			}
			seen[lc] = true
			normalized = append(normalized, category{Name: name, Color: strings.TrimSpace(c.Color)})
			if len(normalized) >= maxCategories {
				break
			}
		}
		pref.Categories = normalized
		if err := s.db.Put(db.BucketPreferences, key, pref); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to save categories")
			return
		}
		s.sendJSON(w, http.StatusOK, pref)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
