package server

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/umailserver/umailserver/internal/config"
)

// TestReloadConfig_Classification verifies the dispatcher routes each changed
// section correctly: a per-request value (OOF) and a disabled-service retune
// (POP3 stays disabled, so its restart is a no-op) are applied live, while a
// structural change (hostname) is reported as restart-required. It also confirms
// the live config pointer is swapped. POP3 is kept disabled so the test never
// binds a real port.
func TestReloadConfig_Classification(t *testing.T) {
	s := &Server{logger: slog.Default()}
	old := &config.Config{}
	old.POP3.Port = 995 // POP3 disabled (Enabled defaults to false)
	s.config.Store(old)

	newCfg := *old
	newCfg.Server.Hostname = "new.example.com" // structural -> restart_required
	newCfg.OOF.DefaultEnabled = true           // per-request read -> applied live
	newCfg.POP3.Port = 996                     // pop3 section differs; disabled -> live no-op

	applied, restart := s.ReloadConfig(&newCfg)

	if !slices.Contains(applied, "oof") {
		t.Errorf("expected oof in applied, got %v", applied)
	}
	if !slices.Contains(applied, "pop3") {
		t.Errorf("expected pop3 in applied, got %v", applied)
	}
	if !slices.Contains(restart, "server") {
		t.Errorf("expected server in restart_required, got %v", restart)
	}
	if slices.Contains(restart, "pop3") {
		t.Errorf("pop3 was applied live and must not be in restart_required, got %v", restart)
	}
	if s.cfg().Server.Hostname != "new.example.com" {
		t.Error("live config was not swapped to the new config")
	}
	if !s.cfg().OOF.DefaultEnabled {
		t.Error("live config does not reflect the OOF change")
	}
}

// TestReloadConfig_NoChangeIsNoop verifies that reloading an identical config
// reports nothing applied and nothing requiring a restart. This is what makes
// the file watcher reacting to the server's own SaveAtomic write harmless.
func TestReloadConfig_NoChangeIsNoop(t *testing.T) {
	s := &Server{logger: slog.Default()}
	cfg := &config.Config{}
	cfg.Server.Hostname = "mail.example.com"
	s.config.Store(cfg)

	same := *cfg
	applied, restart := s.ReloadConfig(&same)

	if len(applied) != 0 {
		t.Errorf("expected no applied sections, got %v", applied)
	}
	if len(restart) != 0 {
		t.Errorf("expected no restart-required sections, got %v", restart)
	}
}
