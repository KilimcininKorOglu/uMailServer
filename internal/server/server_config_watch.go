package server

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/umailserver/umailserver/internal/config"
)

// configWatchInterval is how often the on-disk config file is polled for
// changes. Config edits are not latency-critical, so a few seconds keeps the
// reload responsive without busy-polling.
const configWatchInterval = 3 * time.Second

// startConfigReload wires the two external reload triggers — a polling file
// watcher on the config path and a SIGHUP handler — into the same live
// ReloadConfig path the admin API uses. Both are no-ops when the server was
// started without a config file (defaults-only runs).
func (s *Server) startConfigReload() {
	if s.configPath == "" {
		s.logger.Info("Config hot-reload disabled (no config file loaded)")
		return
	}

	// Poll the file for content changes (mod-time + content hash). The handler
	// only fires after a successful, validated Load, so a syntactically broken
	// edit leaves the running config untouched (fail-safe).
	s.configWatcher = config.NewWatcher(s.configPath, s.logger, func(_, newCfg *config.Config) {
		s.applyReloadedConfig(newCfg)
	})
	s.configWatcher.SetCurrentConfig(s.cfg())
	if err := s.configWatcher.Start(configWatchInterval); err != nil {
		s.logger.Warn("Config file watcher disabled", "error", err)
		s.configWatcher = nil
	}

	// Reload on SIGHUP, the conventional "reread your config" signal.
	s.sighupCh = make(chan os.Signal, 1)
	signal.Notify(s.sighupCh, syscall.SIGHUP)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-s.sighupCh:
				newCfg, err := config.Load(s.configPath)
				if err != nil {
					s.logger.Error("SIGHUP config reload failed; keeping current config", "error", err)
					continue
				}
				s.applyReloadedConfig(newCfg)
			case <-s.ctx.Done():
				return
			}
		}
	}()
	s.logger.Info("Config hot-reload enabled", "path", s.configPath, "poll_interval", configWatchInterval)
}

// applyReloadedConfig applies an externally-loaded config (from the file watcher
// or SIGHUP) to the running server and republishes it to the admin Settings API
// so a subsequent GET reflects the on-disk change. ReloadConfig is a no-op when
// nothing changed, so reacting to the server's own SaveAtomic write is harmless.
func (s *Server) applyReloadedConfig(newCfg *config.Config) {
	s.ReloadConfig(newCfg)
	if s.apiServer != nil {
		s.apiServer.SetConfigManager(newCfg, s.configPath)
	}
}

// stopConfigReload tears down the file watcher and SIGHUP handler. Safe to call
// when hot-reload was never started.
func (s *Server) stopConfigReload() {
	if s.sighupCh != nil {
		signal.Stop(s.sighupCh)
	}
	if s.configWatcher != nil {
		s.configWatcher.Stop()
		s.configWatcher = nil
	}
}
