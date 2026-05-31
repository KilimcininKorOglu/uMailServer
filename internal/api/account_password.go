package api

import "net/http"

// handleAccountPassword lets an authenticated user change their own password
// (the "Manage Account" action in webmail). It verifies the current password
// before storing a new hash.
func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.NewPassword) < 8 {
		s.sendError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}

	localPart, domain := parseEmail(user)
	account, err := s.db.GetAccount(domain, localPart)
	if err != nil || account == nil {
		s.sendError(w, http.StatusNotFound, "account not found")
		return
	}

	if ok, _ := s.verifyPassword(req.CurrentPassword, account.PasswordHash); !ok {
		s.sendError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	newHash, err := s.hashPassword(req.NewPassword)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	account.PasswordHash = newHash
	account.MustChangePassword = false
	if err := s.db.UpdateAccount(account); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to update password")
		return
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{"message": "password updated"})
}
