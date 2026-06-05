package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/umailserver/umailserver/internal/alert"
	"github.com/umailserver/umailserver/internal/api"
	"github.com/umailserver/umailserver/internal/auth"
	"github.com/umailserver/umailserver/internal/caldav"
	"github.com/umailserver/umailserver/internal/carddav"
	"github.com/umailserver/umailserver/internal/config"
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/health"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/jmap"
	"github.com/umailserver/umailserver/internal/logging"
	"github.com/umailserver/umailserver/internal/pop3"
	"github.com/umailserver/umailserver/internal/push"
	"github.com/umailserver/umailserver/internal/queue"
	"github.com/umailserver/umailserver/internal/ratelimit"
	"github.com/umailserver/umailserver/internal/search"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/sieve"
	"github.com/umailserver/umailserver/internal/smtp"
	"github.com/umailserver/umailserver/internal/storage"
	umailTLS "github.com/umailserver/umailserver/internal/tls"
	"github.com/umailserver/umailserver/internal/tracing"
	"github.com/umailserver/umailserver/internal/webhook"
	"golang.org/x/crypto/bcrypt"
)

// Server is the main uMailServer instance
type Server struct {
	// config holds the live configuration. It is read lock-free via s.cfg() and
	// swapped atomically by ReloadConfig, so running goroutines always observe a
	// consistent *config.Config. reloadMu serializes whole reload operations
	// (admin-API PUT, SIGHUP, file-watch) so their service restarts never overlap.
	config            atomic.Pointer[config.Config]
	reloadMu          sync.Mutex
	configPath        string
	configWatcher     *config.Watcher
	sighupCh          chan os.Signal
	logger            *slog.Logger
	database          *db.DB
	queue             *queue.Manager
	msgStore          *storage.MessageStore
	smtpServer        *smtp.Server
	imapServer        *imap.Server
	apiServer         *api.Server
	adminServer       *api.AdminServer
	tlsManager        *umailTLS.Manager
	webhookMgr        *webhook.Manager
	alertMgr          *alert.Manager
	pushSvc           *push.Service
	searchSvc         *search.Service
	sieveManager      *sieve.Manager
	storageDB         *storage.Database
	mailstore         *imap.BboltMailstore
	pop3Server        *pop3.Server
	mcpHTTPServer     *http.Server
	healthMonitor     *health.Monitor
	rateLimiter       *ratelimit.RateLimiter
	tracingProvider   *tracing.Provider
	manageSieveServer *sieve.ManageSieveServer
	caldavServer      *caldav.Server
	caldavHTTPServer  *http.Server
	carddavServer     *carddav.Server
	carddavHTTPServer *http.Server
	jmapServer        *jmap.Server
	jmapHTTPServer    *http.Server
	metricsHTTPServer *http.Server

	// Semantic-core canonical store and mutation pipeline.
	// These are initialized during New() when the data directory is available.
	// They are nil if semantic-core is disabled or not yet initialized.
	semcoreStore *semcore.Store
	mutationPipe *semcore.MutationPipeline

	// S/MIME and OpenPGP keystores
	smimeKeystore   *smtp.SMIMEKeystore
	openpgpKeystore *smtp.OpenPGPKeystore

	// LDAP authentication client (optional, nil if LDAP disabled)
	ldapClient *auth.LDAPClient

	// Submission SMTP servers (ports 587/465)
	submissionServer    *smtp.Server
	submissionTLSServer *smtp.Server

	// Search indexing worker pool
	indexWork chan indexJob

	// Vacation reply deduplication: key = recipient+"|"+sender -> last sent time
	vacationReplies   map[string]time.Time
	vacationRepliesMu sync.Mutex

	// Background task semaphore to limit concurrent goroutines spawned per delivery
	bgSem chan struct{}

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// New creates a new Server instance
func New(cfg *config.Config) (*Server, error) {
	// Setup log output
	var logOutput io.Writer
	if cfg.Logging.Output == "stdout" || cfg.Logging.Output == "" {
		logOutput = os.Stdout
	} else if cfg.Logging.Output == "stderr" {
		logOutput = os.Stderr
	} else {
		writer, err := logging.NewRotatingWriter(
			cfg.Logging.Output,
			cfg.Logging.MaxSizeMB,
			cfg.Logging.MaxBackups,
			cfg.Logging.MaxAgeDays,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create log writer: %w", err)
		}
		logOutput = writer
	}

	logger := slog.New(newLogHandler(logOutput, cfg.Logging.Format, parseLogLevel(cfg.Logging.Level)))

	// Initialize distributed tracing
	tracingProvider, err := tracing.NewProvider(tracing.Config{
		Enabled:      cfg.Tracing.Enabled,
		ServiceName:  cfg.Tracing.ServiceName,
		Exporter:     cfg.Tracing.Exporter,
		OTLPEndpoint: cfg.Tracing.OTLPEndpoint,
		Environment:  cfg.Tracing.Environment,
		Attributes:   cfg.Tracing.Attributes,
		SampleRate:   cfg.Tracing.SampleRate,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize tracing: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &Server{
		logger:          logger,
		tracingProvider: tracingProvider,
		ctx:             ctx,
		cancel:          cancel,
		sieveManager:    sieve.NewManager(),
		smimeKeystore:   smtp.NewSMIMEKeystore(),
		openpgpKeystore: smtp.NewOpenPGPKeystore(),
		bgSem:           make(chan struct{}, 100),
	}
	// Publish the initial config so s.cfg() and every downstream reader observe
	// it before any service starts.
	s.config.Store(cfg)

	// Persist Sieve scripts under the data dir so they survive restarts;
	// fall back to in-memory storage if the directory cannot be prepared.
	if err := s.sieveManager.SetStorageDir(filepath.Join(cfg.Server.DataDir, "sieve")); err != nil {
		logger.Warn("Sieve persistence disabled", "error", err)
	}

	// Load configured S/MIME signing keys into the keystore (outbound signing).
	s.loadSMIMESigningKeys()

	// Initialize database
	database, err := db.Open(cfg.DatabasePath())
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	s.database = database

	// Run pending database migrations
	if err := database.RunMigrations(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}
	if err := syncConfiguredDomains(database, cfg.Domains); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("failed to sync configured domains: %w", err)
	}
	// Backfill tenant ownership: every domain must belong to a tenant. Legacy
	// domains each get their own single-domain tenant (id == domain name).
	backfilled, err := database.EnsureTenantsForDomains()
	if err != nil {
		if cerr := database.Close(); cerr != nil {
			logger.Error("failed to close database after tenant backfill error", "error", cerr)
		}
		return nil, fmt.Errorf("failed to backfill tenants: %w", err)
	}
	if backfilled > 0 {
		logger.Info("backfilled tenant ownership for domains", "count", backfilled)
	}
	if err := ensureBootstrapAdminAccounts(database, cfg.Domains, logger); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("failed to bootstrap admin accounts: %w", err)
	}

	// Initialize message store (use same path as IMAP mailstore)
	msgStorePath := s.cfg().Server.DataDir + "/mail/messages"
	msgStore, err := storage.NewMessageStoreWithOptions(msgStorePath, cfg.Storage.Sync)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("failed to create message store: %w", err)
	}
	s.msgStore = msgStore

	// Initialize TLS manager
	tlsConfig := umailTLS.Config{
		AutoTLS:           cfg.TLS.ACME.Enabled,
		Email:             cfg.TLS.ACME.Email,
		Domains:           []string{cfg.Server.Hostname},
		UseStaging:        cfg.TLS.ACME.Provider == "letsencrypt-staging",
		Challenge:         cfg.TLS.ACME.Challenge,
		DNSProvider:       cfg.TLS.ACME.DNSProvider,
		CertFile:          cfg.TLS.CertFile,
		KeyFile:           cfg.TLS.KeyFile,
		MinVersion:        parseTLSMinVersion(cfg.TLS.MinVersion),
		ClientAuth:        cfg.TLS.ClientAuth.Enabled,
		RequireClientCert: cfg.TLS.ClientAuth.RequireCert,
		ClientCAFile:      cfg.TLS.ClientAuth.CAFile,
	}
	// Map verify mode string to tls.ClientAuthType
	switch cfg.TLS.ClientAuth.VerifyMode {
	case "verify_if_given":
		tlsConfig.ClientAuthMode = tls.VerifyClientCertIfGiven
	case "require_and_verify":
		tlsConfig.ClientAuthMode = tls.RequireAndVerifyClientCert
	case "request":
		tlsConfig.ClientAuthMode = tls.RequestClientCert
	case "require_any":
		tlsConfig.ClientAuthMode = tls.RequireAnyClientCert
	default:
		if cfg.TLS.ClientAuth.Enabled {
			if cfg.TLS.ClientAuth.RequireCert {
				tlsConfig.ClientAuthMode = tls.RequireAndVerifyClientCert
			} else {
				tlsConfig.ClientAuthMode = tls.VerifyClientCertIfGiven
			}
		}
	}

	tlsManager, err := umailTLS.NewManager(tlsConfig, logger)
	if err != nil {
		_ = msgStore.Close()
		_ = database.Close()
		return nil, fmt.Errorf("failed to create TLS manager: %w", err)
	}
	s.tlsManager = tlsManager

	// Initialize webhook manager
	webhookMgr := webhook.NewManager(database, cfg.Security.JWTSecret)
	webhookMgr.SetTracingProvider(s.tracingProvider)
	s.webhookMgr = webhookMgr

	// Initialize alert manager from config (disabled by default unless cfg.Alert.Enabled)
	s.alertMgr = alert.NewManager(buildAlertConfig(cfg.Alert), s.logger)
	s.alertMgr.SetAllowPrivateIP(cfg.Alert.AllowPrivateIP)

	// Initialize push notification service (subject + optional VAPID keys
	// come from `cfg.Push`; on-disk fallback handles the unconfigured case).
	if cfg.Push.Enabled {
		pushDataDir := filepath.Join(s.cfg().Server.DataDir, "push")
		pushSvc, err := push.NewServiceWithConfig(pushDataDir, push.Config{
			VAPIDPublicKey:  cfg.Push.VAPIDPublicKey,
			VAPIDPrivateKey: cfg.Push.VAPIDPrivateKey,
			Subject:         cfg.Push.Subject,
		}, logger)
		if err != nil {
			logger.Warn("Failed to initialize push service", "error", err)
		} else {
			s.pushSvc = pushSvc
			logger.Info("Push notification service initialized")
		}
	} else {
		logger.Info("Push notification service disabled in config")
	}

	// Initialize LDAP client if enabled
	if cfg.LDAP.Enabled {
		ldapCfg := auth.LDAPConfig{
			Enabled:        cfg.LDAP.Enabled,
			URL:            cfg.LDAP.URL,
			BindDN:         cfg.LDAP.BindDN,
			BindPassword:   cfg.LDAP.BindPassword,
			BaseDN:         cfg.LDAP.BaseDN,
			UserFilter:     cfg.LDAP.UserFilter,
			EmailAttribute: cfg.LDAP.EmailAttribute,
			NameAttribute:  cfg.LDAP.NameAttribute,
			GroupAttribute: cfg.LDAP.GroupAttribute,
			AdminGroups:    cfg.LDAP.AdminGroups,
			StartTLS:       cfg.LDAP.StartTLS,
			SkipVerify:     cfg.LDAP.SkipVerify,
			RootCA:         cfg.LDAP.RootCA,
			Timeout:        cfg.LDAP.Timeout,
			Environment:    cfg.Tracing.Environment,
		}
		ldapClient, err := auth.NewLDAPClient(ldapCfg)
		if err != nil {
			_ = tlsManager.Close()
			_ = msgStore.Close()
			_ = database.Close()
			return nil, fmt.Errorf("failed to create LDAP client: %w", err)
		}
		s.ldapClient = ldapClient
		logger.Info("LDAP authentication enabled", "url", cfg.LDAP.URL)
	}

	// Initialize storage database for search
	storageDBPath := s.cfg().Server.DataDir + "/mail/mail.db"
	storageDB, err := storage.OpenDatabaseWithOptions(storageDBPath, cfg.Storage.Sync)
	if err != nil {
		_ = tlsManager.Close()
		_ = msgStore.Close()
		_ = database.Close()
		return nil, fmt.Errorf("failed to open storage database: %w", err)
	}

	// Initialize search service
	s.storageDB = storageDB

	// Provision standard folders for every existing account so that accounts
	// created through any path (admin API, CLI, quickstart, bootstrap, MCP,
	// migration) expose a consistent folder set across IMAP/JMAP/EWS/webmail.
	// Idempotent; covers accounts created while the server was offline.
	ensureAllAccountsDefaultMailboxes(database, storageDB, logger)

	searchSvc := search.NewService(storageDB, msgStore, logger)
	s.searchSvc = searchSvc
	s.indexWork = make(chan indexJob, 1000)

	// Initialize semantic-core canonical store and mutation pipeline.
	// The semcore store lives under dataDir/semcore/ and uses a separate bbolt DB.
	// It is safe to create even if backfill hasn't run yet — the mutation
	// pipeline will lazily register mailbox/folder identities as needed.
	semcoreStore, err := semcore.NewStore(s.cfg().Server.DataDir)
	if err != nil {
		// Best-effort cleanup of partially-initialized resources.
		//nolint:errcheck // cleanup in error path; error is already returned
		_ = tlsManager.Close()
		//nolint:errcheck // cleanup in error path; error is already returned
		_ = msgStore.Close()
		//nolint:errcheck // cleanup in error path; error is already returned
		_ = database.Close()
		return nil, fmt.Errorf("failed to create semcore store: %w", err)
	}
	s.semcoreStore = semcoreStore
	s.mutationPipe = semcore.NewMutationPipeline(semcoreStore.Identity(), semcoreStore.Lifecycle())
	logger.Info("Semantic-core store initialized")

	// Wire canonical identity store into search service so that search documents
	// use ItemId as DocID and can resolve hits back to semantic-core items.
	if s.searchSvc != nil {
		s.searchSvc.SetIdentityStore(semcoreStore.Identity())
		logger.Info("Search service wired to semantic-core identity store")
	}

	// Initialize rate limiter with config
	s.rateLimiter = ratelimit.New(storageDB.Bolt(), buildRateLimitConfig(cfg))

	// Initialize health monitor
	s.healthMonitor = health.NewMonitor("1.0.0")

	return s, nil
}

