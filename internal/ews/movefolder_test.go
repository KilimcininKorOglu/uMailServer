package ews

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMoveFolder verifies the MoveFolder operation re-parents a folder under the
// destination folder.
func TestMoveFolder(t *testing.T) {
	identity, sync, tomb, msgStore, policyStore, collabStore, cleanup := tmpDirectoryStores(t)
	t.Cleanup(cleanup)

	srv := NewServer(identity, sync, tomb, msgStore, nil, nil, nil, nil, nil, collabStore, policyStore, nil, nil, nil)

	email := "alice@ex.test"
	if _, err := identity.EnsureMailboxId(email); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	srcID, err := identity.EnsureFolderId(email, "Projects", "")
	if err != nil {
		t.Fatalf("EnsureFolderId src: %v", err)
	}
	destID, err := identity.EnsureFolderId(email, "Archive", "")
	if err != nil {
		t.Fatalf("EnsureFolderId dest: %v", err)
	}

	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" ` +
		`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">` +
		`<soap:Body><m:MoveFolder>` +
		`<m:ToFolderId><t:FolderId Id="` + destID.String() + `"/></m:ToFolderId>` +
		`<m:FolderIds><t:FolderId Id="` + srcID.String() + `"/></m:FolderIds>` +
		`</m:MoveFolder></soap:Body></soap:Envelope>`

	req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	//nolint:staticcheck // EWS resolveMailboxFromBody reads the plain string context key "X-Email".
	req = req.WithContext(context.WithValue(req.Context(), "X-Email", email))
	rec := httptest.NewRecorder()
	srv.HandleHTTP(rec, req)

	got := rec.Body.String()
	if !strings.Contains(got, `ResponseClass="Success"`) {
		t.Fatalf("MoveFolder: expected Success, got:\n%s", got)
	}

	moved, err := identity.GetFolderByID(srcID)
	if err != nil {
		t.Fatalf("GetFolderByID: %v", err)
	}
	if !moved.ParentID.Equal(destID) {
		t.Errorf("MoveFolder: src ParentID = %v, want %v", moved.ParentID, destID)
	}
}
