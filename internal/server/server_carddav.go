package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/umailserver/umailserver/internal/carddav"
)

// startCardDAV creates and starts the CardDAV server
func (s *Server) startCardDAV() {
	if !s.config.CardDAV.Enabled {
		return
	}

	addr := fmt.Sprintf("%s:%d", s.config.CardDAV.Bind, s.config.CardDAV.Port)
	carddavDataDir := filepath.Join(s.config.Server.DataDir, "carddav")

	carddavServer := carddav.NewServer(carddavDataDir, s.logger)
	// Set auth handler - use same auth as submission SMTP
	carddavServer.SetAuthFunc(func(user, pass string) (bool, error) {
		ok, err := s.authenticate(user, pass)
		return ok, err
	})
	carddavServer.SetTracingProvider(s.tracingProvider)

	// Route contacts persistence through the canonical collaboration store so
	// CardDAV shares one source of truth with EWS and webmail. SetCollaborationStore
	// is also kept for ChangeKey-based ETag derivation in response building.
	if s.semcoreStore != nil {
		carddavServer.SetCollaborationStore(s.semcoreStore.Collaboration())
		carddavServer.UseCanonicalStore(s.semcoreStore.Collaboration(), s.semcoreStore.Identity())
	}

	s.carddavServer = carddavServer

	srv := &http.Server{
		Addr:              addr,
		Handler:           carddavServer,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.carddavHTTPServer = srv

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("CardDAV server error", "error", err)
		}
	}()

	s.logger.Info("CardDAV server started", "addr", addr)
}
