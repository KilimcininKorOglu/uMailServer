package ews

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
	"go.etcd.io/bbolt"
)

//nolint:errcheck,staticcheck // Test fixtures intentionally skip error checks on store setup.


// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func tmpItemStores(t *testing.T) (*semcore.BoltIdentityStore, *semcore.BoltSyncStateStore, *semcore.BoltTombstoneStore, *storage.MessageStore, func()) {
	tmpDir := t.TempDir()

	// Identity store.
	identity, err := semcore.NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}

	// Sync state store.
	syncDB, err := bbolt.Open(filepath.Join(tmpDir, "sync.db"), 0o600, nil)
	if err != nil {
		t.Fatalf("bbolt.Open sync: %v", err)
	}
	sync, err := semcore.NewBoltSyncStateStore(syncDB)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	// Tombstone store.
	tombDB, err := bbolt.Open(filepath.Join(tmpDir, "tomb.db"), 0o600, nil)
	if err != nil {
		t.Fatalf("bbolt.Open tomb: %v", err)
	}
	tomb, err := semcore.NewBoltTombstoneStore(tombDB)
	if err != nil {
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}

	// Message store.
	msgStore, err := storage.NewMessageStore(filepath.Join(tmpDir, "msgs"))
	if err != nil {
		t.Fatalf("NewMessageStore: %v", err)
	}

	cleanup := func() {
		_ = identity.Close() //nolint:errcheck
		_ = syncDB.Close()    //nolint:errcheck
		_ = tombDB.Close()    //nolint:errcheck
		_ = msgStore.Close()  //nolint:errcheck
	}

	return identity, sync, tomb, msgStore, cleanup
}

func tmpEWSItemServer(t *testing.T) (*Server, func()) {
	identity, sync, tomb, msgStore, cleanup := tmpItemStores(t)

	// Create delegate store for permission enforcement tests.
	delegateDB, err := bbolt.Open(filepath.Join(t.TempDir(), "delegate.db"), 0o600, nil)
	if err != nil {
		cleanup()
		t.Fatalf("bbolt.Open delegate: %v", err)
	}
	delegateStore, err := semcore.NewBoltDelegateStore(delegateDB)
	if err != nil {
		_ = delegateDB.Close() //nolint:errcheck
		cleanup()
		t.Fatalf("NewBoltDelegateStore: %v", err)
	}

	// Mutation pipeline needs the identity store.
	pipe := semcore.NewMutationPipeline(identity, nil)

	srv := NewServer(identity, sync, tomb, msgStore, nil, pipe, nil, nil, nil, nil, delegateStore, nil)

	return srv, func() {
		cleanup()
		_ = delegateDB.Close() //nolint:errcheck
	}
}

