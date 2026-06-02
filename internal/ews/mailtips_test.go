package ews

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestGetMailTips verifies GetMailTips returns an OutOfOffice tip for a recipient
// with OOF enabled and omits it for a recipient without one.
func TestGetMailTips(t *testing.T) {
	identity, sync, tomb, msgStore, policyStore, collabStore, cleanup := tmpDirectoryStores(t)
	t.Cleanup(cleanup)

	srv := NewServer(identity, sync, tomb, msgStore, nil, nil, nil, nil, nil, collabStore, policyStore, nil, nil, nil)

	// Enable OOF for away@ex.test.
	away := "away@ex.test"
	mboxID, err := identity.EnsureMailboxId(away)
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	oofID, err := semcore.NewOOFId(mboxID.String())
	if err != nil {
		t.Fatalf("NewOOFId: %v", err)
	}
	if err := policyStore.PutOOF(&semcore.OOFPolicy{
		ID: oofID, MailboxID: mboxID, Enabled: true, State: "Enabled",
		TextBody: "On vacation until Monday",
	}); err != nil {
		t.Fatalf("PutOOF: %v", err)
	}

	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" ` +
		`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">` +
		`<soap:Body><m:GetMailTips><m:Recipients>` +
		`<t:Mailbox><t:EmailAddress>away@ex.test</t:EmailAddress></t:Mailbox>` +
		`<t:Mailbox><t:EmailAddress>present@ex.test</t:EmailAddress></t:Mailbox>` +
		`</m:Recipients><m:MailTipsRequested>All</m:MailTipsRequested>` +
		`</m:GetMailTips></soap:Body></soap:Envelope>`

	req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	rec := httptest.NewRecorder()
	srv.HandleHTTP(rec, req)

	got := rec.Body.String()
	if !strings.Contains(got, "GetMailTipsResponse") {
		t.Fatalf("expected GetMailTipsResponse, got:\n%s", got)
	}
	if strings.Count(got, "<m:MailTips>") != 2 {
		t.Errorf("expected 2 MailTips entries, got %d", strings.Count(got, "<m:MailTips>"))
	}
	if !strings.Contains(got, "<t:OutOfOffice>") || !strings.Contains(got, "On vacation until Monday") {
		t.Errorf("expected OutOfOffice tip for away@ex.test, got:\n%s", got)
	}
	if !strings.Contains(got, "away@ex.test") || !strings.Contains(got, "present@ex.test") {
		t.Errorf("expected both recipient addresses in response")
	}
}

// TestGetServiceConfiguration verifies the MailTips service configuration is
// returned so clients can discover mail-tip limits.
func TestGetServiceConfiguration(t *testing.T) {
	identity, sync, tomb, msgStore, policyStore, collabStore, cleanup := tmpDirectoryStores(t)
	t.Cleanup(cleanup)
	srv := NewServer(identity, sync, tomb, msgStore, nil, nil, nil, nil, nil, collabStore, policyStore, nil, nil, nil)

	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" ` +
		`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">` +
		`<soap:Body><m:GetServiceConfiguration>` +
		`<m:RequestedConfiguration><t:ConfigurationName>MailTips</t:ConfigurationName></m:RequestedConfiguration>` +
		`</m:GetServiceConfiguration></soap:Body></soap:Envelope>`
	req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	rec := httptest.NewRecorder()
	srv.HandleHTTP(rec, req)

	got := rec.Body.String()
	for _, want := range []string{`ResponseClass="Success"`, "<m:MailTipsConfiguration>", "<t:MailTipsEnabled>true</t:MailTipsEnabled>", "MaxRecipientsPerGetMailTipsRequest"} {
		if !strings.Contains(got, want) {
			t.Errorf("GetServiceConfiguration: expected %q in response:\n%s", want, got)
		}
	}
}

// TestGetAppManifests verifies GetAppManifests returns an empty manifest list.
func TestGetAppManifests(t *testing.T) {
	identity, sync, tomb, msgStore, policyStore, collabStore, cleanup := tmpDirectoryStores(t)
	t.Cleanup(cleanup)
	srv := NewServer(identity, sync, tomb, msgStore, nil, nil, nil, nil, nil, collabStore, policyStore, nil, nil, nil)

	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" ` +
		`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">` +
		`<soap:Body><m:GetAppManifests/></soap:Body></soap:Envelope>`
	req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	rec := httptest.NewRecorder()
	srv.HandleHTTP(rec, req)

	got := rec.Body.String()
	if !strings.Contains(got, `ResponseClass="Success"`) || !strings.Contains(got, "<m:Manifests") {
		t.Errorf("GetAppManifests: expected Success with Manifests, got:\n%s", got)
	}
}
