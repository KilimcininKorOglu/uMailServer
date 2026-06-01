package api

import (
	"net/http"
	"sort"
	"strings"
)

// directoryEntry is one address-book entry for the composer's recipient
// autocomplete (the organization Global Address List).
type directoryEntry struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

const maxDirectoryResults = 25

// handleDirectorySearch resolves names/addresses from the organization
// directory (the caller's own domain), like the Exchange GAL. It returns only
// active accounts and exposes only their address and local part — never
// credentials or quota. GET /api/v1/directory?q=<query>
func (s *Server) handleDirectorySearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.db == nil {
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"entries": []directoryEntry{}})
		return
	}

	at := strings.LastIndexByte(user, '@')
	if at < 0 {
		s.sendError(w, http.StatusBadRequest, "invalid user")
		return
	}
	domain := strings.ToLower(user[at+1:])
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	accounts, err := s.db.ListAccountsByDomain(domain)
	if err != nil {
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"entries": []directoryEntry{}})
		return
	}

	entries := make([]directoryEntry, 0, len(accounts))
	for _, a := range accounts {
		if !a.IsActive {
			continue
		}
		if strings.EqualFold(a.Email, user) {
			continue // don't suggest the caller to themselves
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(a.Email), query) &&
			!strings.Contains(strings.ToLower(a.LocalPart), query) {
			continue
		}
		entries = append(entries, directoryEntry{Email: a.Email, Name: a.LocalPart})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Email < entries[j].Email })
	if len(entries) > maxDirectoryResults {
		entries = entries[:maxDirectoryResults]
	}
	s.sendJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}
