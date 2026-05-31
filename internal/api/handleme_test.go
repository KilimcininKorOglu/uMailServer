package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/db"
)

// TestHandleMe_ReturnsAuthenticatedUser verifies that /auth/me echoes the
// identity the auth middleware placed in the request context, which is what the
// SPA relies on to rehydrate its session after a reload.
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	// The auth middleware and handleMe use plain string context keys; the test
	// must match those exact keys to exercise the handler.
	ctx := context.WithValue(req.Context(), "user", "alice@local.test") //nolint:staticcheck // must match the middleware's string context key
	ctx = context.WithValue(ctx, "isAdmin", true)                       //nolint:staticcheck // must match the middleware's string context key
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	server.handleMe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Email   string `json:"email"`
		IsAdmin bool   `json:"isAdmin"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Email != "alice@local.test" {
		t.Errorf("expected email alice@local.test, got %q", body.Email)
	}
	if !body.IsAdmin {
		t.Errorf("expected isAdmin true")
	}
}

// TestHandleMe_NoUser returns 401 when the context has no authenticated user, so
// an unauthenticated reload probe does not falsely restore a session.
func TestHandleMe_NoUser(t *testing.T) {
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

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with no user in context, got %d", rec.Code)
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
