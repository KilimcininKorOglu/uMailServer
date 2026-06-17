package api

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/auth"
	"github.com/umailserver/umailserver/internal/db"
)

// domainEgressIPGroupKey is the per-domain setting holding the Relay IP-group
// name the domain's outbound mail egresses from. It mirrors the constant the
// queue/server relay path reads, so the admin write and the delivery read agree.
const domainEgressIPGroupKey = "egress.ip_group"

// setDomainEgressIPGroup stores (or clears, when empty) the domain's egress IP
// group assignment in its Settings map.
func setDomainEgressIPGroup(domain *db.DomainData, group string) {
	group = strings.TrimSpace(group)
	if domain.Settings == nil {
		if group == "" {
			return
		}
		domain.Settings = map[string]string{}
	}
	if group == "" {
		delete(domain.Settings, domainEgressIPGroupKey)
		return
	}
	domain.Settings[domainEgressIPGroupKey] = group
}

// handleDomains lists and creates domains
//
//	@Summary List domains
//	@Description Returns a list of all domains
//	@Tags Domains
//	@Produce json
//	@Security BearerAuth
//	@Success 200 {array} map[string]interface{} "List of domains"
//	@Router /api/v1/domains [get]
//	@Summary Create domain
//	@Description Creates a new domain
//	@Tags Domains
//	@Accept json
//	@Produce json
//	@Security BearerAuth
//	@Success 201 {object} map[string]interface{} "Domain created"
//	@Router /api/v1/domains [post]
func (s *Server) handleDomains(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listDomains(w, r)
	case http.MethodPost:
		s.createDomain(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleDomainDetail(w http.ResponseWriter, r *http.Request) {
	domain := strings.TrimPrefix(r.URL.Path, "/api/v1/domains/")

	switch r.Method {
	case http.MethodGet:
		s.getDomain(w, r, domain)
	case http.MethodPut:
		s.updateDomain(w, r, domain)
	case http.MethodDelete:
		s.deleteDomain(w, r, domain)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Domain handlers

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	ts := s.callerTenantScope(r)
	domains, err := s.db.ListDomains()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list domains")
		return
	}

	var result []map[string]interface{}
	for _, d := range domains {
		// A tenant-admin only sees domains owned by its tenant.
		if !ts.allowsTenant(d.TenantID) {
			continue
		}
		result = append(result, domainToJSON(d))
	}

	s.sendJSON(w, http.StatusOK, result)
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	// Provisioning a new domain is a super-admin operation; a tenant-admin
	// cannot add domains to its tenant in this phase.
	if ts := s.callerTenantScope(r); ts.isTenantAdmin && !ts.isSuperAdmin {
		s.sendError(w, http.StatusForbidden, "only a super-admin can create domains")
		return
	}

	var req struct {
		Name              string `json:"name"`
		MaxAccounts       int    `json:"max_accounts"`
		MaxMailboxSize    int64  `json:"max_mailbox_size"`
		QuotaWarn         int64  `json:"quota_warn"`
		QuotaProhibitSend int64  `json:"quota_prohibit_send"`
	}

	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		s.sendError(w, http.StatusBadRequest, "domain name is required")
		return
	}
	if req.MaxMailboxSize < 0 || req.QuotaWarn < 0 || req.QuotaProhibitSend < 0 {
		s.sendError(w, http.StatusBadRequest, "quota thresholds must be non-negative")
		return
	}

	// Validate domain name format
	if err := validateDomainName(req.Name); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid domain name")
		return
	}

	// Validate max accounts if provided
	if req.MaxAccounts < 0 {
		s.sendError(w, http.StatusBadRequest, "max_accounts must be non-negative")
		return
	}
	if req.MaxAccounts > 1000000 {
		s.sendError(w, http.StatusBadRequest, "max_accounts exceeds maximum allowed")
		return
	}

	// Reject duplicates: re-creating an existing domain must not silently
	// overwrite its configuration (e.g. resetting MaxAccounts or DKIM keys).
	if _, err := s.db.GetDomain(req.Name); err == nil {
		s.sendError(w, http.StatusConflict, "domain already exists")
		return
	}

	domain := &db.DomainData{
		Name:              req.Name,
		MaxAccounts:       req.MaxAccounts,
		MaxMailboxSize:    req.MaxMailboxSize,
		QuotaWarn:         req.QuotaWarn,
		QuotaProhibitSend: req.QuotaProhibitSend,
		IsActive:          true,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}

	// Generate DKIM key pair for the domain
	privKey, _, err := auth.GenerateDKIMKeyPair(2048)
	if err == nil {
		domain.DKIMSelector = "default"
		domain.DKIMPublicKey = auth.GetPublicKeyForDNS(privKey)
		privKeyBytes := x509.MarshalPKCS1PrivateKey(privKey)
		domain.DKIMPrivateKey = string(pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: privKeyBytes,
		}))
	}

	if err := s.db.CreateDomain(domain); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to create domain")
		return
	}

	// Flush the TLS domain cache so the new domain can immediately obtain
	// a certificate without waiting for the cache TTL to expire.
	if s.flushTLSCache != nil {
		s.flushTLSCache()
	}

	s.sendJSON(w, http.StatusCreated, domainToJSON(domain))
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request, name string) {
	domain, err := s.db.GetDomain(name)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "domain not found")
		return
	}
	if !s.callerTenantScope(r).allowsTenant(domain.TenantID) {
		s.sendError(w, http.StatusForbidden, "domain outside your tenant")
		return
	}

	s.sendJSON(w, http.StatusOK, domainToJSON(domain))
}

