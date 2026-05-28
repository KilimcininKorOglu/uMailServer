package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

func TestExtractDomainFromEmail(t *testing.T) {
	tests := []struct {
		email    string
		expected string
	}{
		{"user@example.com", "example.com"},
		{"test@mail.domain.org", "mail.domain.org"},
		{"invalid", ""},
		{"", ""},
	}

	for _, tt := range tests {
		result := extractDomainFromEmail(tt.email)
		if result != tt.expected {
			t.Errorf("extractDomainFromEmail(%q) = %q, want %q", tt.email, result, tt.expected)
		}
	}
}

func TestExtractEmailFromHost(t *testing.T) {
	tests := []struct {
		host     string
		expected string
	}{
		{"user@example.com", "user@example.com"},
		{"mail.example.com", ""},
		{"example.com:443", ""},
	}

	for _, tt := range tests {
		result := extractEmailFromHost(tt.host)
		if result != tt.expected {
			t.Errorf("extractEmailFromHost(%q) = %q, want %q", tt.host, result, tt.expected)
		}
	}
}

func TestAutoconfigServer(t *testing.T) {
	// Create a minimal server for testing
	s := &Server{}

	// Test buildAutoconfig
	config := s.buildAutoconfig("example.com")
	if config == nil {
		t.Fatal("Expected non-nil config")
	}
	if config.Version != "1.1" {
		t.Errorf("Expected version 1.1, got %s", config.Version)
	}
	if len(config.Providers) != 1 {
		t.Fatal("Expected 1 provider")
	}
	if config.Providers[0].ID != "example.com" {
		t.Errorf("Expected provider ID example.com, got %s", config.Providers[0].ID)
	}
	if len(config.Providers[0].IncomingServers) != 1 {
		t.Fatal("Expected 1 incoming server")
	}
	if len(config.Providers[0].OutgoingServers) != 1 {
		t.Fatal("Expected 1 outgoing server")
	}
}

func TestGetMailServer(t *testing.T) {
	result := getMailServer("example.com")
	expected := "mail.example.com"
	if result != expected {
		t.Errorf("getMailServer(%q) = %q, want %q", "example.com", result, expected)
	}
}

func TestGetSocketType(t *testing.T) {
	if getSocketType(true) != "SSL" {
		t.Error("Expected SSL for true")
	}
	if getSocketType(false) != "plain" {
		t.Error("Expected plain for false")
	}
}

func TestGetAuthMethod(t *testing.T) {
	if getAuthMethod(true) != "password-encrypted" {
		t.Error("Expected password-encrypted for true")
	}
	if getAuthMethod(false) != "password-cleartext" {
		t.Error("Expected password-cleartext for false")
	}
}

func TestHandleAutodiscover_GetRequest(t *testing.T) {
	s := &Server{}

	// Test GET request with email query param
	req := httptest.NewRequest(http.MethodGet, "/autodiscover/autodiscover.xml?email=user@example.com", nil)
	w := httptest.NewRecorder()

	// This would fail because s.db is nil, but it should not panic
	s.handleAutodiscover(w, req)

	// Should return error due to nil db
	if w.Code == http.StatusOK {
		t.Log("Got OK response (may be expected with nil db)")
	}
}