// cfg returns the live configuration. It is safe to call concurrently with
// ReloadConfig: the pointer is loaded atomically, and a published *config.Config
// is never mutated in place, so callers can read its fields after the load.
func (s *Server) cfg() *config.Config {
	return s.config.Load()
}

// buildRateLimitConfig maps the YAML rate-limit section onto the ratelimit
// package's runtime config. It is the single source of truth for that mapping,
// shared by initial startup (New) and live retuning (ReloadConfig).
func buildRateLimitConfig(cfg *config.Config) *ratelimit.Config {
	return &ratelimit.Config{
		IPPerMinute:       cfg.Security.RateLimit.IPPerMinute,
		IPPerHour:         cfg.Security.RateLimit.IPPerHour,
		IPPerDay:          cfg.Security.RateLimit.IPPerDay,
		IPConnections:     cfg.Security.RateLimit.IPConnections,
		UserPerMinute:     cfg.Security.RateLimit.UserPerMinute,
		UserPerHour:       cfg.Security.RateLimit.UserPerHour,
		UserPerDay:        cfg.Security.RateLimit.UserPerDay,
		UserMaxRecipients: cfg.Security.RateLimit.UserMaxRecipients,
		GlobalPerMinute:   cfg.Security.RateLimit.GlobalPerMinute,
		GlobalPerHour:     cfg.Security.RateLimit.GlobalPerHour,
		CleanupInterval:   5 * time.Minute,
	}
}

