package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// reqAsUser builds a request carrying the authenticated-user context value the
// handlers read (reusing the shared withUser context helper).
func reqAsUser(method, target, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	return r.WithContext(withUser(r.Context(), "admin@test.com"))
}

func TestHandleSignature_RoundTrip(t *testing.T) {
	server, database, _ := helperSetupAccount(t)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	// GET before any save returns an empty signature.
	rec := httptest.NewRecorder()
	server.handleSignature(rec, reqAsUser(http.MethodGet, "/api/v1/signature", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET empty: expected 200, got %d", rec.Code)
	}
	var got signaturePref
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if got.Signature != "" {
		t.Errorf("expected empty signature, got %q", got.Signature)
	}

	// PUT a signature.
	const sig = "Best regards,\nAlice"
	body := `{"signature":"Best regards,\nAlice"}`
	rec = httptest.NewRecorder()
	server.handleSignature(rec, reqAsUser(http.MethodPut, "/api/v1/signature", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET returns the saved value.
	rec = httptest.NewRecorder()
	server.handleSignature(rec, reqAsUser(http.MethodGet, "/api/v1/signature", ""))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET after PUT: %v", err)
	}
	if got.Signature != sig {
		t.Errorf("expected %q, got %q", sig, got.Signature)
	}
}

// TestHandleSignature_IsolatedFromPreferences verifies the signature key does not
// clobber the boolean UI preferences stored under the bare user key.
func TestHandleSignature_IsolatedFromPreferences(t *testing.T) {
	server, database, _ := helperSetupAccount(t)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	// Save boolean preferences first.
	rec := httptest.NewRecorder()
	server.handlePreferences(rec, reqAsUser(http.MethodPut, "/api/v1/preferences", `{"darkMode":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT preferences: expected 200, got %d", rec.Code)
	}

	// Save a signature.
	rec = httptest.NewRecorder()
	server.handleSignature(rec, reqAsUser(http.MethodPut, "/api/v1/signature", `{"signature":"sig"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT signature: expected 200, got %d", rec.Code)
	}

	// Preferences must still decode as the bool map (no type collision).
	rec = httptest.NewRecorder()
	server.handlePreferences(rec, reqAsUser(http.MethodGet, "/api/v1/preferences", ""))
	var resp struct {
		Preferences map[string]bool `json:"preferences"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode preferences: %v", err)
	}
	if !resp.Preferences["darkMode"] {
		t.Errorf("expected darkMode preference preserved, got %+v", resp.Preferences)
	}
}

func TestHandleSignature_TooLong(t *testing.T) {
	server, database, _ := helperSetupAccount(t)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	body, err := json.Marshal(signaturePref{Signature: strings.Repeat("x", maxSignatureLength+1)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	server.handleSignature(rec, reqAsUser(http.MethodPut, "/api/v1/signature", string(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for over-long signature, got %d", rec.Code)
	}
}
