package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/config"
)

// secretValues are sentinel secret strings planted in a config; none of them may
// ever appear in the settings DTO sent to the admin panel.
var secretValues = map[string]func(*config.Config){
	"jwt-secret-SENTINEL":    func(c *config.Config) { c.Security.JWTSecret = "jwt-secret-SENTINEL" },
	"totp-key-SENTINEL":      func(c *config.Config) { c.Security.TOTPKey = "totp-key-SENTINEL" },
	"ldap-bindpw-SENTINEL":   func(c *config.Config) { c.LDAP.BindPassword = "ldap-bindpw-SENTINEL" },
	"mcp-token-SENTINEL":     func(c *config.Config) { c.MCP.AuthToken = "mcp-token-SENTINEL" },
	"mcp-admintok-SENTINEL":  func(c *config.Config) { c.MCP.AdminAuthToken = "mcp-admintok-SENTINEL" },
	"alert-smtppw-SENTINEL":  func(c *config.Config) { c.Alert.SMTPPassword = "alert-smtppw-SENTINEL" },
	"vapid-privkey-SENTINEL": func(c *config.Config) { c.Push.VAPIDPrivateKey = "vapid-privkey-SENTINEL" },
	"webhook-header-SENTINEL": func(c *config.Config) {
		c.Alert.WebhookHeaders = map[string]string{"Authorization": "webhook-header-SENTINEL"}
	},
}

// TestConfigDTO_ExcludesSecrets fails loudly if any secret reaches the JSON the
// admin GET returns. This is a security guard: the panel must never see secrets.
func TestConfigDTO_ExcludesSecrets(t *testing.T) {
	cfg := &config.Config{}
	for _, set := range secretValues {
		set(cfg)
	}

	data, err := json.Marshal(configToDTO(cfg))
	if err != nil {
		t.Fatalf("marshal DTO: %v", err)
	}
	for secret := range secretValues {
		if strings.Contains(string(data), secret) {
			t.Errorf("settings DTO leaked secret %q", secret)
		}
	}
}

// TestApplyConfigDTO_PreservesSecretsAndAppliesEdits verifies the PUT model: the
// secrets-free DTO applied onto a clone of the live config leaves every secret
// intact while still applying the editable change.
func TestApplyConfigDTO_PreservesSecretsAndAppliesEdits(t *testing.T) {
	cur := &config.Config{}
	for _, set := range secretValues {
		set(cur)
	}
	cur.POP3.Enabled = true

	clone, err := cloneConfig(cur)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	// The admin edits the DTO (which never carries secrets) and PUTs it back.
	req := configToDTO(cur)
	req.POP3.Enabled = false
	applyConfigDTO(clone, &req)

	if clone.Security.JWTSecret != "jwt-secret-SENTINEL" {
		t.Errorf("JWT secret was clobbered: %q", clone.Security.JWTSecret)
	}
	if clone.LDAP.BindPassword != "ldap-bindpw-SENTINEL" {
		t.Errorf("LDAP bind password was clobbered: %q", clone.LDAP.BindPassword)
	}
	if clone.MCP.AuthToken != "mcp-token-SENTINEL" {
		t.Errorf("MCP auth token was clobbered: %q", clone.MCP.AuthToken)
	}
	if clone.Alert.SMTPPassword != "alert-smtppw-SENTINEL" {
		t.Errorf("alert SMTP password was clobbered: %q", clone.Alert.SMTPPassword)
	}
	if clone.Push.VAPIDPrivateKey != "vapid-privkey-SENTINEL" {
		t.Errorf("VAPID private key was clobbered: %q", clone.Push.VAPIDPrivateKey)
	}
	if clone.POP3.Enabled {
		t.Error("editable field pop3.enabled was not applied")
	}
}