// parseLogLevel parses log level string
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug", "trace":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "fatal":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func newLogHandler(output io.Writer, format string, level slog.Level) slog.Handler {
	options := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(format, "text") {
		return slog.NewTextHandler(output, options)
	}
	return slog.NewJSONHandler(output, options)
}

func parseTLSMinVersion(version string) uint16 {
	switch version {
	case "1.2":
		return tls.VersionTLS12
	case "1.3":
		return tls.VersionTLS13
	default:
		return 0
	}
}

func syncConfiguredDomains(database *db.DB, configuredDomains []config.DomainConfig) error {
	for _, domain := range configuredDomains {
		existingDomain, err := database.GetDomain(domain.Name)
		if err != nil {
			if !strings.HasPrefix(err.Error(), "key not found:") {
				return err
			}
			if err := database.CreateDomain(&db.DomainData{
				Name:           domain.Name,
				MaxAccounts:    domain.MaxAccounts,
				MaxMailboxSize: int64(domain.MaxMailboxSize),
				DKIMSelector:   domain.DKIM.Selector,
				IsActive:       true,
			}); err != nil {
				return err
			}
			continue
		}

		existingDomain.MaxAccounts = domain.MaxAccounts
		existingDomain.MaxMailboxSize = int64(domain.MaxMailboxSize)
		existingDomain.DKIMSelector = domain.DKIM.Selector
		if err := database.UpdateDomain(existingDomain); err != nil {
			return err
		}
	}
	return nil
}

