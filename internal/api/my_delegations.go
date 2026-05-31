package api

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
)

// myDelegationCreateRequest is the self-service POST body. The owner is always
// the authenticated user, so only the grantee and rights are supplied.
type myDelegationCreateRequest struct {
	Grantee         string   `json:"grantee"`
	Rights          []string `json:"rights"`
	CanSendAs       bool     `json:"canSendAs"`
	CanSendOnBehalf bool     `json:"canSendOnBehalf"`
}

// handleMyDelegations lets a user manage delegates on their OWN mailbox without
// admin rights: GET lists them, POST grants one. The owner is always the
// authenticated user (unlike the admin endpoint, which takes an owner field).
func (s *Server) handleMyDelegations(w http.ResponseWriter, r *http.Request) {
	if s.semStore == nil {
		s.sendError(w, http.StatusServiceUnavailable, "delegation store not available")
		return
	}
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ownerID, err := s.semStore.Identity().EnsureMailboxId(user)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to resolve mailbox")
		return
	}

	switch r.Method {
	case http.MethodGet:
		grants, err := s.semStore.Delegation().ListDelegates(ownerID)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to list delegations")
			return
		}
		out := make([]delegationDTO, 0, len(grants))
		for _, g := range grants {
			out = append(out, delegationDTO{
				ID:              g.ID.String(),
				Owner:           user,
				Grantee:         g.DelegateEmail,
				Mailbox:         user,
				Rights:          rightsString(g.Permissions),
				CanSendAs:       g.CanSendAs,
				CanSendOnBehalf: g.CanSendOnBehalf,
				CreatedAt:       g.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			})
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"delegations": out, "count": len(out)})
	case http.MethodPost:
		var req myDelegationCreateRequest
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		grantee := strings.TrimSpace(strings.ToLower(req.Grantee))
		if grantee == "" {
			s.sendError(w, http.StatusBadRequest, "grantee is required")
			return
		}
		if grantee == strings.ToLower(user) {
			s.sendError(w, http.StatusBadRequest, "you cannot delegate to yourself")
			return
		}
		delegate := &semcore.DelegateUser{
			OwnerID:         ownerID,
			DelegateEmail:   grantee,
			DelegateUserID:  grantee,
			Permissions:     permissionsFromRights(req.Rights),
			DeliverRequests: semcore.DeliverDelegatesAndMe,
			GrantedBy:       user,
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
			Owner:           user,
			Grantee:         grantee,
			Mailbox:         user,
			Rights:          rightsString(delegate.Permissions),
			CanSendAs:       delegate.CanSendAs,
			CanSendOnBehalf: delegate.CanSendOnBehalf,
			CreatedAt:       delegate.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleMyDelegationDetail removes a delegate from the authenticated user's own
// mailbox. The grant's owner must match the caller so a user can only revoke
// delegations on their own mailbox.
func (s *Server) handleMyDelegationDetail(w http.ResponseWriter, r *http.Request) {
	if s.semStore == nil {
		s.sendError(w, http.StatusServiceUnavailable, "delegation store not available")
		return
	}
	if r.Method != http.MethodDelete {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/delegations/")
	if id == "" {
		s.sendError(w, http.StatusBadRequest, "delegation id required")
		return
	}
	delID, err := semcore.NewDelegateId(id)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid delegation id")
		return
	}
	ownerID, err := s.semStore.Identity().EnsureMailboxId(user)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to resolve mailbox")
		return
	}
	grant, err := s.semStore.Delegation().GetDelegate(delID)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "delegation not found")
		return
	}
	if grant.OwnerID.String() != ownerID.String() {
		// Don't reveal other owners' grants; treat as not found.
		s.sendError(w, http.StatusNotFound, "delegation not found")
		return
	}
	if err := s.semStore.Delegation().RemoveDelegate(delID); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to remove delegation")
		return
	}
	s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