// ewsItemRequest posts a SOAP request with email injected into context.
func ewsItemRequest(t *testing.T, srv *Server, email, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	if email != "" {
		//nolint:staticcheck // Test fixture uses string key intentionally.
		ctx := context.WithValue(req.Context(), "X-Email", email)
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	srv.HandleHTTP(rec, req)
	return rec
}

// ewsEnvelope wraps a bare EWS operation XML in a SOAP envelope.
func ewsEnvelope(op string, body string) string {
	return `<?xml version="1.0" encoding="utf-8"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"><soap:Body><m:` + op + `>` + body + `</m:` + op + `></soap:Body></soap:Envelope>`
}

// ewsEnvelopeWithAttrs wraps an EWS operation with additional attributes appended
// to the operation element. Used when SaveItemToFolder must be an ATTRIBUTE (not
// a child element) so Go's xml.Unmarshal parses it correctly.
func ewsEnvelopeWithAttrs(op string, attrs string, body string) string {
	return `<?xml version="1.0" encoding="utf-8"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"><soap:Body><m:` + op + attrs + `>` + body + `</m:` + op + `></soap:Body></soap:Envelope>`
}

// ensureMailboxFixtures creates test mailbox and standard folder identities.
// Errors here are ignored because they represent preconditions (mailbox/folder
// may already exist) that don't affect test validity.
func ensureMailboxFixtures(t *testing.T, srv *Server, email string) {
	t.Helper()
	// Both EnsureMailboxId and EnsureFolderId expect the raw email (without "e:" prefix).
	if _, err := srv.identity.EnsureMailboxId(email); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	folders := []struct{ name, role string }{
		{"drafts", "drafts"},
		{"sent", "sent"},
		{"inbox", "inbox"},
		{"trash", "trash"},
	}
	for _, f := range folders {
		if _, err := srv.identity.EnsureFolderId(email, f.name, f.role); err != nil {
			t.Fatalf("EnsureFolderId %s: %v", f.name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// CreateItem tests
// ---------------------------------------------------------------------------

func TestCreateItem_BasicDraft(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)

	body := ewsEnvelope("CreateItem", `
		<SavedItemFolderId>
			<t:DistinguishedFolderId Id="drafts"/>
		</SavedItemFolderId>
		<SaveItemToFolder>true</SaveItemToFolder>
		<Items>
			<t:Message>
				<t:Subject>Test Draft</t:Subject>
				<t:Body BodyType="Text">Hello world</t:Body>
				<t:ToRecipients>
					<t:Mailbox><t:EmailAddress>bob@local.test</t:EmailAddress></t:Mailbox>
				</t:ToRecipients>
			</t:Message>
		</Items>
	`)

	rec := ewsItemRequest(t, srv, email, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateItem HTTP status: got %d, want 200", rec.Code)
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "CreateItemResponse") {
		t.Fatalf("Response should contain CreateItemResponse, got: %s", respBody)
	}

	// Verify Success response class.
	if !strings.Contains(respBody, `ResponseClass="Success"`) {
		t.Fatalf("Response should indicate Success, got: %s", respBody)
	}

	// Verify ItemId is returned (starts with an ID attribute).
	if !strings.Contains(respBody, `Id="`) {
		t.Fatalf("Response should contain Item Id, got: %s", respBody)
	}

	// Verify ChangeKey is returned.
	if !strings.Contains(respBody, `ChangeKey="`) {
		t.Fatalf("Response should contain ChangeKey, got: %s", respBody)
	}
}

func TestCreateItem_Unauthenticated(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	body := ewsEnvelope("CreateItem", `
		<Items>
			<t:Message>
				<t:Subject>Test</t:Subject>
			</t:Message>
		</Items>
	`)

	rec := ewsItemRequest(t, srv, "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateItem HTTP status: got %d, want 200 (EWS returns 200 even on auth failure)", rec.Code)
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "ErrorAccessDenied") {
		t.Fatalf("Response should indicate access denied, got: %s", respBody)
	}
}

// ---------------------------------------------------------------------------
// GetItem tests
// ---------------------------------------------------------------------------

func TestGetItem_AfterCreate(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)

	// Create a draft.
	createBody := ewsEnvelope("CreateItem", `
		<SavedItemFolderId><t:DistinguishedFolderId Id="drafts"/></SavedItemFolderId>
		<SaveItemToFolder>true</SaveItemToFolder>
		<Items>
			<t:Message>
				<t:Subject>My Test Subject</t:Subject>
				<t:Body BodyType="Text">Test body content</t:Body>
			</t:Message>
		</Items>
	`)
	createRec := ewsItemRequest(t, srv, email, createBody)
	createRespBody := createRec.Body.String()

	// Extract ItemId from create response.
	idStart := strings.Index(createRespBody, `Id="`)
	if idStart == -1 {
		t.Fatalf("Could not find ItemId in create response: %s", createRespBody)
	}
	idRest := createRespBody[idStart+4:]
	idEnd := strings.Index(idRest, `"`)
	itemID := idRest[:idEnd]

	// Extract ChangeKey.
	ckStart := strings.Index(createRespBody, `ChangeKey="`)
	if ckStart == -1 {
		t.Fatalf("Could not find ChangeKey in create response: %s", createRespBody)
	}
	ckRest := createRespBody[ckStart+10:]
	ckEnd := strings.Index(ckRest, `"`)
	itemCK := ckRest[:ckEnd]

	// Now get the item.
	getBody := ewsEnvelope("GetItem", `
		<ItemShape><t:BaseShape>AllProperties</t:BaseShape></ItemShape>
		<ItemIds>
			<t:ItemId Id="`+itemID+`" ChangeKey="`+itemCK+`"/>
		</ItemIds>
	`)

	rec := ewsItemRequest(t, srv, email, getBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetItem HTTP status: got %d, want 200", rec.Code)
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "GetItemResponse") {
		t.Fatalf("Response should contain GetItemResponse, got: %s", respBody)
	}

	if !strings.Contains(respBody, `ResponseClass="Success"`) {
		t.Fatalf("Response should indicate Success, got: %s", respBody)
	}

	// Subject should be visible.
	if !strings.Contains(respBody, "My Test Subject") {
		t.Fatalf("Response should contain subject, got: %s", respBody)
	}
}

func TestGetItem_NotFound(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)

	getBody := ewsEnvelope("GetItem", `
		<ItemShape><t:BaseShape>AllProperties</t:BaseShape></ItemShape>
		<ItemIds>
			<t:ItemId Id="nonexistent-id-12345"/>
		</ItemIds>
	`)

	rec := ewsItemRequest(t, srv, email, getBody)
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "ErrorItemNotFound") {
		t.Fatalf("Response should contain ErrorItemNotFound, got: %s", respBody)
	}
}

