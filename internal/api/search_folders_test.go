package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// userReq builds a request carrying the authenticated-user context the search
// folder handlers read, bypassing the auth middleware for a direct handler call.
func userReq(method, target, user, body string) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	return req.WithContext(context.WithValue(req.Context(), "user", user)) //nolint:staticcheck // string key matches handler convention
}

// TestSearchFolders_CRUD exercises the create/list/update/delete lifecycle of a
// webmail saved search and proves it round-trips the canonical definition.
func TestSearchFolders_CRUD(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())
	const user = "alice@example.com"

	// Create.
	rec := httptest.NewRecorder()
	server.handleSearchFolders(rec, userReq(http.MethodPost, "/api/v1/search-folders", user,
		`{"name":"From Boss","from":"boss@corp.com","body":"urgent","base_folders":["INBOX"]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var created searchFolderDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" || created.Name != "From Boss" || created.From != "boss@corp.com" {
		t.Fatalf("unexpected created folder: %+v", created)
	}

	// List reflects the new folder and its criteria.
	rec = httptest.NewRecorder()
	server.handleSearchFolders(rec, userReq(http.MethodGet, "/api/v1/search-folders", user, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var listResp struct {
		SearchFolders []searchFolderDTO `json:"search_folders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.SearchFolders) != 1 || listResp.SearchFolders[0].ID != created.ID {
		t.Fatalf("list mismatch: %+v", listResp.SearchFolders)
	}
	if listResp.SearchFolders[0].Body != "urgent" || len(listResp.SearchFolders[0].BaseFolders) != 1 {
		t.Errorf("criteria not round-tripped: %+v", listResp.SearchFolders[0])
	}

	// Update the criteria.
	rec = httptest.NewRecorder()
	server.handleSearchFolderPath(rec, userReq(http.MethodPut, "/api/v1/search-folders/"+created.ID, user,
		`{"name":"From Boss","subject":"Q3","base_folders":["INBOX","Sent"]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.handleSearchFolders(rec, userReq(http.MethodGet, "/api/v1/search-folders", user, ""))
	listResp.SearchFolders = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list after update: %v", err)
	}
	if len(listResp.SearchFolders) != 1 || listResp.SearchFolders[0].Subject != "Q3" || listResp.SearchFolders[0].From != "" {
		t.Fatalf("update not reflected: %+v", listResp.SearchFolders)
	}

	// Delete.
	rec = httptest.NewRecorder()
	server.handleSearchFolderPath(rec, userReq(http.MethodDelete, "/api/v1/search-folders/"+created.ID, user, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	server.handleSearchFolders(rec, userReq(http.MethodGet, "/api/v1/search-folders", user, ""))
	listResp.SearchFolders = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list after delete: %v", err)
	}
	if len(listResp.SearchFolders) != 0 {
		t.Fatalf("expected no search folders after delete, got %d", len(listResp.SearchFolders))
	}
}

// TestSearchFolders_Ownership proves a caller cannot reach another user's search
// folder by id.
func TestSearchFolders_Ownership(t *testing.T) {
	server := newFilterTestServer(t, t.TempDir())

	// Owner creates a search folder.
	rec := httptest.NewRecorder()
	server.handleSearchFolders(rec, userReq(http.MethodPost, "/api/v1/search-folders", "owner@example.com",
		`{"name":"Mine","from":"x@y.com"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d", rec.Code)
	}
	var created searchFolderDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// A different user must get 404 for update, delete, and results.
	for _, m := range []struct {
		method, suffix string
	}{
		{http.MethodPut, ""},
		{http.MethodDelete, ""},
		{http.MethodGet, "/results"},
	} {
		rec = httptest.NewRecorder()
		server.handleSearchFolderPath(rec, userReq(m.method, "/api/v1/search-folders/"+created.ID+m.suffix, "intruder@example.com", `{"name":"Mine"}`))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", m.method, m.suffix, rec.Code)
		}
	}
}
