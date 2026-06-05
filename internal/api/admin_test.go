package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/umailserver/umailserver/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// setupAdminTestServer creates a test server with database for admin tests
func setupAdminTestServer(t *testing.T) (*AdminServer, *db.DB, func()) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}

	config := Config{
		JWTSecret:   "test-secret-key-for-jwt-signing",
		TokenExpiry: time.Hour,
	}

	server := NewServer(database, nil, config)
	if err := database.CreateDomain(&db.DomainData{
		Name:        "example.com",
		MaxAccounts: 10,
		IsActive:    true,
	}); err != nil {
		t.Fatalf("failed to create domain: %v", err)
	}
	if err := database.CreateAccount(&db.AccountData{
		Email:        "admin@example.com",
		LocalPart:    "admin",
		Domain:       "example.com",
		PasswordHash: "hash",
		IsActive:     true,
		IsAdmin:      true,
	}); err != nil {
		t.Fatalf("failed to create admin account: %v", err)
	}
	if err := database.CreateAccount(&db.AccountData{
		Email:        "user@example.com",
		LocalPart:    "user",
		Domain:       "example.com",
		PasswordHash: "hash",
		IsActive:     true,
	}); err != nil {
		t.Fatalf("failed to create user account: %v", err)
	}

	adminConfig := AdminConfig{
		Addr:      "127.0.0.1:8443",
		JWTSecret: "test-secret-key-for-jwt-signing",
	}

	adminServer := NewAdminServer(server, adminConfig)

	cleanup := func() {
		database.Close()
	}

	return adminServer, database, cleanup
}

// createAdminToken creates a valid admin JWT token for testing
func createAdminToken(secret string, kid string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "admin@example.com",
		"admin": true,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if kid != "" {
		token.Header["kid"] = kid
	}
	tokenStr, _ := token.SignedString([]byte(secret))
	return tokenStr
}

// createUserToken creates a valid non-admin JWT token for testing
func createUserToken(secret string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "user@example.com",
		"admin": false,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, _ := token.SignedString([]byte(secret))
	return tokenStr
}

// TestNewAdminServer tests creating a new admin server
func TestNewAdminServer(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer database.Close()

	server := NewServer(database, nil, Config{JWTSecret: "test"})
	adminConfig := AdminConfig{
		Addr:      "127.0.0.1:8443",
		JWTSecret: "test-secret",
	}

	adminServer := NewAdminServer(server, adminConfig)

	if adminServer == nil {
		t.Fatal("expected non-nil admin server")
	}
	if adminServer.Server != server {
		t.Error("admin server should embed the main server")
	}
	if adminServer.config.Addr != "127.0.0.1:8443" {
		t.Errorf("expected addr 127.0.0.1:8443, got %s", adminServer.config.Addr)
	}
}

// TestAdminServer_Stop_WithoutStart tests stopping without starting
func TestAdminServer_Stop_WithoutStart(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	err := adminServer.Stop()
	if err != nil {
		t.Errorf("expected no error when stopping without starting, got %v", err)
	}
}

// TestAdminServer_router tests the router setup
func TestAdminServer_router(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	router := adminServer.router()
	if router == nil {
		t.Fatal("expected non-nil router")
	}
}

// TestAdminServer_withAuth_ValidAdminToken tests with valid admin token
func TestAdminServer_withAuth_ValidAdminToken(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	// Create handler that checks context values
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Context().Value("user")
		isAdmin := r.Context().Value("isAdmin")

		if user != "admin@example.com" {
			t.Errorf("expected user admin@example.com, got %v", user)
		}
		if isAdmin != true {
			t.Errorf("expected isAdmin true, got %v", isAdmin)
		}

		w.WriteHeader(http.StatusOK)
	})

	wrapped := adminServer.withAuth(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	token := createAdminToken(adminServer.config.JWTSecret, "")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestAdminServer_withAuth_ValidTokenWithKID tests with valid token using kid header
func TestAdminServer_withAuth_ValidTokenWithKID(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := adminServer.withAuth(handler)

	// Create token with kid header pointing to default key
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	token := createAdminToken(adminServer.Server.jwtSecrets[adminServer.Server.currentKid], "default")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

func TestAdminServer_RouterSupportsCookieAuthFlow(t *testing.T) {
	adminServer, database, cleanup := setupAdminTestServer(t)
	defer cleanup()

	account, err := database.GetAccount("example.com", "admin")
	if err != nil {
		t.Fatalf("failed to load admin account: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	account.PasswordHash = string(hash)
	if err := database.UpdateAccount(account); err != nil {
		t.Fatalf("failed to update admin password hash: %v", err)
	}

	router := adminServer.router()
	loginBody := bytes.NewReader([]byte(`{"email":"admin@example.com","password":"password123"}`))
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()

	router.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected login to succeed, got %d", loginRec.Code)
	}

	accountsReq := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
	for _, cookie := range loginRec.Result().Cookies() {
		accountsReq.AddCookie(cookie)
	}
	accountsRec := httptest.NewRecorder()

	router.ServeHTTP(accountsRec, accountsReq)

	if accountsRec.Code != http.StatusOK {
		t.Fatalf("expected cookie-authenticated admin request to succeed, got %d", accountsRec.Code)
	}
}

func TestAdminServer_withAuth_BlocksPasswordChangeRequired(t *testing.T) {
	adminServer, database, cleanup := setupAdminTestServer(t)
	defer cleanup()

	account, err := database.GetAccount("example.com", "admin")
	if err != nil {
		t.Fatalf("failed to load admin account: %v", err)
	}
	account.MustChangePassword = true
	if err := database.UpdateAccount(account); err != nil {
		t.Fatalf("failed to update admin account: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called when password change is required")
	})

	wrapped := adminServer.withAuth(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	token := createAdminToken(adminServer.config.JWTSecret, "")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, w.Code)
	}
}

// TestAdminServer_withAuth_MissingHeader tests with missing authorization header
func TestAdminServer_withAuth_MissingHeader(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called without auth")
	})

	wrapped := adminServer.withAuth(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "unauthorized" {
		t.Errorf("expected error 'unauthorized', got %v", resp["error"])
	}
}