func ensureBootstrapAdminAccounts(database *db.DB, configuredDomains []config.DomainConfig, logger *slog.Logger) error {
	const bootstrapAdminLocalPart = "admin"
	const bootstrapAdminDefaultPassword = "password"

	for _, domain := range configuredDomains {
		accounts, err := database.ListAccountsByDomain(domain.Name)
		if err != nil {
			return err
		}

		hasAdmin := false
		hasBootstrapAddress := false
		var bootstrapAdminAccount *db.AccountData
		for _, account := range accounts {
			if strings.EqualFold(account.LocalPart, bootstrapAdminLocalPart) {
				hasBootstrapAddress = true
				if account.IsAdmin {
					bootstrapAdminAccount = account
				}
			}
			if account.IsAdmin {
				hasAdmin = true
			}
		}

		if bootstrapAdminAccount != nil && !bootstrapAdminAccount.MustChangePassword &&
			bcrypt.CompareHashAndPassword([]byte(bootstrapAdminAccount.PasswordHash), []byte(bootstrapAdminDefaultPassword)) == nil {
			bootstrapAdminAccount.MustChangePassword = true
			if err := database.UpdateAccount(bootstrapAdminAccount); err != nil {
				return err
			}
			logger.Info("Enabled bootstrap password change requirement for admin account", "email", bootstrapAdminAccount.Email)
		}

		if hasAdmin {
			continue
		}
		if hasBootstrapAddress {
			logger.Warn("Skipping bootstrap admin creation because admin address already exists without admin privileges",
				"domain", domain.Name,
				"email", "admin@"+domain.Name,
			)
			continue
		}

		passwordHash, err := bcrypt.GenerateFromPassword([]byte(bootstrapAdminDefaultPassword), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("failed to hash bootstrap admin password: %w", err)
		}

		now := time.Now()
		email := "admin@" + domain.Name
		if err := database.CreateAccount(&db.AccountData{
			Email:              email,
			LocalPart:          bootstrapAdminLocalPart,
			Domain:             domain.Name,
			PasswordHash:       string(passwordHash),
			APOPHash:           fmt.Sprintf("%x", sha256.Sum256([]byte(bootstrapAdminDefaultPassword))),
			MustChangePassword: true,
			IsAdmin:            true,
			IsActive:           true,
			CreatedAt:          now,
			UpdatedAt:          now,
		}); err != nil {
			return err
		}

		logger.Info("Created bootstrap admin account for configured domain", "email", email)
	}

	return nil
}

