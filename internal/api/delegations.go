package api

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
)

// delegationDTO is the admin-UI shape for a delegation grant. It mirrors the
// frontend DelegationEntry interface (web/admin/src/pages/Delegation.tsx).
type delegationDTO struct {
	ID              string `json:"id"`
	Owner           string `json:"owner"`
	Grantee         string `json:"grantee"`
	Mailbox         string `json:"mailbox"`
	Rights          string `json:"rights"`
	CanSendAs       bool   `json:"canSendAs"`
	CanSendOnBehalf bool   `json:"canSendOnBehalf"`
	CreatedAt       string `json:"createdAt"`
}

// delegationCreateRequest is the POST body for creating a delegation grant.
type delegationCreateRequest struct {
	Owner           string   `json:"owner"`
	Grantee         string   `json:"grantee"`
	Rights          []string `json:"rights"` // subset of {"read","write"}
	CanSendAs       bool     `json:"canSendAs"`
	CanSendOnBehalf bool     `json:"canSendOnBehalf"`
}

// handleDelegations handles GET (list all) and POST (create) on
// /api/v1/admin/delegations.
func (s *Server) handleDelegations(w http.ResponseWriter, r *http.Request) {
	if s.semStore == nil {
		s.sendError(w, http.StatusServiceUnavailable, "delegation store not available")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listDelegations(w, r)
	case http.MethodPost:
		s.createDelegation(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleDelegationDetail handles DELETE /api/v1/admin/delegations/{id}.
func (s *Server) handleDelegationDetail(w http.ResponseWriter, r *http.Request) {
	if s.semStore == nil {
		s.sendError(w, http.StatusServiceUnavailable, "delegation store not available")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/delegations/")
	if id == "" {
		s.sendError(w, http.StatusBadRequest, "delegation id required")
		return
	}
	if r.Method != http.MethodDelete {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	delID, err := semcore.NewDelegateId(id)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid delegation id")
		return
	}
	if err := s.semStore.Delegation().RemoveDelegate(delID); err != nil {
		s.sendError(w, http.StatusNotFound, "delegation not found")
		return
	}
	s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// listDelegations returns every delegation grant, resolving each owner
// MailboxId back to its account email for display.
func (s *Server) listDelegations(w http.ResponseWriter, _ *http.Request) {
	grants, err := s.semStore.Delegation().ListAllDelegates()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list delegations")
		return
	}
	emails, err := s.semStore.Identity().MailboxEmailsByID()
	if err != nil {
		// Fall back to raw IDs rather than failing the whole listing.
		emails = map[string]string{}
	}

	out := make([]delegationDTO, 0, len(grants))
	for _, g := range grants {
		owner := emails[g.OwnerID.String()]
		if owner == "" {
			owner = g.OwnerID.String()
		}
		out = append(out, delegationDTO{
			ID:              g.ID.String(),
			Owner:           owner,
			Grantee:         g.DelegateEmail,
			Mailbox:         owner,
			Rights:          rightsString(g.Permissions),
			CanSendAs:       g.CanSendAs,
			CanSendOnBehalf: g.CanSendOnBehalf,
			CreatedAt:       g.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"delegations": out,
		"count":       len(out),
	})
}

// createDelegation creates (or updates) a delegation grant.
func (s *Server) createDelegation(w http.ResponseWriter, r *http.Request) {
	var req delegationCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Owner = strings.TrimSpace(strings.ToLower(req.Owner))
	req.Grantee = strings.TrimSpace(strings.ToLower(req.Grantee))
	if req.Owner == "" || req.Grantee == "" {
		s.sendError(w, http.StatusBadRequest, "owner and grantee are required")
		return
	}
	if req.Owner == req.Grantee {
		s.sendError(w, http.StatusBadRequest, "owner and grantee must differ")
		return
	}

	ownerID, err := s.semStore.Identity().EnsureMailboxId(req.Owner)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to resolve owner mailbox")
		return
	}

	grantedBy, ok := r.Context().Value("user").(string)
	if !ok {
		grantedBy = ""
	}

	delegate := &semcore.DelegateUser{
		OwnerID:         ownerID,
		DelegateEmail:   req.Grantee,
		DelegateUserID:  req.Grantee,
		Permissions:     permissionsFromRights(req.Rights),
		DeliverRequests: semcore.DeliverDelegatesAndMe,
		GrantedBy:       grantedBy,
		CanSendAs:       req.CanSendAs,
		CanSendOnBehalf: req.CanSendOnBehalf,
	}
	id, err := s.semStore.Delegation().PutDelegate(delegate)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to store delegation")
		return
	}
	s.sendJSON(w, http.StatusCreated, delegationDTO{
		ID:              id.String(),
		Owner:           req.Owner,
		Grantee:         req.Grantee,
		Mailbox:         req.Owner,
		Rights:          rightsString(delegate.Permissions),
		CanSendAs:       delegate.CanSendAs,
		CanSendOnBehalf: delegate.CanSendOnBehalf,
		CreatedAt:       delegate.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

// permissionsFromRights maps the admin-UI read/write toggles onto canonical
// per-folder permission levels. Read grants Reviewer on inbox+calendar; write
// grants Author (which implies read).
func permissionsFromRights(rights []string) semcore.DelegateFolderPermissions {
	var read, write bool
	for _, rt := range rights {
		switch strings.ToLower(strings.TrimSpace(rt)) {
		case "read":
			read = true
		case "write":
			write = true
		}
	}
	level := semcore.DelegateFolderPermissionNone
	switch {
	case write:
		level = semcore.DelegateFolderPermissionAuthor
	case read:
		level = semcore.DelegateFolderPermissionReviewer
	}
	if level == semcore.DelegateFolderPermissionNone {
		return semcore.DelegateFolderPermissions{}
	}
	return semcore.DelegateFolderPermissions{Inbox: level, Calendar: level}
}

// rightsString renders a permission set back into the "read, write" display
// string the admin UI expects. It inspects the explicit level (Inbox, falling
// back to Calendar) so an unset/None permission yields no rights — the model's
// CanRead* helpers treat an empty level as readable, which is wrong here.
func rightsString(p semcore.DelegateFolderPermissions) string {
	level := p.Inbox
	if level == "" {
		level = p.Calendar
	}
	switch level {
	case semcore.DelegateFolderPermissionReviewer:
		return "read"
	case semcore.DelegateFolderPermissionAuthor, semcore.DelegateFolderPermissionDelegate:
		return "read, write"
	default:
		return ""
	}
}
