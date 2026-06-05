package api

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
