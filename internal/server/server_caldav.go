package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/umailserver/umailserver/internal/caldav"
)

// startCalDAV creates and starts the CalDAV server
func (s *Server) startCalDAV() {
	if !s.cfg().CalDAV.Enabled {
		return
	}

	addr := fmt.Sprintf("%s:%d", s.cfg().CalDAV.Bind, s.cfg().CalDAV.Port)
	caldavDataDir := filepath.Join(s.cfg().Server.DataDir, "caldav")

	caldavServer := caldav.NewServer(caldavDataDir, s.logger)
	// Set auth handler - use same auth as submission SMTP
	caldavServer.SetAuthFunc(func(user, pass string) (bool, error) {
		ok, err := s.authenticate(user, pass)
		return ok, err
	})
	caldavServer.SetTracingProvider(s.tracingProvider)

	// Route calendar persistence through the canonical collaboration store so
	// CalDAV shares one source of truth with EWS and webmail (an event created
	// via any surface is visible from all). SetCollaborationStore is also kept
	// for ChangeKey-based ETag derivation in response building.
	if s.semcoreStore != nil {
		caldavServer.SetCollaborationStore(s.semcoreStore.Collaboration())
		caldavServer.UseCanonicalStore(s.semcoreStore.Collaboration(), s.semcoreStore.Identity())
	}

	s.caldavServer = caldavServer

	srv := &http.Server{
		Addr:              addr,
		Handler:           caldavServer,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.caldavHTTPServer = srv

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("CalDAV server error", "error", err)
		}
	}()

	s.logger.Info("CalDAV server started", "addr", addr)
}
