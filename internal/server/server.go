package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
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
	"github.com/umailserver/umailserver/internal/cluster"
	"github.com/umailserver/umailserver/internal/config"
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/db/postgres"
	"github.com/umailserver/umailserver/internal/ews"
	"github.com/umailserver/umailserver/internal/health"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/jmap"
	"github.com/umailserver/umailserver/internal/logging"
	"github.com/umailserver/umailserver/internal/mailappend"
	"github.com/umailserver/umailserver/internal/pop3"
	"github.com/umailserver/umailserver/internal/push"
	"github.com/umailserver/umailserver/internal/queue"
	"github.com/umailserver/umailserver/internal/ratelimit"
	"github.com/umailserver/umailserver/internal/search"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/sieve"
	"github.com/umailserver/umailserver/internal/smtp"
	"github.com/umailserver/umailserver/internal/spam"
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
	database          db.Store
	queue             *queue.Manager
	msgStore          *storage.MessageStore
	smtpServer        *smtp.Server
	imapServer        *imap.Server
	apiServer         *api.Server
	adminServer       *api.AdminServer
	ewsServer         *ews.Server
	tlsManager        *umailTLS.Manager
	webhookMgr        *webhook.Manager
	alertMgr          *alert.Manager
	pushSvc           *push.Service
	searchSvc         *search.Service
	sieveManager      *sieve.Manager
	storageDB         storageBackend
	storageSharesDB   bool
	quotaStore        ratelimit.QuotaStore
	spamStore         spam.Store
	mailstore         *imap.BboltMailstore
	pop3Server        *pop3.Server
	mcpHTTPServer     *http.Server
	healthMonitor     *health.Monitor
	rateLimiter       *ratelimit.RateLimiter
	clusterManager    *cluster.ClusterManager
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
	// semcoreStore is the canonical semantic-core surface for the configured
	// backend: a bbolt *semcore.Store or the relational *postgres.DB, behind the
	// semanticStores seam (see semantic.go).
	semcoreStore semanticStores
	mutationPipe *semcore.MutationPipeline
	// appender is the shared canonical message-append core (mailappend.Appender)
	// SMTP delivery, EWS CreateItem, and the MAPI write ROPs all write through, so
	// a message authored on any surface is visible identically on all of them.
	appender *mailappend.Appender

	// S/MIME and OpenPGP keystores
	smimeKeystore   *smtp.SMIMEKeystore
	openpgpKeystore *smtp.OpenPGPKeystore

	// LDAP authentication client (optional, nil if LDAP disabled)
	ldapClient *auth.LDAPClient

	// Submission SMTP servers (ports 587/465)
	submissionServer    *smtp.Server
	submissionTLSServer *smtp.Server
	lmtpServer          *smtp.Server

	// Search indexing worker pool
	indexWork chan indexJob

	// Vacation reply deduplication: key = recipient+"|"+sender -> last sent time
	vacationReplies   map[string]time.Time
	vacationRepliesMu sync.Mutex

	// Background task semaphore to limit concurrent goroutines spawned per delivery
	bgSem chan struct{}

	// scheduledCancel stops the running scheduled-send release loop so a config
	// change can restart it; nil when the loop is not running. Written only at
	// startup and under reloadMu (via restartScheduledSender).
	scheduledCancel context.CancelFunc

	// recoverableCancel stops the running recoverable-items retention cleaner so a
	// config change can restart it; nil when the loop is not running. Written only
	// at startup and under reloadMu (via restartRecoverableItemsCleaner).
	recoverableCancel context.CancelFunc

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once

	// quotaDirty collects accounts whose mailbox size changed (via the storage
	// quota hook) since the last reconcile tick; the background reconciler sets
	// each one's QuotaUsed to its true stored size, coalescing bursts.
	quotaDirtyMu sync.Mutex
	quotaDirty   map[string]struct{}
}

