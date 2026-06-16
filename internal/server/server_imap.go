package server

import (
	"fmt"
	"time"

	"github.com/umailserver/umailserver/internal/imap"
)

// startIMAP creates and starts the IMAP server.
func (s *Server) startIMAP(mailstore *imap.BboltMailstore) error {
	if !s.cfg().IMAP.Enabled {
		s.logger.Info("IMAP server disabled")
		return nil
	}

	imapAddr := fmt.Sprintf("%s:%d", s.cfg().IMAP.Bind, s.cfg().IMAP.Port)
	imapCfg := &imap.Config{
		Addr:                 imapAddr,
		Logger:               s.logger,
		SharedFoldersEnabled: s.cfg().Storage.SharedFolders,
	}
	// Only wire TLS (and thus advertise STARTTLS) when a usable certificate is
	// configured; otherwise STARTTLS-requiring clients would fail the handshake.
	if s.tlsManager.IsEnabled() {
		imapCfg.TLSConfig = s.tlsManager.GetTLSConfig()
	}

	imapServer := imap.NewServer(imapCfg, mailstore)
	imapServer.SetAuthFunc(s.authenticate)
	imapServer.SetAuthLimits(s.cfg().Security.MaxLoginAttempts, time.Duration(s.cfg().Security.LockoutDuration))
	imapServer.SetReadTimeout(10 * time.Minute)
	imapServer.SetWriteTimeout(10 * time.Minute)
	imapServer.SetIdleTimeout(time.Duration(s.cfg().IMAP.IdleTimeout))
	imapServer.SetMaxConnections(s.cfg().IMAP.MaxConnections)
	imapServer.SetMaxConnectionsPerIP(s.cfg().Security.RateLimit.IMAPConnections)
	imapServer.SetTracingProvider(s.tracingProvider)
	imapServer.SetLoginResultHandler(s.protoLoginHandler("imap"))
	// Public folders are read live (hot-reloaded), not captured at startup.
	imapServer.SetPublicFoldersEnabledFunc(func() bool { return s.cfg().PublicFolders.Enabled })
	// Require TLS before auth only when the central security.require_tls_for_auth
	// switch demands it AND a certificate is available; with no cert STARTTLS is
	// not advertised, so requiring it would strand clients (matches POP3/SMTP).
	requireTLS := s.cfg().Security.RequireTLSForAuth && s.tlsManager.IsEnabled()
	imapServer.SetAllowPlainAuth(!requireTLS)
	imapServer.SetOnExpunge(func(user, mailbox string, uid uint32) {
		// Expunging a message from the Scheduled folder cancels its send.
		s.cancelScheduledOnExpunge(user, mailbox, uid)
		// Expunging from the Recoverable Items folder drops its retention record.
		s.dropRecoverableOnExpunge(user, mailbox, uid)
		if s.searchSvc != nil {
			// IMAP expunge doesn't have ItemId readily available; pass empty
			// string to use legacy folder:uid removal.
			s.searchSvc.RemoveMessage(user, mailbox, uid, "")
		}
	})

	if err := imapServer.Start(); err != nil {
		return fmt.Errorf("failed to start IMAP server: %w", err)
	}
	s.imapServer = imapServer
	s.logger.Info("IMAP server started", "addr", imapAddr)
	return nil
}
