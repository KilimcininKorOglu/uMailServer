package server

import (
	"context"
	"reflect"

	"github.com/umailserver/umailserver/internal/config"
)

// ReloadConfig applies newCfg to the running server without a full restart.
//
// It publishes newCfg as the live config, then diffs it against the previous
// config section by section and routes each changed section to the right live
// action: restart the affected protocol listener, retune a manager in place, or
// rely on the config swap alone for values read per request. Sections that
// cannot be changed safely while serving (the data directory, databases, the
// HTTP/admin listeners that carry the request, TLS identity, logging sink, and
// every secret) are reported in restartRequired and left for a manual restart.
//
// applied lists the sections that took effect live; restartRequired lists the
// sections that changed but need a restart. Per-section restart failures are
// logged (Rule 10: fail loud) and do not abort the rest of the reload.
//
// reloadMu serializes whole reloads so the admin-API PUT, SIGHUP, and file-watch
// triggers never overlap each other's service restarts.
func (s *Server) ReloadConfig(newCfg *config.Config) (applied, restartRequired []string) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	old := s.cfg()
	s.config.Store(newCfg)
	if old == nil {
		return nil, nil
	}

	changed := func(a, b any) bool { return !reflect.DeepEqual(a, b) }

	// The inbound SMTP pipeline embeds the spam, AV, and DMARC stages, so a
	// change to any of those sections is applied by rebuilding the pipeline via
	// an SMTP listener restart.
	smtpDirty := changed(old.SMTP, newCfg.SMTP)
	spamDirty := changed(old.Spam, newCfg.Spam)
	avDirty := changed(old.AV, newCfg.AV)
	dmarcDirty := changed(old.DMARC, newCfg.DMARC)
	if smtpDirty || spamDirty || avDirty || dmarcDirty {
		s.restartSMTP()
		if smtpDirty {
			applied = append(applied, "smtp")
		}
		if spamDirty {
			applied = append(applied, "spam")
		}
		if avDirty {
			applied = append(applied, "av")
		}
		if dmarcDirty {
			applied = append(applied, "dmarc")
		}
	}

	if changed(old.IMAP, newCfg.IMAP) {
		s.restartIMAP()
		applied = append(applied, "imap")
	}
	if changed(old.POP3, newCfg.POP3) {
		s.restartPOP3()
		applied = append(applied, "pop3")
	}
	if changed(old.ManageSieve, newCfg.ManageSieve) {
		s.restartManageSieve()
		applied = append(applied, "managesieve")
	}
	if changed(old.CalDAV, newCfg.CalDAV) {
		s.restartCalDAV()
		applied = append(applied, "caldav")
	}
	if changed(old.CardDAV, newCfg.CardDAV) {
		s.restartCardDAV()
		applied = append(applied, "carddav")
	}
	if changed(old.JMAP, newCfg.JMAP) {
		s.restartJMAP()
		applied = append(applied, "jmap")
	}
	if changed(old.MCP, newCfg.MCP) {
		s.restartMCP()
		applied = append(applied, "mcp")
	}
	if changed(old.Metrics, newCfg.Metrics) {
		s.restartMetrics()
		applied = append(applied, "metrics")
	}

	// The global rate limiter retunes in place. The remaining Security fields
	// (auth limits, audit log, secrets) are captured by listeners and the API at
	// startup, so a change there needs a restart.
	if changed(old.Security.RateLimit, newCfg.Security.RateLimit) && s.rateLimiter != nil {
		s.rateLimiter.SetConfig(buildRateLimitConfig(newCfg))
		applied = append(applied, "security.rate_limit")
	}
	oldSec, newSec := old.Security, newCfg.Security
	oldSec.RateLimit, newSec.RateLimit = config.RateLimitConfig{}, config.RateLimitConfig{}
	if changed(oldSec, newSec) {
		restartRequired = append(restartRequired, "security")
	}

	// OOF and notification defaults are read per request, so the config swap
	// above is enough for them to take effect live.
	if changed(old.OOF, newCfg.OOF) {
		applied = append(applied, "oof")
	}
	if changed(old.Notifications, newCfg.Notifications) {
		applied = append(applied, "notifications")
	}

	// Structural and built-at-startup sections cannot be hot-swapped safely:
	// they own open file handles, database connections, the request-bearing
	// HTTP/admin listeners, TLS identity, or secrets.
	for _, sec := range []struct {
		name       string
		oldV, newV any
	}{
		{"server", old.Server, newCfg.Server},
		{"tls", old.TLS, newCfg.TLS},
		{"http", old.HTTP, newCfg.HTTP},
		{"admin", old.Admin, newCfg.Admin},
		{"logging", old.Logging, newCfg.Logging},
		{"database", old.Database, newCfg.Database},
		{"storage", old.Storage, newCfg.Storage},
		{"tracing", old.Tracing, newCfg.Tracing},
		{"ldap", old.LDAP, newCfg.LDAP},
		{"alert", old.Alert, newCfg.Alert},
		{"push", old.Push, newCfg.Push},
		{"signing", old.Signing, newCfg.Signing},
		{"domains", old.Domains, newCfg.Domains},
	} {
		if changed(sec.oldV, sec.newV) {
			restartRequired = append(restartRequired, sec.name)
		}
	}

	if len(applied) > 0 || len(restartRequired) > 0 {
		s.logger.Info("Configuration reloaded",
			"applied", applied,
			"restart_required", restartRequired,
		)
	}
	return applied, restartRequired
}

