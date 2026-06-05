package api

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/umailserver/umailserver/internal/db"
)

// directoryEntry is one address-book entry for the composer's recipient
// autocomplete (the organization Global Address List).
type directoryEntry struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	Photo      string `json:"photo,omitempty"`      // avatar endpoint URL when the user has a photo
	Title      string `json:"title,omitempty"`      // job title
	Department string `json:"department,omitempty"` // department / team
	Phone      string `json:"phone,omitempty"`      // business phone
}

const maxDirectoryResults = 25

// handleDirectorySearch resolves names/addresses from the organization
// directory (every domain in the caller's tenant), like the Exchange GAL. It
// returns only active accounts and exposes only their address and local part —
// never credentials or quota. GET /api/v1/directory?q=<query>
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
	ownDomain := strings.ToLower(user[at+1:])
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	// The GAL is tenant-scoped: search across every domain in the caller's
	// tenant. Fall back to the caller's own domain for legacy tokens that
	// predate the tenant claim (and for single-domain tenants this is identical).
	scopeDomains := []string{ownDomain}
	if tid, ok := r.Context().Value(contextKeyTenantID).(string); ok && tid != "" {
		if doms, derr := s.db.ListDomainsByTenant(tid); derr == nil && len(doms) > 0 {
			scopeDomains = scopeDomains[:0]
			for _, d := range doms {
				scopeDomains = append(scopeDomains, d.Name)
			}
		}
	}

	var accounts []*db.AccountData
	for _, dom := range scopeDomains {
		da, derr := s.db.ListAccountsByDomain(dom)
		if derr != nil {
			continue
		}
		accounts = append(accounts, da...)
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
			!strings.Contains(strings.ToLower(a.LocalPart), query) &&
			!strings.Contains(strings.ToLower(a.DisplayName), query) {
			continue
		}
		name := a.DisplayName
		if name == "" {
			name = a.LocalPart
		}
		entry := directoryEntry{
			Email:      a.Email,
			Name:       name,
			Title:      a.Title,
			Department: a.Department,
			Phone:      a.Phone,
		}
		if len(a.Avatar) > 0 {
			entry.Photo = "/api/v1/avatar?email=" + url.QueryEscape(a.Email)
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Email < entries[j].Email })
	if len(entries) > maxDirectoryResults {
		entries = entries[:maxDirectoryResults]
	}
	s.sendJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}