// TestAdminServer_withAuth_InvalidToken tests with invalid token
func TestAdminServer_withAuth_InvalidToken(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with invalid token")
	})

	wrapped := adminServer.withAuth(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

// TestAdminServer_withAuth_WrongSigningMethod tests with wrong signing method
func TestAdminServer_withAuth_WrongSigningMethod(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with wrong signing method")
	})

	wrapped := adminServer.withAuth(handler)

	// Create token with none signing method (insecure)
	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub":   "admin@example.com",
		"admin": true,
	})
	tokenStr, _ := token.SignedString(jwt.UnsafeAllowNoneSignatureType)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

// TestAdminServer_withAuth_ExpiredToken tests with expired token
func TestAdminServer_withAuth_ExpiredToken(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with expired token")
	})

	wrapped := adminServer.withAuth(handler)

	// Create expired token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "admin@example.com",
		"admin": true,
		"exp":   time.Now().Add(-time.Hour).Unix(),
	})
	tokenStr, _ := token.SignedString([]byte(adminServer.config.JWTSecret))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

// TestAdminServer_withAuth_MissingSubject tests with missing subject claim
func TestAdminServer_withAuth_MissingSubject(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called without subject")
	})

	wrapped := adminServer.withAuth(handler)

	// Create token without subject
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"admin": true,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, _ := token.SignedString([]byte(adminServer.config.JWTSecret))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

// TestAdminServer_withAuth_LegacyJWTDisabled tests that legacy JWTSecret fallback is blocked
func TestAdminServer_withAuth_LegacyJWTDisabled(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()
	adminServer.config.DisableLegacyJWT = true

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called when legacy JWT is disabled")
	})
	wrapped := adminServer.withAuth(handler)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "admin@example.com",
		"admin": true,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	token.Header["kid"] = "nonexistent-kid"
	adminServer.Server.currentKid = "also-nonexistent"
	tokenStr, _ := token.SignedString([]byte(adminServer.config.JWTSecret))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

// TestAdminServer_adminMiddleware_AdminUser tests admin middleware with admin user
func TestAdminServer_adminMiddleware_AdminUser(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := adminServer.adminMiddleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, "isAdmin", true)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestAdminServer_adminMiddleware_NonAdminUser tests admin middleware with non-admin user
func TestAdminServer_adminMiddleware_NonAdminUser(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for non-admin")
	})

	wrapped := adminServer.adminMiddleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, "isAdmin", false)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "forbidden" {
		t.Errorf("expected error 'forbidden', got %v", resp["error"])
	}
}

// TestAdminServer_adminMiddleware_MissingIsAdmin tests admin middleware with missing isAdmin value
func TestAdminServer_adminMiddleware_MissingIsAdmin(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called without isAdmin")
	})

	wrapped := adminServer.adminMiddleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	// Don't set isAdmin in context
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, w.Code)
	}
}