// restartSMTP stops the inbound, submission, and submission-TLS SMTP servers and
// re-creates the enabled ones from the live config. Because startSMTP rebuilds
// the inbound processing pipeline, this is also how spam/AV/DMARC changes take
// effect.
func (s *Server) restartSMTP() {
	if s.smtpServer != nil {
		if err := s.smtpServer.Stop(); err != nil {
			s.logger.Error("reload: failed to stop SMTP server", "error", err)
		}
		s.smtpServer = nil
	}
	if s.submissionServer != nil {
		if err := s.submissionServer.Stop(); err != nil {
			s.logger.Error("reload: failed to stop submission server", "error", err)
		}
		s.submissionServer = nil
	}
	if s.submissionTLSServer != nil {
		if err := s.submissionTLSServer.Stop(); err != nil {
			s.logger.Error("reload: failed to stop submission TLS server", "error", err)
		}
		s.submissionTLSServer = nil
	}
	s.startSMTP()
}

// restartIMAP stops the running IMAP listener (if any) and re-creates it from
// the live config, leaving it stopped when IMAP is disabled.
func (s *Server) restartIMAP() {
	if s.imapServer != nil {
		if err := s.imapServer.Stop(); err != nil {
			s.logger.Error("reload: failed to stop IMAP server", "error", err)
		}
		s.imapServer = nil
	}
	if err := s.startIMAP(s.mailstore); err != nil {
		s.logger.Error("reload: failed to start IMAP server", "error", err)
	}
}

// restartPOP3 stops the running POP3 listener (if any) and re-creates it from
// the live config, leaving it stopped when POP3 is disabled. This is the
// flagship live toggle: disabling POP3 frees its port immediately.
func (s *Server) restartPOP3() {
	if s.pop3Server != nil {
		if err := s.pop3Server.Stop(); err != nil {
			s.logger.Error("reload: failed to stop POP3 server", "error", err)
		}
		s.pop3Server = nil
	}
	if err := s.startPOP3(s.mailstore); err != nil {
		s.logger.Error("reload: failed to start POP3 server", "error", err)
	}
}

// restartManageSieve stops the running ManageSieve listener (if any) and
// re-creates it from the live config.
func (s *Server) restartManageSieve() {
	if s.manageSieveServer != nil {
		if err := s.manageSieveServer.Close(); err != nil {
			s.logger.Error("reload: failed to stop ManageSieve server", "error", err)
		}
		s.manageSieveServer = nil
	}
	s.startManageSieve()
}

// restartCalDAV shuts the CalDAV HTTP server down and re-creates it from the
// live config.
func (s *Server) restartCalDAV() {
	s.shutdownHTTPServer(s.caldavHTTPServer, "CalDAV server")
	s.caldavHTTPServer = nil
	s.caldavServer = nil
	s.startCalDAV()
}

// restartCardDAV shuts the CardDAV HTTP server down and re-creates it from the
// live config.
func (s *Server) restartCardDAV() {
	s.shutdownHTTPServer(s.carddavHTTPServer, "CardDAV server")
	s.carddavHTTPServer = nil
	s.carddavServer = nil
	s.startCardDAV()
}

// restartJMAP shuts the JMAP HTTP server down and re-creates it from the live
// config.
func (s *Server) restartJMAP() {
	s.shutdownHTTPServer(s.jmapHTTPServer, "JMAP server")
	s.jmapHTTPServer = nil
	s.jmapServer = nil
	s.startJMAP()
}

// restartMCP shuts the MCP HTTP server down and re-creates it from the live
// config.
func (s *Server) restartMCP() {
	s.shutdownHTTPServer(s.mcpHTTPServer, "MCP server")
	s.mcpHTTPServer = nil
	s.startMCP()
}

// restartMetrics shuts the Prometheus metrics HTTP server down and re-creates it
// from the live config.
func (s *Server) restartMetrics() {
	s.stopMetrics(context.Background())
	s.metricsHTTPServer = nil
	s.startMetrics()
}
