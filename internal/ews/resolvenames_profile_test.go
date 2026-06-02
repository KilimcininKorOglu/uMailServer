package ews

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/semcore"
)

// TestResolveNamesFullContactData verifies that ResolveNames returns the
// account's profile fields (job title, department, phone) inside a Contact when
// the client requests full contact data — so Outlook can render a rich card.
func TestResolveNamesFullContactData(t *testing.T) {
	identity, sync, tomb, msgStore, policyStore, collabStore, cleanup := tmpDirectoryStores(t)
	t.Cleanup(cleanup)

	database, err := db.Open(t.TempDir() + "/dir.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})
	if err := database.CreateDomain(&db.DomainData{Name: "ex.test", IsActive: true}); err != nil {
		t.Fatalf("create domain: %v", err)
	}
	if err := database.CreateAccount(&db.AccountData{
		Email: "jane@ex.test", LocalPart: "jane", Domain: "ex.test", IsActive: true,
		DisplayName: "Jane Doe", Title: "Staff Engineer", Department: "Platform", Phone: "+1 555 0100",
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}

	pipe := semcore.NewMutationPipeline(identity, nil)
	srv := NewServer(identity, sync, tomb, msgStore, nil, database, pipe, nil, nil, collabStore, policyStore, nil, nil, nil)

	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" ` +
		`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">` +
		`<soap:Body><m:ResolveNames ReturnFullContactData="true">` +
		`<m:UnresolvedEntry>jane</m:UnresolvedEntry>` +
		`</m:ResolveNames></soap:Body></soap:Envelope>`
	req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	rec := httptest.NewRecorder()
	srv.HandleHTTP(rec, req)

	got := rec.Body.String()
	if !strings.Contains(got, `ResponseClass="Success"`) {
		t.Fatalf("ResolveNames: expected success, got:\n%s", got)
	}
	for _, want := range []string{"jane@ex.test", "Jane Doe", "<Contact ", "<JobTitle", "Staff Engineer", "<Department", "Platform", "BusinessPhone", "+1 555 0100"} {
		if !strings.Contains(got, want) {
			t.Errorf("ResolveNames full contact data: expected %q in response:\n%s", want, got)
		}
	}
}
