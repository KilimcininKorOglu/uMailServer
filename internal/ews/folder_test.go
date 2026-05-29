package ews

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func tmpBoltDB(t *testing.T) *bbolt.DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := bbolt.Open(dbPath, 0o600, nil)
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	return db
}

func tmpEWSServer(t *testing.T) (*Server, func()) {
	// Each store gets its own temp DB to avoid cross-store conflicts.
	identityDB := tmpBoltDB(t)
	syncDB := tmpBoltDB(t)
	tombDB := tmpBoltDB(t)

	// Use NewBoltIdentityStore with a temp dir (creates its own DB).
	store, err := semcore.NewBoltIdentityStore(t.TempDir())
	if err != nil {
		_ = identityDB.Close()
		_ = syncDB.Close()
		_ = tombDB.Close()
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	syncStore, err := semcore.NewBoltSyncStateStore(syncDB)
	if err != nil {
		_ = identityDB.Close()
		_ = syncDB.Close()
		_ = tombDB.Close()
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}
	tombStore, err := semcore.NewBoltTombstoneStore(tombDB)
	if err != nil {
		_ = identityDB.Close()
		_ = syncDB.Close()
		_ = tombDB.Close()
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}
	srv := NewServer(store, syncStore, tombStore, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	cleanup := func() {
		_ = store.Close()
		_ = identityDB.Close()
		_ = syncDB.Close()
		_ = tombDB.Close()
	}
	return srv, cleanup
}

// ewsRequest posts a SOAP request with email injected into context.
// body should be raw EWS operation XML (without outer SOAP Envelope wrapper).
func ewsRequest(t *testing.T, srv *Server, email string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	if email != "" {
		ctx := context.WithValue(req.Context(), "X-Email", email)
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	srv.HandleHTTP(rec, req)
	return rec
}

// unmarshalFromBody extracts the first child of <soap:Body> and unmarshals it into v.
// It handles the namespace prefix issue by wrapping the extracted element in a synthetic
// SOAP envelope with proper namespace declarations, then using xml.Decoder with DecodeElement.
func unmarshalFromBody(t *testing.T, body []byte, v interface{}) {
	start := bytes.Index(body, []byte("<soap:Body>"))
	end := bytes.Index(body, []byte("</soap:Body>"))
	if start == -1 || end == -1 {
		t.Fatalf("no SOAP Body found in response: %s", string(body))
	}
	bodyContent := body[start+len("<soap:Body>") : end]
	trimmed := bytes.TrimLeft(bodyContent, " \n\r\t")

	// Find the first '<' and extract from there.
	firstBracket := bytes.Index(trimmed, []byte("<"))
	if firstBracket < 0 {
		t.Fatalf("no element found in SOAP body: %s", string(body))
	}
	inner := trimmed[firstBracket:]

	// Build a synthetic envelope with proper namespace declarations so the
	// decoder can resolve m:/t: prefixes. This handles both response types
	// (<m:GetFolderResponse>) and response message types (<m:GetFolderResponseMessage>).
	const MESSAGES_NS = "http://schemas.microsoft.com/exchange/services/2006/messages"
	const TYPES_NS = "http://schemas.microsoft.com/exchange/services/2006/types"
	synthetic := []byte(`<?xml version="1.0" encoding="utf-8"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:t="` + TYPES_NS + `" xmlns:m="` + MESSAGES_NS + `"><soap:Body>`)
	synthetic = append(synthetic, inner...)
	synthetic = append(synthetic, []byte(`</soap:Body></soap:Envelope>`)...)

	decoder := xml.NewDecoder(bytes.NewReader(synthetic))
	for {
		tok, err := decoder.Token()
		if err != nil {
			t.Fatalf("decoder error: %v", err)
		}
		if tok == nil {
			t.Fatalf("no matching element found in SOAP body: %s", string(body))
		}
		switch elem := tok.(type) {
		case xml.StartElement:
			// Skip SOAP envelope wrappers.
			if elem.Name.Local == "Envelope" || elem.Name.Local == "Header" || elem.Name.Local == "Body" {
				continue
			}
			if err := decoder.DecodeElement(v, &elem); err != nil {
				t.Fatalf("unmarshal element <%s>: %v\nbody: %s", elem.Name.Local, err, string(body))
			}
			return
		}
	}
}

// ---------------------------------------------------------------------------
// GetFolder
// ---------------------------------------------------------------------------

func TestGetFolder_DistinguishedInbox(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	mboxKey := "e:alice@example.com"
	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	_, err := srv.identity.EnsureFolderId(mboxKey, "INBOX", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	body := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"
               xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"
               xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages">
  <soap:Body>
    <GetFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
      <FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape>
      <FolderIds><t:DistinguishedFolderId Id="inbox"/></FolderIds>
    </GetFolder>
  </soap:Body>
</soap:Envelope>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp GetFolderResponse
	unmarshalFromBody(t, rec.Body.Bytes(), &resp)
	if len(resp.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg := resp.ResponseMessages.Messages[0]
	if msg.ResponseCode.Value != ErrNoError {
		t.Errorf("expected no error, got: %s", msg.ResponseCode.Value)
	}
}

func TestGetFolder_UnknownDistinguishedFolder(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}

	body := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"
               xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"
               xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages">
  <soap:Body>
    <GetFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
      <FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape>
      <FolderIds><t:DistinguishedFolderId Id="nonexistent_folder"/></FolderIds>
    </GetFolder>
  </soap:Body>
</soap:Envelope>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp GetFolderResponse
	unmarshalFromBody(t, rec.Body.Bytes(), &resp)
	if len(resp.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg := resp.ResponseMessages.Messages[0]
	if msg.ResponseCode.Value != ErrErrorFolderNotFound {
		t.Errorf("expected ErrErrorFolderNotFound, got: %s", msg.ResponseCode.Value)
	}
}

// ---------------------------------------------------------------------------
// FindFolder
// ---------------------------------------------------------------------------

func TestFindFolder_UserFolders(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	mboxKey := "e:alice@example.com"

	for _, name := range []string{"Projects", "Archive"} {
		_, err := srv.identity.EnsureFolderId(mboxKey, name, "")
		if err != nil {
			t.Fatalf("EnsureFolderId %s: %v", name, err)
		}
	}

	body := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"
               xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"
               xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages">
  <soap:Body>
    <FindFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages" Traversal="Shallow">
      <FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape>
      <ParentFolderIds><t:DistinguishedFolderId Id="msgfolderroot"/></ParentFolderIds>
    </FindFolder>
  </soap:Body>
</soap:Envelope>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp FindFolderResponse
	unmarshalFromBody(t, rec.Body.Bytes(), &resp)
	if len(resp.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg := resp.ResponseMessages.Messages[0]
	if msg.ResponseCode.Value != ErrNoError {
		t.Errorf("expected no error, got: %s", msg.ResponseCode.Value)
	}
}

// ---------------------------------------------------------------------------
// CreateFolder
// ---------------------------------------------------------------------------

func TestCreateFolder_UserFolder(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}

	body := `<CreateFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <ParentFolderId><t:DistinguishedFolderId Id="msgfolderroot"/></ParentFolderId>
  <Folders><t:Folder><t:DisplayName>My Projects</t:DisplayName><t:FolderClass>IPF.Note</t:FolderClass></t:Folder></Folders>
</CreateFolder>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var resp CreateFolderResponse
	unmarshalFromBody(t, rec.Body.Bytes(), &resp)
	if len(resp.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg := resp.ResponseMessages.Messages[0]
	if msg.ResponseClass != "Created" {
		t.Errorf("expected ResponseClass 'Created', got: %s", msg.ResponseClass)
	}
	if msg.ResponseCode.Value != ErrNoError {
		t.Errorf("expected no error, got: %s", msg.ResponseCode.Value)
	}
	if len(msg.Folders.Folders) == 0 || msg.Folders.Folders[0].FolderID.ID == "" {
		t.Error("expected non-empty FolderId")
	}
}

func TestCreateFolder_WithoutDisplayName(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}

	body := `<CreateFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <ParentFolderId><t:DistinguishedFolderId Id="msgfolderroot"/></ParentFolderId>
  <Folders><t:Folder><t:FolderClass>IPF.Note</t:FolderClass></t:Folder></Folders>
</CreateFolder>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp CreateFolderResponse
	unmarshalFromBody(t, rec.Body.Bytes(), &resp)
	if len(resp.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg := resp.ResponseMessages.Messages[0]
	if msg.ResponseCode.Value != ErrErrorInvalidOperation {
		t.Errorf("expected ErrErrorInvalidOperation for missing DisplayName, got: %s", msg.ResponseCode.Value)
	}
}

// ---------------------------------------------------------------------------
// UpdateFolder
// ---------------------------------------------------------------------------

func TestUpdateFolder_RenameUserFolder(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	mboxKey := "e:alice@example.com"

	folderID, err := srv.identity.EnsureFolderId(mboxKey, "Old Name", "")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	body := `<UpdateFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <FolderChanges>
    <t:FolderChange>
      <t:FolderId Id="` + folderID.String() + `"/>
      <t:Updates>
        <t:SetFolderField><t:FieldURI uri="folder:DisplayName"/><t:Folder><t:DisplayName>New Name</t:DisplayName></t:Folder></t:SetFolderField>
      </t:Updates>
    </t:FolderChange>
  </FolderChanges>
</UpdateFolder>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp UpdateFolderResponse
	unmarshalFromBody(t, rec.Body.Bytes(), &resp)
	if len(resp.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg := resp.ResponseMessages.Messages[0]
	if msg.ResponseCode.Value != ErrNoError {
		t.Errorf("expected no error, got: %s", msg.ResponseCode.Value)
	}
}

func TestUpdateFolder_CannotModifyDistinguishedFolder(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	mboxKey := "e:alice@example.com"

	inboxID, err := srv.identity.EnsureFolderId(mboxKey, "INBOX", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	body := `<UpdateFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <FolderChanges>
    <t:FolderChange>
      <t:FolderId Id="` + inboxID.String() + `" ChangeKey="` + inboxID.String() + `"/>
      <t:Updates>
        <t:SetFolderField><t:FieldURI uri="folder:DisplayName"/><t:Folder><t:DisplayName>Renamed Inbox</t:DisplayName></t:Folder></t:SetFolderField>
      </t:Updates>
    </t:FolderChange>
  </FolderChanges>
</UpdateFolder>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp UpdateFolderResponse
	unmarshalFromBody(t, rec.Body.Bytes(), &resp)
	if len(resp.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg := resp.ResponseMessages.Messages[0]
	if msg.ResponseCode.Value != ErrErrorInvalidOperation {
		t.Errorf("expected ErrErrorInvalidOperation for distinguished folder rename, got: %s", msg.ResponseCode.Value)
	}
}

// ---------------------------------------------------------------------------
// DeleteFolder
// ---------------------------------------------------------------------------

func TestDeleteFolder_UserFolder(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	mboxKey := "e:alice@example.com"

	folderID, err := srv.identity.EnsureFolderId(mboxKey, "To Delete", "")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	body := `<DeleteFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <FolderIds><t:FolderId Id="` + folderID.String() + `"/></FolderIds>
</DeleteFolder>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}

	// Verify folder is gone.
	_, err = srv.identity.GetFolderID(mboxKey, "To Delete")
	if err == nil {
		t.Error("expected folder to be deleted, but it still exists")
	}
}

func TestDeleteFolder_CannotDeleteDistinguishedFolder(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	mboxKey := "e:alice@example.com"

	inboxID, err := srv.identity.EnsureFolderId(mboxKey, "INBOX", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	body := `<DeleteFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <FolderIds><t:FolderId Id="` + inboxID.String() + `"/></FolderIds>
</DeleteFolder>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}

	// Distinguished folder should NOT be deleted.
	_, err = srv.identity.GetFolderID(mboxKey, "INBOX")
	if err != nil {
		t.Errorf("distinguished folder should not be deleted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SyncFolderHierarchy
// ---------------------------------------------------------------------------

func TestSyncFolderHierarchy_InitialSync(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	mboxKey := "e:alice@example.com"

	for _, name := range []string{"Folder A", "Folder B"} {
		_, err := srv.identity.EnsureFolderId(mboxKey, name, "")
		if err != nil {
			t.Fatalf("EnsureFolderId %s: %v", name, err)
		}
	}

	body := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"
               xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"
               xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages">
  <soap:Body>
    <SyncFolderHierarchy xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
      <FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape>
      <SyncState/>
    </SyncFolderHierarchy>
  </soap:Body>
</soap:Envelope>`

	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var resp SyncFolderHierarchyResponse
	unmarshalFromBody(t, rec.Body.Bytes(), &resp)
	if len(resp.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg := resp.ResponseMessages.Messages[0]
	if msg.ResponseCode != string(ErrNoError) {
		t.Errorf("expected no error, got: %s", msg.ResponseCode)
	}
	if msg.SyncState == "" {
		t.Error("expected non-empty SyncState")
	}
	if len(msg.Changes.Updates) == 0 || len(msg.Changes.Updates[0].Folders) < 2 {
		t.Errorf("expected at least 2 folder updates, got %d", len(msg.Changes.Updates))
	}
}

func TestSyncFolderHierarchy_IncrementalSync(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	mboxKey := "e:alice@example.com"

	// Create initial folder and do initial sync.
	_, err := srv.identity.EnsureFolderId(mboxKey, "Initial Folder", "")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	body1 := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"
               xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"
               xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages">
  <soap:Body>
    <SyncFolderHierarchy xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
      <FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape>
      <SyncState/>
    </SyncFolderHierarchy>
  </soap:Body>
</soap:Envelope>`

	rec1 := ewsRequest(t, srv, "alice@example.com", body1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("initial sync status: %d", rec1.Code)
	}
	var resp1 SyncFolderHierarchyResponse
	unmarshalFromBody(t, rec1.Body.Bytes(), &resp1)
	if len(resp1.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	syncState := resp1.ResponseMessages.Messages[0].SyncState
	if syncState == "" {
		t.Fatal("expected non-empty SyncState")
	}

	// Create new folder between syncs.
	_, err = srv.identity.EnsureFolderId(mboxKey, "New Folder", "")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	// Incremental sync.
	body2 := `<SyncFolderHierarchy xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape>
  <SyncState>` + syncState + `</SyncState>
</SyncFolderHierarchy>`

	rec2 := ewsRequest(t, srv, "alice@example.com", body2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("incremental sync status: %d", rec2.Code)
	}
	var resp2 SyncFolderHierarchyResponse
	unmarshalFromBody(t, rec2.Body.Bytes(), &resp2)
	if len(resp2.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg2 := resp2.ResponseMessages.Messages[0]
	if msg2.ResponseCode != string(ErrNoError) {
		t.Errorf("expected no error on incremental sync, got: %s", msg2.ResponseCode)
	}
}

// ---------------------------------------------------------------------------
// HTTP method validation
// ---------------------------------------------------------------------------

func TestGetFolder_GETNotAllowed(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/EWS/Exchange.asmx", nil)
	rec := httptest.NewRecorder()
	srv.HandleHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected %d for GET, got: %d", http.StatusMethodNotAllowed, rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Malformed request
// ---------------------------------------------------------------------------

func TestGetFolder_MalformedXML(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}

	// Truncated XML — should return SOAP fault, not crash.
	body := `<GetFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <FolderShape><t:BaseShape>Default</t:BaseShape></FolderShape>
  <FolderIds><t:FolderId Id="`
	rec := ewsRequest(t, srv, "alice@example.com", body)
	_ = rec.Code // status may be 200 with SOAP fault body; just ensure no panic
}
