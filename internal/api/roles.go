package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/db"
)

// Permission strings — the set of built-in admin permissions.
const (
	PermissionSystemAdmin   = "SystemAdmin"
	PermissionSystemAdminRO = "SystemAdminRO"
	PermissionDomainAdmin   = "DomainAdmin"
	PermissionDomainAdminRO = "DomainAdminRO"
	PermissionOrgAdmin     = "OrgAdmin"
	PermissionDomainPurge  = "DomainPurge"
	PermissionResetPasswd   = "ResetPasswd"
)

// BuiltInPermissions is the list of all defined permission strings.
var BuiltInPermissions = []string{
	PermissionSystemAdmin,
	PermissionSystemAdminRO,
	PermissionDomainAdmin,
	PermissionDomainAdminRO,
	PermissionOrgAdmin,
	PermissionDomainPurge,
	PermissionResetPasswd,
}

// handleAdminRoles serves GET (list) and POST (create) for /api/v1/admin/roles.
func (s *Server) handleAdminRoles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		roles, err := s.db.ListRoles()
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "list_roles_failed: "+err.Error())
			return
		}
		s.sendJSON(w, http.StatusOK, roles)

	case http.MethodPost:
		var role db.Role
		if err := json.NewDecoder(r.Body).Decode(&role); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid_json: "+err.Error())
			return
		}
		if role.ID == "" {
			role.ID = uuid.NewString()
		}
		if role.Name == "" {
			s.sendError(w, http.StatusBadRequest, "name_required: role name is required")
			return
		}
		if err := s.db.CreateRole(&role); err != nil {
			s.sendError(w, http.StatusInternalServerError, "create_role_failed: "+err.Error())
			return
		}
		s.sendJSON(w, http.StatusCreated, role)

	default:
		w.Header().Set("Allow", "GET, POST")
		s.sendError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

// handleAdminRolePermissions serves GET for /api/v1/admin/roles/permissions.
func (s *Server) handleAdminRolePermissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		s.sendError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	s.sendJSON(w, http.StatusOK, BuiltInPermissions)
}

// handleAdminRoleByID serves GET, PATCH, DELETE for /api/v1/admin/roles/{id}.
func (s *Server) handleAdminRoleByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/roles/")
	id = strings.TrimSuffix(id, "/users")
	if id == "" {
		s.sendError(w, http.StatusBadRequest, "id_required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		role, err := s.db.GetRole(id)
		if err != nil {
			s.sendError(w, http.StatusNotFound, "role_not_found")
			return
		}
		perms, err := s.db.GetRolePermissions(id)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "get_permissions_failed: "+err.Error())
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]any{"role": role, "permissions": perms})

	case http.MethodPatch:
		var role db.Role
		if err := json.NewDecoder(r.Body).Decode(&role); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid_json: "+err.Error())
			return
		}
		role.ID = id
		if err := s.db.UpdateRole(&role); err != nil {
			s.sendError(w, http.StatusNotFound, "role_not_found")
			return
		}
		s.sendJSON(w, http.StatusOK, role)

	case http.MethodDelete:
		if err := s.db.DeleteRole(id); err != nil {
			s.sendError(w, http.StatusNotFound, "role_not_found")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
		s.sendError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

// handleAdminAccountRoles serves GET, POST, DELETE for /api/v1/admin/accounts/{email}/roles.
func (s *Server) handleAdminAccountRoles(w http.ResponseWriter, r *http.Request) {
	// Path: /api/v1/admin/accounts/{email}/roles[/roleId]
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/")
	segments := strings.Split(path, "/roles")
	email := segments[0]
	if email == "" {
		s.sendError(w, http.StatusBadRequest, "email_required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		roles, err := s.db.GetUserRoles(email)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "get_user_roles_failed: "+err.Error())
			return
		}
		s.sendJSON(w, http.StatusOK, roles)

	case http.MethodPost:
		var req struct {
			RoleID string `json:"role_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid_json: "+err.Error())
			return
		}
		if req.RoleID == "" {
			s.sendError(w, http.StatusBadRequest, "role_id_required")
			return
		}
		if err := s.db.AssignRoleToUser(email, req.RoleID); err != nil {
			s.sendError(w, http.StatusNotFound, "role_not_found")
			return
		}
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		// /roles/{roleId} suffix
		roleID := ""
		if len(segments) > 1 {
			roleID = strings.TrimPrefix(segments[1], "/")
		}
		if roleID == "" {
			s.sendError(w, http.StatusBadRequest, "role_id_required")
			return
		}
		if err := s.db.RemoveRoleFromUser(email, roleID); err != nil {
			s.sendError(w, http.StatusNotFound, "assignment_not_found")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		s.sendError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}