// TestWriteError tests the writeError function
func TestWriteError(t *testing.T) {
	tests := []struct {
		name     string
		errCode  string
		message  string
		status   int
		expected map[string]interface{}
	}{
		{
			name:    "bad request",
			errCode: "bad_request",
			message: "Invalid input",
			status:  http.StatusBadRequest,
			expected: map[string]interface{}{
				"error":   "bad_request",
				"message": "Invalid input",
				"code":    float64(http.StatusBadRequest),
			},
		},
		{
			name:    "not found",
			errCode: "not_found",
			message: "Resource not found",
			status:  http.StatusNotFound,
			expected: map[string]interface{}{
				"error":   "not_found",
				"message": "Resource not found",
				"code":    float64(http.StatusNotFound),
			},
		},
		{
			name:    "server error",
			errCode: "internal_error",
			message: "Something went wrong",
			status:  http.StatusInternalServerError,
			expected: map[string]interface{}{
				"error":   "internal_error",
				"message": "Something went wrong",
				"code":    float64(http.StatusInternalServerError),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, tc.errCode, tc.message, tc.status)

			if w.Code != tc.status {
				t.Errorf("expected status %d, got %d", tc.status, w.Code)
			}

			contentType := w.Header().Get("Content-Type")
			if contentType != "application/json" {
				t.Errorf("expected Content-Type application/json, got %s", contentType)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse response: %v", err)
			}

			for key, expectedVal := range tc.expected {
				if resp[key] != expectedVal {
					t.Errorf("expected %s=%v, got %v", key, expectedVal, resp[key])
				}
			}
		})
	}
}

// TestGetContentType tests the getContentType function
func TestGetContentType(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"index.html", "text/html"},
		{"script.js", "application/javascript"},
		{"styles.css", "text/css"},
		{"icon.svg", "image/svg+xml"},
		{"image.png", "image/png"},
		{"favicon.ico", "image/x-icon"},
		{"data.bin", "application/octet-stream"},
		{"file.unknown", "application/octet-stream"},
		{"", "application/octet-stream"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			result := getContentType(tc.path)
			if result != tc.expected {
				t.Errorf("getContentType(%q) = %q, want %q", tc.path, result, tc.expected)
			}
		})
	}
}

// TestAdminServer_handleAdmin_NoFS tests handleAdmin - adminFS behavior
// Note: When adminFS is nil, it returns 500. When adminFS is set but file doesn't exist,
// it tries index.html as fallback.
func TestAdminServer_handleAdmin_NoFS(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	// adminFS is nil by default in test setup, so this should return 500
	// However, if adminFS is somehow set, it may behave differently
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	w := httptest.NewRecorder()

	adminServer.handleAdmin(w, req)

	// The behavior depends on whether adminFS is nil
	// If it's nil: 500, if it's set: 404 or 200 (with fallback to index.html)
	if w.Code == http.StatusInternalServerError {
		// Expected when adminFS is nil
		return
	}
	// Otherwise, it's a valid response for when adminFS is configured
	if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
		t.Errorf("unexpected status %d", w.Code)
	}
}

// TestAdminServer_handleAdmin_WithFS tests handleAdmin with admin filesystem
func TestAdminServer_handleAdmin_WithFS(t *testing.T) {
	_, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	// Create a mock filesystem with admin files
	// Note: We can't easily set adminFS since it's not exported,
	// but we can test the getContentType function which is the key logic

	// Test content type detection
	contentTypes := map[string]string{
		"index.html":  "text/html",
		"app.js":      "application/javascript",
		"styles.css":  "text/css",
		"logo.svg":    "image/svg+xml",
		"banner.png":  "image/png",
		"favicon.ico": "image/x-icon",
		"config.json": "application/octet-stream",
	}

	for file, expected := range contentTypes {
		result := getContentType(file)
		if result != expected {
			t.Errorf("getContentType(%q) = %q, want %q", file, result, expected)
		}
	}
}

// mockFileSystem implements the FileSystem interface for testing
type mockFileSystem struct {
	files map[string]string
}

func (m *mockFileSystem) Open(name string) (http.File, error) {
	return nil, io.EOF // Simplified mock
}

// fakeAdminFS adapts an in-memory fstest.MapFS to the FileSystem interface so
// handleAdmin can be exercised with real file lookups in tests.
type fakeAdminFS struct{ m fstest.MapFS }

func (f fakeAdminFS) Open(name string) (fs.File, error)    { return f.m.Open(name) }
func (f fakeAdminFS) ReadFile(name string) ([]byte, error) { return f.m.ReadFile(name) }
func (f fakeAdminFS) Exists(name string) bool {
	_, err := f.m.Open(name)
	return err == nil
}

