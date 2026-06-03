package server

import (
	"fmt"
	"time"

	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/pop3"
)

// startPOP3 creates and starts the POP3 server (if enabled).
func (s *Server) startPOP3(mailstore *imap.BboltMailstore) error {
	if !s.cfg().POP3.Enabled {
		return nil
	}

	pop3Addr := fmt.Sprintf("%s:%d", s.cfg().POP3.Bind, s.cfg().POP3.Port)
	pop3Adapter := &pop3MailstoreAdapter{
		mailstore: mailstore,
		msgStore:  s.msgStore,
	}
	pop3Server := pop3.NewServer(pop3Addr, pop3Adapter, s.logger)
	pop3Server.SetAuthFunc(s.authenticate)
	pop3Server.SetAuthLimits(s.cfg().Security.MaxLoginAttempts, time.Duration(s.cfg().Security.LockoutDuration))
	pop3Server.SetReadTimeout(10 * time.Minute)
	pop3Server.SetWriteTimeout(10 * time.Minute)
	pop3Server.SetMaxConnections(s.cfg().POP3.MaxConnections)
	pop3Server.SetLoginResultHandler(s.protoLoginHandler("pop3"))
	pop3Server.SetTracingProvider(s.tracingProvider)

	// Only enforce TLS-before-auth when TLS is actually available. STLS is
	// gated on the TLS config, so requiring TLS without it would leave POP3
	// permanently unauthenticatable (matches IMAP, which allows plaintext auth
	// when TLS is disabled).
	if s.tlsManager.IsEnabled() {
		pop3Server.SetRequireTLS(true)
		pop3Server.SetTLSConfig(&pop3.TLSConfig{
			CertFile: s.cfg().TLS.CertFile,
			KeyFile:  s.cfg().TLS.KeyFile,
		})
	}

	if err := pop3Server.Start(); err != nil {
		return fmt.Errorf("failed to start POP3 server: %w", err)
	}
	s.pop3Server = pop3Server
	s.logger.Info("POP3 server started", "addr", pop3Addr)
	return nil
}
