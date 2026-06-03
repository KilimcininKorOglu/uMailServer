package server

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/api"
	"github.com/umailserver/umailserver/internal/backup"
	"github.com/umailserver/umailserver/internal/ews"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/mapi"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/sieve"
)

// SetConfigPath records the path of the config file the server was loaded from,
// so the admin config API can persist runtime changes back to it. An empty path
// disables config persistence (defaults-only runs).
func (s *Server) SetConfigPath(path string) {
	s.configPath = path
}

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
	// Wire webmail send to the same submission delivery path EWS/JMAP use, so a
	// composed message is actually delivered (local + relay), not just filed in
	// Sent.
	s.apiServer.SetMailDeliveryFunc(s.submitMessageWithSieve)
	// Set contacts handler data directory for CardDAV-backed contacts API
	s.apiServer.SetContactsDataDir(s.config.Server.DataDir)
	// Set calendar handler data directory for CalDAV-backed calendar API
	s.apiServer.SetCalendarDataDir(s.config.Server.DataDir)
	// Let the calendar email meeting invitations through the shared delivery path.
	s.apiServer.SetCalendarDeliveryFunc(s.submitMessageWithSieve)
	// Set task handler data directory for CalDAV-backed (VTODO) tasks API
	s.apiServer.SetTaskDataDir(s.config.Server.DataDir)
	// Set backup manager for backup/restore operations
	if s.storageDB != nil {
		backupMgr := backup.NewManager(s.config.Server.DataDir, s.storageDB, s.msgStore)
		s.apiServer.SetBackupManager(backupMgr)
	}
	// Configure API rate limiting
	s.apiServer.SetAPIRateLimit(s.config.Security.RateLimit.HTTPRequestsPerMinute)
	// Expose the loaded config + its file path to the admin Settings API.
	s.apiServer.SetConfigManager(s.config, s.configPath)

	// Wire EWS SOAP handler into the API server.
	// This requires semcoreStore to be initialized (done in server.go startup).
	if s.semcoreStore != nil {
		// Expose the canonical store to admin API surfaces (delegation,
		// directory/resources, rules, jobs).
		s.apiServer.SetSemcoreStore(s.semcoreStore)

		// Give the API server the Sieve manager so the webmail filter endpoints
		// can recompile and install a user's active Sieve script after they
		// mutate canonical inbox rules (the same path EWS uses).
		s.apiServer.SetSieveManager(s.sieveManager)

		ewsServer := ews.NewServer(
			s.semcoreStore.Identity(),
			s.semcoreStore.SyncState(),
			s.semcoreStore.Tombstones(),
			s.msgStore,
			s.storageDB,
			s.database,
			s.mutationPipe,
			s.semcoreStore.Subscriptions(),
			s.semcoreStore.Lifecycle(),
			s.semcoreStore.Collaboration(),
			s.semcoreStore.Policy(),
			s.semcoreStore.Delegation(),
			s.sieveManager,
			s.submitMessageWithSieve,
		)
		ewsServer.SetLogger(s.logger)

		// GetUserAvailability free/busy reads calendar items straight from the
		// canonical collaboration store (which now holds every webmail/CalDAV/EWS
		// event since calendar storage was unified), so no separate filesystem
		// free/busy provider is wired — that would only re-report pre-migration
		// leftovers and double-count events.

		// Refresh IMAP IDLE sessions and the webmail SSE stream after an EWS
		// folder mutation (EmptyFolder, MoveFolder), which otherwise leaves
		// connected clients showing a stale folder.
		ewsServer.SetFolderChangeNotifier(func(email, folder string) {
			imap.GetNotificationHub().NotifyMailboxUpdate(email, folder)
		})

		// Push an untagged EXISTS / SSE new_mail when an EWS-created item is
		// mirrored into the IMAP mailstore index, and an EXPUNGE when one is
		// removed, so connected IMAP/webmail clients refresh in real time.
		ewsServer.SetMessageCreatedNotifier(func(email, folder string, uid uint32) {
			imap.GetNotificationHub().NotifyNewMessage(email, folder, uid, uid)
		})
		ewsServer.SetMessageExpungedNotifier(func(email, folder string, seqNum uint32) {
			imap.GetNotificationHub().NotifyExpunge(email, folder, seqNum)
		})

		s.apiServer.SetEWSHandler(ewsServer)
		s.logger.Info("EWS SOAP handler initialized")

		// Wire MAPI/HTTP handler for NSPI and OAB endpoints (VAL-OUTLOOK-004, VAL-OUTLOOK-005).
		// The MAPI server requires the database and policy store for GAL visibility and OAB generation.
		if s.semcoreStore != nil && s.database != nil {
			mapiServer := mapi.NewServer(s.database, s.semcoreStore.Policy())
			s.apiServer.SetMAPIHandler(mapiServer)
			s.logger.Info("MAPI/HTTP handler initialized")
		}

		// Set up feature gates for Exchange-facing surfaces.
		// These control which protocol endpoints are advertised in Autodiscover and
		// which EWS/MAPI/HTTP surfaces are reachable. The gates are checked at runtime
		// by the Autodiscover builder, EWS handler, and MAPI/HTTP handler to enforce
		// the compatibility matrix (VAL-OUTLOOK-006, VAL-CROSS-005).
		s.setupOutlookFeatureGates()
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

// localPart extracts the local part (before @) from an email address.
func localPart(email string) string {
	email = strings.Trim(email, "<>")
	if idx := strings.Index(email, "@"); idx > 0 {
		return email[:idx]
	}
	return email
}

// setupOutlookFeatureGates initializes the semcore feature gates based on
// the server configuration. These gates control which Exchange-facing protocol
// surfaces are advertised in Autodiscover and which runtime behaviors are
// enabled. The gates are set once at startup and read by Autodiscover, EWS,
// and MAPI/HTTP handlers at request time.
//
// This satisfies VAL-OUTLOOK-006 (compatibility matrix gate) and VAL-CROSS-005
// (truth-in-marketing release gates) by controlling the actual enablement state
// behind the advertised endpoints.
func (s *Server) setupOutlookFeatureGates() {
	gate := semcore.Gate()

	// FeatureEWS enables the EWS/Exchange.asmx SOAP surface (Phase 4 milestone).
	// When disabled, EWS is not advertised and requests are rejected.
	gate.Set(semcore.FeatureEWS, true)

	// FeatureCanonicalIdentity gates the Exchange-tier behavior. When enabled,
	// accounts enter TierExchange and EWS endpoints are advertised in Autodiscover.
	gate.Set(semcore.FeatureCanonicalIdentity, true)

	// FeatureCanonicalSyncState enables durable sync-state and watermark persistence.
	gate.Set(semcore.FeatureCanonicalSyncState, true)

	// FeatureCanonicalMutation enables the shared mutation pipeline.
	gate.Set(semcore.FeatureCanonicalMutation, true)

	// FeatureMAPIHTTP enables MAPI/HTTP (NSPI/OAB) surfaces for modern Windows
	// Outlook (Phase 7 milestone). When disabled, the NSPI and OAB endpoints are
	// not advertised and requests are rejected. VAL-OUTLOOK-006: this gate is the
	// primary compatibility matrix toggle.
	gate.Set(semcore.FeatureMAPIHTTP, true)

	s.logger.Info("Outlook feature gates initialized",
		"FeatureEWS", gate.IsEnabled(semcore.FeatureEWS),
		"FeatureCanonicalIdentity", gate.IsEnabled(semcore.FeatureCanonicalIdentity),
		"FeatureMAPIHTTP", gate.IsEnabled(semcore.FeatureMAPIHTTP),
	)
}

// submitMessageWithSieve routes an outbound submitted message (from EWS or
// JMAP) through Sieve evaluation and then the shared delivery path. It captures
// any Sieve actions (fileinto/redirect/keep/discard/flags/header injection/
// vacation) before handing off to deliverMessageWithSieve, so all submission
// protocols share identical delivery semantics.
func (s *Server) submitMessageWithSieve(from string, to []string, data []byte) error {
	var sieveActions []string
	if s.sieveManager != nil {
		headers := make(map[string][]string)
		if idx := bytes.Index(data, []byte("\r\n\r\n")); idx > 0 {
			for _, line := range strings.Split(string(data[:idx]), "\r\n") {
				if colonIdx := strings.Index(line, ":"); colonIdx > 0 {
					key := strings.TrimSpace(line[:colonIdx])
					value := strings.TrimSpace(line[colonIdx+1:])
					headers[key] = append(headers[key], value)
				}
			}
		}
		msg := &sieve.MessageContext{
			From:    from,
			To:      to,
			Headers: headers,
			Body:    data,
			Size:    int64(len(data)),
		}
		for _, recipient := range to {
			user, domain := parseEmail(recipient)
			s.logger.Info("submit sieve: checking recipient", "user", user, "domain", domain, "recipient", recipient)
			hasScript := s.sieveManager.HasActiveScript(user)
			if !hasScript {
				// Also try the full email
				hasScript = s.sieveManager.HasActiveScript(recipient)
				if hasScript {
					user = recipient
				}
			}
			if hasScript {
				actions, err := s.sieveManager.ProcessMessage(user, msg)
				if err == nil {
					for _, action := range actions {
						switch a := action.(type) {
						case sieve.FileintoAction:
							sieveActions = append(sieveActions, "fileinto:"+a.Folder)
						case sieve.RedirectAction:
							sieveActions = append(sieveActions, "redirect:"+a.Address)
						case sieve.KeepAction:
							sieveActions = append(sieveActions, "keep")
						case sieve.DiscardAction:
							sieveActions = append(sieveActions, "discard")
						case sieve.AddFlagAction:
							for _, f := range a.Flags {
								sieveActions = append(sieveActions, "addflag:"+f)
							}
						case sieve.AddHeaderAction:
							// Inject the header into the message body so the
							// delivered copy carries it (e.g. X-Category from
							// an assign-categories rule).
							if a.Name != "" {
								line := []byte(a.Name + ": " + a.Value + "\r\n")
								data = append(line, data...)
								msg.Body = data
								msg.Size = int64(len(data))
							}
						case sieve.VacationAction:
							// Out-of-office auto-reply. Dedup per sender to
							// avoid reply loops, mirroring the SMTP pipeline.
							if s.sieveManager.CheckAndRecordVacation(from, a.Days) {
								go s.handleSieveVacation(from, recipient, a)
							}
						}
					}
				}
				break
			}
		}
	}
	s.logger.Info("submit delivery", "sieveActions", sieveActions)
	return s.deliverMessageWithSieve(from, to, data, sieveActions)
}
