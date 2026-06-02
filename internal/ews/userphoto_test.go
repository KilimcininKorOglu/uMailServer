package ews

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/db"
)

func TestGetUserPhoto(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 1, 2, 3}
	if err := database.CreateAccount(&db.AccountData{Email: "alice@ex.test", LocalPart: "alice", Domain: "ex.test", IsActive: true, Avatar: png, AvatarType: "image/png"}); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if err := database.CreateAccount(&db.AccountData{Email: "bob@ex.test", LocalPart: "bob", Domain: "ex.test", IsActive: true}); err != nil {
		t.Fatalf("create bob: %v", err)
	}

	srv := NewServer(nil, nil, nil, nil, nil, database, nil, nil, nil, nil, nil, nil, nil, nil)

	call := func(inner string) string {
		body := `<?xml version="1.0" encoding="utf-8"?>` +
			`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
			`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages" ` +
			`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">` +
			`<soap:Body>` + inner + `</soap:Body></soap:Envelope>`
		req := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		rec := httptest.NewRecorder()
		srv.HandleHTTP(rec, req)
		return rec.Body.String()
	}

	// Avatar present → Success with base64 PictureData.
	got := call(`<m:GetUserPhoto><m:Email>alice@ex.test</m:Email></m:GetUserPhoto>`)
	if !strings.Contains(got, `ResponseClass="Success"`) || !strings.Contains(got, "<m:PictureData>") {
		t.Errorf("alice photo: expected success with PictureData, got:\n%s", got)
	}
	if want := base64.StdEncoding.EncodeToString(png); !strings.Contains(got, want) {
		t.Errorf("alice photo: expected base64 %q in response:\n%s", want, got)
	}
	if !strings.Contains(got, "<m:HasChanged>true</m:HasChanged>") {
		t.Errorf("alice photo: expected HasChanged true, got:\n%s", got)
	}

	// No avatar → ItemNotFound error.
	got = call(`<m:GetUserPhoto><m:Email>bob@ex.test</m:Email></m:GetUserPhoto>`)
	if !strings.Contains(got, "ErrorItemNotFound") {
		t.Errorf("bob photo: expected ErrorItemNotFound, got:\n%s", got)
	}

	// Missing email → error (InvalidOperation).
	got = call(`<m:GetUserPhoto></m:GetUserPhoto>`)
	if !strings.Contains(got, `ResponseClass="Error"`) {
		t.Errorf("empty email: expected an error response, got:\n%s", got)
	}
}
