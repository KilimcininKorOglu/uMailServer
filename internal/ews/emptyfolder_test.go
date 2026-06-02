package ews

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestEmptyFolder verifies the EmptyFolder operation deletes every item in the
// target folder (resolved by DistinguishedFolderId) and reports Success.
func TestEmptyFolder(t *testing.T) {
	identity, sync, tomb, msgStore, policyStore, collabStore, cleanup := tmpDirectoryStores(t)
	t.Cleanup(cleanup)

	srv := NewServer(identity, sync, tomb, msgStore, nil, nil, nil, nil, nil, collabStore, policyStore, nil, nil, nil)

	email := "alice@ex.test"
	if _, err := identity.EnsureMailboxId(email); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	folderID, err := identity.EnsureFolderId(email, "calendar", "calendar")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}
	mailboxID, err := semcore.NewMailboxId(email)
	if err != nil {
		t.Fatalf("NewMailboxId: %v", err)
	}

	// Seed two calendar items. deleteItemFromCollab deletes by RawHash, so the
	// store key must equal RawHash.
	for i := 0; i < 2; i++ {
		hash := fmt.Sprintf("hash-%d", i)
		ck, ckErr := semcore.NewCalendarChangeKey(fmt.Sprintf("ck-%d", i))
		if ckErr != nil {
			t.Fatalf("NewCalendarChangeKey: %v", ckErr)
		}
		rec := &semcore.StoredCalendarItemIdentity{
			ID:        semcore.MustCalendarItemId(fmt.Sprintf("cal-%d", i)),
			FolderID:  folderID,
			MailboxID: mailboxID,
			ChangeKey: ck,
			Kind:      semcore.CollabKindEvent,
			RawHash:   hash,
			RawData:   "BEGIN:VEVENT\r\nSUMMARY:Event\r\nDTSTART:20260603T090000Z\r\nEND:VEVENT",
		}
		if err := collabStore.PutCalendarItemIdentityUnsafe(hash, rec); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	before, berr := collabStore.ListCalendarItemsByFolder(folderID)
	if berr != nil {
		t.Fatalf("list before: %v", berr)
	}
	if len(before) != 2 {
		t.Fatalf("setup: expected 2 items, got %d", len(before))
	}

	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" ` +
		`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">` +
		`<soap:Body><m:EmptyFolder DeleteType="HardDelete" DeleteSubFolders="false">` +
		`<m:FolderIds><t:DistinguishedFolderId Id="calendar"/></m:FolderIds>` +
		`</m:EmptyFolder></soap:Body></soap:Envelope>`

	req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	//nolint:staticcheck // EWS resolveMailboxFromBody reads the plain string context key "X-Email".
	req = req.WithContext(context.WithValue(req.Context(), "X-Email", email))
	out := httptest.NewRecorder()
	srv.HandleHTTP(out, req)

	got := out.Body.String()
	if !strings.Contains(got, `ResponseClass="Success"`) {
		t.Fatalf("EmptyFolder: expected Success, got:\n%s", got)
	}
	after, aerr := collabStore.ListCalendarItemsByFolder(folderID)
	if aerr != nil {
		t.Fatalf("list after: %v", aerr)
	}
	if len(after) != 0 {
		t.Errorf("EmptyFolder: expected 0 items after, got %d", len(after))
	}
}
