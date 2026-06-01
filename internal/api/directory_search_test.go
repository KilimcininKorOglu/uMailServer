package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

func newDirectoryServer(t *testing.T) *Server {
	t.Helper()
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	if err := database.CreateDomain(&db.DomainData{Name: "test.com", MaxAccounts: 100, IsActive: true}); err != nil {
		t.Fatalf("create domain: %v", err)
	}
	mk := func(local string, active bool) {
		if err := database.CreateAccount(&db.AccountData{
			Email: local + "@test.com", LocalPart: local, Domain: "test.com",
			PasswordHash: "x", IsActive: active,
		}); err != nil {
			t.Fatalf("create account %s: %v", local, err)
		}
	}
	mk("admin", true) // the caller (reqAsUser injects admin@test.com)
	mk("bob", true)
	mk("barbara", true)
	mk("carol", true)
	mk("dormant", false)
	// An account in another domain must never appear in test.com's GAL.
	if err := database.CreateDomain(&db.DomainData{Name: "other.com", MaxAccounts: 10, IsActive: true}); err != nil {
		t.Fatalf("create other domain: %v", err)
	}
	if err := database.CreateAccount(&db.AccountData{Email: "eve@other.com", LocalPart: "eve", Domain: "other.com", PasswordHash: "x", IsActive: true}); err != nil {
		t.Fatalf("create eve: %v", err)
	}

	return NewServer(database, nil, Config{JWTSecret: "test-secret", TokenExpiry: time.Hour})
}

func directoryEntries(t *testing.T, rec *httptest.ResponseRecorder) []directoryEntry {
	t.Helper()
	var resp struct {
		Entries []directoryEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Entries
}

func TestDirectorySearch_PrefixWithinDomain(t *testing.T) {
	s := newDirectoryServer(t)
	rec := httptest.NewRecorder()
	s.handleDirectorySearch(rec, reqAsUser(http.MethodGet, "/api/v1/directory?q=bar", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	entries := directoryEntries(t, rec)
	if len(entries) != 1 || entries[0].Email != "barbara@test.com" {
		t.Fatalf("expected only barbara, got %+v", entries)
	}
}

func TestDirectorySearch_ExcludesSelfInactiveAndOtherDomains(t *testing.T) {
	s := newDirectoryServer(t)
	rec := httptest.NewRecorder()
	s.handleDirectorySearch(rec, reqAsUser(http.MethodGet, "/api/v1/directory", ""))
	entries := directoryEntries(t, rec)
	emails := map[string]bool{}
	for _, e := range entries {
		emails[e.Email] = true
	}
	if emails["admin@test.com"] {
		t.Error("the caller must not be suggested to themselves")
	}
	if emails["dormant@test.com"] {
		t.Error("inactive accounts must not appear in the GAL")
	}
	if emails["eve@other.com"] {
		t.Error("accounts from other domains must not appear in the GAL")
	}
	if !emails["bob@test.com"] || !emails["carol@test.com"] {
		t.Errorf("expected active same-domain peers, got %+v", entries)
	}
}
