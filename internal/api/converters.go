package api

import (
	"github.com/umailserver/umailserver/internal/db"
)

func domainToJSON(d *db.DomainData) map[string]interface{} {
	result := map[string]interface{}{
		"name":                   d.Name,
		"max_accounts":           d.MaxAccounts,
		"is_active":              d.IsActive,
		"created_at":             d.CreatedAt,
		"updated_at":             d.UpdatedAt,
		"company_name":           d.CompanyName,
		"from_template_internal": d.FromTemplateInternal,
		"from_template_external": d.FromTemplateExternal,
		"egress_ip_group":        d.Settings[domainEgressIPGroupKey],
	}
	if d.DKIMSelector != "" {
		result["dkim_selector"] = d.DKIMSelector
		result["dkim_public_key"] = d.DKIMPublicKey
	}
	return result
}

func accountToJSON(a *db.AccountData) map[string]interface{} {
	result := map[string]interface{}{
		"email":                a.Email,
		"is_admin":             a.IsAdmin,
		"is_tenant_admin":      a.IsTenantAdmin,
		"is_active":            a.IsActive,
		"must_change_password": a.MustChangePassword,
		"quota_used":           a.QuotaUsed,
		"quota_limit":          a.QuotaLimit,
		"forward_to":           a.ForwardTo,
		"forward_keep_copy":    a.ForwardKeepCopy,
		"created_at":           a.CreatedAt,
		"updated_at":           a.UpdatedAt,
		"last_login":           a.LastLoginAt,
		"has_avatar":           len(a.Avatar) > 0,
		"display_name":         a.DisplayName,
		"title":                a.Title,
		"department":           a.Department,
		"phone":                a.Phone,
		"send_policy":          mailScopePolicyForJSON(a.SendPolicy),
		"receive_policy":       mailScopePolicyForJSON(a.ReceivePolicy),
	}
	if a.VacationSettings != "" {
		result["vacation_settings"] = a.VacationSettings
	}
	return result
}

// validMailScopePolicy reports whether a per-account send/receive scope value is
// one the API accepts: "" and "anyone" both mean unrestricted, "internal"
// restricts to locally hosted domains.
func validMailScopePolicy(v string) bool {
	switch v {
	case "", "anyone", "internal":
		return true
	default:
		return false
	}
}

// normalizeMailScopePolicy collapses the stored scope to the canonical internal
// representation: "internal" stays, everything else (default/"anyone") is the
// empty unrestricted value.
func normalizeMailScopePolicy(v string) string {
	if v == "internal" {
		return "internal"
	}
	return ""
}

// mailScopePolicyForJSON renders the stored scope for API responses, surfacing
// the default empty value as the explicit "anyone" the admin UI selects.
func mailScopePolicyForJSON(v string) string {
	if v == "internal" {
		return "internal"
	}
	return "anyone"
}

func aliasToJSON(a *db.AliasData) map[string]interface{} {
	return map[string]interface{}{
		"alias":      a.Alias + "@" + a.Domain,
		"target":     a.Target,
		"domain":     a.Domain,
		"is_active":  a.IsActive,
		"created_at": a.CreatedAt,
	}
}
