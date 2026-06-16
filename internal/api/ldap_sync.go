package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/audit"
	"github.com/umailserver/umailserver/internal/auth"
	"github.com/umailserver/umailserver/internal/db"
)

// ldapClient is the LDAP client used for provisioning operations.
// It is nil when LDAP is not enabled.
var ldapClient interface {
	SearchUsers(query string, limit int) ([]*auth.LDAPUser, error)
	GetUserByDN(dn string) (*auth.LDAPUser, error)
	ValidateConnection() error
	IsEnabled() bool
}

// SetLDAPClient sets the LDAP client for LDAP provisioning operations.
func (s *Server) SetLDAPClient(client interface {
	SearchUsers(query string, limit int) ([]*auth.LDAPUser, error)
	GetUserByDN(dn string) (*auth.LDAPUser, error)
	ValidateConnection() error
	IsEnabled() bool
}) {
	ldapClient = client
}

// handleLDAPSync handles LDAP search, import, and sync operations.
// Routes:
//   - GET  /api/v1/admin/ldap/search?q=query       Search LDAP users
//   - POST /api/v1/admin/ldap/import                Import selected LDAP users as accounts
//   - POST /api/v1/admin/ldap/validate              Validate LDAP connection
func (s *Server) handleLDAPSync(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleLDAPSearch(w, r)
	case http.MethodPost:
		s.handleLDAPAction(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleLDAPSearch performs an LDAP directory search.
func (s *Server) handleLDAPSearch(w http.ResponseWriter, r *http.Request) {
	if ldapClient == nil || !ldapClient.IsEnabled() {
		s.sendError(w, http.StatusBadRequest, "ldap is not configured")
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		s.sendError(w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}

	users, err := ldapClient.SearchUsers(query, 50)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		result = append(result, map[string]interface{}{
			"dn":           u.DN,
			"username":     u.Username,
			"email":        u.Email,
			"display_name": u.DisplayName,
			"groups":       u.Groups,
			"is_admin":     u.IsAdmin,
		})
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"users": result,
		"total": len(result),
	})
}

// handleLDAPAction handles POST actions: validate, import.
func (s *Server) handleLDAPAction(w http.ResponseWriter, r *http.Request) {
	if ldapClient == nil || !ldapClient.IsEnabled() {
		s.sendError(w, http.StatusBadRequest, "ldap is not configured")
		return
	}

	var req struct {
		Action string   `json:"action"` // "validate", "import"
		Users  []string `json:"users"`  // list of DNs to import
		Domain string   `json:"domain"` // target domain for imported accounts
	}

	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	switch req.Action {
	case "validate":
		if err := ldapClient.ValidateConnection(); err != nil {
			s.sendError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	case "import":
		s.handleLDAPImport(w, r, req.Users, req.Domain)

	default:
		s.sendError(w, http.StatusBadRequest, "unknown action: "+req.Action)
	}
}

type importResult struct {
	DN     string `json:"dn"`
	Email  string `json:"email"`
	Status string `json:"status"` // "imported", "skipped", "error"
	Error  string `json:"error,omitempty"`
}

// handleLDAPImport imports selected LDAP users as mail accounts.
func (s *Server) handleLDAPImport(w http.ResponseWriter, r *http.Request, userDNs []string, domain string) {
	if len(userDNs) == 0 {
		s.sendError(w, http.StatusBadRequest, "no users to import")
		return
	}
	if domain == "" {
		s.sendError(w, http.StatusBadRequest, "domain is required")
		return
	}

	// Verify domain exists
	if _, err := s.db.GetDomain(domain); err != nil {
		s.sendError(w, http.StatusNotFound, "domain not found: "+domain)
		return
	}

	authUser, _ := r.Context().Value("user").(string) //nolint:errcheck
	actor := authUser
	if actor == "" {
		actor = "system"
	}

	results := make([]importResult, 0, len(userDNs))
	for _, dn := range userDNs {
		ldapU, err := ldapClient.GetUserByDN(dn)
		if err != nil {
			results = append(results, importResult{DN: dn, Status: "error", Error: err.Error()})
			continue
		}

		if ldapU.Email == "" {
			results = append(results, importResult{
				DN:     dn,
				Status: "skipped",
				Error:  "no email attribute found for user",
			})
			continue
		}

		// Parse email to get local part; validate domain matches target
		localPart, userDomain := parseEmail(ldapU.Email)
		if userDomain != domain {
			// Email domain doesn't match target domain — use username as local part
			localPart = ldapU.Username
			if localPart == "" {
				localPart = strings.Split(dn, ",")[0] // fall back to DN fragment
			}
		}

		email := localPart + "@" + domain

		// Check if account already exists
		if _, err := s.db.GetAccount(domain, localPart); err == nil {
			results = append(results, importResult{
				DN:     dn,
				Email:  email,
				Status: "skipped",
				Error:  "account already exists",
			})
			continue
		}

		// Generate a random password — the LDAP user authenticates via LDAP,
		// not local password. We still store a hash so the account record is valid.
		password := generateSecureJWTSecret()
		hashedPassword, hashErr := s.hashPassword(password)
		if hashErr != nil {
			results = append(results, importResult{
				DN:     dn,
				Email:  email,
				Status: "error",
				Error:  "failed to hash password",
			})
			continue
		}

		accountRecord := &db.AccountData{
			Email:        email,
			LocalPart:    localPart,
			Domain:       domain,
			PasswordHash:  hashedPassword,
			IsAdmin:      false,
			IsActive:     true,
			DisplayName:  ldapU.DisplayName,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}

		err = s.db.CreateAccount(accountRecord)
		if err != nil {
			results = append(results, importResult{
				DN:     dn,
				Email:  email,
				Status: "error",
				Error:  err.Error(),
			})
			continue
		}

		s.auditLogger.LogAccountCreate(actor, email, audit.ExtractIP(r))
		results = append(results, importResult{
			DN:     dn,
			Email:  email,
			Status: "imported",
		})
	}

	imported := 0
	skipped := 0
	errored := 0
	for _, res := range results {
		switch res.Status {
		case "imported":
			imported++
		case "skipped":
			skipped++
		case "error":
			errored++
		}
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"results": map[string]interface{}{
			"imported": imported,
			"skipped":  skipped,
			"errored":  errored,
			"details":  results,
		},
	})
}
