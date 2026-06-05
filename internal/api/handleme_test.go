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

// meCookieRequest builds a GET /api/v1/auth/me carrying a valid jwt cookie for
// the given subject, signed with the server's secret — mirroring how the SPA
// sends its HttpOnly session cookie. handleMe validates the token itself (it is
// in authMiddleware's skip list), so the cookie, not a context value, is what
// drives the result.
func meCookieRequest(t *testing.T, secret, sub string, admin bool) *http.Request {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   sub,
		"admin": admin,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "jwt", Value: tokenStr})
	return req
}

// TestHandleMe_ReturnsAuthenticatedUser verifies that /auth/me echoes the
// identity carried by a valid session cookie, which is what the SPA relies on
// to rehydrate its session after a reload.
func TestHandleMe_ReturnsAuthenticatedUser(t *testing.T) {
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
	server.handleMe(rec, meCookieRequest(t, "test-secret", "alice@local.test", true))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Authenticated bool   `json:"authenticated"`
		Email         string `json:"email"`
		IsAdmin       bool   `json:"isAdmin"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.Authenticated {
		t.Errorf("expected authenticated true")
	}
	if body.Email != "alice@local.test" {
		t.Errorf("expected email alice@local.test, got %q", body.Email)
	}
	if !body.IsAdmin {
		t.Errorf("expected isAdmin true")
	}
}

// TestHandleMe_NoSession returns a soft 200 with authenticated:false when no
// valid session cookie is present, so an unauthenticated reload probe neither
// restores a session nor logs a 401 on the login screen.
func TestHandleMe_NoSession(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()

	server.handleMe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with no session, got %d", rec.Code)
	}
	var body struct {
		Authenticated bool   `json:"authenticated"`
		Email         string `json:"email"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Authenticated {
		t.Errorf("expected authenticated false with no session")
	}
	if body.Email != "" {
		t.Errorf("expected no email with no session, got %q", body.Email)
	}
}

// TestHandleMe_InvalidMethod rejects non-GET requests.
func TestHandleMe_InvalidMethod(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()

	server.handleMe(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST, got %d", rec.Code)
	}
}
