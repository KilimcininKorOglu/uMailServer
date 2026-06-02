package api

import (
	"net/http"
	"time"
)

// handleProfile lets a signed-in user read and update their own directory
// profile fields (display name, title, department, phone). It deliberately
// touches ONLY those fields — never admin status, quota, or password — so it is
// safe to expose outside the admin-gated /accounts routes.
// GET  /api/v1/profile  → current profile
// PUT  /api/v1/profile  → update the provided fields
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	caller, ok := r.Context().Value("user").(string)
	if !ok || caller == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	user, domain := parseEmail(caller)
	account, err := s.db.GetAccount(domain, user)
	if err != nil || account == nil {
		s.sendError(w, http.StatusNotFound, "account not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.sendJSON(w, http.StatusOK, map[string]any{
			"email":        account.Email,
			"display_name": account.DisplayName,
			"title":        account.Title,
			"department":   account.Department,
			"phone":        account.Phone,
			"has_avatar":   len(account.Avatar) > 0,
		})
	case http.MethodPut:
		var req struct {
			DisplayName *string `json:"display_name"`
			Title       *string `json:"title"`
			Department  *string `json:"department"`
			Phone       *string `json:"phone"`
		}
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
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
			s.sendError(w, http.StatusInternalServerError, "failed to update profile")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]any{
			"email":        account.Email,
			"display_name": account.DisplayName,
			"title":        account.Title,
			"department":   account.Department,
			"phone":        account.Phone,
		})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