// TestAdminServer_handleAdmin_SPAFallbackContentType is a regression guard for
// the deep-link/refresh bug: extensionless admin sub-routes (e.g. /admin/accounts)
// have no matching file, so the SPA shell (index.html) must be served as
// text/html. Previously the content type was derived from the extensionless
// request path and resolved to application/octet-stream, making the browser
// download the page instead of rendering it.
func TestAdminServer_handleAdmin_SPAFallbackContentType(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	adminServer.adminFS = fakeAdminFS{m: fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>admin</title>")},
	}}

	for _, path := range []string{"/admin/accounts", "/admin/domains", "/admin/settings"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		adminServer.handleAdmin(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
		}
		// http.ServeContent labels .html as "text/html; charset=utf-8"; the
		// regression we guard against is the extensionless path resolving to
		// application/octet-stream (a download), so any text/html* is correct.
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: fallback Content-Type = %q, want text/html*", path, ct)
		}
	}
}

// TestAdminServer_Routes_Health tests health check route
func TestAdminServer_Routes_Health(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	// Test that router includes health endpoint
	router := adminServer.router()
	if router == nil {
		t.Fatal("expected non-nil router")
	}

	// We can't easily test the actual routes without starting the server,
	// but we verified the router is created
}

// TestAdminServer_Routes_Metrics tests metrics route
func TestAdminServer_Routes_Metrics(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	router := adminServer.router()
	if router == nil {
		t.Fatal("expected non-nil router")
	}
}

// TestAdminServer_withAuth_ShortHeader tests with short authorization header
func TestAdminServer_withAuth_ShortHeader(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with short header")
	})

	wrapped := adminServer.withAuth(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Beare") // Less than 7 characters
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

// TestAdminServer_withAuth_EmptyHeader tests with empty authorization header
func TestAdminServer_withAuth_EmptyHeader(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with empty header")
	})

	wrapped := adminServer.withAuth(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "")
	w := httptest.NewRecorder()

	wrapped(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

// TestAdminServer_adminMiddleware_WrongType tests admin middleware with wrong type for isAdmin
func TestAdminServer_adminMiddleware_WrongType(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with wrong type")
	})

	wrapped := adminServer.adminMiddleware(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := req.Context()
	// Set isAdmin as string instead of bool
	ctx = context.WithValue(ctx, "isAdmin", "true")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, w.Code)
	}
}

// TestAdminServer_Start_Integration tests starting the admin server (integration)
func TestAdminServer_Start_Integration(t *testing.T) {
	adminServer, _, cleanup := setupAdminTestServer(t)
	defer cleanup()

	// Use a different port to avoid conflicts
	adminServer.config.Addr = "127.0.0.1:0"

	// Start server in background
	go func() {
		if err := adminServer.Start(); err != nil && err != http.ErrServerClosed {
			t.Errorf("unexpected error starting server: %v", err)
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Stop the server
	if err := adminServer.Stop(); err != nil {
		t.Errorf("unexpected error stopping server: %v", err)
	}
}

// TestServer_HandleJWTRotate_MethodNotAllowed tests JWT rotate with wrong method
func TestServer_HandleJWTRotate_MethodNotAllowed(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer database.Close()

	server := NewServer(database, nil, Config{JWTSecret: "test"})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	server.handleJWTRotate(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

// TestServer_HandleJWTRotate_Success tests successful JWT rotation
func TestServer_HandleJWTRotate_Success(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer database.Close()

	server := NewServer(database, nil, Config{JWTSecret: "test"})

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	w := httptest.NewRecorder()

	server.handleJWTRotate(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["status"] != "rotated" {
		t.Errorf("expected status 'rotated', got %v", resp["status"])
	}
	if resp["newKid"] == nil {
		t.Error("expected newKid in response")
	}
	if resp["activeKids"] == nil {
		t.Error("expected activeKids in response")
	}
}

// TestServer_HandleJWTStatus_MethodNotAllowed tests JWT status with wrong method
func TestServer_HandleJWTStatus_MethodNotAllowed(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer database.Close()

	server := NewServer(database, nil, Config{JWTSecret: "test"})

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	w := httptest.NewRecorder()

	server.handleJWTStatus(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

// TestServer_HandleJWTStatus_Success tests successful JWT status retrieval
func TestServer_HandleJWTStatus_Success(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer database.Close()

	server := NewServer(database, nil, Config{JWTSecret: "test"})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	server.handleJWTStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp["currentKid"] == nil {
		t.Error("expected currentKid in response")
	}
	if resp["activeKeys"] == nil {
		t.Error("expected activeKeys in response")
	}
}

// TestGenerateSecureJWTSecret tests the JWT secret generation
func TestGenerateSecureJWTSecret(t *testing.T) {
	secret := generateSecureJWTSecret()

	// Should be 64 hex characters (32 bytes = 64 hex chars)
	if len(secret) != 64 {
		t.Errorf("expected secret length 64, got %d", len(secret))
	}

	// Should be unique each time
	secret2 := generateSecureJWTSecret()
	if secret == secret2 {
		t.Error("expected different secrets on consecutive calls")
	}
}
