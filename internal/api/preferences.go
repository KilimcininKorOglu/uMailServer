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
		prefs, err := s.db.GetUIPrefs(user)
		if err != nil {
			prefs = map[string]bool{}
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"preferences": prefs})
	case http.MethodPut, http.MethodPost:
		var prefs map[string]bool
		if err := decodeJSON(r, &prefs); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := s.db.PutUIPrefs(user, prefs); err != nil {
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

const (
	maxSignatureLength = 10000
	maxTemplateLength  = 50000
)

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

	switch r.Method {
	case http.MethodGet:
		sig, err := s.db.GetSignature(user)
		if err != nil {
			sig = ""
		}
		s.sendJSON(w, http.StatusOK, signaturePref{Signature: sig})
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
		if err := s.db.PutSignature(user, pref.Signature); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to save signature")
			return
		}
		s.sendJSON(w, http.StatusOK, pref)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSignatures returns or mutates the user's full signature list.
// GET  /api/v1/signatures          → list all signatures
// POST /api/v1/signatures          → create or replace a named signature (body: {name, body, is_html, ord})
// DELETE /api/v1/signatures?name=X → delete a named signature
func (s *Server) handleSignatures(w http.ResponseWriter, r *http.Request) {
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
		entries, err := s.db.ListSignatures(user)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to load signatures")
			return
		}
		if entries == nil {
			entries = []db.Signature{}
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"signatures": entries})

	case http.MethodPost:
		var entry db.Signature
		if err := decodeJSON(r, &entry); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Name == "" {
			s.sendError(w, http.StatusBadRequest, "signature name is required")
			return
		}
		if len(entry.Name) > 100 {
			s.sendError(w, http.StatusBadRequest, "signature name exceeds 100 characters")
			return
		}
		if len(entry.Body) > maxSignatureLength {
			s.sendError(w, http.StatusBadRequest, "signature body exceeds maximum length of 10000")
			return
		}
		if err := s.db.PutSignatureEntry(user, entry); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to save signature")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"signature": entry})

	case http.MethodDelete:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			s.sendError(w, http.StatusBadRequest, "signature name is required")
			return
		}
		if err := s.db.DeleteSignatureEntry(user, name); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to delete signature")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"deleted": name})

	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleTemplates returns or mutates the user's message template list.
// GET  /api/v1/templates          → list all templates
// POST /api/v1/templates         → create or replace a named template
// DELETE /api/v1/templates?name=X → delete a named template
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
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
		entries, err := s.db.ListTemplates(user)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to load templates")
			return
		}
		if entries == nil {
			entries = []db.Template{}
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"templates": entries})

	case http.MethodPost:
		var entry db.Template
		if err := decodeJSON(r, &entry); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Name == "" {
			s.sendError(w, http.StatusBadRequest, "template name is required")
			return
		}
		if len(entry.Name) > 100 {
			s.sendError(w, http.StatusBadRequest, "template name exceeds 100 characters")
			return
		}
		if len(entry.Body) > maxTemplateLength {
			s.sendError(w, http.StatusBadRequest, "template body exceeds maximum length of 50000")
			return
		}
		if err := s.db.PutTemplateEntry(user, entry); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to save template")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"template": entry})

	case http.MethodDelete:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			s.sendError(w, http.StatusBadRequest, "template name is required")
			return
		}
		if err := s.db.DeleteTemplateEntry(user, name); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to delete template")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"deleted": name})

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
	maxCategories    = 50
	maxCategoryName  = 100
	maxCategoryColor = 32
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

	switch r.Method {
	case http.MethodGet:
		stored, err := s.db.GetCategories(user)
		if err != nil {
			stored = nil
		}
		cats := make([]category, 0, len(stored))
		for _, c := range stored {
			cats = append(cats, category{Name: c.Name, Color: c.Color})
		}
		s.sendJSON(w, http.StatusOK, categoriesPref{Categories: cats})
	case http.MethodPut, http.MethodPost:
		var pref categoriesPref
		if err := decodeJSON(r, &pref); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// Normalize: trim, drop empties, dedupe by name (case-insensitive), cap.
		seen := map[string]bool{}
		normalized := make([]category, 0, len(pref.Categories))
		stored := make([]db.Category, 0, len(pref.Categories))
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
			color := strings.TrimSpace(c.Color)
			normalized = append(normalized, category{Name: name, Color: color})
			stored = append(stored, db.Category{Name: name, Color: color})
			if len(normalized) >= maxCategories {
				break
			}
		}
		pref.Categories = normalized
		if err := s.db.PutCategories(user, stored); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to save categories")
			return
		}
		s.sendJSON(w, http.StatusOK, pref)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