// OpenStore opens the account/metadata store for the configured backend and
// applies its schema/migrations. The bbolt store runs the embedded KV
// migrations; the PostgreSQL store applies the relational schema. The returned
// db.Store is the only handle the rest of the server uses. It is exported so the
// CLI (account/domain/queue subcommands) opens the SAME backend the server would
// from a single canonical path, rather than hard-coding bbolt.
func OpenStore(ctx context.Context, cfg *config.Config) (db.Store, error) {
	switch cfg.DatabaseBackend() {
	case "postgres":
		pg, err := postgres.Open(ctx, cfg.Database.DSN)
		if err != nil {
			return nil, fmt.Errorf("failed to open postgres database: %w", err)
		}
		if err := pg.Migrate(ctx); err != nil {
			if cerr := pg.Close(); cerr != nil {
				return nil, fmt.Errorf("failed to apply postgres schema: %w (close: %v)", err, cerr)
			}
			return nil, fmt.Errorf("failed to apply postgres schema: %w", err)
		}
		return pg, nil
	default:
		boltDB, err := db.Open(cfg.DatabasePath())
		if err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}
		if err := boltDB.RunMigrations(); err != nil {
			if cerr := boltDB.Close(); cerr != nil {
				return nil, fmt.Errorf("failed to run migrations: %w (close: %v)", err, cerr)
			}
			return nil, fmt.Errorf("failed to run migrations: %w", err)
		}
		return boltDB, nil
	}
}

// storageBackend is the message-metadata / mailbox / search surface the server
// holds on s.storageDB. Both the bbolt *storage.Database and the relational
// *postgres.DB satisfy it, so the composition root can pick either. It is the
// union of every consumer interface the handle is passed to downstream:
// imap.MetadataStore (the IMAP mailstore + search), jmap.MailStore (adds
// GetChangesSince), api.MailStore (adds the backup job/manifest surface), and
// ews.MailStore (a metadata subset). Overlapping methods across these share
// identical signatures, so the embedding is well-formed.
type storageBackend interface {
	imap.MetadataStore
	jmap.MailStore
	api.MailStore
	ews.MailStore
	// SetQuotaHook registers the quota-reconcile callback fired on index size
	// changes; MailboxUsedBytes is the authoritative stored size it reconciles to.
	SetQuotaHook(func(user string))
	MailboxUsedBytes(user string) (int64, error)
	// CurrentChangeState returns the change-journal head as an opaque token. The
	// EAS Sync handoff anchors its incremental cursor to it once the initial
	// snapshot is drained; both the bbolt and PostgreSQL backends implement it.
	CurrentChangeState(user string) (string, error)
}

