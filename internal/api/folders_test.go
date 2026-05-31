package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/storage"
)

// newFolderTestServer returns a server whose mailDB is backed by a real on-disk
// storage database so the folder handlers exercise actual mailbox CRUD.
func newFolderTestServer(t *testing.T) *Server {
	t.Helper()
	server, database, _ := helperSetupAccount(t)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close account db: %v", err)
		}
	})
	mailDB, err := storage.OpenDatabase(t.TempDir() + "/mail.db")
	if err != nil {
		t.Fatalf("open mail db: %v", err)
	}
	t.Cleanup(func() {
		if err := mailDB.Close(); err != nil {
			t.Errorf("close mail db: %v", err)
		}
	})
	server.mailDB = mailDB
	return server
}

func TestHandleFolders_CreateRenameDelete(t *testing.T) {
	server := newFolderTestServer(t)

	// Create a folder.
	rec := httptest.NewRecorder()
	server.handleFolders(rec, reqAsUser(http.MethodPost, "/api/v1/folders", `{"name":"Projects"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// It must appear in the list.
	rec = httptest.NewRecorder()
	server.handleFolders(rec, reqAsUser(http.MethodGet, "/api/v1/folders", ""))
	if !strings.Contains(rec.Body.String(), "Projects") {
		t.Fatalf("expected Projects in list, got %s", rec.Body.String())
	}

	// Rename it.
	rec = httptest.NewRecorder()
	server.handleFolderPath(rec, reqAsUser(http.MethodPut, "/api/v1/folders/Projects", `{"name":"Work"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	server.handleFolders(rec, reqAsUser(http.MethodGet, "/api/v1/folders", ""))
	if body := rec.Body.String(); !strings.Contains(body, "Work") || strings.Contains(body, "Projects") {
		t.Fatalf("expected Work and not Projects after rename, got %s", body)
	}

	// Delete it.
	rec = httptest.NewRecorder()
	server.handleFolderPath(rec, reqAsUser(http.MethodDelete, "/api/v1/folders/Work", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", rec.Code)
	}
}

func TestHandleFolders_RejectStandardMailboxMutation(t *testing.T) {
	server := newFolderTestServer(t)

	// Deleting a built-in mailbox is forbidden, so INBOX cannot be destroyed.
	rec := httptest.NewRecorder()
	server.handleFolderPath(rec, reqAsUser(http.MethodDelete, "/api/v1/folders/INBOX", ""))
	if rec.Code != http.StatusForbidden {
		t.Errorf("delete INBOX: expected 403, got %d", rec.Code)
	}

	// Renaming a built-in mailbox is forbidden.
	rec = httptest.NewRecorder()
	server.handleFolderPath(rec, reqAsUser(http.MethodPut, "/api/v1/folders/Sent", `{"name":"Outbox"}`))
	if rec.Code != http.StatusForbidden {
		t.Errorf("rename Sent: expected 403, got %d", rec.Code)
	}

	// Creating a folder named after a built-in mailbox is a conflict.
	rec = httptest.NewRecorder()
	server.handleFolders(rec, reqAsUser(http.MethodPost, "/api/v1/folders", `{"name":"Drafts"}`))
	if rec.Code != http.StatusConflict {
		t.Errorf("create Drafts: expected 409, got %d", rec.Code)
	}
}

func TestHandleFolders_InvalidName(t *testing.T) {
	server := newFolderTestServer(t)

	for _, bad := range []string{`{"name":""}`, `{"name":"a/b"}`, `{"name":"  "}`} {
		rec := httptest.NewRecorder()
		server.handleFolders(rec, reqAsUser(http.MethodPost, "/api/v1/folders", bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %s: expected 400, got %d", bad, rec.Code)
		}
	}
}