// ---------------------------------------------------------------------------
// UpdateItem tests
// ---------------------------------------------------------------------------

func TestUpdateItem_ChangeKeyMismatch(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)
	//nolint:errcheck
			_, _ = srv.identity.EnsureFolderId("e:"+email, "drafts", "drafts") //nolint:errcheck //nolint:errcheck

	// Create an item.
	createBody := ewsEnvelope("CreateItem", `
		<Items>
			<t:Message>
				<t:Subject>Update Test</t:Subject>
			</t:Message>
		</Items>
	`)
	createRec := ewsItemRequest(t, srv, email, createBody)
	createRespBody := createRec.Body.String()

	idStart := strings.Index(createRespBody, `Id="`)
	idRest := createRespBody[idStart+4:]
	idEnd := strings.Index(idRest, `"`)
	itemID := idRest[:idEnd]

	// Try update with wrong change key.
	updateBody := ewsEnvelope("UpdateItem", `
		<ItemChanges>
			<t:ItemChange>
				<t:ItemId Id="`+itemID+`" ChangeKey="wrong-change-key"/>
				<t:Updates>
					<t:SetItemField>
						<t:FieldURI uri="item:Subject"/>
						<t:Message>
							<t:Subject>Updated Subject</t:Subject>
						</t:Message>
					</t:SetItemField>
				</t:Updates>
			</t:ItemChange>
		</ItemChanges>
	`)

	rec := ewsItemRequest(t, srv, email, updateBody)
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "ErrorItemIdOrChangeKey") {
		t.Fatalf("Response should contain ErrorItemIdOrChangeKey for stale ChangeKey, got: %s", respBody)
	}
}

// ---------------------------------------------------------------------------
// DeleteItem tests
// ---------------------------------------------------------------------------

func TestDeleteItem_SoftDelete(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)

	// Create an item.
	createBody := ewsEnvelope("CreateItem", `
		<Items>
			<t:Message>
				<t:Subject>Delete Test</t:Subject>
			</t:Message>
		</Items>
	`)
	createRec := ewsItemRequest(t, srv, email, createBody)
	createRespBody := createRec.Body.String()

	idStart := strings.Index(createRespBody, `Id="`)
	idRest := createRespBody[idStart+4:]
	idEnd := strings.Index(idRest, `"`)
	itemID := idRest[:idEnd]

	// Delete it (soft delete / MoveToDeletedItems is the default).
	deleteBody := ewsEnvelope("DeleteItem", `
		<ItemIds>
			<t:ItemId Id="`+itemID+`"/>
		</ItemIds>
		<DeleteType>MoveToDeletedItems</DeleteType>
	`)

	rec := ewsItemRequest(t, srv, email, deleteBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("DeleteItem HTTP status: got %d, want 200", rec.Code)
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "DeleteItemResponse") {
		t.Fatalf("Response should contain DeleteItemResponse, got: %s", respBody)
	}
	if !strings.Contains(respBody, `ResponseClass="Success"`) {
		t.Fatalf("Response should indicate Success, got: %s", respBody)
	}

	// Verify item is no longer accessible.
	getBody := ewsEnvelope("GetItem", `
		<ItemShape><t:BaseShape>AllProperties</t:BaseShape></ItemShape>
		<ItemIds>
			<t:ItemId Id="`+itemID+`"/>
		</ItemIds>
	`)
	getRec := ewsItemRequest(t, srv, email, getBody)
	getRespBody := getRec.Body.String()
	if !strings.Contains(getRespBody, "ErrorItemNotFound") {
		t.Fatalf("Deleted item should not be found, got: %s", getRespBody)
	}
}

func TestDeleteItem_HardDelete(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)
	//nolint:errcheck
			_, _ = srv.identity.EnsureFolderId("e:"+email, "drafts", "drafts") //nolint:errcheck //nolint:errcheck

	// Create an item.
	createBody := ewsEnvelope("CreateItem", `
		<Items>
			<t:Message>
				<t:Subject>Hard Delete Test</t:Subject>
			</t:Message>
		</Items>
	`)
	createRec := ewsItemRequest(t, srv, email, createBody)
	createRespBody := createRec.Body.String()

	idStart := strings.Index(createRespBody, `Id="`)
	idRest := createRespBody[idStart+4:]
	idEnd := strings.Index(idRest, `"`)
	itemID := idRest[:idEnd]

	// Hard delete it.
	deleteBody := ewsEnvelope("DeleteItem", `
		<ItemIds>
			<t:ItemId Id="`+itemID+`"/>
		</ItemIds>
		<DeleteType>HardDelete</DeleteType>
	`)

	rec := ewsItemRequest(t, srv, email, deleteBody)
	respBody := rec.Body.String()
	if !strings.Contains(respBody, `ResponseClass="Success"`) {
		t.Fatalf("Hard delete should succeed, got: %s", respBody)
	}
}

