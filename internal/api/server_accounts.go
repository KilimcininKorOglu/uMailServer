package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/umailserver/umailserver/internal/audit"
	"github.com/umailserver/umailserver/internal/db"
)

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listAccounts(w, r)
	case http.MethodPost:
		s.createAccount(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAccounts lists and creates accounts
//
//	@Summary List accounts
//	@Description Returns a list of all accounts for a domain
//	@Tags Accounts
//	@Produce json
//	@Security BearerAuth
//	@Success 200 {array} map[string]interface{} "List of accounts"
//	@Router /api/v1/accounts [get]
//	@Summary Create account
//	@Description Creates a new email account
//	@Tags Accounts
//	@Accept json
//	@Produce json
//	@Security BearerAuth
//	@Success 201 {object} map[string]interface{} "Account created"
//	@Router /api/v1/accounts [post]
func (s *Server) handleAccountDetail(w http.ResponseWriter, r *http.Request) {
	suffix := r.URL.Path[len("/api/v1/accounts/"):]

	// Handle TOTP 2FA sub-paths
	if len(suffix) > 11 && suffix[len(suffix)-11:] == "/totp/setup" {
		email := suffix[:len(suffix)-11]
		s.handleTOTPSetup(w, r, email)
		return
	}
	if len(suffix) > 13 && suffix[len(suffix)-13:] == "/totp/disable" {
		email := suffix[:len(suffix)-13]
		s.handleTOTPDisable(w, r, email)
		return
	}
	if len(suffix) > 12 && suffix[len(suffix)-12:] == "/totp/verify" {
		email := suffix[:len(suffix)-12]
		s.handleTOTPVerify(w, r, email)
		return
	}

	// Regular account detail
	switch r.Method {
	case http.MethodGet:
		s.getAccount(w, r, suffix)
	case http.MethodPut:
		s.updateAccount(w, r, suffix)
	case http.MethodDelete:
		s.deleteAccount(w, r, suffix)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Account handlers

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	ts := s.callerTenantScope(r)
	authUser, _ := r.Context().Value("user").(string)

	// A genuine non-admin end-user (has an identity but no admin authority) may
	// only view its own account.
	if !ts.isSuperAdmin && !ts.isTenantAdmin && authUser != "" {
		user, domain := parseEmail(authUser)
		account, err := s.db.GetAccount(domain, user)
		if err != nil || account == nil {
			s.sendError(w, http.StatusNotFound, "account not found")
			return
		}
		s.sendJSON(w, http.StatusOK, []map[string]interface{}{accountToJSON(account)})
		return
	}

	// A specific domain was requested: it must be within the caller's scope.
	if requested := r.URL.Query().Get("domain"); requested != "" {
		if !s.allowsDomain(ts, requested) {
			s.sendError(w, http.StatusForbidden, "domain outside your tenant")
			return
		}
		accounts, err := s.db.ListAccountsByDomain(requested)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to list accounts")
			return
		}
		s.sendAccounts(w, accounts)
		return
	}

	// No domain filter: enumerate the domains in the caller's scope. Only a
	// tenant-admin is restricted to its own tenant; a super-admin (or an
	// already-gated caller) sees all domains.
	var domains []*db.DomainData
	var err error
	if ts.isTenantAdmin && !ts.isSuperAdmin {
		domains, err = s.db.ListDomainsByTenant(ts.tenantID)
	} else {
		domains, err = s.db.ListDomains()
	}
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list accounts")
		return
	}

	var accounts []*db.AccountData
	for _, d := range domains {
		domainAccounts, accErr := s.db.ListAccountsByDomain(d.Name)
		if accErr != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to list accounts for domain")
			return
		}
		accounts = append(accounts, domainAccounts...)
	}
	s.sendAccounts(w, accounts)
}

