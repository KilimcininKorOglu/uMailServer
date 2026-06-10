package api

import (
	"net/http"
	"time"
)

// handleProfile lets a signed-in user read and update their own directory
// profile fields (display name, title, department, phone). Writes deliberately
// touch ONLY those fields — never admin status, quota, or password — so it is
// safe to expose outside the admin-gated /accounts routes. The read response
// additionally surfaces the user's own quota usage and graduated thresholds
// (read-only) so webmail can render the storage gauge; these are never writable
// here.
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
			"email":               account.Email,
			"display_name":        account.DisplayName,
			"title":               account.Title,
			"department":          account.Department,
			"phone":               account.Phone,
			"timezone":            account.Timezone,
			"locale":              account.Locale,
			"theme":               account.Theme,
			"onboarded":           account.Onboarded,
			"has_avatar":          len(account.Avatar) > 0,
			"quota_used":          account.QuotaUsed,
			"quota_limit":         account.QuotaLimit,
			"quota_warn":          account.QuotaWarn,
			"quota_prohibit_send": account.QuotaProhibitSend,
		})
	case http.MethodPut:
		var req struct {
			DisplayName *string `json:"display_name"`
			Title       *string `json:"title"`
			Department  *string `json:"department"`
			Phone       *string `json:"phone"`
			Timezone    *string `json:"timezone"`
			Locale      *string `json:"locale"`
			Theme       *string `json:"theme"`
			Onboarded   *bool   `json:"onboarded"`
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
		if req.Timezone != nil {
			// Reject anything that is not a loadable IANA zone (empty means
			// "follow the device"); a bad value would break server-side date
			// rendering that does time.LoadLocation(account.Timezone).
			if *req.Timezone != "" {
				if _, err := time.LoadLocation(*req.Timezone); err != nil {
					s.sendError(w, http.StatusBadRequest, "invalid timezone")
					return
				}
			}
			account.Timezone = *req.Timezone
		}
		if req.Locale != nil {
			account.Locale = *req.Locale
		}
		if req.Theme != nil {
			account.Theme = *req.Theme
		}
		if req.Onboarded != nil {
			account.Onboarded = *req.Onboarded
		}
		account.UpdatedAt = time.Now()
		if err := s.db.UpdateAccount(account); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to update profile")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]any{
			"email":               account.Email,
			"display_name":        account.DisplayName,
			"title":               account.Title,
			"department":          account.Department,
			"phone":               account.Phone,
			"timezone":            account.Timezone,
			"locale":              account.Locale,
			"theme":               account.Theme,
			"onboarded":           account.Onboarded,
			"quota_used":          account.QuotaUsed,
			"quota_limit":         account.QuotaLimit,
			"quota_warn":          account.QuotaWarn,
			"quota_prohibit_send": account.QuotaProhibitSend,
		})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
