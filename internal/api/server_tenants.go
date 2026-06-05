package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

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
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tenants/")
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
	w.WriteHeader(http.StatusNoContent)
}