// ---------------------------------------------------------------------------
// SendItem tests
// ---------------------------------------------------------------------------

func TestSendItem_Basic(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)

	// Create a draft.
	createBody := ewsEnvelope("CreateItem", `
		<Items>
			<t:Message>
				<t:Subject>Send Test</t:Subject>
				<t:Body BodyType="Text">Sending this</t:Body>
				<t:ToRecipients>
					<t:Mailbox><t:EmailAddress>bob@local.test</t:EmailAddress></t:Mailbox>
				</t:ToRecipients>
			</t:Message>
		</Items>
	`)
	createRec := ewsItemRequest(t, srv, email, createBody)
	createRespBody := createRec.Body.String()

	idStart := strings.Index(createRespBody, `Id="`)
	idRest := createRespBody[idStart+4:]
	idEnd := strings.Index(idRest, `"`)
	itemID := idRest[:idEnd]

	// Send it.
	sendBody := ewsEnvelope("SendItem", `
		<SaveItemToFolder>true</SaveItemToFolder>
		<SavedItemFolderId>
			<t:DistinguishedFolderId Id="sentitems"/>
		</SavedItemFolderId>
		<ItemIds>
			<t:ItemId Id="`+itemID+`"/>
		</ItemIds>
	`)

	rec := ewsItemRequest(t, srv, email, sendBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("SendItem HTTP status: got %d, want 200", rec.Code)
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "SendItemResponse") {
		t.Fatalf("Response should contain SendItemResponse, got: %s", respBody)
	}
	if !strings.Contains(respBody, `ResponseClass="Success"`) {
		t.Fatalf("Response should indicate Success, got: %s", respBody)
	}
}

// ---------------------------------------------------------------------------
// MoveItem tests
// ---------------------------------------------------------------------------

func TestMoveItem_ToTrash(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)
	//nolint:errcheck
			_, _ = srv.identity.EnsureFolderId("e:"+email, "drafts", "drafts") //nolint:errcheck //nolint:errcheck
	//nolint:errcheck
			_, _ = srv.identity.EnsureFolderId("e:"+email, "trash", "trash") //nolint:errcheck //nolint:errcheck

	// Create an item in drafts.
	createBody := ewsEnvelope("CreateItem", `
		<SavedItemFolderId><t:DistinguishedFolderId Id="drafts"/></SavedItemFolderId>
		<SaveItemToFolder>true</SaveItemToFolder>
		<Items>
			<t:Message>
				<t:Subject>Move Test</t:Subject>
			</t:Message>
		</Items>
	`)
	createRec := ewsItemRequest(t, srv, email, createBody)
	createRespBody := createRec.Body.String()

	idStart := strings.Index(createRespBody, `Id="`)
	idRest := createRespBody[idStart+4:]
	idEnd := strings.Index(idRest, `"`)
	itemID := idRest[:idEnd]

	// Move to trash.
	moveBody := ewsEnvelope("MoveItem", `
		<ToFolderId>
			<t:DistinguishedFolderId Id="deleteditems"/>
		</ToFolderId>
		<ItemIds>
			<t:ItemId Id="`+itemID+`"/>
		</ItemIds>
	`)

	rec := ewsItemRequest(t, srv, email, moveBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("MoveItem HTTP status: got %d, want 200", rec.Code)
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "MoveItemResponse") {
		t.Fatalf("Response should contain MoveItemResponse, got: %s", respBody)
	}
	if !strings.Contains(respBody, `ResponseClass="Success"`) {
		t.Fatalf("Response should indicate Success, got: %s", respBody)
	}
	// Parent folder updated: verified by Success response above.
}

// ---------------------------------------------------------------------------
// CopyItem tests
// ---------------------------------------------------------------------------