// ensureAllAccountsDefaultMailboxes provisions the standard folders for every
// existing account at startup. It is idempotent and runs regardless of which
// path created the account, so the default folder set is consistent across all
// protocols even for accounts created while the server was offline.
func ensureAllAccountsDefaultMailboxes(database *db.DB, storageDB *storage.Database, logger *slog.Logger) {
	if database == nil || storageDB == nil {
		return
	}
	domains, err := database.ListDomains()
	if err != nil {
		logger.Warn("failed to list domains for default mailbox provisioning", "error", err)
		return
	}
	provisioned := 0
	for _, domain := range domains {
		accounts, err := database.ListAccountsByDomain(domain.Name)
		if err != nil {
			logger.Warn("failed to list accounts for default mailbox provisioning", "domain", domain.Name, "error", err)
			continue
		}
		for _, account := range accounts {
			if err := storageDB.EnsureDefaultMailboxes(account.Email); err != nil {
				logger.Warn("failed to provision default mailboxes", "email", account.Email, "error", err)
				continue
			}
			provisioned++
		}
	}
	if provisioned > 0 {
		logger.Info("Ensured default mailboxes for existing accounts", "accounts", provisioned)
	}
}

// GetDatabase returns the database instance
func (s *Server) GetDatabase() *db.DB {
	return s.database
}

