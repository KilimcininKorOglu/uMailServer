package server

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Stop gracefully stops all server components
func (s *Server) Stop() error {
	s.logger.Info("Stopping uMailServer...")

	// Remove PID file
	pidFile := NewPIDFile(s.cfg().Server.DataDir)
	if err := pidFile.Remove(); err != nil {
		s.logger.Debug("Failed to remove PID file", "error", err)
	}

	// Signal cancellation
	s.cancel()

	// Stop config hot-reload (file watcher + SIGHUP handler) so no reload races
	// the shutdown.
	s.stopConfigReload()

	// Close search indexing work queue to drain workers (once only)
	s.stopOnce.Do(func() { close(s.indexWork) })

	// Stop SMTP server
	if s.smtpServer != nil {
		if err := s.smtpServer.Stop(); err != nil {
			s.logger.Error("Failed to stop SMTP server", "error", err)
		}
	}

	// Stop submission SMTP servers
	if s.submissionServer != nil {
		if err := s.submissionServer.Stop(); err != nil {
			s.logger.Error("Failed to stop submission server", "error", err)
		}
	}
	if s.submissionTLSServer != nil {
		if err := s.submissionTLSServer.Stop(); err != nil {
			s.logger.Error("Failed to stop submission TLS server", "error", err)
		}
	}

	// Stop IMAP server
	if s.imapServer != nil {
		if err := s.imapServer.Stop(); err != nil {
			s.logger.Error("Failed to stop IMAP server", "error", err)
		}
	}

	// Stop POP3 server
	if s.pop3Server != nil {
		if err := s.pop3Server.Stop(); err != nil {
			s.logger.Error("Failed to stop POP3 server", "error", err)
		}
	}

	// Stop MCP server
	s.shutdownHTTPServer(s.mcpHTTPServer, "MCP server")

	// Stop ManageSieve server
	if s.manageSieveServer != nil {
		if err := s.manageSieveServer.Close(); err != nil {
			s.logger.Error("Failed to stop ManageSieve server", "error", err)
		}
	}

	// Stop CalDAV server
	s.shutdownHTTPServer(s.caldavHTTPServer, "CalDAV server")
	if s.caldavHTTPServer != nil {
		s.logger.Debug("CalDAV server stopped")
	}

	// Stop CardDAV server
	s.shutdownHTTPServer(s.carddavHTTPServer, "CardDAV server")
	if s.carddavHTTPServer != nil {
		s.logger.Debug("CardDAV server stopped")
	}

	// Stop JMAP server
	s.shutdownHTTPServer(s.jmapHTTPServer, "JMAP server")
	if s.jmapHTTPServer != nil {
		s.logger.Debug("JMAP server stopped")
	}

	// Stop API server
	if s.apiServer != nil {
		if err := s.apiServer.Stop(); err != nil {
			s.logger.Error("Failed to stop API server", "error", err)
		}
	}

	// Stop Prometheus metrics server
	s.shutdownHTTPServer(s.metricsHTTPServer, "metrics server")

	// Stop admin server
	if s.adminServer != nil {
		if err := s.adminServer.Stop(); err != nil {
			s.logger.Error("Failed to stop admin server", "error", err)
		}
	}

	// Stop queue manager
	if s.queue != nil {
		s.queue.Stop()
	}

	// Stop rate limiter cleanup goroutine
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}

	// Close the HA cluster manager (Redis connections) when present.
	if s.clusterManager != nil {
		if err := s.clusterManager.Close(); err != nil {
			s.logger.Error("failed to close cluster manager", "error", err)
		}
	}

	// Wait for search index workers to drain before closing databases
	s.wg.Wait()

	// Drain all EWS subscriptions so that long-lived sync clients
	// receive an explicit termination signal on next GetEvents call.
	// This prevents clients from silently continuing with stale watermarks
	// after a server restart (VAL-CROSS-008).
	if s.semcoreStore != nil {
		if subs := s.semcoreStore.Subscriptions(); subs != nil {
			n, err := subs.ExpireAllSubscriptions()
			if err != nil {
				s.logger.Warn("Failed to drain subscriptions during shutdown", "error", err)
			} else if n > 0 {
				s.logger.Info("Drained subscriptions during shutdown", "count", n)
			}
		}
	}

	// Close message store
	if s.msgStore != nil {
		_ = s.msgStore.Close()
	}

	// Close mailstore (IMAP bbolt database)
	if s.mailstore != nil {
		_ = s.mailstore.Close()
	}

	// Close database
	if s.database != nil {
		_ = s.database.Close()
	}

	// Close storage database. In postgres mode storageDB is the SAME handle as
	// database (already closed just above), so skip the second close to avoid
	// double-closing the shared connection pool.
	if s.storageDB != nil && !s.storageSharesDB {
		_ = s.storageDB.Close()
	}

	// Stop tracing provider
	if s.tracingProvider != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), s.forceCloseAfter())
		if err := s.tracingProvider.Stop(shutdownCtx); err != nil {
			s.logger.Error("Failed to stop tracing provider", "error", err)
		}
		shutdownCancel()
	}

	s.logger.Info("uMailServer stopped")
	return nil
}

func (s *Server) forceCloseAfter() time.Duration {
	if s.cfg().Server.ForceCloseAfter > 0 {
		return time.Duration(s.cfg().Server.ForceCloseAfter) * time.Second
	}
	return 60 * time.Second
}

func (s *Server) shutdownHTTPServer(server *http.Server, name string) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.forceCloseAfter())
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		s.logger.Error("Failed to stop "+name, "error", err)
	}
}

// Wait waits for shutdown signal
func (s *Server) Wait() error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	s.logger.Info("Received signal", "signal", sig)

	return s.Stop()
}