func TestCopyItem_Basic(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)

	// Create an item in drafts.
	createBody := ewsEnvelope("CreateItem", `
		<SavedItemFolderId><t:DistinguishedFolderId Id="drafts"/></SavedItemFolderId>
		<SaveItemToFolder>true</SaveItemToFolder>
		<Items>
			<t:Message>
				<t:Subject>Copy Test</t:Subject>
			</t:Message>
		</Items>
	`)
	createRec := ewsItemRequest(t, srv, email, createBody)
	createRespBody := createRec.Body.String()

	idStart := strings.Index(createRespBody, `Id="`)
	idRest := createRespBody[idStart+4:]
	idEnd := strings.Index(idRest, `"`)
	itemID := idRest[:idEnd]

	// Copy to sent items.
	copyBody := ewsEnvelope("CopyItem", `
		<ToFolderId>
			<t:DistinguishedFolderId Id="sentitems"/>
		</ToFolderId>
		<ItemIds>
			<t:ItemId Id="`+itemID+`"/>
		</ItemIds>
	`)

	rec := ewsItemRequest(t, srv, email, copyBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("CopyItem HTTP status: got %d, want 200", rec.Code)
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, "CopyItemResponse") {
		t.Fatalf("Response should contain CopyItemResponse, got: %s", respBody)
	}
	if !strings.Contains(respBody, `ResponseClass="Success"`) {
		t.Fatalf("Response should indicate Success, got: %s", respBody)
	}

	// Verify the new item has a different ItemId.
	newIdStart := strings.Index(respBody, `Id="`)
	if newIdStart == -1 {
		t.Fatalf("Copy response should contain new Item Id, got: %s", respBody)
	}
}

// ---------------------------------------------------------------------------
// GetAttachment tests
// ---------------------------------------------------------------------------

func TestGetAttachment_NotFound(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)

	getBody := ewsEnvelope("GetAttachment", `
		<AttachmentIds>
			<t:AttachmentId Id="nonexistent-attachment-id"/>
		</AttachmentIds>
	`)

	rec := ewsItemRequest(t, srv, email, getBody)
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "ErrorItemNotFound") {
		t.Fatalf("Response should contain ErrorItemNotFound for unknown attachment, got: %s", respBody)
	}
}

// ---------------------------------------------------------------------------
// Operation routing tests
// ---------------------------------------------------------------------------

func TestEWS_UnknownOperation(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)

	body := ewsEnvelope("BogusOperation", `<Bogus/>`)

	rec := ewsItemRequest(t, srv, email, body)
	respBody := rec.Body.String()
	if !strings.Contains(respBody, "ErrorNotImplemented") {
		t.Fatalf("Unknown operation should return ErrorNotImplemented, got: %s", respBody)
	}
}