// GetQueue returns the queue manager
func (s *Server) GetQueue() *queue.Manager {
	return s.queue
}

// loadSMIMESigningKeys loads outbound S/MIME signing key pairs from the
// configured directory into the S/MIME keystore. Each sender has a PEM
// certificate "<email>.crt" and matching private key "<email>.key". Missing,
// unreadable, or unparseable pairs are logged and skipped (fail-open): signing
// is best-effort and never blocks startup.
func (s *Server) loadSMIMESigningKeys() {
	if !s.cfg().Signing.Enabled || s.cfg().Signing.KeyDir == "" {
		return
	}
	entries, err := os.ReadDir(s.cfg().Signing.KeyDir)
	if err != nil {
		s.logger.Warn("S/MIME signing enabled but key directory is unreadable",
			"dir", s.cfg().Signing.KeyDir, "error", err)
		return
	}
	loaded := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".crt") {
			continue
		}
		email := strings.ToLower(strings.TrimSuffix(name, ".crt"))
		certPath := filepath.Join(s.cfg().Signing.KeyDir, name)
		keyPath := filepath.Join(s.cfg().Signing.KeyDir, strings.TrimSuffix(name, ".crt")+".key")

		certPEM, err := os.ReadFile(filepath.Clean(certPath))
		if err != nil {
			s.logger.Warn("failed to read S/MIME certificate", "file", certPath, "error", err)
			continue
		}
		keyPEM, err := os.ReadFile(filepath.Clean(keyPath))
		if err != nil {
			s.logger.Warn("missing S/MIME private key for certificate", "cert", certPath, "key", keyPath, "error", err)
			continue
		}
		if _, err := auth.ParseCertificate(certPEM); err != nil {
			s.logger.Warn("invalid S/MIME certificate, skipping", "file", certPath, "error", err)
			continue
		}
		if _, err := auth.ParsePrivateKey(keyPEM); err != nil {
			s.logger.Warn("invalid S/MIME private key, skipping", "file", keyPath, "error", err)
			continue
		}
		s.smimeKeystore.SetKeys(email, &smtp.SMIMEUserKeys{SigningCert: certPEM, SigningKey: keyPEM})
		loaded++
	}
	s.logger.Info("Loaded S/MIME signing keys", "count", loaded, "dir", s.cfg().Signing.KeyDir)
}
