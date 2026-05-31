package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMyDelegations_GrantListRevoke(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())

	// Grant a delegate on the authenticated user's own mailbox.
	rec := httptest.NewRecorder()
	server.handleMyDelegations(rec, reqAsUser(http.MethodPost, "/api/v1/delegations",
		`{"grantee":"bob@test.com","rights":["read","write"],"canSendOnBehalf":true}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created delegationDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Grantee != "bob@test.com" || created.Owner != "admin@test.com" {
		t.Errorf("unexpected grant: %+v", created)
	}
	if created.Rights != "read, write" || !created.CanSendOnBehalf {
		t.Errorf("rights/sob not persisted: %+v", created)
	}

	// List shows it.
	rec = httptest.NewRecorder()
	server.handleMyDelegations(rec, reqAsUser(http.MethodGet, "/api/v1/delegations", ""))
	if !strings.Contains(rec.Body.String(), "bob@test.com") {
		t.Fatalf("expected bob in list, got %s", rec.Body.String())
	}

	// Revoke it.
	rec = httptest.NewRecorder()
	server.handleMyDelegationDetail(rec, reqAsUser(http.MethodDelete, "/api/v1/delegations/"+created.ID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	server.handleMyDelegations(rec, reqAsUser(http.MethodGet, "/api/v1/delegations", ""))
	if strings.Contains(rec.Body.String(), "bob@test.com") {
		t.Fatalf("expected no delegations after revoke, got %s", rec.Body.String())
	}
}

func TestMyDelegations_RejectSelf(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())
	rec := httptest.NewRecorder()
	server.handleMyDelegations(rec, reqAsUser(http.MethodPost, "/api/v1/delegations",
		`{"grantee":"admin@test.com","rights":["read"]}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for self-delegation, got %d", rec.Code)
	}
}
