package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/sieve"
	"github.com/umailserver/umailserver/internal/vacation"
)

// Test handleVacation dispatcher
func TestHandleVacation_Get(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("GET", "/api/v1/vacation", nil)
	req = req.WithContext(withUser(req.Context(), "user@example.com"))
	w := httptest.NewRecorder()

	server.handleVacation(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	var result VacationConfig
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result.Subject != "Out of Office" {
		t.Errorf("Expected default subject, got %s", result.Subject)
	}
}

func TestHandleVacation_Put(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	startDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	endDate := time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339)

	body := VacationConfig{
		Enabled:          true,
		StartDate:        &startDate,
		EndDate:          &endDate,
		Subject:          "Vacation",
		Message:          "I'm on vacation",
		SendInterval:     24,
		ExcludeAddresses: []string{"boss@example.com"},
		IgnoreLists:      true,
		IgnoreBulk:       true,
	}
	bodyJSON, _ := json.Marshal(body)

	req := httptest.NewRequest("PUT", "/api/v1/vacation", bytes.NewReader(bodyJSON))
	req = req.WithContext(withUser(req.Context(), "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleVacation(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandleVacation_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("DELETE", "/api/v1/vacation", nil)
	req = req.WithContext(withUser(req.Context(), "user@example.com"))
	w := httptest.NewRecorder()

	server.handleVacation(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if result["status"] != "deleted" {
		t.Errorf("Expected status 'deleted', got %s", result["status"])
	}
}

func TestHandleVacation_MethodNotAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("PATCH", "/api/v1/vacation", nil)
	req = req.WithContext(withUser(req.Context(), "user@example.com"))
	w := httptest.NewRecorder()

	server.handleVacation(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// Test handleGetVacation
func TestHandleGetVacation_Unauthorized(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("GET", "/api/v1/vacation", nil)
	// No user context
	w := httptest.NewRecorder()

	server.handleGetVacation(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleGetVacation_WithDates(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("GET", "/api/v1/vacation", nil)
	req = req.WithContext(withUser(req.Context(), "user@example.com"))
	w := httptest.NewRecorder()

	server.handleGetVacation(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	var result VacationConfig
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Stub implementation returns default values with empty dates
	if result.Subject != "Out of Office" {
		t.Errorf("Expected default subject 'Out of Office', got %s", result.Subject)
	}

	// Dates should be nil/zero for default config
	if result.StartDate != nil {
		t.Error("Expected start_date to be nil for default config")
	}
}

// Test handleSetVacation
func TestHandleSetVacation_InvalidBody(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("PUT", "/api/v1/vacation", bytes.NewReader([]byte("invalid json")))
	req = req.WithContext(withUser(req.Context(), "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleSetVacation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleSetVacation_MissingSubject(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	body := VacationConfig{
		Enabled: true,
		Subject: "",
		Message: "Test message",
	}
	bodyJSON, _ := json.Marshal(body)

	req := httptest.NewRequest("PUT", "/api/v1/vacation", bytes.NewReader(bodyJSON))
	req = req.WithContext(withUser(req.Context(), "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleSetVacation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleSetVacation_MissingMessage(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	body := VacationConfig{
		Enabled: true,
		Subject: "Test Subject",
		Message: "",
	}
	bodyJSON, _ := json.Marshal(body)

	req := httptest.NewRequest("PUT", "/api/v1/vacation", bytes.NewReader(bodyJSON))
	req = req.WithContext(withUser(req.Context(), "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleSetVacation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleSetVacation_Unauthorized(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	body := VacationConfig{
		Enabled: true,
		Subject: "Test",
		Message: "Test message",
	}
	bodyJSON, _ := json.Marshal(body)

	req := httptest.NewRequest("PUT", "/api/v1/vacation", bytes.NewReader(bodyJSON))
	// No user context
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleSetVacation(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// Test handleDeleteVacation
func TestHandleDeleteVacation_Unauthorized(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("DELETE", "/api/v1/vacation", nil)
	// No user context
	w := httptest.NewRecorder()

	server.handleDeleteVacation(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// Test handleAdminVacations
func TestHandleAdminVacations_Success(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("GET", "/api/v1/admin/vacations", nil)
	req = req.WithContext(withUser(req.Context(), "admin@example.com"))
	req = req.WithContext(withIsAdmin(req.Context(), true))
	w := httptest.NewRecorder()

	server.handleAdminVacations(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if _, ok := result["active_vacations"]; !ok {
		t.Error("Expected active_vacations in response")
	}
}

func TestHandleAdminVacations_MethodNotAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("POST", "/api/v1/admin/vacations", nil)
	req = req.WithContext(withUser(req.Context(), "admin@example.com"))
	req = req.WithContext(withIsAdmin(req.Context(), true))
	w := httptest.NewRecorder()

	server.handleAdminVacations(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleAdminVacations_NotAdmin(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	req := httptest.NewRequest("GET", "/api/v1/admin/vacations", nil)
	req = req.WithContext(withUser(req.Context(), "user@example.com"))
	req = req.WithContext(withIsAdmin(req.Context(), false))
	w := httptest.NewRecorder()

	server.handleAdminVacations(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// Test vacation helper functions
func TestGetVacationConfig(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	config, err := server.getVacationConfig("user@example.com")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if config.Subject != "Out of Office" {
		t.Errorf("Expected default subject 'Out of Office', got %s", config.Subject)
	}

	if config.SendInterval != 7*24*time.Hour {
		t.Errorf("Expected default interval 7 days, got %v", config.SendInterval)
	}
}

func TestSetVacationConfig(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	config := &vacation.Config{
		Enabled:      true,
		Subject:      "Custom Subject",
		Message:      "Custom Message",
		SendInterval: 48 * time.Hour,
	}

	err := server.setVacationConfig("user@example.com", config)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
}

func TestDeleteVacationConfig(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	err := server.deleteVacationConfig("user@example.com")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
}

func TestListActiveVacations(t *testing.T) {
	tmpDir := t.TempDir()
	server := NewTestServer(t, tmpDir)

	vacations := server.listActiveVacations()
	if vacations == nil {
		t.Error("Expected empty slice, got nil")
	}

	if len(vacations) != 0 {
		t.Errorf("Expected 0 vacations, got %d", len(vacations))
	}
}

// TestVacationConfig_PersistsToCanonicalOOF verifies that when the semcore
// policy store is wired (production), the webmail vacation endpoints write to
// the canonical OOF policy and recompile the Sieve script — rather than the
// legacy db.BucketVacation store, which is never read at delivery. This is the
// behavior that makes a webmail-configured vacation auto-reply actually fire.
func TestVacationConfig_PersistsToCanonicalOOF(t *testing.T) {
	tmpDir := t.TempDir()
	server := newFilterTestServer(t, tmpDir) // wires a real semcore store
	server.SetSieveManager(sieve.NewManager())

	user := "vac@example.com"
	cfg := &vacation.Config{
		Enabled:      true,
		Subject:      "Tatildeyim",
		Message:      "Döndüğümde yanıt vereceğim.",
		SendInterval: 24 * time.Hour,
	}
	if err := server.setVacationConfig(user, cfg); err != nil {
		t.Fatalf("setVacationConfig: %v", err)
	}

	mbid, err := semcore.NewMailboxId(user)
	if err != nil {
		t.Fatalf("NewMailboxId: %v", err)
	}
	oofID, err := semcore.NewOOFId(mbid.String())
	if err != nil {
		t.Fatalf("NewOOFId: %v", err)
	}

	// Canonical OOF must hold the policy as the single source of truth.
	policy, err := server.semStore.Policy().GetOOF(oofID)
	if err != nil {
		t.Fatalf("GetOOF: %v", err)
	}
	if !policy.Enabled || policy.State != "Enabled" {
		t.Errorf("policy not enabled: enabled=%v state=%q", policy.Enabled, policy.State)
	}
	if policy.Subject != cfg.Subject || policy.TextBody != cfg.Message {
		t.Errorf("policy content mismatch: subject=%q body=%q", policy.Subject, policy.TextBody)
	}
	if !policy.IsActiveNow() {
		t.Error("an enabled (non-scheduled) policy must be active now")
	}

	// Reading back through getVacationConfig must reflect the OOF policy.
	got, err := server.getVacationConfig(user)
	if err != nil {
		t.Fatalf("getVacationConfig: %v", err)
	}
	if !got.Enabled || got.Subject != cfg.Subject || got.Message != cfg.Message {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Delete must disable the policy so recompile drops the vacation action.
	if err := server.deleteVacationConfig(user); err != nil {
		t.Fatalf("deleteVacationConfig: %v", err)
	}
	policy, err = server.semStore.Policy().GetOOF(oofID)
	if err != nil {
		t.Fatalf("GetOOF after delete: %v", err)
	}
	if policy.Enabled {
		t.Error("policy must be disabled after deleteVacationConfig")
	}
}

// TestParseLegacyVacationSettings verifies the admin-set legacy vacation JSON
// parses into a vacation.Config for bridging onto the canonical OOF policy.
func TestParseLegacyVacationSettings(t *testing.T) {
	raw := `{"enabled":true,"subject":"OOO","message":"Away","start_date":"2026-06-10","end_date":"2026-06-20","send_interval":48}`
	cfg, err := parseLegacyVacationSettings(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.Enabled || cfg.Subject != "OOO" || cfg.Message != "Away" {
		t.Errorf("fields mismatch: %+v", cfg)
	}
	if cfg.StartDate.IsZero() || cfg.EndDate.IsZero() {
		t.Errorf("dates not parsed: start=%v end=%v", cfg.StartDate, cfg.EndDate)
	}
	if cfg.SendInterval != 48*time.Hour {
		t.Errorf("send interval = %v, want 48h", cfg.SendInterval)
	}
	if _, err := parseLegacyVacationSettings("not json"); err == nil {
		t.Error("expected error on invalid JSON")
	}
}

// Helper function
