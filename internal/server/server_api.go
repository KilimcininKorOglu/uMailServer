package server

import (
	"fmt"
	"time"

	"github.com/umailserver/umailserver/internal/api"
	"github.com/umailserver/umailserver/internal/backup"
	"github.com/umailserver/umailserver/internal/ews"
)

// startAPI creates and starts the HTTP API server (webmail + admin).
func (s *Server) startAPI() {
	if !s.config.HTTP.Enabled {
		s.logger.Info("API server disabled")
		return
	}

	apiAddr := fmt.Sprintf("%s:%d", s.config.HTTP.Bind, s.config.HTTP.Port)
	plainHTTPAddr := ""
	if s.config.HTTP.HTTPPort > 0 && s.config.HTTP.HTTPPort != s.config.HTTP.Port {
		plainHTTPAddr = fmt.Sprintf("%s:%d", s.config.HTTP.Bind, s.config.HTTP.HTTPPort)
	}

	apiCfg := api.Config{
		Addr:             apiAddr,
		PlainAddr:        plainHTTPAddr,
		JWTSecret:        s.config.Security.JWTSecret,
		DisableLegacyJWT: s.config.Security.DisableLegacyJWT,
		TOTPKey:          s.config.Security.TOTPKey,
		CorsOrigins:      s.config.HTTP.CorsOrigins,
		TrustedProxies:   s.config.HTTP.TrustedProxies,
		DrainTimeout:     time.Duration(s.config.Server.GracefulTimeout) * time.Second,
		ShutdownTimeout:  time.Duration(s.config.Server.ForceCloseAfter) * time.Second,
		PasswordHasher:   "bcrypt", // or "argon2id" (OWASP recommended)
		AuditLog: api.AuditLogConfig{
			Path:       s.config.Security.AuditLog.Path,
			MaxSizeMB:  s.config.Security.AuditLog.MaxSizeMB,
			MaxBackups: s.config.Security.AuditLog.MaxBackups,
			MaxAgeDays: s.config.Security.AuditLog.MaxAgeDays,
		},
		DataDir:               s.config.Server.DataDir,
		SeparateAdminListener: s.config.Admin.Enabled,
	}
	s.apiServer = api.NewServer(s.database, s.logger, apiCfg)
	if s.tlsManager != nil {
		s.apiServer.SetACMEChallengeHandler(s.tlsManager.HTTPChallengeHandler())
	}
	s.apiServer.SetSearchService(s.searchSvc)
	s.apiServer.SetTracingProvider(s.tracingProvider)
	if s.queue != nil {
		s.apiServer.SetQueueManager(s.queue)
	}
	// Set health monitor
	if s.healthMonitor != nil {
		s.apiServer.SetHealthMonitor(s.healthMonitor)
	}
	// Set mail database for email operations
	if s.storageDB != nil {
		s.apiServer.SetMailDB(s.storageDB)
	}
	// Set message store for email operations
	if s.msgStore != nil {
		s.apiServer.SetMsgStore(s.msgStore)
	}
	// Set backup manager for backup/restore operations
	if s.storageDB != nil {
		backupMgr := backup.NewManager(s.config.Server.DataDir, s.storageDB, s.msgStore)
		s.apiServer.SetBackupManager(backupMgr)
	}
	// Configure API rate limiting
	s.apiServer.SetAPIRateLimit(s.config.Security.RateLimit.HTTPRequestsPerMinute)

	// Wire EWS SOAP handler into the API server.
	// This requires semcoreStore to be initialized (done in server.go startup).
	if s.semcoreStore != nil {
		ewsServer := ews.NewServer(
			s.semcoreStore.Identity(),
			s.semcoreStore.SyncState(),
			s.semcoreStore.Tombstones(),
			s.msgStore,
			s.storageDB,
			s.mutationPipe,
		)
		s.apiServer.SetEWSHandler(ewsServer)
		s.logger.Info("EWS SOAP handler initialized")
	}

	go func() {
		if err := s.apiServer.Start(apiCfg.Addr); err != nil {
			s.logger.Error("API server error", "error", err)
		}
	}()
	s.logger.Info("API server started", "addr", apiCfg.Addr)
	if apiCfg.PlainAddr != "" {
		s.logger.Info("Plain HTTP API server started", "addr", apiCfg.PlainAddr)
	}

	// Start admin server on separate port (localhost only)
	if s.config.Admin.Enabled {
		adminCfg := api.AdminConfig{
			Addr:             fmt.Sprintf("%s:%d", s.config.Admin.Bind, s.config.Admin.Port),
			JWTSecret:        s.config.Security.JWTSecret,
			DisableLegacyJWT: s.config.Security.DisableLegacyJWT,
			AuditLog: api.AuditLogConfig{
				Path:       s.config.Security.AuditLog.Path,
				MaxSizeMB:  s.config.Security.AuditLog.MaxSizeMB,
				MaxBackups: s.config.Security.AuditLog.MaxBackups,
				MaxAgeDays: s.config.Security.AuditLog.MaxAgeDays,
			},
		}
		s.adminServer = api.NewAdminServer(s.apiServer, adminCfg)

		go func() {
			if err := s.adminServer.Start(); err != nil {
				s.logger.Error("Admin API server error", "error", err)
			}
		}()
		s.logger.Info("Admin API server started", "addr", adminCfg.Addr)
	}
}
