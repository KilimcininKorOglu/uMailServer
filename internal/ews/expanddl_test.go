package ews

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/db"
)

// TestExpandDL verifies the EWS ExpandDL operation expands a mail group to its
// member mailboxes and returns NameResolutionNoResults for a non-group address.
func TestExpandDL(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/dl.db")
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
	if err := database.CreateMailGroup(&db.MailGroup{
		Email: "team@ex.test", LocalPart: "team", Domain: "ex.test", IsActive: true,
		Members: []string{"alice@ex.test", "bob@ex.test"},
	}); err != nil {
		t.Fatalf("create mail group: %v", err)
	}

	srv := NewServer(nil, nil, nil, nil, nil, database, nil, nil, nil, nil, nil, nil, nil, nil)

	call := func(addr string) string {
		body := `<?xml version="1.0" encoding="utf-8"?>` +
			`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
			`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" ` +
			`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">` +
			`<soap:Body><m:ExpandDL><m:Mailbox><t:EmailAddress>` + addr +
			`</t:EmailAddress></m:Mailbox></m:ExpandDL></soap:Body></soap:Envelope>`
		req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		rec := httptest.NewRecorder()
		srv.HandleHTTP(rec, req)
		return rec.Body.String()
	}

	got := call("team@ex.test")
	if !strings.Contains(got, `ResponseClass="Success"`) {
		t.Fatalf("ExpandDL: expected Success, got:\n%s", got)
	}
	for _, want := range []string{`TotalItemsInView="2"`, "alice@ex.test", "bob@ex.test", "<t:MailboxType>Mailbox</t:MailboxType>"} {
		if !strings.Contains(got, want) {
			t.Errorf("ExpandDL: expected %q in response:\n%s", want, got)
		}
	}

	got = call("nobody@ex.test")
	if !strings.Contains(got, "ErrorNameResolutionNoResults") {
		t.Errorf("ExpandDL non-group: expected ErrorNameResolutionNoResults, got:\n%s", got)
	}
}
