package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/umailserver/umailserver/internal/audit"
)

// Branding and feature flags are stored as namespaced keys inside a tenant's
// free-form Settings map, so per-tenant customization needs no schema change.
const (
	brandingAppNameKey      = "branding.app_name"
	brandingLogoURLKey      = "branding.logo_url"
	brandingPrimaryColorKey = "branding.primary_color"
	featureKeyPrefix        = "feature."
)

// brandingDTO is the typed shape webmail and the admin UI exchange. Features map
// a flag name to its on/off state.
type brandingDTO struct {
	AppName      string          `json:"app_name"`
	LogoURL      string          `json:"logo_url"`
	PrimaryColor string          `json:"primary_color"`
	Features     map[string]bool `json:"features"`
}

// brandingFromSettings extracts the typed branding view from a tenant's raw
// Settings map.
func brandingFromSettings(settings map[string]string) brandingDTO {
	b := brandingDTO{Features: map[string]bool{}}
	if settings == nil {
		return b
	}
	b.AppName = settings[brandingAppNameKey]
	b.LogoURL = settings[brandingLogoURLKey]
	b.PrimaryColor = settings[brandingPrimaryColorKey]
	for k, v := range settings {
		if name, ok := strings.CutPrefix(k, featureKeyPrefix); ok && name != "" {
			b.Features[name] = v == "true"
		}
	}
	return b
}

// applyBrandingToSettings merges a branding payload into a tenant's Settings map,
// preserving any unrelated keys. An empty branding string clears that key; the
// feature set fully replaces the previous flags.
func applyBrandingToSettings(settings map[string]string, b brandingDTO) map[string]string {
	if settings == nil {
		settings = map[string]string{}
	}
	setOrClear(settings, brandingAppNameKey, b.AppName)
	setOrClear(settings, brandingLogoURLKey, b.LogoURL)
	setOrClear(settings, brandingPrimaryColorKey, b.PrimaryColor)
	for k := range settings {
		if strings.HasPrefix(k, featureKeyPrefix) {
			delete(settings, k)
		}
	}
	for name, on := range b.Features {
		if name = strings.TrimSpace(name); name != "" {
			settings[featureKeyPrefix+name] = strconv.FormatBool(on)
		}
	}
	return settings
}

func setOrClear(m map[string]string, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		m[key] = v
	} else {
		delete(m, key)
	}
}

// handleBranding is a public endpoint: it resolves a domain to its owning
// tenant and returns that tenant's branding so the webmail login screen can be
// styled before the user authenticates. The domain comes from the `domain`
// query parameter, falling back to the request Host. Unknown domains yield
// empty branding (the client applies its own defaults).
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain == "" {
		domain = hostToDomain(r.Host)
	}
	out := brandingDTO{Features: map[string]bool{}}
	if domain != "" {
		if dom, err := s.db.GetDomain(domain); err == nil && dom.TenantID != "" {
			if tenant, terr := s.db.GetTenant(dom.TenantID); terr == nil {
				out = brandingFromSettings(tenant.Settings)
			}
		}
	}
	s.sendJSON(w, http.StatusOK, out)
}

// hostToDomain strips a leading "mail." label and any port from an HTTP Host,
// yielding a best-effort mail domain to resolve branding against.
func hostToDomain(host string) string {
	if host == "" {
		return ""
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if rest, ok := strings.CutPrefix(host, "mail."); ok {
		return rest
	}
	return host
}

// getTenantBranding returns a tenant's current branding for the admin UI to
// populate its editor. Scoped to a super-admin or the tenant's own admin.
func (s *Server) getTenantBranding(w http.ResponseWriter, r *http.Request, id string) {
	if !s.callerTenantScope(r).allowsTenant(id) {
		s.sendError(w, http.StatusForbidden, "tenant outside your scope")
		return
	}
	tenant, err := s.db.GetTenant(id)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "tenant not found")
		return
	}
	s.sendJSON(w, http.StatusOK, brandingFromSettings(tenant.Settings))
}

// updateTenantBranding lets a super-admin or the tenant's own admin set the
// tenant's branding and feature flags (self-service customization). Unlike
// rename/suspend, this is not restricted to super-admins.
func (s *Server) updateTenantBranding(w http.ResponseWriter, r *http.Request, id string) {
	if !s.callerTenantScope(r).allowsTenant(id) {
		s.sendError(w, http.StatusForbidden, "tenant outside your scope")
		return
	}
	tenant, err := s.db.GetTenant(id)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "tenant not found")
		return
	}
	var req brandingDTO
	if derr := decodeJSON(r, &req); derr != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tenant.Settings = applyBrandingToSettings(tenant.Settings, req)
	if uerr := s.db.UpdateTenant(tenant); uerr != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to update tenant branding")
		return
	}
	s.auditLogger.LogTenant(audit.TenantUpdate, tenantAuditActor(r), id, audit.ExtractIP(r))
	s.sendJSON(w, http.StatusOK, brandingFromSettings(tenant.Settings))
}