// sendAccounts marshals a list of accounts to JSON.
func (s *Server) sendAccounts(w http.ResponseWriter, accounts []*db.AccountData) {
	var result []map[string]interface{}
	for _, a := range accounts {
		result = append(result, accountToJSON(a))
	}
	s.sendJSON(w, http.StatusOK, result)
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	isAdmin, _ := r.Context().Value("isAdmin").(bool)

	var req struct {
		Email         string `json:"email"`
		Password      string `json:"password"`
		IsAdmin       bool   `json:"is_admin"`
		IsTenantAdmin bool   `json:"is_tenant_admin"`
		QuotaLimit    *int64 `json:"quota_limit"`
		Avatar        string `json:"avatar"` // optional data URL profile photo
		DisplayName   string `json:"display_name"`
		Title         string `json:"title"`
		Department    string `json:"department"`
		Phone         string `json:"phone"`
	}

	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		s.sendError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	// Validate the optional avatar up front so a bad image fails before the
	// account is created.
	var avatarBytes []byte
	var avatarType string
	if req.Avatar != "" {
		mime, raw, perr := parseAvatarDataURL(req.Avatar)
		if perr != nil {
			s.sendError(w, http.StatusBadRequest, perr.Error())
			return
		}
		avatarBytes, avatarType = raw, mime
	}

	if req.QuotaLimit != nil && *req.QuotaLimit < 0 {
		s.sendError(w, http.StatusBadRequest, "quota_limit must be non-negative")
		return
	}

	// Non-admin cannot create admin accounts
	if !isAdmin && req.IsAdmin {
		s.sendError(w, http.StatusForbidden, "only admins can create admin accounts")
		return
	}

	// Validate email format
	if err := validateEmailFormat(req.Email); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid email format")
		return
	}

	// Validate password strength
	if err := validatePassword(req.Password); err != nil {
		s.sendError(w, http.StatusBadRequest, "password does not meet complexity requirements")
		return
	}

	user, domain := parseEmail(req.Email)

	// Tenant scope: a tenant-admin may only create accounts in its own tenant's
	// domains; a super-admin may create in any.
	if !s.allowsDomain(s.callerTenantScope(r), domain) {
		s.sendError(w, http.StatusForbidden, "domain outside your tenant")
		return
	}

	// Reject duplicates: re-creating an existing account must not silently
	// overwrite it (which would reset the existing user's password).
	if _, err := s.db.GetAccount(domain, user); err == nil {
		s.sendError(w, http.StatusConflict, "account already exists")
		return
	}

	// Hash password with configured hasher
	hashedPassword, err := s.hashPassword(req.Password)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	account := &db.AccountData{
		Email:         req.Email,
		LocalPart:     user,
		Domain:        domain,
		PasswordHash:  hashedPassword,
		APOPHash:      fmt.Sprintf("%x", sha256.Sum256([]byte(req.Password))),
		IsAdmin:       req.IsAdmin && isAdmin,
		IsTenantAdmin: req.IsTenantAdmin && isAdmin,
		IsActive:      true,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		Avatar:        avatarBytes,
		AvatarType:    avatarType,
		DisplayName:   req.DisplayName,
		Title:         req.Title,
		Department:    req.Department,
		Phone:         req.Phone,
	}
	if req.QuotaLimit != nil {
		account.QuotaLimit = *req.QuotaLimit
	}

	if err := s.db.CreateAccount(account); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to create account")
		return
	}

	// Provision the standard folders so every protocol (IMAP/JMAP/EWS/webmail)
	// sees a consistent set immediately after creation. Best-effort.
	if s.mailDB != nil {
		if err := s.mailDB.EnsureDefaultMailboxes(req.Email); err != nil {
			s.logger.Warn("failed to provision default mailboxes", "email", req.Email, "error", err)
		}
	}

	// Audit account creation
	actor := "system"
	if authUser := r.Context().Value("user"); authUser != nil {
		if userStr, ok := authUser.(string); ok {
			actor = userStr
		}
	}
	s.auditLogger.LogAccountCreate(actor, req.Email, audit.ExtractIP(r))

	s.sendJSON(w, http.StatusCreated, accountToJSON(account))
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request, email string) {
	user, domain := parseEmail(email)

	if !s.mayAccessAccount(r, email) {
		s.sendError(w, http.StatusForbidden, "access denied")
		return
	}

	account, err := s.db.GetAccount(domain, user)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "account not found")
		return
	}

	s.sendJSON(w, http.StatusOK, accountToJSON(account))
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request, email string) {
	user, domain := parseEmail(email)

	account, err := s.db.GetAccount(domain, user)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "account not found")
		return
	}

	// Authorization check: prevent privilege escalation
	authUser, ok := r.Context().Value("user").(string)
	if !ok || authUser == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	isAdmin, _ := r.Context().Value("isAdmin").(bool)

	if !s.mayAccessAccount(r, email) {
		s.sendError(w, http.StatusForbidden, "access denied")
		return
	}

	// Parse request body first to check IsAdmin modification
	var req struct {
		Password             *string `json:"password"`
		MustChangePassword   *bool   `json:"must_change_password"`
		IsAdmin              *bool   `json:"is_admin"`
		IsTenantAdmin        *bool   `json:"is_tenant_admin"`
		IsActive             *bool   `json:"is_active"`
		ForwardTo            *string `json:"forward_to"`
		ForwardKeepCopy      *bool   `json:"forward_keep_copy"`
		QuotaLimit           *int64  `json:"quota_limit"`
		VacationSettings     *string `json:"vacation_settings"`
		CurrentAdminPassword string  `json:"current_admin_password"`
		DisplayName          *string `json:"display_name"`
		Title                *string `json:"title"`
		Department           *string `json:"department"`
		Phone                *string `json:"phone"`
	}

	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	mustChangePassword, _ := r.Context().Value("mustChangePassword").(bool)
	if mustChangePassword {
		if authUser != email {
			s.sendError(w, http.StatusForbidden, "password change required")
			return
		}
		if req.Password == nil || *req.Password == "" {
			s.sendError(w, http.StatusForbidden, "password change required")
			return
		}
		if req.MustChangePassword != nil || req.IsAdmin != nil || req.IsTenantAdmin != nil || req.IsActive != nil || req.ForwardTo != nil ||
			req.ForwardKeepCopy != nil || req.QuotaLimit != nil || req.VacationSettings != nil ||
			req.CurrentAdminPassword != "" {
			s.sendError(w, http.StatusForbidden, "only password updates are allowed until the required password change is completed")
			return
		}
		if err := validatePassword(*req.Password); err != nil {
			s.sendError(w, http.StatusBadRequest, "password does not meet complexity requirements")
			return
		}
	}

	if req.QuotaLimit != nil && *req.QuotaLimit < 0 {
		s.sendError(w, http.StatusBadRequest, "quota_limit must be non-negative")
		return
	}

	canControlMustChangePassword := isAdmin && authUser != email && req.Password != nil && *req.Password != ""
	if req.MustChangePassword != nil && !canControlMustChangePassword {
		req.MustChangePassword = nil
	}

	requestedIsAdmin := account.IsAdmin
	if req.IsAdmin != nil {
		requestedIsAdmin = *req.IsAdmin
	}

	// Non-admin cannot grant admin privileges
	if !isAdmin && req.IsAdmin != nil && *req.IsAdmin {
		s.sendError(w, http.StatusForbidden, "only admins can grant admin privileges")
		return
	}

	// Admins can only promote other users (not themselves) to admin
	if isAdmin && req.IsAdmin != nil && authUser == email && account.IsAdmin != requestedIsAdmin {
		s.sendError(w, http.StatusForbidden, "cannot modify your own admin status")
		return
	}

	// Admin status changes require re-authentication (current admin password)
	if isAdmin && req.IsAdmin != nil && account.IsAdmin != requestedIsAdmin {
		if req.CurrentAdminPassword == "" {
			s.sendError(w, http.StatusForbidden, "current_admin_password required for admin privilege changes")
			return
		}
		// Verify the acting admin's password
		adminUser, adminDomain := parseEmail(authUser)
		adminAccount, err := s.db.GetAccount(adminDomain, adminUser)
		if err != nil {
			s.sendError(w, http.StatusForbidden, "unable to verify admin credentials")
			return
		}
		matches, _ := s.verifyPassword(req.CurrentAdminPassword, adminAccount.PasswordHash)
		if !matches {
			s.sendError(w, http.StatusForbidden, "invalid current_admin_password")
			return
		}
		// Audit log the privilege change
		ip := audit.ExtractIP(r)
		action := "demoted"
		if requestedIsAdmin {
			action = "promoted"
		}
		s.auditLogger.LogAccountUpdate(authUser, email, ip, []string{"admin_status_" + action})
	}

	if req.Password != nil && *req.Password != "" {
		// Hash new password with configured hasher
		hashedPassword, err := s.hashPassword(*req.Password)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}
		account.PasswordHash = hashedPassword
		account.APOPHash = fmt.Sprintf("%x", sha256.Sum256([]byte(*req.Password)))
		if canControlMustChangePassword {
			if req.MustChangePassword != nil {
				account.MustChangePassword = *req.MustChangePassword
			} else {
				account.MustChangePassword = true
			}
		} else {
			account.MustChangePassword = false
		}
	}
	// Only a global super-admin may change global-admin status; a tenant-admin's
	// req.IsAdmin is ignored (granting true is already rejected above).
	if req.IsAdmin != nil && isAdmin {
		account.IsAdmin = *req.IsAdmin
	}
	// Likewise, only a super-admin may grant/revoke tenant-admin.
	if req.IsTenantAdmin != nil && isAdmin {
		account.IsTenantAdmin = *req.IsTenantAdmin
	}
	if req.IsActive != nil {
		account.IsActive = *req.IsActive
	}
	if req.ForwardTo != nil {
		account.ForwardTo = *req.ForwardTo
	}
	if req.ForwardKeepCopy != nil {
		account.ForwardKeepCopy = *req.ForwardKeepCopy
	}
	if req.QuotaLimit != nil {
		account.QuotaLimit = *req.QuotaLimit
	}
	if req.VacationSettings != nil {
		account.VacationSettings = *req.VacationSettings
		// Bridge the admin-set vacation reply onto the canonical OOF policy
		// (shared with webmail, EWS, and JMAP) so it is visible across every
		// surface. When the canonical store is wired it is the single source, so
		// the legacy field is cleared to avoid a second auto-reply at delivery.
		if s.semStore != nil && *req.VacationSettings != "" {
			cfg, perr := parseLegacyVacationSettings(*req.VacationSettings)
			if perr != nil {
				s.sendError(w, http.StatusBadRequest, "invalid vacation_settings JSON")
				return
			}
			if serr := s.setVacationConfig(email, cfg); serr != nil {
				s.sendError(w, http.StatusInternalServerError, "failed to apply vacation settings")
				return
			}
			account.VacationSettings = ""
		}
	}
	if req.DisplayName != nil {
		account.DisplayName = *req.DisplayName
	}
	if req.Title != nil {
		account.Title = *req.Title
	}
	if req.Department != nil {
		account.Department = *req.Department
	}
	if req.Phone != nil {
		account.Phone = *req.Phone
	}
	account.UpdatedAt = time.Now()

	if err := s.db.UpdateAccount(account); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to update account")
		return
	}

	s.sendJSON(w, http.StatusOK, accountToJSON(account))
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, email string) {
	user, domain := parseEmail(email)

	authUser, _ := r.Context().Value("user").(string)

	if !s.mayAccessAccount(r, email) {
		s.sendError(w, http.StatusForbidden, "access denied")
		return
	}

	if err := s.db.DeleteAccount(domain, user); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to delete account")
		return
	}

	// Audit account deletion
	actor := "system"
	if authUser != "" {
		actor = authUser
	}
	s.auditLogger.LogAccountDelete(actor, email, audit.ExtractIP(r))

	w.WriteHeader(http.StatusNoContent)
}
