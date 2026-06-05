package api

import (
	"net/http"
	"strings"
)

// tenantScope is the caller's multi-tenant authority for control-plane (admin)
// requests, read from the JWT claims placed in the request context.
type tenantScope struct {
	tenantID      string // owner of the caller's domain
	isSuperAdmin  bool   // global operator: unrestricted across all tenants
	isTenantAdmin bool   // self-service admin bound to tenantID
}

// callerTenantScope reads the caller's tenant authority from the request
// context (populated by authMiddleware).
func (s *Server) callerTenantScope(r *http.Request) tenantScope {
	ts := tenantScope{}
	if v, ok := r.Context().Value(contextKeyTenantID).(string); ok {
		ts.tenantID = v
	}
	if v, ok := r.Context().Value("isAdmin").(bool); ok { //nolint:staticcheck // shared string context key set by authMiddleware
		ts.isSuperAdmin = v
	}
	if v, ok := r.Context().Value(contextKeyTenantAdmin).(bool); ok {
		ts.isTenantAdmin = v
	}
	return ts
}

// allowsTenant reports whether this scope may act on resources owned by the
// given tenant id. Only a self-service tenant-admin is constrained (to its own
// tenant). A super-admin — and any caller without tenant-admin scope, which the
// route's admin gate has already authorized — is unconstrained.
func (ts tenantScope) allowsTenant(tenantID string) bool {
	if !ts.isTenantAdmin || ts.isSuperAdmin {
		return true
	}
	return ts.tenantID != "" && ts.tenantID == tenantID
}

// allowsDomain reports whether this scope may act on the given domain, resolving
// the domain's owning tenant. Only tenant-admins are restricted to their tenant.
func (s *Server) allowsDomain(ts tenantScope, domain string) bool {
	if !ts.isTenantAdmin || ts.isSuperAdmin {
		return true
	}
	return ts.allowsTenant(s.tenantIDForDomain(domain))
}

// mayAccessAccount reports whether the caller may read/modify the account with
// the given email. Precedence: a super-admin may access any account; a
// tenant-admin only accounts in its own tenant's domains; a genuine non-admin
// end-user only its own account. A caller with no identity (already-gated
// internal/test calls) is allowed.
func (s *Server) mayAccessAccount(r *http.Request, targetEmail string) bool {
	ts := s.callerTenantScope(r)
	if ts.isSuperAdmin {
		return true
	}
	if ts.isTenantAdmin {
		_, domain := parseEmail(targetEmail)
		return s.allowsDomain(ts, domain)
	}
	if authUser, ok := r.Context().Value("user").(string); ok && authUser != "" { //nolint:staticcheck // shared string context key set by authMiddleware
		return strings.EqualFold(authUser, targetEmail)
	}
	return true
}

// tenantSuspendedForDomain reports whether the tenant owning the given domain is
// suspended (IsActive == false). Unknown domain/tenant => not suspended.
func (s *Server) tenantSuspendedForDomain(domain string) bool {
	if s.db == nil || domain == "" {
		return false
	}
	dom, err := s.db.GetDomain(domain)
	if err != nil || dom.TenantID == "" {
		return false
	}
	tenant, err := s.db.GetTenant(dom.TenantID)
	return err == nil && !tenant.IsActive
}

// senderInScope reports whether the caller may see a queue entry whose envelope
// sender is the given address: a super-admin sees all, a tenant-admin only mail
// from its own tenant's domains. System bounces (empty sender) resolve to an
// empty domain, which a tenant-admin is not allowed to see.
func (s *Server) senderInScope(r *http.Request, from string) bool {
	_, domain := parseEmail(from)
	return s.allowsDomain(s.callerTenantScope(r), domain)
}

// tenantIDForDomain resolves the tenant that owns a domain, or "" if the domain
// is unknown or carries no tenant yet. It never errors: tenant scope is
// best-effort identity metadata layered on top of the existing per-account
// auth, so a lookup miss simply yields an empty (unscoped) tenant.
func (s *Server) tenantIDForDomain(domain string) string {
	if s.db == nil || domain == "" {
		return ""
	}
	d, err := s.db.GetDomain(domain)
	if err != nil || d == nil {
		return ""
	}
	return d.TenantID
}
