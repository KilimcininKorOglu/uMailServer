package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/audit"
	"github.com/umailserver/umailserver/internal/db"
)

// tenantAuditActor returns the acting admin's address for a tenant governance
// audit event, falling back to "system" when the request carries no user.
func tenantAuditActor(r *http.Request) string {
	if u, ok := r.Context().Value("user").(string); ok && u != "" {
		return u
	}
	return "system"
}

func tenantToJSON(t *db.TenantData) map[string]interface{} {
	return map[string]interface{}{
		"id":         t.ID,
		"name":       t.Name,
		"is_active":  t.IsActive,
		"settings":   t.Settings,
		"created_at": t.CreatedAt,
		"updated_at": t.UpdatedAt,
	}
}

func (s *Server) handleTenants(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listTenants(w, r)
	case http.MethodPost:
		s.createTenant(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleTenantDetail(w http.ResponseWriter, r *http.Request) {
	id, sub, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/v1/tenants/"), "/")
	if sub == "export" {
		if r.Method != http.MethodGet {
			s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.exportTenant(w, r, id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getTenant(w, r, id)
	case http.MethodPut:
		s.updateTenant(w, r, id)
	case http.MethodDelete:
		s.deleteTenant(w, r, id)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) listTenants(w http.ResponseWriter, r *http.Request) {
	ts := s.callerTenantScope(r)
	tenants, err := s.db.ListTenants()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	var result []map[string]interface{}
	for _, t := range tenants {
		// A tenant-admin only sees its own tenant.
		if !ts.allowsTenant(t.ID) {
			continue
		}
		result = append(result, tenantToJSON(t))
	}
	s.sendJSON(w, http.StatusOK, result)
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	// Provisioning a tenant is a super-admin operation.
	if ts := s.callerTenantScope(r); ts.isTenantAdmin && !ts.isSuperAdmin {
		s.sendError(w, http.StatusForbidden, "only a super-admin can create tenants")
		return
	}
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		s.sendError(w, http.StatusBadRequest, "tenant id is required")
		return
	}
	if _, err := s.db.GetTenant(req.ID); err == nil {
		s.sendError(w, http.StatusConflict, "tenant already exists")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.ID
	}
	tenant := &db.TenantData{ID: req.ID, Name: name, IsActive: true}
	if err := s.db.CreateTenant(tenant); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to create tenant")
		return
	}
	s.auditLogger.LogTenant(audit.TenantCreate, tenantAuditActor(r), tenant.ID, audit.ExtractIP(r))
	s.sendJSON(w, http.StatusCreated, tenantToJSON(tenant))
}

func (s *Server) getTenant(w http.ResponseWriter, r *http.Request, id string) {
	if !s.callerTenantScope(r).allowsTenant(id) {
		s.sendError(w, http.StatusForbidden, "tenant outside your scope")
		return
	}
	tenant, err := s.db.GetTenant(id)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "tenant not found")
		return
	}
	s.sendJSON(w, http.StatusOK, tenantToJSON(tenant))
}

func (s *Server) updateTenant(w http.ResponseWriter, r *http.Request, id string) {
	// Renaming and suspend/activate are super-admin operations.
	if ts := s.callerTenantScope(r); ts.isTenantAdmin && !ts.isSuperAdmin {
		s.sendError(w, http.StatusForbidden, "only a super-admin can modify tenants")
		return
	}
	tenant, err := s.db.GetTenant(id)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "tenant not found")
		return
	}
	var req struct {
		Name     *string `json:"name"`
		IsActive *bool   `json:"is_active"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	wasActive := tenant.IsActive
	if req.Name != nil {
		tenant.Name = strings.TrimSpace(*req.Name)
	}
	if req.IsActive != nil {
		tenant.IsActive = *req.IsActive
	}
	tenant.UpdatedAt = time.Now()
	if err := s.db.UpdateTenant(tenant); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to update tenant")
		return
	}
	// Suspend/activate is recorded distinctly from a plain rename so the
	// governance trail shows lifecycle transitions explicitly.
	actor, ip := tenantAuditActor(r), audit.ExtractIP(r)
	switch {
	case wasActive && !tenant.IsActive:
		s.auditLogger.LogTenant(audit.TenantSuspend, actor, tenant.ID, ip)
	case !wasActive && tenant.IsActive:
		s.auditLogger.LogTenant(audit.TenantActivate, actor, tenant.ID, ip)
	default:
		s.auditLogger.LogTenant(audit.TenantUpdate, actor, tenant.ID, ip)
	}
	s.sendJSON(w, http.StatusOK, tenantToJSON(tenant))
}

func (s *Server) deleteTenant(w http.ResponseWriter, r *http.Request, id string) {
	if ts := s.callerTenantScope(r); ts.isTenantAdmin && !ts.isSuperAdmin {
		s.sendError(w, http.StatusForbidden, "only a super-admin can delete tenants")
		return
	}
	// Refuse to delete a tenant that still owns domains (avoids orphaning them).
	domains, err := s.db.ListDomainsByTenant(id)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to check tenant domains")
		return
	}
	if len(domains) > 0 {
		s.sendError(w, http.StatusConflict, "cannot delete a tenant that still owns domains")
		return
	}
	if err := s.db.DeleteTenant(id); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to delete tenant")
		return
	}
	s.auditLogger.LogTenant(audit.TenantDelete, tenantAuditActor(r), id, audit.ExtractIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// exportTenant returns a portable snapshot of a tenant's configuration: the
// tenant record, its domains, accounts, aliases, and mail groups. Secrets
// (password hashes, DKIM private keys) are excluded — it reuses the same
// secret-free converters the admin API serves. Available to a super-admin or
// the tenant's own admin.
func (s *Server) exportTenant(w http.ResponseWriter, r *http.Request, id string) {
	if !s.callerTenantScope(r).allowsTenant(id) {
		s.sendError(w, http.StatusForbidden, "tenant outside your scope")
		return
	}
	tenant, err := s.db.GetTenant(id)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "tenant not found")
		return
	}
	domains, err := s.db.ListDomainsByTenant(id)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list tenant domains")
		return
	}

	owned := make(map[string]bool, len(domains))
	domainsJSON := make([]map[string]any, 0, len(domains))
	accountsJSON := make([]map[string]any, 0)
	for _, d := range domains {
		owned[d.Name] = true
		domainsJSON = append(domainsJSON, domainToJSON(d))
		accounts, aerr := s.db.ListAccountsByDomain(d.Name)
		if aerr != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to list tenant accounts")
			return
		}
		for _, a := range accounts {
			accountsJSON = append(accountsJSON, accountToJSON(a))
		}
	}

	aliases, err := s.db.ListAliases()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list aliases")
		return
	}
	aliasesJSON := make([]map[string]any, 0)
	for _, a := range aliases {
		if owned[a.Domain] {
			aliasesJSON = append(aliasesJSON, aliasToJSON(a))
		}
	}

	groups, err := s.db.ListMailGroups()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list mail groups")
		return
	}
	groupsJSON := make([]map[string]any, 0)
	for _, g := range groups {
		if owned[g.Domain] {
			groupsJSON = append(groupsJSON, mailGroupToJSON(g))
		}
	}

	s.auditLogger.LogTenant(audit.TenantExport, tenantAuditActor(r), id, audit.ExtractIP(r))
	s.sendJSON(w, http.StatusOK, map[string]any{
		"tenant":      tenantToJSON(tenant),
		"domains":     domainsJSON,
		"accounts":    accountsJSON,
		"aliases":     aliasesJSON,
		"mail_groups": groupsJSON,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
	})
}
