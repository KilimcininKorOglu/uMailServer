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
	if s.cfg().IMAP.STARTTLSPort <= 0 {
		imapServer.SetAllowPlainAuth(true)
	}
	imapServer.SetOnExpunge(func(user, mailbox string, uid uint32) {
		// Expunging a message from the Scheduled folder cancels its send.
		s.cancelScheduledOnExpunge(user, mailbox, uid)
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
