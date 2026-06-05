package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/umailserver/umailserver/internal/db"
)

// scopedRequest builds a request whose context carries the tenant authority the
// handlers read via callerTenantScope (mirrors what authMiddleware sets).
func scopedRequest(tenantID string, superAdmin, tenantAdmin bool) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/x", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, "isAdmin", superAdmin) //nolint:staticcheck // shared string context key set by authMiddleware
	ctx = context.WithValue(ctx, contextKeyTenantID, tenantID)
	ctx = context.WithValue(ctx, contextKeyTenantAdmin, tenantAdmin)
	return req.WithContext(ctx)
}

// TestGetAccount_TenantScope verifies that a tenant-admin can only read accounts
// in its own tenant, while a super-admin reads any — the core isolation
// guarantee once the admin gate is opened to tenant-admins.
func TestGetAccount_TenantScope(t *testing.T) {
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

	// Two single-domain tenants (CreateDomain assigns each its own tenant).
	for _, d := range []string{"a.test", "b.test"} {
		if err := database.CreateDomain(&db.DomainData{Name: d, MaxAccounts: 10}); err != nil {
			t.Fatalf("CreateDomain %s: %v", d, err)
		}
	}
	if err := database.CreateAccount(&db.AccountData{Email: "u@a.test", LocalPart: "u", Domain: "a.test", PasswordHash: "h", IsActive: true}); err != nil {
		t.Fatalf("CreateAccount u: %v", err)
	}
	if err := database.CreateAccount(&db.AccountData{Email: "v@b.test", LocalPart: "v", Domain: "b.test", PasswordHash: "h", IsActive: true}); err != nil {
		t.Fatalf("CreateAccount v: %v", err)
	}

	// Tenant-admin of a.test: own account 200, other tenant 403.
	rec := httptest.NewRecorder()
	server.getAccount(rec, scopedRequest("a.test", false, true), "u@a.test")
	if rec.Code != http.StatusOK {
		t.Errorf("tenant-admin own account: want 200, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	server.getAccount(rec, scopedRequest("a.test", false, true), "v@b.test")
	if rec.Code != http.StatusForbidden {
		t.Errorf("tenant-admin cross-tenant account: want 403, got %d", rec.Code)
	}

	// Super-admin sees any tenant's account.
	rec = httptest.NewRecorder()
	server.getAccount(rec, scopedRequest("", true, false), "v@b.test")
	if rec.Code != http.StatusOK {
		t.Errorf("super-admin any account: want 200, got %d", rec.Code)
	}
}

// tenantTokenRequest builds a GET /auth/me carrying a jwt cookie with tenant
// scope claims, signed with the server secret.
func tenantTokenRequest(t *testing.T, secret, sub, tenant string, tenantAdmin, admin bool) *http.Request {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          sub,
		"admin":        admin,
		"tenant":       tenant,
		"tenant_admin": tenantAdmin,
		"exp":          time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "jwt", Value: signed})
	return req
}

// TestHandleStats_TenantScope verifies a tenant-admin's stats count only its
// own tenant's domains/accounts, while a super-admin sees the whole instance.
func TestHandleStats_TenantScope(t *testing.T) {
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
	mk := func(email, lp, dom string) {
		if err := database.CreateAccount(&db.AccountData{Email: email, LocalPart: lp, Domain: dom, PasswordHash: "h", IsActive: true}); err != nil {
			t.Fatalf("CreateAccount %s: %v", email, err)
		}
	}
	mk("u@a.test", "u", "a.test")
	mk("v@b.test", "v", "b.test")
	mk("w@b.test", "w", "b.test")

	decode := func(rec *httptest.ResponseRecorder) map[string]float64 {
		var m map[string]float64
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode stats: %v", err)
		}
		return m
	}

	// Tenant-admin of a.test: 1 domain, 1 account.
	rec := httptest.NewRecorder()
	server.handleStats(rec, scopedRequest("a.test", false, true))
	m := decode(rec)
	if m["domains"] != 1 || m["accounts"] != 1 {
		t.Errorf("tenant-admin stats: want domains=1 accounts=1, got %v", m)
	}

	// Super-admin: both domains, all three accounts.
	rec = httptest.NewRecorder()
	server.handleStats(rec, scopedRequest("", true, false))
	m = decode(rec)
	if m["domains"] != 2 || m["accounts"] != 3 {
		t.Errorf("super-admin stats: want domains=2 accounts=3, got %v", m)
	}
}

// TestListQueue_TenantScope verifies a tenant-admin only sees queue entries
// sent from its own tenant's domains.
func TestListQueue_TenantScope(t *testing.T) {
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
	if err := database.Enqueue(&db.QueueEntry{ID: "q-a", From: "u@a.test", To: []string{"x@ext.test"}, Status: "pending"}); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if err := database.Enqueue(&db.QueueEntry{ID: "q-b", From: "v@b.test", To: []string{"y@ext.test"}, Status: "pending"}); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	// Tenant-admin of a.test: only the a.test entry.
	rec := httptest.NewRecorder()
	server.listQueue(rec, scopedRequest("a.test", false, true))
	var list []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 || list[0]["from"] != "u@a.test" {
		t.Errorf("tenant-admin queue: want only u@a.test, got %v", list)
	}

	// Super-admin: both entries.
	rec = httptest.NewRecorder()
	server.listQueue(rec, scopedRequest("", true, false))
	list = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("super-admin queue: want 2 entries, got %d", len(list))
	}
}