// openStorage selects the message-metadata backend for the configured engine.
// For bbolt it opens the dedicated mail.db and derives the spam / rate-limit
// quota stores from its raw handle. For postgres the metadata, spam, and quota
// surfaces are all served by the SAME *postgres.DB that OpenStore already
// returned as db.Store — so message metadata, search, accounts, spam tokens,
// and daily quotas share one connection pool and no bbolt is opened at all.
// The returned sharesDB flag tells the shutdown path not to double-close that
// shared handle.
func openStorage(cfg *config.Config, database db.Store, dataDir string) (sb storageBackend, quota ratelimit.QuotaStore, spamStore spam.Store, sharesDB bool, err error) {
	switch cfg.DatabaseBackend() {
	case "postgres":
		pg, ok := database.(*postgres.DB)
		if !ok {
			return nil, nil, nil, false, fmt.Errorf("postgres backend: account store is %T, not *postgres.DB", database)
		}
		return pg, pg, pg, true, nil
	default:
		storageDB, oerr := storage.OpenDatabaseWithOptions(dataDir+"/mail/mail.db", cfg.Storage.Sync)
		if oerr != nil {
			return nil, nil, nil, false, oerr
		}
		return storageDB, ratelimit.NewBoltStore(storageDB.Bolt()), spam.NewBoltStore(storageDB.Bolt()), false, nil
	}
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

	// Initialize the account/metadata store on the configured backend (bbolt by
	// default, PostgreSQL when database.backend is "postgres"). Engine-specific
	// open + migrate happens here on the concrete type; everything downstream
	// holds the db.Store interface.
	database, err := OpenStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s.database = database

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
	// Schema + bootstrap are done; release the boot-time advisory lock so the next
	// node can proceed (Postgres backend only; a no-op otherwise). Holding it from
	// Migrate through here serializes concurrent fresh-DB boots at the DB level, so
	// no compose-level start ordering is needed.
	if pg, ok := database.(*postgres.DB); ok {
		pg.ReleaseInitLock(ctx)
	}

	// Initialize message store (use same path as IMAP mailstore)
	msgStorePath := s.cfg().Server.DataDir + "/mail/messages"
	msgStore, err := storage.NewMessageStoreWithOptions(msgStorePath, cfg.Storage.Sync)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("failed to create message store: %w", err)
	}
	s.msgStore = msgStore

	// Initialize TLS manager. TLS is "enabled" when a certificate can actually
	// be served: ACME is on, or a manual cert/key pair is configured. This drives
	// IsEnabled(), which gates STARTTLS/STLS advertisement across SMTP/IMAP/POP3 --
	// advertising an upgrade with no usable certificate strands STARTTLS-requiring
	// clients (RFC 3207 downgrade protection aborts instead of falling back).
	tlsConfig := umailTLS.Config{
		Enabled:           cfg.TLS.ACME.Enabled || (cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != ""),
		AutoTLS:           cfg.TLS.ACME.Enabled,
		Email:             cfg.TLS.ACME.Email,
		Domains:           domainsForTLS(cfg.Server.Hostname),
		UseStaging:        cfg.TLS.ACME.Provider == "letsencrypt-staging",
		Challenge:         cfg.TLS.ACME.Challenge,
		DNSProvider:       cfg.TLS.ACME.DNSProvider,
		ACMEEndpoint:      cfg.TLS.ACME.DirectoryURL,
		ACMECACertFile:    cfg.TLS.ACME.CACertFile,
		CertFile:          cfg.TLS.CertFile,
		KeyFile:           cfg.TLS.KeyFile,
		MinVersion:        parseTLSMinVersion(cfg.TLS.MinVersion),
		ClientAuth:        cfg.TLS.ClientAuth.Enabled,
		RequireClientCert: cfg.TLS.ClientAuth.RequireCert,
		ClientCAFile:      cfg.TLS.ClientAuth.CAFile,
		CacheBackend:      cfg.TLS.ACME.CacheBackend,
		CacheDir:          cfg.TLS.ACME.CacheDir,
		RenewBeforeDays:   cfg.TLS.ACME.RenewBeforeDays,
	}
	// Share the certificate cache through the canonical store when configured so
	// active-active nodes do not each issue their own copy. Always provided; the
	// manager only uses it when CacheBackend is "store".
	tlsConfig.CacheStore = tlsCacheStore{store: database}
	// The distributed locker serializes certmagic issuance/renewal across
	// active-active nodes sharing that store. The owner identifies THIS node so a
	// lease it holds is never released by another node (hostname+pid is unique per
	// process and across hosts). os.Hostname rarely errors; on failure we fall
	// back to a synthetic owner so issuance is still serialized instead of
	// silently falling through to an unhandled empty-string owner.
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown"
	}
	tlsConfig.Locker = newTLSLocker(database, fmt.Sprintf("%s-%d", hostname, os.Getpid()))
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

	// Feed the live tenant domain set to the ACME host policy so certificates can
	// be issued for every hosted domain (and its mail./autodiscover./smtp./imap.
	// service names), not just the server's own hostname. The TLS package never
	// imports db; it consults this callback behind a short-lived cache.
	tlsManager.SetDomainSource(func() ([]string, error) {
		domains, lerr := database.ListDomains()
		if lerr != nil {
			return nil, lerr
		}
		names := make([]string, 0, len(domains))
		for _, d := range domains {
			if d != nil && d.Name != "" {
				names = append(names, d.Name)
			}
		}
		return names, nil
	})

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

	// Initialize the message-metadata / search backend for the configured engine.
	// bbolt opens a dedicated mail.db; postgres reuses the account store's pool
	// (so storageDB and database are the same *postgres.DB — see storageSharesDB).
	// The spam and rate-limit quota stores come from the same selection.
	storageDB, quotaStore, spamStore, sharesDB, err := openStorage(cfg, database, s.cfg().Server.DataDir)
	if err != nil {
		_ = tlsManager.Close()
		_ = msgStore.Close()
		_ = database.Close()
		return nil, fmt.Errorf("failed to open storage database: %w", err)
	}
	s.storageDB = storageDB
	s.storageSharesDB = sharesDB
	s.quotaStore = quotaStore
	s.spamStore = spamStore
	// Drive quota reconciliation from the single index size-change chokepoint so
	// QuotaUsed stays authoritative across every write/delete surface.
	storageDB.SetQuotaHook(s.markQuotaDirty)

	// Provision standard folders for every existing account so that accounts
	// created through any path (admin API, CLI, quickstart, bootstrap, MCP,
	// migration) expose a consistent folder set across IMAP/JMAP/EWS/webmail.
	// Idempotent; covers accounts created while the server was offline.
	ensureAllAccountsDefaultMailboxes(database, storageDB, logger)

	searchSvc := search.NewService(storageDB, msgStore, logger)
	s.searchSvc = searchSvc
	s.indexWork = make(chan indexJob, 1000)

	// Initialize the canonical semantic-core store for the configured backend.
	// bbolt opens a separate DB under dataDir/semcore/; postgres reuses the same
	// *postgres.DB as the account/metadata store (so semcore, accounts, and
	// message metadata share one pool and no bbolt is opened). Both are held
	// behind the semanticStores seam. It is safe to create even before backfill —
	// the mutation pipeline lazily registers mailbox/folder identities as needed.
	if pg, ok := database.(*postgres.DB); ok {
		s.semcoreStore = pgSemantic{pg}
		logger.Info("Semantic-core store initialized", "backend", "postgres")
	} else {
		semcoreStore, serr := semcore.NewStore(s.cfg().Server.DataDir)
		if serr != nil {
			// Best-effort cleanup of partially-initialized resources.
			//nolint:errcheck // cleanup in error path; error is already returned
			_ = tlsManager.Close()
			//nolint:errcheck // cleanup in error path; error is already returned
			_ = msgStore.Close()
			//nolint:errcheck // cleanup in error path; error is already returned
			_ = database.Close()
			return nil, fmt.Errorf("failed to create semcore store: %w", serr)
		}
		s.semcoreStore = boltSemantic{semcoreStore}
		logger.Info("Semantic-core store initialized", "backend", "bbolt")
	}
	s.mutationPipe = semcore.NewMutationPipeline(s.semcoreStore.Identity(), s.semcoreStore.Lifecycle())

	// Build the shared canonical-append core from the process stores. SMTP
	// delivery, EWS CreateItem, and the MAPI write ROPs route their tail-end write
	// (identity + blob + IMAP index + real-time/search signals) through this one
	// Appender so every surface converges on the same canonical record. The
	// notifier and search hooks mirror what deliverLocal published inline.
	if s.storageDB != nil && s.msgStore != nil {
		s.appender = mailappend.NewAppender(s.mutationPipe, s.msgStore, s.storageDB, distinguishedRole)
		s.appender.SetLogger(logger)
		s.appender.SetNotifier(func(email, folder string, uid uint32) {
			imap.GetNotificationHub().NotifyNewMessage(email, folder, uid, uid)
		})
		s.appender.SetIndexer(func(email string, uid uint32, itemID, conversationID string) {
			select {
			case s.indexWork <- indexJob{email: email, uid: uid, itemID: itemID, conversationID: conversationID}:
			default:
				logger.Warn("Search index queue full, dropping index job", "email", email, "uid", uid)
			}
		})
	}

	// Wire canonical identity store into search service so that search documents
	// use ItemId as DocID and can resolve hits back to semantic-core items.
	if s.searchSvc != nil {
		s.searchSvc.SetIdentityStore(s.semcoreStore.Identity())
		logger.Info("Search service wired to semantic-core identity store")
	}

	// Re-home any legacy spam-role junk folder onto the canonical "junk" role.
	// Older EWS code filed junk under role "spam"; converge it (and its mirrored
	// storage mailbox) so EWS/IMAP/POP3/webmail all read one Junk folder.
	// Idempotent and a no-op when no spam-role folder exists.
	convergeLegacyJunkFolders(database, s.semcoreStore.Identity(), storageDB, logger)

	// Initialize rate limiter with config
	s.rateLimiter = ratelimit.New(quotaStore, buildRateLimitConfig(cfg))

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
		DomainPerMinute:   cfg.Security.RateLimit.DomainPerMinute,
		DomainPerHour:     cfg.Security.RateLimit.DomainPerHour,
		DomainPerDay:      cfg.Security.RateLimit.DomainPerDay,
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

func syncConfiguredDomains(database db.Store, configuredDomains []config.DomainConfig) error {
	for _, domain := range configuredDomains {
		existingDomain, err := database.GetDomain(domain.Name)
		if err != nil {
			if !errors.Is(err, db.ErrNotFound) {
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

func ensureBootstrapAdminAccounts(database db.Store, configuredDomains []config.DomainConfig, logger *slog.Logger) error {
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
func ensureAllAccountsDefaultMailboxes(database db.Store, storageDB storageBackend, logger *slog.Logger) {
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
func (s *Server) GetDatabase() db.Store {
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

// domainsForTLS returns the list of domains to request TLS certificates for,
// including the primary hostname and its admin/mail subdomains.
func domainsForTLS(hostname string) []string {
	if hostname == "" {
		return nil
	}
	domains := []string{hostname}
	// Strip leading "mail." prefix if present to get the base domain
	domain := strings.TrimPrefix(hostname, "mail.")
	subdomains := []string{"admin", "smtp", "imap", "pop", "owa", "ews"}
	for _, sub := range subdomains {
		domains = append(domains, sub+"."+domain)
	}
	return domains
}