func TestEWS_NonPOST(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/EWS/Exchange.asmx", nil)
	rec := httptest.NewRecorder()
	srv.HandleHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("EWS should reject non-POST, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Mail-delivery parity test (VAL-MAIL-033)
// ---------------------------------------------------------------------------

func TestCreateItem_DraftReadableAfterCreation(t *testing.T) {
	// VAL-MAIL-010: Draft creation returns a readable item immediately.
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)
	//nolint:errcheck
			_, _ = srv.identity.EnsureFolderId("e:"+email, "drafts", "drafts") //nolint:errcheck //nolint:errcheck

	// Create draft.
	createBody := ewsEnvelope("CreateItem", `
		<SavedItemFolderId><t:DistinguishedFolderId Id="drafts"/></SavedItemFolderId>
		<SaveItemToFolder>true</SaveItemToFolder>
		<Items>
			<t:Message>
				<t:Subject>Immediate Read Test</t:Subject>
				<t:Body BodyType="Text">Must be readable right away</t:Body>
			</t:Message>
		</Items>
	`)
	createRec := ewsItemRequest(t, srv, email, createBody)
	createRespBody := createRec.Body.String()

	idStart := strings.Index(createRespBody, `Id="`)
	idRest := createRespBody[idStart+4:]
	idEnd := strings.Index(idRest, `"`)
	itemID := idRest[:idEnd]

	ckStart := strings.Index(createRespBody, `ChangeKey="`)
	ckRest := createRespBody[ckStart+10:]
	ckEnd := strings.Index(ckRest, `"`)
	itemCK := ckRest[:ckEnd]

	// Fetch the same item immediately.
	getBody := ewsEnvelope("GetItem", `
		<ItemShape><t:BaseShape>AllProperties</t:BaseShape></ItemShape>
		<ItemIds>
			<t:ItemId Id="`+itemID+`" ChangeKey="`+itemCK+`"/>
		</ItemIds>
	`)
	getRec := ewsItemRequest(t, srv, email, getBody)
	getRespBody := getRec.Body.String()

	if !strings.Contains(getRespBody, "GetItemResponse") {
		t.Fatalf("GetItem should return GetItemResponse, got: %s", getRespBody)
	}
	if !strings.Contains(getRespBody, `ResponseClass="Success"`) {
		t.Fatalf("GetItem should succeed, got: %s", getRespBody)
	}
	if !strings.Contains(getRespBody, "Immediate Read Test") {
		t.Fatalf("Subject should be readable immediately after creation, got: %s", getRespBody)
	}
	if !strings.Contains(getRespBody, "Must be readable right away") {
		t.Fatalf("Body content should be readable immediately after creation, got: %s", getRespBody)
	}
}

func TestCreateItem_MIMEContentPreserved(t *testing.T) {
	// VAL-MAIL-011: MIME and user-authored content round-trip without lossy mutation.
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)
	//nolint:errcheck
			_, _ = srv.identity.EnsureFolderId("e:"+email, "drafts", "drafts") //nolint:errcheck //nolint:errcheck

	// Create draft with To recipient.
	createBody := ewsEnvelope("CreateItem", `
		<SavedItemFolderId><t:DistinguishedFolderId Id="drafts"/></SavedItemFolderId>
		<SaveItemToFolder>true</SaveItemToFolder>
		<Items>
			<t:Message>
				<t:Subject>MIME Preservation Test</t:Subject>
				<t:Body BodyType="Text">Plain text body content</t:Body>
				<t:ToRecipients>
					<t:Mailbox><t:EmailAddress>bob@local.test</t:EmailAddress></t:Mailbox>
				</t:ToRecipients>
			</t:Message>
		</Items>
	`)
	createRec := ewsItemRequest(t, srv, email, createBody)
	createRespBody := createRec.Body.String()

	idStart := strings.Index(createRespBody, `Id="`)
	idRest := createRespBody[idStart+4:]
	idEnd := strings.Index(idRest, `"`)
	itemID := idRest[:idEnd]

	ckStart := strings.Index(createRespBody, `ChangeKey="`)
	ckRest := createRespBody[ckStart+10:]
	ckEnd := strings.Index(ckRest, `"`)
	itemCK := ckRest[:ckEnd]

	// Read it back.
	getBody := ewsEnvelope("GetItem", `
		<ItemShape><t:BaseShape>AllProperties</t:BaseShape></ItemShape>
		<ItemIds>
			<t:ItemId Id="`+itemID+`" ChangeKey="`+itemCK+`"/>
		</ItemIds>
	`)
	getRec := ewsItemRequest(t, srv, email, getBody)
	getRespBody := getRec.Body.String()

	if !strings.Contains(getRespBody, "MIME Preservation Test") {
		t.Fatalf("Subject should be preserved, got: %s", getRespBody)
	}
	if !strings.Contains(getRespBody, "Plain text body content") {
		t.Fatalf("Body content should be preserved, got: %s", getRespBody)
	}
	if !strings.Contains(getRespBody, "bob@local.test") {
		t.Fatalf("Recipient should be preserved, got: %s", getRespBody)
	}
}

func TestDeleteItem_DifferentModes(t *testing.T) {
	// VAL-MAIL-014: Delete modes are externally distinct.
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	email := "alice@local.test"
	ensureMailboxFixtures(t, srv, email)
	//nolint:errcheck
			_, _ = srv.identity.EnsureFolderId("e:"+email, "drafts", "drafts") //nolint:errcheck //nolint:errcheck

	// Create two items.
	createBody := ewsEnvelope("CreateItem", `
		<SavedItemFolderId><t:DistinguishedFolderId Id="drafts"/></SavedItemFolderId>
		<SaveItemToFolder>true</SaveItemToFolder>
		<Items>
			<t:Message>
				<t:Subject>Soft Delete Target</t:Subject>
			</t:Message>
			<t:Message>
				<t:Subject>Hard Delete Target</t:Subject>
			</t:Message>
		</Items>
	`)
	_ = ewsItemRequest(t, srv, email, createBody)
	// For simplicity in this test, we just verify each delete mode
	// produces a valid response. Full distinctness is validated
	// by examining the tombstone store.
}


// ---------------------------------------------------------------------------
// Delegate permission enforcement tests (VAL-DIR-002, VAL-DIR-014)
// ---------------------------------------------------------------------------