func (s *Server) updateDomain(w http.ResponseWriter, r *http.Request, name string) {
	domain, err := s.db.GetDomain(name)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "domain not found")
		return
	}
	if !s.callerTenantScope(r).allowsTenant(domain.TenantID) {
		s.sendError(w, http.StatusForbidden, "domain outside your tenant")
		return
	}

	var req struct {
		MaxAccounts          int    `json:"max_accounts"`
		MaxMailboxSize       int64  `json:"max_mailbox_size"`
		QuotaWarn            int64  `json:"quota_warn"`
		QuotaProhibitSend    int64  `json:"quota_prohibit_send"`
		IsActive             bool   `json:"is_active"`
		CompanyName          string `json:"company_name"`
		FromTemplateInternal string `json:"from_template_internal"`
		FromTemplateExternal string `json:"from_template_external"`
		EgressIPGroup        string `json:"egress_ip_group"`
	}

	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.MaxAccounts < 0 {
		s.sendError(w, http.StatusBadRequest, "max_accounts must be non-negative")
		return
	}
	if req.MaxAccounts > 1000000 {
		s.sendError(w, http.StatusBadRequest, "max_accounts exceeds maximum allowed")
		return
	}
	if req.MaxMailboxSize < 0 || req.QuotaWarn < 0 || req.QuotaProhibitSend < 0 {
		s.sendError(w, http.StatusBadRequest, "quota thresholds must be non-negative")
		return
	}

	// Prevent deactivation if active accounts exist
	if domain.IsActive && !req.IsActive {
		accounts, err := s.db.ListAccountsByDomain(name)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to check domain accounts")
			return
		}
		if len(accounts) > 0 {
			s.sendError(w, http.StatusConflict, "cannot deactivate domain with active accounts")
			return
		}
	}

	domain.MaxAccounts = req.MaxAccounts
	domain.MaxMailboxSize = req.MaxMailboxSize
	domain.QuotaWarn = req.QuotaWarn
	domain.QuotaProhibitSend = req.QuotaProhibitSend
	domain.IsActive = req.IsActive
	domain.CompanyName = req.CompanyName
	domain.FromTemplateInternal = req.FromTemplateInternal
	domain.FromTemplateExternal = req.FromTemplateExternal
	setDomainEgressIPGroup(domain, req.EgressIPGroup)
	domain.UpdatedAt = time.Now()

	if err := s.db.UpdateDomain(domain); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to update domain")
		return
	}

	s.sendJSON(w, http.StatusOK, domainToJSON(domain))
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request, name string) {
	// Deleting a domain is a super-admin (provisioning) operation; a tenant-admin
	// cannot remove domains from its tenant.
	if ts := s.callerTenantScope(r); ts.isTenantAdmin && !ts.isSuperAdmin {
		s.sendError(w, http.StatusForbidden, "only a super-admin can delete domains")
		return
	}

	if err := s.db.DeleteDomain(name); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to delete domain")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