func TestHandleAutodiscover_MethodNotAllowed(t *testing.T) {
	s := &Server{}

	// Test PUT request (should be rejected with method not allowed)
	req := httptest.NewRequest(http.MethodPut, "/autodiscover/autodiscover.xml", nil)
	w := httptest.NewRecorder()

	s.handleAutodiscover(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestHandleAutodiscover_EmptyEmail(t *testing.T) {
	s := &Server{}

	// Test GET request without email param and non-email host (should return error)
	req := httptest.NewRequest(http.MethodGet, "/autodiscover/autodiscover.xml", nil)
	req.Host = "example.com" // No email in host
	w := httptest.NewRecorder()

	s.handleAutodiscover(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandleAutoconfig_GetRequest(t *testing.T) {
	s := &Server{}

	// Test GET request
	req := httptest.NewRequest(http.MethodGet, "/.well-known/autoconfig/mail/config-v1.1.xml", nil)
	w := httptest.NewRecorder()

	// This would fail because s.db is nil, but it should not panic
	s.handleAutoconfig(w, req)

	// Should return error due to nil db
	if w.Code == http.StatusOK {
		t.Log("Got OK response (may be expected with nil db)")
	}
}

func TestHandleAutoconfig_PostRequest(t *testing.T) {
	s := &Server{}

	// Test POST request (should be rejected)
	req := httptest.NewRequest(http.MethodPost, "/.well-known/autoconfig/mail/config-v1.1.xml", nil)
	w := httptest.NewRecorder()

	s.handleAutoconfig(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestHandleAutoconfig_EmptyDomain(t *testing.T) {
	s := &Server{}

	// Test GET request with host that has no dot (returns empty domain)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/autoconfig/mail/config-v1.1.xml", nil)
	req.Host = "localhost" // No dot, so domain will be empty
	w := httptest.NewRecorder()

	s.handleAutoconfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestBuildAutodiscoverResponse(t *testing.T) {
	s := &Server{}

	resp := s.buildAutodiscoverResponse("user@example.com", "example.com")
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}
	if resp.Space == "" {
		t.Error("Expected non-empty space")
	}
	if resp.Response.User.EMailAddress != "user@example.com" {
		t.Errorf("Expected email user@example.com, got %s", resp.Response.User.EMailAddress)
	}
	if resp.Response.Account.AccountType != "email" {
		t.Errorf("Expected account type email, got %s", resp.Response.Account.AccountType)
	}
	if len(resp.Response.Account.Protocol) != 2 {
		t.Errorf("Expected 2 protocols (IMAP+SMTP) when FeatureEWS is disabled, got %d", len(resp.Response.Account.Protocol))
	}
}

// TestBuildAutodiscoverResponse_ExchangeTier verifies that when FeatureEWS is enabled,
// the Exchange tier adds an EWS protocol entry alongside IMAP/SMTP.
func TestBuildAutodiscoverResponse_ExchangeTier(t *testing.T) {
	s := &Server{}

	// Enable both canonical identity (triggers Exchange tier) and EWS gate.
	semcore.Gate().Set(semcore.FeatureCanonicalIdentity, true)
	semcore.Gate().Set(semcore.FeatureEWS, true)
	defer func() {
		semcore.Gate().Set(semcore.FeatureCanonicalIdentity, false)
		semcore.Gate().Set(semcore.FeatureEWS, false)
	}()

	resp := s.buildAutodiscoverResponse("user@example.com", "example.com")
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	// Exchange tier with EWS enabled should advertise 3 protocols: IMAP, SMTP, EWS
	if len(resp.Response.Account.Protocol) != 3 {
		t.Errorf("Expected 3 protocols (IMAP+SMTP+EWS) in Exchange tier, got %d", len(resp.Response.Account.Protocol))
	}

	// Verify the third protocol is EWS
	protoTypes := make([]string, len(resp.Response.Account.Protocol))
	for i, p := range resp.Response.Account.Protocol {
		protoTypes[i] = p.Type
	}
	foundEWS := false
	for _, pt := range protoTypes {
		if pt == "EWS" {
			foundEWS = true
			break
		}
	}
	if !foundEWS {
		t.Error("Expected EWS protocol in Exchange tier response, but it was not found")
	}
}

// TestBuildAutodiscoverResponse_IMAPOnlyTier verifies that when FeatureEWS is enabled
// but FeatureCanonicalIdentity is not, accounts remain in TierIMAPOnly and only
// receive IMAP/SMTP settings.
func TestBuildAutodiscoverResponse_IMAPOnlyTier(t *testing.T) {
	s := &Server{}

	// Enable EWS but NOT canonical identity — should stay in IMAP-only tier.
	semcore.Gate().Set(semcore.FeatureEWS, true)
	defer func() {
		semcore.Gate().Set(semcore.FeatureEWS, false)
	}()

	resp := s.buildAutodiscoverResponse("user@example.com", "example.com")
	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	// Without canonical identity, Exchange tier is not active — only IMAP/SMTP.
	if len(resp.Response.Account.Protocol) != 2 {
		t.Errorf("Expected 2 protocols (IMAP+SMTP) in IMAP-only tier, got %d", len(resp.Response.Account.Protocol))
	}
}

func TestExtractDomainFromRequest(t *testing.T) {
	tests := []struct {
		host   string
		expect string
	}{
		{"example.com", "example.com"},
		{"mail.example.com", "mail.example.com"},
		{"example.com:8080", "example.com"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = tt.host

		result := extractDomainFromRequest(req)
		if result != tt.expect {
			t.Errorf("extractDomainFromRequest(%q) = %q, want %q", tt.host, result, tt.expect)
		}
	}
}