// TestConfigDTO_RoundTrip verifies an unedited GET→PUT round trip leaves every
// editable section unchanged (no field is silently dropped by the projection).
func TestConfigDTO_RoundTrip(t *testing.T) {
	cur := &config.Config{}
	cur.Server.Hostname = "mail.example.com"
	cur.SMTP.Inbound.Enabled = true
	cur.SMTP.Inbound.Port = 25
	cur.SMTP.Inbound.MaxMessageSize = 50 * config.MB
	cur.IMAP.Port = 993
	cur.POP3.Enabled = true
	cur.POP3.Port = 995
	cur.Security.RateLimit.UserPerHour = 500
	cur.Spam.Greylisting.Delay = config.Duration(60_000_000_000) // 60s

	// Normalize through a YAML round trip first so cur matches what the running
	// server actually holds (config.Load already normalized nil maps/slices to
	// empty), keeping the comparison free of nil-vs-empty artifacts.
	cur, err := cloneConfig(cur)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	clone, err := cloneConfig(cur)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	req := configToDTO(cur)
	applyConfigDTO(clone, &req)

	for _, sec := range changedSections(cur, clone, nil) {
		t.Errorf("round trip unexpectedly changed section %q", sec)
	}
}

// TestScheduledSendDTO_RoundTrip verifies the scheduled-send section survives a
// configToDTO -> applyConfigDTO round trip unchanged (so an admin save preserves
// the tuning even without dropping or zeroing it).
func TestScheduledSendDTO_RoundTrip(t *testing.T) {
	cur := &config.Config{}
	cur.Server.Hostname = "mail.example.com"
	cur.ScheduledSend = config.ScheduledSendConfig{Enabled: true, MaxHorizonDays: 90, TickSeconds: 45, MaxPerUser: 25}

	cur, err := cloneConfig(cur)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	clone, err := cloneConfig(cur)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	req := configToDTO(cur)
	applyConfigDTO(clone, &req)

	if clone.ScheduledSend != cur.ScheduledSend {
		t.Errorf("round trip changed scheduled_send: got %+v, want %+v", clone.ScheduledSend, cur.ScheduledSend)
	}
	if slices.Contains(changedSections(cur, clone, nil), "scheduled_send") {
		t.Error("scheduled_send reported as changed after an identity round trip")
	}
}

// TestValidateConfigDTO_ScheduledSendBounds rejects out-of-range scheduled-send
// values (including the all-zero shape a section-omitting PUT would produce),
// guarding against silently zeroing the live config.
func TestValidateConfigDTO_ScheduledSendBounds(t *testing.T) {
	base := configToDTO(config.DefaultConfig())
	if msg, ok := validateConfigDTO(&base); !ok {
		t.Fatalf("DefaultConfig DTO must be valid, got %q", msg)
	}

	cases := []struct {
		name   string
		mutate func(*serverConfigDTO)
	}{
		{"zero horizon", func(d *serverConfigDTO) { d.ScheduledSend.MaxHorizonDays = 0 }},
		{"horizon too large", func(d *serverConfigDTO) { d.ScheduledSend.MaxHorizonDays = 4000 }},
		{"tick too small", func(d *serverConfigDTO) { d.ScheduledSend.TickSeconds = 1 }},
		{"tick too large", func(d *serverConfigDTO) { d.ScheduledSend.TickSeconds = 7200 }},
		{"zero per-user", func(d *serverConfigDTO) { d.ScheduledSend.MaxPerUser = 0 }},
		{"all zero (omitted section)", func(d *serverConfigDTO) { d.ScheduledSend = scheduledSendSectionDTO{} }},
	}
	for _, tc := range cases {
		dto := base
		tc.mutate(&dto)
		if _, ok := validateConfigDTO(&dto); ok {
			t.Errorf("%s: expected validation to fail, but it passed", tc.name)
		}
	}
}

// TestChangedSections reports exactly the sections that differ.
func TestChangedSections(t *testing.T) {
	before := &config.Config{}
	after := &config.Config{}
	after.POP3.Enabled = true
	after.Server.Hostname = "new.example.com"

	got := map[string]bool{}
	for _, s := range changedSections(before, after, nil) {
		got[s] = true
	}
	if !got["pop3"] {
		t.Error("expected pop3 to be reported as changed")
	}
	if !got["server"] {
		t.Error("expected server to be reported as changed")
	}
	if got["imap"] {
		t.Error("imap did not change but was reported")
	}

	// applied sections are excluded from the changed list.
	if filtered := changedSections(before, after, []string{"pop3"}); slices.Contains(filtered, "pop3") {
		t.Error("expected pop3 to be excluded when listed in applied")
	}
}
