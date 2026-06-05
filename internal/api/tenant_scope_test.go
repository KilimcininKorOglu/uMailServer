package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/umailserver/umailserver/internal/db"
)

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