// TestExportTenant verifies the tenant export is scoped to the caller's tenant,
// gathers the tenant's domains/accounts/aliases, and never leaks account
// secrets (the password hash must not appear in the exported snapshot).
func TestExportTenant(t *testing.T) {
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

	// Two single-domain tenants; CreateDomain assigns each its own tenant whose
	// id equals the domain name.
	for _, d := range []string{"a.test", "b.test"} {
		if err := database.CreateDomain(&db.DomainData{Name: d, MaxAccounts: 10}); err != nil {
			t.Fatalf("CreateDomain %s: %v", d, err)
		}
	}
	if err := database.CreateAccount(&db.AccountData{Email: "u@a.test", LocalPart: "u", Domain: "a.test", PasswordHash: "secret-hash", IsActive: true}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := database.CreateAlias(&db.AliasData{Alias: "info", Target: "u@a.test", Domain: "a.test", IsActive: true}); err != nil {
		t.Fatalf("CreateAlias: %v", err)
	}

	// Tenant-admin of a.test exports its own tenant.
	rec := httptest.NewRecorder()
	server.exportTenant(rec, scopedRequest("a.test", false, true), "a.test")
	if rec.Code != http.StatusOK {
		t.Fatalf("own-tenant export: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var export struct {
		Domains  []map[string]any `json:"domains"`
		Accounts []map[string]any `json:"accounts"`
		Aliases  []map[string]any `json:"aliases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &export); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(export.Domains) != 1 || export.Domains[0]["name"] != "a.test" {
		t.Errorf("export domains: want only a.test, got %v", export.Domains)
	}
	if len(export.Accounts) != 1 || export.Accounts[0]["email"] != "u@a.test" {
		t.Errorf("export accounts: want only u@a.test, got %v", export.Accounts)
	}
	if len(export.Aliases) != 1 || export.Aliases[0]["alias"] != "info@a.test" {
		t.Errorf("export aliases: want info@a.test, got %v", export.Aliases)
	}
	// The snapshot must never carry the password hash — exporting config is not
	// a backdoor to credentials.
	if _, leaked := export.Accounts[0]["password_hash"]; leaked {
		t.Error("export leaked the account password hash")
	}
	if body := rec.Body.String(); strings.Contains(body, "secret-hash") {
		t.Errorf("export body contains the raw password hash: %s", body)
	}

	// Tenant-admin of a.test cannot export another tenant.
	rec = httptest.NewRecorder()
	server.exportTenant(rec, scopedRequest("a.test", false, true), "b.test")
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant export: want 403, got %d", rec.Code)
	}

	// Super-admin can export any tenant.
	rec = httptest.NewRecorder()
	server.exportTenant(rec, scopedRequest("", true, false), "b.test")
	if rec.Code != http.StatusOK {
		t.Errorf("super-admin export: want 200, got %d", rec.Code)
	}
}

// TestEnforceAuthenticatedAccount_TenantSuspended verifies that suspending a
// tenant blocks authentication for its accounts on the HTTP auth path.
func TestEnforceAuthenticatedAccount_TenantSuspended(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})

	if err := database.CreateDomain(&db.DomainData{Name: "sus.test", MaxAccounts: 10}); err != nil {
		t.Fatalf("CreateDomain: %v", err)
	}
	if err := database.CreateAccount(&db.AccountData{Email: "u@sus.test", LocalPart: "u", Domain: "sus.test", PasswordHash: "h", IsActive: true}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Active tenant: authentication passes.
	if _, err := enforceAuthenticatedAccount(database, "u@sus.test", false); err != nil {
		t.Fatalf("active tenant should authenticate: %v", err)
	}

	// Suspend the tenant.
	tenant, err := database.GetTenant("sus.test")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	tenant.IsActive = false
	if err := database.UpdateTenant(tenant); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}

	// Suspended tenant: authentication is rejected.
	if _, err := enforceAuthenticatedAccount(database, "u@sus.test", false); err == nil {
		t.Error("suspended tenant must block authentication")
	}
}

// TestHandleMe_SurfacesTenantScope verifies that the tenant + tenant_admin
// claims flow through authenticateRequest into the /auth/me response, which is
// how the SPA learns the caller's tenant scope for self-service admin.
func TestHandleMe_SurfacesTenantScope(t *testing.T) {
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

	rec := httptest.NewRecorder()
	server.handleMe(rec, tenantTokenRequest(t, "test-secret", "u@acme.test", "acme", true, false))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Authenticated bool   `json:"authenticated"`
		Tenant        string `json:"tenant"`
		TenantAdmin   bool   `json:"tenant_admin"`
		IsAdmin       bool   `json:"isAdmin"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.Authenticated {
		t.Fatal("expected authenticated true")
	}
	if body.Tenant != "acme" {
		t.Errorf("tenant claim not surfaced: got %q want acme", body.Tenant)
	}
	if !body.TenantAdmin {
		t.Error("tenant_admin claim not surfaced")
	}
	if body.IsAdmin {
		t.Error("tenant-admin must not be reported as global super-admin")
	}
}
