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
	config            *config.Config
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
		config:          cfg,
		logger:          logger,
		tracingProvider: tracingProvider,
		ctx:             ctx,
		cancel:          cancel,
		sieveManager:    sieve.NewManager(),
		smimeKeystore:   smtp.NewSMIMEKeystore(),
		openpgpKeystore: smtp.NewOpenPGPKeystore(),
		bgSem:           make(chan struct{}, 100),
	}

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
	if err := ensureBootstrapAdminAccounts(database, cfg.Domains, logger); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("failed to bootstrap admin accounts: %w", err)
	}

	// Initialize message store (use same path as IMAP mailstore)
	msgStorePath := s.config.Server.DataDir + "/mail/messages"
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
		pushDataDir := filepath.Join(s.config.Server.DataDir, "push")
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
	storageDBPath := s.config.Server.DataDir + "/mail/mail.db"
	storageDB, err := storage.OpenDatabaseWithOptions(storageDBPath, cfg.Storage.Sync)
	if err != nil {
		_ = tlsManager.Close()
		_ = msgStore.Close()
		_ = database.Close()
		return nil, fmt.Errorf("failed to open storage database: %w", err)
	}

	// Initialize search service
	s.storageDB = storageDB
	searchSvc := search.NewService(storageDB, msgStore, logger)
	s.searchSvc = searchSvc
	s.indexWork = make(chan indexJob, 1000)

	// Initialize semantic-core canonical store and mutation pipeline.
	// The semcore store lives under dataDir/semcore/ and uses a separate bbolt DB.
	// It is safe to create even if backfill hasn't run yet — the mutation
	// pipeline will lazily register mailbox/folder identities as needed.
	semcoreStore, err := semcore.NewStore(s.config.Server.DataDir)
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
	s.mutationPipe = semcore.NewMutationPipeline(semcoreStore.Identity())
	logger.Info("Semantic-core store initialized")

	// Initialize rate limiter with config
	rateLimiterConfig := &ratelimit.Config{
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
	s.rateLimiter = ratelimit.New(storageDB.Bolt(), rateLimiterConfig)

	// Initialize health monitor
	s.healthMonitor = health.NewMonitor("1.0.0")

	return s, nil
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

// GetDatabase returns the database instance
func (s *Server) GetDatabase() *db.DB {
	return s.database
}

// GetQueue returns the queue manager
func (s *Server) GetQueue() *queue.Manager {
	return s.queue
}