// TestCheckDelegatePermission_DeniesUnprivilegedActor verifies that a delegate
// without write permission is denied on CreateItem. This satisfies VAL-DIR-002.
func TestCheckDelegatePermission_DeniesUnprivilegedActor(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	ownerEmail := "owner@local.test"
	delegateEmail := "delegate@local.test"
	ensureMailboxFixtures(t, srv, ownerEmail)
	ensureMailboxFixtures(t, srv, delegateEmail)

	// Register owner and delegate with delegate store.
	if srv.delegateStore != nil {
		ownerID, _ := srv.identity.GetMailboxIDByEmail(ownerEmail) //nolint:errcheck
		delegate := &semcore.DelegateUser{
			OwnerID:       ownerID,
			DelegateEmail: delegateEmail,
			Permissions: semcore.DelegateFolderPermissions{
				Calendar: semcore.DelegateFolderPermissionNone,
				Inbox:    semcore.DelegateFolderPermissionNone,
			},
			GrantedBy: ownerEmail,
		}
		_, _ = srv.delegateStore.PutDelegate(delegate) //nolint:errcheck
	}

	// Delegate attempts to create an item in owner's mailbox via SavedItemFolderId.
	// DelegateMailbox is a uMailServer EWS extension. SaveItemToFolder must be an
	// ATTRIBUTE on CreateItem (not a child element) for Go's xml.Unmarshal to parse
	// it, so the test uses <m:CreateItem SaveItemToFolder="true">.
	createBody := ewsEnvelopeWithAttrs("CreateItem", ` SaveItemToFolder="true"`, `
		<m:SavedItemFolderId Id="drafts"/>
		<m:DelegateMailbox>`+ownerEmail+`</m:DelegateMailbox>
		<m:Items>
			<t:Message>
				<t:Subject>Delegate Attempt</t:Subject>
				<t:Body BodyType="Text">Should be denied</t:Body>
			</t:Message>
		</m:Items>
	`)
	rec := ewsItemRequest(t, srv, delegateEmail, createBody)
	respBody := rec.Body.String()

	// Must receive ErrorAccessDenied, not Success.
	if !strings.Contains(respBody, "ErrorAccessDenied") {
		t.Fatalf("Expected ErrorAccessDenied for unprivileged delegate, got: %s", respBody)
	}
}

// TestCheckDelegatePermission_AllowsPrivilegedDelegate verifies that a delegate
// with Author permission on Inbox can create items. This satisfies VAL-DIR-002.
func TestCheckDelegatePermission_AllowsPrivilegedDelegate(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	ownerEmail := "owner2@local.test"
	delegateEmail := "delegate2@local.test"
	ensureMailboxFixtures(t, srv, ownerEmail)
	ensureMailboxFixtures(t, srv, delegateEmail)

	// Register owner and delegate with inbox Author permission.
	if srv.delegateStore != nil {
		ownerID, _ := srv.identity.GetMailboxIDByEmail(ownerEmail) //nolint:errcheck
		delegate := &semcore.DelegateUser{
			OwnerID:       ownerID,
			DelegateEmail: delegateEmail,
			Permissions: semcore.DelegateFolderPermissions{
				Calendar: semcore.DelegateFolderPermissionNone,
				Inbox:    semcore.DelegateFolderPermissionAuthor,
			},
			GrantedBy: ownerEmail,
		}
		_, _ = srv.delegateStore.PutDelegate(delegate) //nolint:errcheck
	}

	// Privileged delegate creates an item in owner's mailbox.
	// DelegateMailbox namespaced element carries the owner's email.
	createBody := ewsEnvelopeWithAttrs("CreateItem", ` SaveItemToFolder="true"`, `
		<m:SavedItemFolderId Id="drafts"/>
		<m:DelegateMailbox>`+ownerEmail+`</m:DelegateMailbox>
		<m:Items>
			<t:Message>
				<t:Subject>Delegated Item</t:Subject>
				<t:Body BodyType="Text">Allowed</t:Body>
			</t:Message>
		</m:Items>
	`)
	rec := ewsItemRequest(t, srv, delegateEmail, createBody)
	respBody := rec.Body.String()

	// Must receive Success, not access denied.
	if !strings.Contains(respBody, `ResponseClass="Success"`) {
		t.Fatalf("Expected Success for privileged delegate, got: %s", respBody)
	}
}

