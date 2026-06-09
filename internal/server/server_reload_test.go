package server

import (
	"context"
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

// TestReloadConfig_ScheduledSendRestart verifies the release loop hot-toggles:
// enabling it via reload starts the loop (applied lists scheduled_send and a
// cancel func is held), and a later disable stops it. A long tick keeps the loop
// idle so it never touches the (absent) store during the test.
func TestReloadConfig_ScheduledSendRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{logger: slog.Default(), ctx: ctx}
	old := &config.Config{} // scheduled-send disabled (zero value)
	s.config.Store(old)

	enable := *old
	enable.ScheduledSend = config.ScheduledSendConfig{Enabled: true, MaxHorizonDays: 365, TickSeconds: 3600, MaxPerUser: 100}
	applied, _ := s.ReloadConfig(&enable)
	if !slices.Contains(applied, "scheduled_send") {
		t.Errorf("expected scheduled_send in applied, got %v", applied)
	}
	if s.scheduledCancel == nil {
		t.Fatal("enabling scheduled-send should have started the release loop")
	}

	disable := enable
	disable.ScheduledSend.Enabled = false
	if _, _ = s.ReloadConfig(&disable); s.scheduledCancel != nil {
		t.Error("disabling scheduled-send should have stopped the release loop")
	}

	cancel()
	s.wg.Wait()
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
