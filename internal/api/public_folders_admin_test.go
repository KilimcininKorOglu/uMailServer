package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// publicFoldersListResponse mirrors the handlePublicFoldersAdmin GET payload so
// the test can assert on folder names and their grants.
type publicFoldersListResponse struct {
	Owner   string `json:"owner"`
	Folders []struct {
		Name   string `json:"name"`
		Grants []struct {
			Grantee string `json:"grantee"`
			Rights  string `json:"rights"`
		} `json:"grants"`
	} `json:"folders"`
}

func listPublicFolders(t *testing.T, server *Server) publicFoldersListResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	server.handlePublicFoldersAdmin(rec, reqAsUser(http.MethodGet, "/api/v1/admin/public-folders?domain=test.com", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out publicFoldersListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return out
}

// TestPublicFoldersAdmin_CRUDAndACL verifies the admin tree lifecycle: a folder
// is created under the domain's reserved public owner, an "anyone" grant is
// added and read back as RFC 4314 rights, clearing the grant removes it, and the
// folder is deletable. This matters because the public-folder feature is built
// entirely on this admin-managed owner+ACL pair — if create/grant/delete drift,
// every consuming surface (IMAP/webmail/EWS) silently loses its source of truth.
func TestPublicFoldersAdmin_CRUDAndACL(t *testing.T) {
	server := newFolderTestServer(t)

	// Create a public folder for the configured domain.
	rec := httptest.NewRecorder()
	server.handlePublicFoldersAdmin(rec, reqAsUser(http.MethodPost, "/api/v1/admin/public-folders", `{"domain":"test.com","name":"Announcements"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// It is owned by the reserved public owner and starts with no grants.
	list := listPublicFolders(t, server)
	if list.Owner != "public@test.com" {
		t.Fatalf("owner: expected public@test.com, got %q", list.Owner)
	}
	if len(list.Folders) != 1 || list.Folders[0].Name != "Announcements" {
		t.Fatalf("expected single Announcements folder, got %+v", list.Folders)
	}
	if len(list.Folders[0].Grants) != 0 {
		t.Fatalf("new folder should have no grants, got %+v", list.Folders[0].Grants)
	}

	// Grant org-wide read via the reserved "anyone" token.
	rec = httptest.NewRecorder()
	server.handlePublicFolderACL(rec, reqAsUser(http.MethodPut, "/api/v1/admin/public-folders/acl", `{"domain":"test.com","name":"Announcements","grantee":"anyone","rights":"lr"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("set acl: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	list = listPublicFolders(t, server)
	if len(list.Folders[0].Grants) != 1 ||
		list.Folders[0].Grants[0].Grantee != "anyone" ||
		list.Folders[0].Grants[0].Rights != "lr" {
		t.Fatalf("expected anyone:lr grant, got %+v", list.Folders[0].Grants)
	}

	// Clearing the rights removes the grant entirely.
	rec = httptest.NewRecorder()
	server.handlePublicFolderACL(rec, reqAsUser(http.MethodPut, "/api/v1/admin/public-folders/acl", `{"domain":"test.com","name":"Announcements","grantee":"anyone","rights":""}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear acl: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	list = listPublicFolders(t, server)
	if len(list.Folders[0].Grants) != 0 {
		t.Fatalf("cleared grant should be gone, got %+v", list.Folders[0].Grants)
	}

	// Delete the folder.
	rec = httptest.NewRecorder()
	server.handlePublicFoldersAdmin(rec, reqAsUser(http.MethodDelete, "/api/v1/admin/public-folders?domain=test.com&name=Announcements", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if list = listPublicFolders(t, server); len(list.Folders) != 0 {
		t.Fatalf("expected no folders after delete, got %+v", list.Folders)
	}
}

// TestPublicFoldersAdmin_Rejections verifies the guardrails: an unknown domain,
// a grantee outside the domain, and malformed rights are all refused. These keep
// the public tree from being created for non-existent tenants or granted to
// foreign accounts (tenant isolation at the management layer).
func TestPublicFoldersAdmin_Rejections(t *testing.T) {
	server := newFolderTestServer(t)

	// Unknown domain cannot host a public folder.
	rec := httptest.NewRecorder()
	server.handlePublicFoldersAdmin(rec, reqAsUser(http.MethodPost, "/api/v1/admin/public-folders", `{"domain":"ghost.example","name":"X"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown domain create: expected 400, got %d", rec.Code)
	}

	// Seed a real folder to exercise the ACL guardrails.
	rec = httptest.NewRecorder()
	server.handlePublicFoldersAdmin(rec, reqAsUser(http.MethodPost, "/api/v1/admin/public-folders", `{"domain":"test.com","name":"Announcements"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// A grantee from another domain is refused.
	rec = httptest.NewRecorder()
	server.handlePublicFolderACL(rec, reqAsUser(http.MethodPut, "/api/v1/admin/public-folders/acl", `{"domain":"test.com","name":"Announcements","grantee":"intruder@other.example","rights":"lr"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("foreign grantee: expected 400, got %d", rec.Code)
	}

	// Malformed rights are refused.
	rec = httptest.NewRecorder()
	server.handlePublicFolderACL(rec, reqAsUser(http.MethodPut, "/api/v1/admin/public-folders/acl", `{"domain":"test.com","name":"Announcements","grantee":"anyone","rights":"zzz"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid rights: expected 400, got %d", rec.Code)
	}
}