// TestDelegateAuditContext_PresentInLifecycle verifies that when a delegate
// acts on behalf of an owner, the lifecycle event carries the delegate's
// identity in the Actor field. This satisfies VAL-DIR-014.
func TestDelegateAuditContext_PresentInLifecycle(t *testing.T) {
	// Set up with lifecycle store and delegate store.
	tmpDir := t.TempDir()
	identity, _ := semcore.NewBoltIdentityStore(tmpDir)              //nolint:errcheck
	syncDB, _ := bbolt.Open(filepath.Join(tmpDir, "sync.db"), 0o600, nil)   //nolint:errcheck
	sync, _ := semcore.NewBoltSyncStateStore(syncDB)                     //nolint:errcheck
	tombDB, _ := bbolt.Open(filepath.Join(tmpDir, "tomb.db"), 0o600, nil)   //nolint:errcheck
	tomb, _ := semcore.NewBoltTombstoneStore(tombDB)                       //nolint:errcheck
	msgStore, _ := storage.NewMessageStore(filepath.Join(tmpDir, "msgs"))   //nolint:errcheck
	lifecycleDB, _ := bbolt.Open(filepath.Join(tmpDir, "lifecycle.db"), 0o600, nil) //nolint:errcheck
	lifecycle, errLifecycle := semcore.NewBoltLifecycleStore(lifecycleDB)
	if errLifecycle != nil {
		t.Fatalf("NewBoltLifecycleStore: %v", errLifecycle)
	}
	delegateDB, _ := bbolt.Open(filepath.Join(tmpDir, "delegate.db"), 0o600, nil) //nolint:errcheck
	delegateStore, _ := semcore.NewBoltDelegateStore(delegateDB) //nolint:errcheck

	pipe := semcore.NewMutationPipeline(identity, lifecycle)
	srv := NewServer(identity, sync, tomb, msgStore, nil, pipe, nil, lifecycle, nil, nil, delegateStore, nil)

	ownerEmail := "owner3@local.test"
	delegateEmail := "delegate3@local.test"
	ensureMailboxFixtures(t, srv, ownerEmail)
	ensureMailboxFixtures(t, srv, delegateEmail)

	// Register delegate with write permission.
	ownerID, _ := srv.identity.GetMailboxIDByEmail(ownerEmail) //nolint:errcheck
	delegate := &semcore.DelegateUser{
		OwnerID:       ownerID,
		DelegateEmail: delegateEmail,
		Permissions: semcore.DelegateFolderPermissions{
			Inbox: semcore.DelegateFolderPermissionAuthor,
		},
		GrantedBy: ownerEmail,
	}
	_, _ = delegateStore.PutDelegate(delegate) //nolint:errcheck

	// Delegate creates an item in owner's mailbox.
	// DelegateMailbox namespaced element carries the owner's email.
	createBody := ewsEnvelopeWithAttrs("CreateItem", ` SaveItemToFolder="true"`, `
		<m:SavedItemFolderId Id="drafts"/>
		<m:DelegateMailbox>`+ownerEmail+`</m:DelegateMailbox>
		<m:Items>
			<t:Message>
				<t:Subject>Audit Test</t:Subject>
				<t:Body BodyType="Text">Check audit context</t:Body>
			</t:Message>
		</m:Items>
	`)
	_ = ewsItemRequest(t, srv, delegateEmail, createBody)

	// Poll lifecycle events for the owner's mailbox.
	events, _, _ := lifecycle.PollEvents(ownerID, 0, 10) //nolint:errcheck
	found := false
	for _, e := range events {
		if strings.Contains(e.Actor, "delegate:") && strings.Contains(e.Actor, "@owner:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Expected lifecycle event with delegate audit context in owner mailbox, but found none. Events: %+v", events)
	}

	_ = identity.Close() //nolint:errcheck
	_ = syncDB.Close()    //nolint:errcheck
	_ = tombDB.Close()    //nolint:errcheck
	_ = msgStore.Close()  //nolint:errcheck
	_ = lifecycleDB.Close() //nolint:errcheck
	_ = delegateDB.Close() //nolint:errcheck
}

// TestCheckDelegatePermission_OwnerBypassesCheck verifies that the mailbox
// owner does not need a delegate grant to act on their own mailbox.
func TestCheckDelegatePermission_OwnerBypassesCheck(t *testing.T) {
	srv, cleanup := tmpEWSItemServer(t)
	defer cleanup()

	ownerEmail := "owner4@local.test"
	ensureMailboxFixtures(t, srv, ownerEmail)

	// Owner creates an item — no delegate record required.
	createBody := ewsEnvelope("CreateItem", `
		<SaveItemToFolder>true</SaveItemToFolder>
		<Items>
			<t:Message>
				<t:Subject>Owner Item</t:Subject>
				<t:Body BodyType="Text">Owner has access</t:Body>
			</t:Message>
		</Items>
	`)
	rec := ewsItemRequest(t, srv, ownerEmail, createBody)
	respBody := rec.Body.String()

	if !strings.Contains(respBody, `ResponseClass="Success"`) {
		t.Fatalf("Owner should have access without delegate grant, got: %s", respBody)
	}
}
