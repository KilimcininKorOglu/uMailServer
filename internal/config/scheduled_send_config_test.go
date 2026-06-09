package config

import "testing"

// TestDefaultConfig_ScheduledSendDefaults pins the shipped defaults: the feature
// is on with a one-year horizon, a 30s release tick, and a 100-per-user cap.
func TestDefaultConfig_ScheduledSendDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.ScheduledSend.Enabled {
		t.Error("scheduled-send should be enabled by default")
	}
	if cfg.ScheduledSend.MaxHorizonDays != 365 {
		t.Errorf("max_horizon_days = %d, want 365", cfg.ScheduledSend.MaxHorizonDays)
	}
	if cfg.ScheduledSend.TickSeconds != 30 {
		t.Errorf("tick_seconds = %d, want 30", cfg.ScheduledSend.TickSeconds)
	}
	if cfg.ScheduledSend.MaxPerUser != 100 {
		t.Errorf("max_per_user = %d, want 100", cfg.ScheduledSend.MaxPerUser)
	}
}

// TestValidate_ScheduledSendBounds rejects a misconfigured enabled feature (a
// zero horizon/tick/cap would silently break it) but tolerates the same zeros
// when the feature is disabled.
func TestValidate_ScheduledSendBounds(t *testing.T) {
	bad := []func(*Config){
		func(c *Config) { c.ScheduledSend.MaxHorizonDays = 0 },
		func(c *Config) { c.ScheduledSend.TickSeconds = 0 },
		func(c *Config) { c.ScheduledSend.MaxPerUser = 0 },
	}
	for i, mutate := range bad {
		cfg := validConfigForTest(t)
		cfg.ScheduledSend.Enabled = true
		mutate(cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("case %d: expected validation error for enabled+invalid scheduled-send", i)
		}
	}

	// Disabled: the zero numerics are tolerated (the loop never runs).
	cfg := validConfigForTest(t)
	cfg.ScheduledSend = ScheduledSendConfig{Enabled: false}
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled scheduled-send with zero numerics must validate, got %v", err)
	}
}
