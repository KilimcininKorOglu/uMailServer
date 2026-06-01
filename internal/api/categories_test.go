package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleCategories_RoundTripAndNormalize(t *testing.T) {
	server, database, _ := helperSetupAccount(t)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	// GET before any save returns an empty (non-null) list.
	rec := httptest.NewRecorder()
	server.handleCategories(rec, reqAsUser(http.MethodGet, "/api/v1/categories", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET empty: expected 200, got %d", rec.Code)
	}
	var got categoriesPref
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Categories == nil || len(got.Categories) != 0 {
		t.Fatalf("expected empty list, got %+v", got.Categories)
	}

	// PUT with a blank name and a duplicate (case-insensitive) — both dropped.
	body := `{"categories":[{"name":"Work","color":"#ef4444"},{"name":"  ","color":"#000"},{"name":"work","color":"#111"},{"name":"Personal","color":"#22c55e"}]}`
	rec = httptest.NewRecorder()
	server.handleCategories(rec, reqAsUser(http.MethodPut, "/api/v1/categories", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.handleCategories(rec, reqAsUser(http.MethodGet, "/api/v1/categories", ""))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Categories) != 2 {
		t.Fatalf("expected blank + duplicate dropped → 2 categories, got %+v", got.Categories)
	}
	if got.Categories[0].Name != "Work" || got.Categories[0].Color != "#ef4444" {
		t.Errorf("first category wrong: %+v", got.Categories[0])
	}
	if got.Categories[1].Name != "Personal" {
		t.Errorf("second category wrong: %+v", got.Categories[1])
	}
}

func TestHandleCategories_IsolatedFromSignature(t *testing.T) {
	server, database, _ := helperSetupAccount(t)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	rec := httptest.NewRecorder()
	server.handleSignature(rec, reqAsUser(http.MethodPut, "/api/v1/signature", `{"signature":"sig"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("signature PUT: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	server.handleCategories(rec, reqAsUser(http.MethodPut, "/api/v1/categories", `{"categories":[{"name":"Work","color":"#ef4444"}]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("categories PUT: %d", rec.Code)
	}
	// Signature must be unchanged by the categories write.
	rec = httptest.NewRecorder()
	server.handleSignature(rec, reqAsUser(http.MethodGet, "/api/v1/signature", ""))
	var sig signaturePref
	if err := json.Unmarshal(rec.Body.Bytes(), &sig); err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if sig.Signature != "sig" {
		t.Errorf("categories write clobbered the signature: %q", sig.Signature)
	}
}
