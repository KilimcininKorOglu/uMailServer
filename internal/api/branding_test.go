package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/db"
)

// TestBrandingRoundTrip verifies branding survives a write-then-read through the
// tenant Settings map and that unrelated settings keys are preserved.
func TestBrandingRoundTrip(t *testing.T) {
	settings := map[string]string{"unrelated": "keep-me"}
	in := brandingDTO{
		AppName:      "Acme Mail",
		LogoURL:      "https://acme.test/logo.png",
		PrimaryColor: "#ff8800",
		Features:     map[string]bool{"calendar": true, "contacts": false},
	}
	settings = applyBrandingToSettings(settings, in)
	if settings["unrelated"] != "keep-me" {
		t.Error("applyBrandingToSettings dropped an unrelated key")
	}
	out := brandingFromSettings(settings)
	if out.AppName != in.AppName || out.LogoURL != in.LogoURL || out.PrimaryColor != in.PrimaryColor {
		t.Errorf("branding fields did not round-trip: %+v", out)
	}
	if !out.Features["calendar"] || out.Features["contacts"] {
		t.Errorf("feature flags did not round-trip: %+v", out.Features)
	}

	// Clearing a field with an empty string removes the key.
	cleared := applyBrandingToSettings(settings, brandingDTO{AppName: "", Features: map[string]bool{}})
	if _, ok := cleared[brandingAppNameKey]; ok {
		t.Error("empty app name should clear the branding key")
	}
}

// TestHandleBranding_PublicResolution verifies the public endpoint resolves a
// domain to its owning tenant's branding, and that an unknown domain yields
// empty branding rather than an error.
func TestHandleBranding_PublicResolution(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})
	server := NewServer(database, nil, Config{JWTSecret: "test-secret"})

	if err := database.CreateDomain(&db.DomainData{Name: "acme.test", MaxAccounts: 10}); err != nil {
		t.Fatalf("CreateDomain: %v", err)
	}
	// CreateDomain assigns the self-tenant "acme.test"; set its branding.
	tenant, err := database.GetTenant("acme.test")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	tenant.Settings = applyBrandingToSettings(nil, brandingDTO{AppName: "Acme Mail", PrimaryColor: "#123456"})
	if err := database.UpdateTenant(tenant); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}

	get := func(query string) brandingDTO {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/branding"+query, nil)
		server.handleBranding(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("branding %q: want 200, got %d", query, rec.Code)
		}
		var b brandingDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatalf("decode branding: %v", err)
		}
		return b
	}

	known := get("?domain=acme.test")
	if known.AppName != "Acme Mail" || known.PrimaryColor != "#123456" {
		t.Errorf("known domain branding not resolved: %+v", known)
	}
	unknown := get("?domain=nobody.test")
	if unknown.AppName != "" {
		t.Errorf("unknown domain should yield empty branding, got %+v", unknown)
	}
}

// TestUpdateTenantBranding_Scope verifies a tenant-admin may set its own
// tenant's branding but not another tenant's.
func TestUpdateTenantBranding_Scope(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})
	server := NewServer(database, nil, Config{JWTSecret: "test-secret"})

	for _, d := range []string{"a.test", "b.test"} {
		if err := database.CreateDomain(&db.DomainData{Name: d, MaxAccounts: 10}); err != nil {
			t.Fatalf("CreateDomain %s: %v", d, err)
		}
	}

	body := `{"app_name":"A Corp","features":{"calendar":true}}`
	put := func(id, tenantScope string, superAdmin, tenantAdmin bool) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/"+id+"/branding", strings.NewReader(body))
		req = req.WithContext(scopedRequest(tenantScope, superAdmin, tenantAdmin).Context())
		server.updateTenantBranding(rec, req, id)
		return rec.Code
	}

	// Tenant-admin of a.test edits its own branding.
	if code := put("a.test", "a.test", false, true); code != http.StatusOK {
		t.Errorf("own-tenant branding: want 200, got %d", code)
	}
	// ...but not another tenant's.
	if code := put("b.test", "a.test", false, true); code != http.StatusForbidden {
		t.Errorf("cross-tenant branding: want 403, got %d", code)
	}
	// Super-admin may edit any tenant's branding.
	if code := put("b.test", "", true, false); code != http.StatusOK {
		t.Errorf("super-admin branding: want 200, got %d", code)
	}

	// The write actually persisted for a.test.
	tenant, err := database.GetTenant("a.test")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if brandingFromSettings(tenant.Settings).AppName != "A Corp" {
		t.Error("branding was not persisted to the tenant")
	}
}
