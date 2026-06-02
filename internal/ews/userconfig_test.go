package ews

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/db"
)

// TestUserConfigurationRoundTrip verifies Create/Get/Update/Delete of an EWS
// UserConfiguration object persists and round-trips per mailbox.
func TestUserConfigurationRoundTrip(t *testing.T) {
	identity, sync, tomb, msgStore, policyStore, collabStore, cleanup := tmpDirectoryStores(t)
	t.Cleanup(cleanup)

	database, err := db.Open(t.TempDir() + "/uc.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})

	srv := NewServer(identity, sync, tomb, msgStore, nil, database, nil, nil, nil, collabStore, policyStore, nil, nil, nil)

	email := "alice@ex.test"
	post := func(inner string) string {
		body := `<?xml version="1.0" encoding="utf-8"?>` +
			`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
			`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" ` +
			`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">` +
			`<soap:Body>` + inner + `</soap:Body></soap:Envelope>`
		req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		//nolint:staticcheck // EWS resolveMailboxFromBody reads the plain string context key "X-Email".
		req = req.WithContext(context.WithValue(req.Context(), "X-Email", email))
		rec := httptest.NewRecorder()
		srv.HandleHTTP(rec, req)
		return rec.Body.String()
	}

	name := `<t:UserConfigurationName Name="OWA.UserOptions"><t:DistinguishedFolderId Id="root"/></t:UserConfigurationName>`
	getName := `<m:UserConfigurationName Name="OWA.UserOptions"><t:DistinguishedFolderId Id="root"/></m:UserConfigurationName>`

	// Create.
	out := post(`<m:CreateUserConfiguration><m:UserConfiguration>` + name +
		`<t:XmlData>Zmlyc3Q=</t:XmlData></m:UserConfiguration></m:CreateUserConfiguration>`)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("Create: expected Success, got:\n%s", out)
	}

	// Get → returns stored XmlData.
	out = post(`<m:GetUserConfiguration>` + getName +
		`<m:UserConfigurationProperties>All</m:UserConfigurationProperties></m:GetUserConfiguration>`)
	if !strings.Contains(out, `ResponseClass="Success"`) || !strings.Contains(out, "<t:XmlData>Zmlyc3Q=</t:XmlData>") {
		t.Fatalf("Get after create: expected stored XmlData, got:\n%s", out)
	}

	// Update → new XmlData.
	out = post(`<m:UpdateUserConfiguration><m:UserConfiguration>` + name +
		`<t:XmlData>c2Vjb25k</t:XmlData></m:UserConfiguration></m:UpdateUserConfiguration>`)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("Update: expected Success, got:\n%s", out)
	}
	out = post(`<m:GetUserConfiguration>` + getName + `</m:GetUserConfiguration>`)
	if !strings.Contains(out, "<t:XmlData>c2Vjb25k</t:XmlData>") {
		t.Fatalf("Get after update: expected updated XmlData, got:\n%s", out)
	}

	// Delete → subsequent Get is ItemNotFound.
	out = post(`<m:DeleteUserConfiguration>` + getName + `</m:DeleteUserConfiguration>`)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Fatalf("Delete: expected Success, got:\n%s", out)
	}
	out = post(`<m:GetUserConfiguration>` + getName + `</m:GetUserConfiguration>`)
	if !strings.Contains(out, "ErrorItemNotFound") {
		t.Errorf("Get after delete: expected ErrorItemNotFound, got:\n%s", out)
	}
}
