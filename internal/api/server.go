package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"

	"github.com/golang-jwt/jwt/v5"
	"github.com/umailserver/umailserver"
	"github.com/umailserver/umailserver/internal/audit"
	"github.com/umailserver/umailserver/internal/backup"
	"github.com/umailserver/umailserver/internal/cluster"
	"github.com/umailserver/umailserver/internal/config"
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/mapi/ntlmssp"
	"github.com/umailserver/umailserver/internal/mcp"
	"github.com/umailserver/umailserver/internal/metrics"
	"github.com/umailserver/umailserver/internal/queue"
	"github.com/umailserver/umailserver/internal/search"
	"github.com/umailserver/umailserver/internal/sieve"
	"github.com/umailserver/umailserver/internal/storage"
	"github.com/umailserver/umailserver/internal/tracing"
	"github.com/umailserver/umailserver/internal/websocket"
)

// HealthMonitor interface for health checks
type HealthMonitor interface {
	HTTPHandler() http.HandlerFunc
}

// contextKey is a custom type for context values to avoid collisions.
// Using string directly as context keys is discouraged because different
// packages could use the same key, causing collisions.
type contextKey string

// Context keys for values stored in request context.
const (
	contextKeyTokenHash   contextKey = "tokenHash"
	contextKeyEmail       contextKey = "X-Email"
	contextKeyTenantID    contextKey = "tenantID"
	contextKeyTenantAdmin contextKey = "tenantAdmin"
)

// ContextKeyEmail is the string value for storing the authenticated email.
// This is exported as a string so that other packages (ews, mapi) can use
// the same key to retrieve the email from context.
// For cross-package context access, we use the string "X-Email" directly.
// Using string as context key is intentional here for cross-package compatibility.
// The SA1029 warning is suppressed because this pattern is required for
// the API server to inject email into context and EWS/MAPI handlers to read it.
const ContextKeyEmail = "X-Email" //nolint:staticcheck

// ntlmConnState holds the per-TCP-connection NTLM CHALLENGE for HTTP-layer NTLM
// on the RPC proxy. HTTP NTLM (RFC 4559) is connection-oriented: the CHALLENGE
// emitted in the 401 must be matched against the AUTHENTICATE that arrives on
// the next request over the same keep-alive connection. The server's ConnContext
// attaches one to every connection; only the RPC-proxy auth reads it.
type ntlmConnState struct {
	mu        sync.Mutex
	challenge [8]byte
	have      bool
}

// ntlmConnKey is the context key under which ntlmConnState is stored per
// connection; a distinct unexported type avoids any context-key collision.
type ntlmConnKey struct{}

// ntlmConnFromContext returns the per-connection NTLM state, or nil when the
// connection was established without ConnContext (e.g. in unit tests).
func ntlmConnFromContext(ctx context.Context) *ntlmConnState {
	cs, ok := ctx.Value(ntlmConnKey{}).(*ntlmConnState)
	if !ok {
		return nil
	}
	return cs
}

// Server represents the admin API server
type Server struct {
	db          db.Store
	logger      *slog.Logger
	config      Config
	mcpServer   *mcp.Server
	sseServer   *websocket.SSEServer
	searchSvc   *search.Service
	msgStore    *storage.MessageStore
	mailDB      MailStore
	mailDeliver func(from string, to []string, data []byte) error
	// Scheduled ("send later") hooks, injected by the main server; nil leaves the
	// feature unavailable (a future SendAt is rejected and the endpoints 503).
	mailSchedule        func(owner, from string, to []string, data []byte, sendAt time.Time) (string, error)
	mailScheduledList   func(owner string) ([]ScheduledMailItem, error)
	mailScheduledCancel func(owner, id string) error
	// Cross-protocol (tri-store) filer + idempotent semcore remover, injected by
	// the main server; nil leaves webmail filing storageDB-only (EWS-invisible).
	mailFileCopy   func(owner, folder string, raw []byte, flags []string) (uint32, string, error)
	mailRemoveCopy func(owner, folder, blobKey string)
	// Soft-delete dumpster capture, injected by the main server; nil leaves a
	// webmail permanent delete unlinking the blob as before.
	mailRecoverCapture func(owner, srcFolder string, raw []byte) bool
	// Dumpster restore, injected by the main server; nil when the dumpster is off.
	mailRecover     func(owner, id string) (string, error)
	calendarDeliver func(from string, to []string, data []byte) error
	queueMgr        *queue.Manager
	httpServer      *http.Server
	plainHTTPServer *http.Server
	// tlsConfig, when set, makes the primary listener serve HTTPS via its
	// GetCertificate callback. The plain HTTP listener (PlainAddr) stays plain so
	// it can still serve the ACME HTTP-01 challenge.
	tlsConfig *tls.Config
	healthMon HealthMonitor

	// Tracing provider for OpenTelemetry
	tracingProvider *tracing.Provider

	// Interface abstractions for testability
	vacationMgr  VacationManager
	filterMgr    FilterManager
	pushSvc      PushService
	rateLimitMgr RateLimitManager

	// File system abstraction for embed.FS
	webmailFS FileSystem
	adminFS   FileSystem

	// Mail handler for user email operations
	mailHandler *MailHandler

	// Notes handler for Outlook-style sticky notes (IPM.StickyNote messages in
	// the Notes folder, shared with EWS/IMAP/JMAP)
	notesHandler *NotesHandler

	// Contacts handler for contact operations via CardDAV
	contactsHandler *ContactsHandler
	calendarHandler *CalendarHandler
	taskHandler     *TaskHandler

	// Audit logger for security events
	auditLogger *audit.Logger

	// Cluster manager for HA/clustering (optional)
	clusterMgr    *cluster.ClusterManager
	clusterConfig *ClusterConfig

	// Backup manager for backup/restore operations
	backupMgr *backup.Manager

	// EWS handler for Exchange Web Services (folder identity surface)
	ewsHandler http.Handler

	// Binary MAPI/HTTP handler for the Offline Address Book (the manifest plus
	// the LZX-compressed Full Details and template files over MS-OXWOAB/MS-OXOAB),
	// served under /mapi/oab/.
	oabHandler http.Handler

	// Binary MAPI/HTTP (NSPI) address-book handler for the Outlook online-mode
	// directory (Bind/QueryRows/GetProps over MS-OXNSPI), served at /mapi/nspi.
	nspiHandler http.Handler

	// Binary MAPI/HTTP (emsmdb) mailbox handler for the Outlook online-mode
	// connector (Connect/Execute/Disconnect over MS-OXCROPS).
	emsmdbHandler http.Handler

	// RPC-over-HTTP (Outlook Anywhere) tunnel handler, served at
	// /rpc/rpcproxy.dll. It carries the same EMSMDB ROPs over MS-RPCH + DCERPC.
	rpchHandler http.Handler

	// Exchange ActiveSync handler at /Microsoft-Server-ActiveSync, with its live
	// config gate (when off the endpoint is not advertised).
	activesyncHandler http.Handler
	activeSyncEnabled func() bool

	// Canonical semantic-core store, used by admin surfaces (delegation,
	// directory/resources, rules, jobs). Held as the SemanticStore interface so
	// the API server names no concrete *semcore.Bolt*Store; a relational
	// aggregate slots in at SetSemcoreStore. Nil when semantic-core is disabled.
	semStore SemanticStore

	// Canonical mutation pipeline, injected alongside the semantic store. The
	// notes handler uses it so a webmail-created note round-trips across
	// protocols through the same pipeline EWS/IMAP use. Nil when semantic-core
	// is disabled.
	mutationPipe *semcore.MutationPipeline

	// Runtime Sieve manager, used to recompile and install a user's active
	// Sieve script after the webmail filter endpoints mutate canonical rules.
	// Nil when Sieve is disabled (recompile becomes a no-op).
	sieveManager *sieve.Manager

	// Read-only durable-job store view, built lazily from semStore. Nil when
	// semantic-core is disabled or the job bucket could not be opened.
	jobStore semcore.JobStore

	// Runtime config view for the admin Settings API. liveConfig is swapped to a
	// validated clone on each successful PUT; it is never mutated in place, so it
	// never races the running server's own config pointer. configPath is the file
	// changes are persisted to (empty disables persistence).
	configMu   sync.Mutex
	liveConfig *config.Config
	configPath string
	// configReloader, when set, applies a persisted config change to the running
	// server live and returns the sections that took effect versus those that
	// need a restart. It makes the running server (not the DTO's static
	// classification) the source of truth for the PUT response.
	configReloader func(newCfg *config.Config) (applied, restartRequired []string)

	// publicFoldersEnabled reports, read live, whether the per-domain
	// public-folder tree is exposed. Injected from the running server's config so
	// a hot-reload toggle applies without a restart; nil leaves it off.
	publicFoldersEnabled func() bool

	// ntlmEnabled reports, read live, whether MAPI/HTTP NTLM is enabled. It gates
	// capturing the per-account NT hash at password-set and login time. Injected
	// from the running server's config so a hot-reload toggle applies without a
	// restart; nil leaves it off.
	ntlmEnabled func() bool

	// HTTP router (cached)
	router http.Handler

	acmeChallengeHandler http.Handler

	// certificateStatusFunc lists the current TLS certificate status per domain
	// for the admin panel; injected by the orchestrator (nil = report empty set).
	certificateStatusFunc func() []TLSCertificateStatus

	// HTTP server lifecycle guard (protects httpServer field)
	serverMu sync.Mutex

	// Login rate limiting
	loginMu       sync.Mutex
	loginAttempts map[string]*loginAttempt

	// Account-based login rate limiting
	accountLoginMu       sync.Mutex
	accountLoginAttempts map[string]*loginAttempt

	// TOTP attempt limiting
	totpMu       sync.Mutex
	totpAttempts map[string]*totpAttempt

	// API rate limiting (HTTPRequestsPerMinute)
	apiRateMu       sync.Mutex
	apiRateAttempts map[string]*apiRateAttempt
	apiRateLimit    int // requests per minute, 0 = disabled

	// Mock errors for testing (used to test error paths)
	vacationGetError     error
	vacationSetError     error
	vacationDeleteError  error
	filterSaveError      error
	filterGetError       error
	pushSubscribeError   error
	pushUnsubscribeError error
	pushSendError        error
	queueMgrStatsError   error

	// Token blacklist for revoked tokens (supports logout before expiry)
	tokenBlacklist   map[string]time.Time
	tokenBlacklistMu sync.RWMutex

	// JWT secret versioning for rotation support
	jwtSecrets map[string]string // kid -> secret
	currentKid string            // active key ID

	// Draining state for zero-downtime deployment
	draining atomic.Bool

	// Background task management
	stopCh   chan struct{}
	stopOnce sync.Once
}

// Config holds API server configuration
type Config struct {
	Addr                  string
	PlainAddr             string
	JWTSecret             string            // Legacy single secret (used if JWTSecretVersions not set)
	JWTSecretVersions     map[string]string // kid -> secret, for key rotation
	DisableLegacyJWT      bool              // When true, disables fallback to legacy JWTSecret after kid rotation
	TokenExpiry           time.Duration
	DrainTimeout          time.Duration
	ShutdownTimeout       time.Duration
	CorsOrigins           []string
	TrustedProxies        []string // IPs that are allowed to set X-Forwarded-For
	TOTPKey               string   // Separate encryption key for TOTP secrets (falls back to JWTSecret if empty)
	AuditLog              AuditLogConfig
	PasswordHasher        string // "bcrypt" (default) or "argon2id"
	DataDir               string // Path to data directory for backups
	SeparateAdminListener bool
}

// AuditLogConfig holds audit logging configuration
type AuditLogConfig struct {
	Path       string // Path to audit log file, empty = disabled
	MaxSizeMB  int    // Max file size before rotation
	MaxBackups int    // Number of backup files to keep
	MaxAgeDays int    // Max age of backup files in days
}

// NewServer creates a new admin API server
func NewServer(database db.Store, logger *slog.Logger, config Config) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if config.JWTSecret == "" {
		logger.Warn("JWTSecret is empty, generating random secret - tokens will not survive restarts")
		config.JWTSecret = generateSecureJWTSecret()
	}
	if config.TokenExpiry == 0 {
		config.TokenExpiry = 24 * time.Hour
	}
	if config.DrainTimeout <= 0 {
		config.DrainTimeout = 30 * time.Second
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 60 * time.Second
	}
	if config.ShutdownTimeout < config.DrainTimeout {
		config.ShutdownTimeout = config.DrainTimeout
	}

	// Initialize JWT secret versioning
	jwtSecrets := make(map[string]string)
	currentKid := "default"
	if len(config.JWTSecretVersions) > 0 {
		// Use configured versions
		for kid, secret := range config.JWTSecretVersions {
			jwtSecrets[kid] = secret
		}
		// Set currentKid to first key in map if not set
		for kid := range config.JWTSecretVersions {
			currentKid = kid
			break
		}
	} else {
		// Migrate legacy single secret to versioned format
		jwtSecrets[currentKid] = config.JWTSecret
	}

	sseServer := websocket.NewSSEServer(logger)
	if len(config.CorsOrigins) > 0 {
		sseServer.SetCorsOrigin(strings.Join(config.CorsOrigins, ","))
	}

	// Capture jwtSecrets and currentKid for closure
	secrets := jwtSecrets
	kid := currentKid
	sseServer.SetAuthFunc(func(token string) (user string, isAdmin bool, err error) {
		parsed, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			// Try kid-based secret lookup first
			if kid, ok := t.Header["kid"].(string); ok && kid != "" {
				if kidSecret, ok := secrets[kid]; ok {
					return []byte(kidSecret), nil
				}
			}
			// Fall back to current kid
			if secret, ok := secrets[kid]; ok {
				return []byte(secret), nil
			}
			// Last resort: try legacy JWTSecret only if not disabled
			if !config.DisableLegacyJWT {
				return []byte(config.JWTSecret), nil
			}
			return nil, fmt.Errorf("unknown signing key")
		})
		if err != nil || !parsed.Valid {
			return "", false, fmt.Errorf("invalid token")
		}
		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			return "", false, fmt.Errorf("invalid claims")
		}
		user, _ = claims["sub"].(string)
		isAdmin, _ = claims["admin"].(bool)
		mustChangePasswordClaim, _ := claims[passwordChangeRequiredClaim].(bool)
		mustChangePassword, err := enforceAuthenticatedAccount(database, user, mustChangePasswordClaim)
		if err != nil {
			return "", false, err
		}
		if mustChangePassword {
			return "", false, fmt.Errorf("password change required")
		}
		return user, isAdmin, nil
	})

	// Initialize audit logger
	auditLogger, err := audit.NewLogger(
		config.AuditLog.Path,
		config.AuditLog.MaxSizeMB,
		config.AuditLog.MaxBackups,
		config.AuditLog.MaxAgeDays,
	)
	if err != nil {
		logger.Warn("failed to initialize audit logger", "error", err)
	}

	return &Server{
		db:             database,
		logger:         logger,
		config:         config,
		mcpServer:      mcp.NewServer(database),
		sseServer:      sseServer,
		webmailFS:      newEmbedFSSub(umailserver.WebmailFS, "webmail/dist"),
		adminFS:        newEmbedFSSub(umailserver.AdminFS, "web/admin/dist"),
		auditLogger:    auditLogger,
		tokenBlacklist: make(map[string]time.Time),
		jwtSecrets:     jwtSecrets,
		currentKid:     currentKid,
		stopCh:         make(chan struct{}),
	}
}

// NewServerWithInterfaces creates a new admin API server with injectable interfaces for testing
func NewServerWithInterfaces(
	database db.Store,
	logger *slog.Logger,
	config Config,
	vacationMgr VacationManager,
	filterMgr FilterManager,
	pushSvc PushService,
	webmailFS FileSystem,
	adminFS FileSystem,
) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if config.JWTSecret == "" {
		logger.Warn("JWTSecret is empty, generating random secret - tokens will not survive restarts")
		config.JWTSecret = generateSecureJWTSecret()
	}
	if config.TokenExpiry == 0 {
		config.TokenExpiry = 24 * time.Hour
	}
	if config.DrainTimeout <= 0 {
		config.DrainTimeout = 30 * time.Second
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 60 * time.Second
	}
	if config.ShutdownTimeout < config.DrainTimeout {
		config.ShutdownTimeout = config.DrainTimeout
	}

	// Initialize JWT secret versioning
	jwtSecrets := make(map[string]string)
	currentKid := "default"
	if len(config.JWTSecretVersions) > 0 {
		for kid, secret := range config.JWTSecretVersions {
			jwtSecrets[kid] = secret
		}
		for kid := range config.JWTSecretVersions {
			currentKid = kid
			break
		}
	} else {
		jwtSecrets[currentKid] = config.JWTSecret
	}

	sseServer := websocket.NewSSEServer(logger)
	if len(config.CorsOrigins) > 0 {
		sseServer.SetCorsOrigin(strings.Join(config.CorsOrigins, ","))
	}

	// Capture jwtSecrets and currentKid for closure
	secrets := jwtSecrets
	kid := currentKid
	sseServer.SetAuthFunc(func(token string) (user string, isAdmin bool, err error) {
		parsed, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			if t.Header["kid"] != nil {
				if kidSecret, ok := secrets[t.Header["kid"].(string)]; ok {
					return []byte(kidSecret), nil
				}
			}
			if secret, ok := secrets[kid]; ok {
				return []byte(secret), nil
			}
			if !config.DisableLegacyJWT {
				return []byte(config.JWTSecret), nil
			}
			return nil, fmt.Errorf("unknown signing key")
		})
		if err != nil || !parsed.Valid {
			return "", false, fmt.Errorf("invalid token")
		}
		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			return "", false, fmt.Errorf("invalid claims")
		}
		user, _ = claims["sub"].(string)
		isAdmin, _ = claims["admin"].(bool)
		mustChangePasswordClaim, _ := claims[passwordChangeRequiredClaim].(bool)
		mustChangePassword, err := enforceAuthenticatedAccount(database, user, mustChangePasswordClaim)
		if err != nil {
			return "", false, err
		}
		if mustChangePassword {
			return "", false, fmt.Errorf("password change required")
		}
		return user, isAdmin, nil
	})

	// Use provided FS or default to embedded
	if webmailFS == nil {
		webmailFS = NewEmbedFSAdapter(umailserver.WebmailFS)
	}
	if adminFS == nil {
		adminFS = NewEmbedFSAdapter(umailserver.AdminFS)
	}

	// Initialize audit logger so SMTP/IMAP/POP3 hooks can route protocol-level
	// auth events into the same sink as HTTP/admin events.
	auditLogger, err := audit.NewLogger(
		config.AuditLog.Path,
		config.AuditLog.MaxSizeMB,
		config.AuditLog.MaxBackups,
		config.AuditLog.MaxAgeDays,
	)
	if err != nil {
		logger.Warn("failed to initialize audit logger", "error", err)
	}

	return &Server{
		db:             database,
		logger:         logger,
		config:         config,
		mcpServer:      mcp.NewServer(database),
		sseServer:      sseServer,
		vacationMgr:    vacationMgr,
		filterMgr:      filterMgr,
		pushSvc:        pushSvc,
		webmailFS:      webmailFS,
		adminFS:        adminFS,
		auditLogger:    auditLogger,
		tokenBlacklist: make(map[string]time.Time),
		jwtSecrets:     jwtSecrets,
		currentKid:     currentKid,
		stopCh:         make(chan struct{}),
	}
}

// ServeHTTP implements the http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("HTTP handler panic", "panic", recovered, "path", r.URL.Path, "stack", string(debug.Stack()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}()
	if s.router == nil {
		s.initRouter()
	}
	s.traceRequest(s.router).ServeHTTP(w, r)
}

// initRouter sets up the HTTP routes (called once on first request)
func (s *Server) initRouter() {
	mux := http.NewServeMux()

	if s.acmeChallengeHandler != nil {
		mux.Handle("/.well-known/acme-challenge/", s.acmeChallengeHandler)
	}

	// Webmail (static files) - user interface
	mux.HandleFunc("/", s.handleWebmail)
	mux.HandleFunc("/webmail/", s.handleWebmail)

	// Admin panel (static files) - admin interface
	if s.config.SeparateAdminListener {
		mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	} else {
		mux.HandleFunc("/admin/", s.handleAdmin)
	}

	// Mozilla-style autoconfig
	mux.HandleFunc("/.well-known/autoconfig/mail/config-v1.1.xml", s.handleAutoconfig)

	// Microsoft Autodiscover
	mux.HandleFunc("/autodiscover/autodiscover.xml", s.handleAutodiscover)

	// Exchange Web Services — EWS/SOAP endpoint.
	// Authenticated via HTTP Basic Auth; credentials are validated and the email
	// is injected into the request context via X-Email for downstream handlers.
	if s.ewsHandler != nil {
		mux.HandleFunc("/EWS/Exchange.asmx", func(w http.ResponseWriter, r *http.Request) {
			email := s.ewsBasicAuth(w, r)
			if email == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="Exchange"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			//nolint:staticcheck // intentional: string key for cross-package context access
			r = r.WithContext(context.WithValue(r.Context(), ContextKeyEmail, email))
			s.ewsHandler.ServeHTTP(w, r)
		})
		// REST photo endpoint Outlook desktop/OWA use:
		// GET /EWS/Exchange.asmx/s/GetUserPhoto?email=&size= → raw image bytes.
		// The exact "/EWS/Exchange.asmx" pattern above does not match this
		// sub-path, so it is registered separately behind the same Basic Auth.
		mux.HandleFunc("/EWS/Exchange.asmx/s/GetUserPhoto", func(w http.ResponseWriter, r *http.Request) {
			email := s.ewsBasicAuth(w, r)
			if email == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="Exchange"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			//nolint:staticcheck // intentional: string key for cross-package context access
			r = r.WithContext(context.WithValue(r.Context(), ContextKeyEmail, email))
			s.handleEWSUserPhoto(w, r)
		})
	}

	// MAPI/HTTP surface for modern Windows Outlook.
	// Includes NSPI (directory/GAL address-book lookup) and OAB (offline address book).
	// VAL-OUTLOOK-004: NSPI directory lookups return policy-correct address book results.
	// VAL-OUTLOOK-005: OAB retrieval supports offline address-book use with full and
	// incremental refresh.
	// VAL-OUTLOOK-008: account-state (inactive / password-change-required) failures are
	// explicit before any mailbox data is returned, even for MAPI/HTTP entry points.
	if s.nspiHandler != nil {
		mux.HandleFunc("/mapi/nspi", func(w http.ResponseWriter, r *http.Request) {
			email := s.mapiBasicAuth(w, r)
			if email == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="MAPI/HTTP"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			//nolint:staticcheck // intentional: string key for cross-package context access
			r = r.WithContext(context.WithValue(r.Context(), ContextKeyEmail, email))
			s.nspiHandler.ServeHTTP(w, r)
		})
	}
	if s.oabHandler != nil {
		// The OAB serves the whole GAL, so it needs no per-user context, only an
		// authenticated caller. Outlook fetches oab.xml and the .lzx files under
		// the OAB directory; the subtree pattern serves them and the exact pattern
		// covers a bare request.
		oab := func(w http.ResponseWriter, r *http.Request) {
			if email := s.mapiBasicAuth(w, r); email == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="MAPI/HTTP"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			s.oabHandler.ServeHTTP(w, r)
		}
		mux.HandleFunc("/mapi/oab/", oab)
		mux.HandleFunc("/mapi/oab", oab)
	}
	if s.emsmdbHandler != nil {
		mux.HandleFunc("/mapi/emsmdb", func(w http.ResponseWriter, r *http.Request) {
			email := s.mapiBasicAuth(w, r)
			if email == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="MAPI/HTTP"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			//nolint:staticcheck // intentional: string key for cross-package context access
			r = r.WithContext(context.WithValue(r.Context(), ContextKeyEmail, email))
			s.emsmdbHandler.ServeHTTP(w, r)
		})
	}
	if s.rpchHandler != nil {
		// RPC-over-HTTP (Outlook Anywhere). The client opens the RPC_OUT_DATA and
		// RPC_IN_DATA channels with a query of the form ?<host>:<port>; the proxy
		// authenticates the mailbox at the HTTP layer (Basic, or NTLM when the
		// opt-in is live), so the query is parsed and ignored.
		mux.HandleFunc("/rpc/rpcproxy.dll", func(w http.ResponseWriter, r *http.Request) {
			email, ok := s.mapiRPCProxyAuth(w, r)
			if !ok {
				// A 401 challenge (or rejection) has already been written.
				return
			}
			//nolint:staticcheck // intentional: string key for cross-package context access
			r = r.WithContext(context.WithValue(r.Context(), ContextKeyEmail, email))
			s.rpchHandler.ServeHTTP(w, r)
		})
	}
	if s.activesyncHandler != nil {
		// Exchange ActiveSync. OPTIONS advertises the protocol and POST commands
		// authenticate inside the handler (Basic), so the whole endpoint is gated
		// only by the live opt-in — when off it is not advertised at all.
		mux.HandleFunc("/Microsoft-Server-ActiveSync", func(w http.ResponseWriter, r *http.Request) {
			if !s.activeSyncOn() {
				http.NotFound(w, r)
				return
			}
			s.activesyncHandler.ServeHTTP(w, r)
		})
	}

	// Health check - use health monitor if available
	if s.healthMon != nil {
		mux.HandleFunc("/health", s.healthMon.HTTPHandler())
	} else {
		mux.HandleFunc("/health", s.handleHealth)
	}

	// Kubernetes readiness probe - returns 200 if ready to accept traffic
	mux.HandleFunc("/health/ready", s.handleReady)

	// Metrics endpoint (admin only)
	mux.HandleFunc("/metrics", s.authMiddleware(s.adminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.Get().HTTPHandler(w, r)
	}))).ServeHTTP)

	// SSE endpoint for real-time updates (requires auth)
	mux.Handle("/api/v1/events", s.authMiddleware(s.sseServer.Handler()))

	// MCP endpoint (protected by auth)
	mux.Handle("/mcp", s.authMiddleware(http.HandlerFunc(s.mcpServer.HandleHTTP)))

	// Public per-tenant branding for the (pre-auth) login screen.
	mux.HandleFunc("/api/v1/branding", s.handleBranding)

	// Authentication
	mux.Handle("/api/v1/auth/login", s.limitBodyMiddleware(http.HandlerFunc(s.handleLogin)))
	mux.Handle("/api/v1/auth/logout", s.rateLimitMiddleware(s.authMiddleware(http.HandlerFunc(s.handleLogout))))

	// Protected routes
	api := http.NewServeMux()

	// Refresh token (requires auth)
	api.HandleFunc("/api/v1/auth/refresh", s.handleRefresh)

	// Current user identity (lets the SPA rehydrate its session after reload)
	api.HandleFunc("/api/v1/auth/me", s.handleMe)

	// Self-service password change for the authenticated user.
	api.HandleFunc("/api/v1/account/password", s.handleAccountPassword)

	// Per-user UI preferences (settings toggles).
	api.HandleFunc("/api/v1/preferences", s.handlePreferences)

	// Per-user outgoing-mail signature.
	api.HandleFunc("/api/v1/signature", s.handleSignature)

	// Per-user master category list (named colors for message labels).
	api.HandleFunc("/api/v1/categories", s.handleCategories)

	// Self-service delegation management (the authenticated user is the owner).
	api.HandleFunc("/api/v1/delegations", s.handleMyDelegations)
	api.HandleFunc("/api/v1/delegations/", s.handleMyDelegationDetail)

	if !s.config.SeparateAdminListener {
		s.registerAdminAPIRoutes(api)
	}

	// Search
	api.HandleFunc("/api/v1/search", s.handleSearch)

	// Threads
	api.HandleFunc("/api/v1/threads", s.handleThreads)
	api.HandleFunc("/api/v1/threads/search", s.handleThreadSearch)
	api.HandleFunc("/api/v1/threads/", s.handleThreadPath)

	// Vacation auto-reply
	api.HandleFunc("/api/v1/vacation", s.handleVacation)

	// Push notifications
	api.HandleFunc("/api/v1/push/vapid-public-key", s.handlePushVAPID)
	api.HandleFunc("/api/v1/push/subscribe", s.handlePushSubscribe)
	api.HandleFunc("/api/v1/push/unsubscribe", s.handlePushUnsubscribe)
	api.HandleFunc("/api/v1/push/subscriptions", s.handlePushSubscriptions)
	api.HandleFunc("/api/v1/push/test", s.handlePushTest)

	// Email filters
	api.HandleFunc("/api/v1/filters", s.handleFilters)
	api.HandleFunc("/api/v1/filters/reorder", s.handleFilterReorder)
	// Exact paths; ServeMux matches these ahead of the "/api/v1/filters/" subtree.
	api.HandleFunc("/api/v1/filters/export", s.handleFiltersExport)
	api.HandleFunc("/api/v1/filters/import", s.handleFiltersImport)
	api.HandleFunc("/api/v1/filters/", s.handleFilterPath)

	// Client sessions (account portal)
	api.HandleFunc("/api/v1/sessions", s.handleSessions)
	api.HandleFunc("/api/v1/sessions/", s.handleSessionRevoke)

	// Mail (user-facing, uses same auth as API)
	// Ensure mailHandler is initialized
	if s.mailHandler == nil {
		s.mailHandler = NewMailHandler()
		s.mailHandler.SetStorage(s.msgStore, s.mailDB)
	}
	if s.mailDeliver != nil {
		s.mailHandler.SetDeliveryFunc(s.mailDeliver)
	}
	s.mailHandler.SetDisplayNameResolver(s.resolveDisplayName)
	s.mailHandler.SetFromNameBuilder(s.buildOutboundFromName)
	s.mailHandler.SetTimezoneResolver(s.resolveTimezone)
	s.applyScheduledFuncs()

	api.HandleFunc("/api/v1/mail/inbox", s.mailHandler.handleMailList)
	api.HandleFunc("/api/v1/mail/sent", http.HandlerFunc(s.mailHandler.handleMailList).ServeHTTP)
	api.HandleFunc("/api/v1/mail/drafts", http.HandlerFunc(s.mailHandler.handleMailList).ServeHTTP)
	api.HandleFunc("/api/v1/mail/trash", http.HandlerFunc(s.mailHandler.handleMailList).ServeHTTP)
	api.HandleFunc("/api/v1/mail/spam", http.HandlerFunc(s.mailHandler.handleMailList).ServeHTTP)
	api.HandleFunc("/api/v1/mail/message", http.HandlerFunc(s.mailHandler.handleMailGet).ServeHTTP)
	api.HandleFunc("/api/v1/mail/attachment", http.HandlerFunc(s.mailHandler.handleMailAttachment).ServeHTTP)
	api.HandleFunc("/api/v1/mail/send", http.HandlerFunc(s.mailHandler.handleMailSend).ServeHTTP)
	api.HandleFunc("/api/v1/scheduled", http.HandlerFunc(s.mailHandler.handleScheduledList).ServeHTTP)
	api.HandleFunc("/api/v1/scheduled/cancel", http.HandlerFunc(s.mailHandler.handleScheduledCancel).ServeHTTP)
	api.HandleFunc("/api/v1/mail/delete", http.HandlerFunc(s.mailHandler.handleMailDelete).ServeHTTP)
	api.HandleFunc("/api/v1/mail/recall", http.HandlerFunc(s.mailHandler.handleMailRecall).ServeHTTP)
	api.HandleFunc("/api/v1/mail/recover", http.HandlerFunc(s.mailHandler.handleMailRecover).ServeHTTP)
	api.HandleFunc("/api/v1/mail/flag", http.HandlerFunc(s.mailHandler.handleMailFlag).ServeHTTP)
	api.HandleFunc("/api/v1/mail/labels", http.HandlerFunc(s.mailHandler.handleMailLabels).ServeHTTP)
	api.HandleFunc("/api/v1/mail/invite", s.handleMailInvite)
	api.HandleFunc("/api/v1/mail/rsvp", s.handleMailRSVP)
	api.HandleFunc("/api/v1/mail/move", http.HandlerFunc(s.mailHandler.handleMailMove).ServeHTTP)
	api.HandleFunc("/api/v1/mail/draft", http.HandlerFunc(s.mailHandler.handleMailDraft).ServeHTTP)
	api.HandleFunc("/api/v1/mail/diagnostics", s.handleMailDiagnostics)
	// Generic per-folder listing for any other mailbox (e.g. Archive or custom
	// folders). Exact routes above take precedence in the mux.
	api.HandleFunc("/api/v1/mail/", http.HandlerFunc(s.mailHandler.handleMailList).ServeHTTP)

	// Notes (Outlook sticky notes): backed by the Notes folder as IPM.StickyNote
	// messages, shared with EWS/IMAP/JMAP. Requires the semcore store to be wired
	// (so a webmail-created note is visible to EWS too).
	if s.notesHandler == nil && s.semStore != nil && s.mutationPipe != nil && s.msgStore != nil && s.mailDB != nil {
		s.notesHandler = NewNotesHandler()
		s.notesHandler.SetStores(s.msgStore, s.mailDB, s.semStore.Identity(), s.mutationPipe)
	}
	if s.notesHandler != nil {
		api.HandleFunc("/api/v1/notes", http.HandlerFunc(s.notesHandler.handleNotes).ServeHTTP)
		api.HandleFunc("/api/v1/notes/", http.HandlerFunc(s.notesHandler.handleNoteDetail).ServeHTTP)
	}

	// User folder management (create/rename/delete custom mailboxes).
	api.HandleFunc("/api/v1/folders", s.handleFolders)
	api.HandleFunc("/api/v1/folders/", s.handleFolderPath)
	api.HandleFunc("/api/v1/public-folders", s.handlePublicFolders)

	// Saved searches (persistent MAPI-style search folders).
	api.HandleFunc("/api/v1/search-folders", s.handleSearchFolders)
	api.HandleFunc("/api/v1/search-folders/", s.handleSearchFolderPath)

	// Mailbox ACL and shared mailbox access
	api.HandleFunc("/api/v1/mailboxes", s.handleMailboxListOwn)
	api.HandleFunc("/api/v1/mailboxes/shared", s.handleSharedMailboxesList)
	api.HandleFunc("/api/v1/mailboxes/shared-as-owner", s.handleGranteesMailboxesList)
	api.HandleFunc("/api/v1/mailboxes/", s.handleMailboxPath)

	// Contacts API (CardDAV-backed)
	if s.contactsHandler == nil && s.config.DataDir != "" {
		s.contactsHandler = NewContactsHandler(s.config.DataDir)
	}
	// User-facing organization directory (GAL) lookup for recipient autocomplete.
	api.HandleFunc("/api/v1/directory", s.handleDirectorySearch)
	// Profile photos: read any colleague's avatar (GAL scope), manage your own.
	api.HandleFunc("/api/v1/avatar", s.handleAvatarGet)
	api.HandleFunc("/api/v1/profile/avatar", s.handleProfileAvatar)
	// Self-service directory profile (display name, title, department, phone).
	api.HandleFunc("/api/v1/profile", s.handleProfile)
	// Bookable rooms for the calendar room picker.
	api.HandleFunc("/api/v1/rooms", s.handleRooms)
	if s.contactsHandler != nil {
		// Dispatch by method: POST creates a contact, GET lists them. Previously
		// POST fell through to the list handler, so created contacts never
		// persisted from the client's perspective.
		api.HandleFunc("/api/v1/contacts", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				s.contactsHandler.handleContactCreate(w, r)
				return
			}
			s.contactsHandler.handleContactsList(w, r)
		})
		api.HandleFunc("/api/v1/contacts/", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPut:
				s.contactsHandler.handleContactUpdate(w, r)
			case http.MethodDelete:
				s.contactsHandler.handleContactDelete(w, r)
			default:
				s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
		})
	}

	// Calendar events (bridged to the same CalDAV store as the protocol server).
	if s.calendarHandler == nil && s.config.DataDir != "" {
		s.calendarHandler = NewCalendarHandler(s.config.DataDir)
		s.calendarHandler.SetRoomLookup(s.roomLookup)
	}
	if s.calendarHandler != nil {
		api.HandleFunc("/api/v1/calendar/events", s.calendarHandler.handleCalendarEvents)
		api.HandleFunc("/api/v1/calendar/events/", s.calendarHandler.handleCalendarEventDetail)
		api.HandleFunc("/api/v1/calendar/freebusy", s.calendarHandler.handleFreeBusy)
	}

	// Tasks (VTODO items in the CalDAV store).
	if s.taskHandler == nil && s.config.DataDir != "" {
		s.taskHandler = NewTaskHandler(s.config.DataDir)
	}
	s.wireCollabTaskStore()
	if s.taskHandler != nil {
		api.HandleFunc("/api/v1/tasks", s.taskHandler.handleTasks)
		api.HandleFunc("/api/v1/tasks/", s.taskHandler.handleTaskDetail)
	}

	// Wrap API with auth middleware and mount to main mux
	apiHandler := s.rateLimitMiddleware(s.limitBodyMiddleware(s.securityHeadersMiddleware(s.csrfMiddleware(s.corsMiddleware(s.authMiddleware(api))))))
	mux.Handle("/api/v1/", apiHandler)

	// Real Exchange (IIS) routes request paths case-insensitively; Outlook for Mac
	// probes mixed-case variants of the interop endpoints (e.g. /ews/Exchange.asmx,
	// /Autodiscover/Autodiscover.xml). Canonicalize those before the case-sensitive
	// mux so they reach the right handler instead of the SPA catch-all (which would
	// return HTML and make Outlook treat EWS as broken and loop on autodiscover).
	s.router = normalizeExchangePath(mux)
}

// exchangeCanonicalPaths are the Outlook/Exchange interop endpoints whose request
// paths must be matched case-insensitively, paired with their canonical mux form.
var exchangeCanonicalPaths = []string{
	"/autodiscover/autodiscover.xml",
	"/EWS/Exchange.asmx/s/GetUserPhoto",
	"/EWS/Exchange.asmx",
	"/mapi/nspi",
	"/mapi/oab",
	"/mapi/emsmdb",
}

// normalizeExchangePath rewrites a case-variant Exchange interop path to its
// canonical form so the case-sensitive ServeMux routes it correctly. Non-Exchange
// paths pass through untouched.
func normalizeExchangePath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, canon := range exchangeCanonicalPaths {
			if r.URL.Path != canon && strings.EqualFold(r.URL.Path, canon) {
				r.URL.Path = canon
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) registerAdminAPIRoutes(api *http.ServeMux) {
	// Tenant-scoped admin surfaces: reachable by a super-admin OR a self-service
	// tenant-admin. Each handler enforces the caller's tenant scope internally
	// (a tenant-admin only sees/touches resources in its own tenant's domains).
	api.HandleFunc("/api/v1/tenants", s.tenantAdminMiddleware(http.HandlerFunc(s.handleTenants)).ServeHTTP)
	api.HandleFunc("/api/v1/tenants/", s.tenantAdminMiddleware(http.HandlerFunc(s.handleTenantDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/domains", s.tenantAdminMiddleware(http.HandlerFunc(s.handleDomains)).ServeHTTP)
	api.HandleFunc("/api/v1/domains/", s.tenantAdminMiddleware(http.HandlerFunc(s.handleDomainDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/accounts", s.tenantAdminMiddleware(http.HandlerFunc(s.handleAccounts)).ServeHTTP)
	api.HandleFunc("/api/v1/accounts/", s.tenantAdminMiddleware(http.HandlerFunc(s.handleAccountDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/aliases", s.tenantAdminMiddleware(http.HandlerFunc(s.handleAliases)).ServeHTTP)
	api.HandleFunc("/api/v1/aliases/", s.tenantAdminMiddleware(http.HandlerFunc(s.handleAliasDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/groups", s.tenantAdminMiddleware(http.HandlerFunc(s.handleMailGroups)).ServeHTTP)
	api.HandleFunc("/api/v1/groups/", s.tenantAdminMiddleware(http.HandlerFunc(s.handleMailGroupDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/queue", s.tenantAdminMiddleware(http.HandlerFunc(s.handleQueue)).ServeHTTP)
	api.HandleFunc("/api/v1/queue/", s.tenantAdminMiddleware(http.HandlerFunc(s.handleQueueDetail)).ServeHTTP)
	// Backup management is an infrastructure surface: super-admin only (a backup
	// captures another user's entire mailbox), served from the admin router so
	// the admin SPA reaches it same-origin on a separate admin listener.
	// Admin-authored global mail rules (apply to all mailboxes, compiled ahead of
	// each user's own rules). Admin-only, served on the admin listener.
	api.HandleFunc("/api/v1/admin/global-rules", s.adminMiddleware(http.HandlerFunc(s.handleGlobalRules)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/global-rules/", s.adminMiddleware(http.HandlerFunc(s.handleGlobalRuleDetail)).ServeHTTP)

	// TLS certificate inventory (domain, validity, expiry) for the admin panel.
	api.HandleFunc("/api/v1/admin/tls/certificates", s.adminMiddleware(http.HandlerFunc(s.handleTLSCertificates)).ServeHTTP)

	// Public-folder tree management: list/create/delete per-domain public folders
	// and edit their ACL grants. Admin-only, served on the admin listener.
	api.HandleFunc("/api/v1/admin/public-folders", s.adminMiddleware(http.HandlerFunc(s.handlePublicFoldersAdmin)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/public-folders/acl", s.adminMiddleware(http.HandlerFunc(s.handlePublicFolderACL)).ServeHTTP)

	api.HandleFunc("/api/v1/backups", s.adminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.handleBackupCreate(w, r)
			return
		}
		s.handleBackupList(w, r)
	})).ServeHTTP)
	api.HandleFunc("/api/v1/backups/", s.adminMiddleware(http.HandlerFunc(s.handleBackupPath)).ServeHTTP)
	api.HandleFunc("/api/v1/backups/per-user/", s.adminMiddleware(http.HandlerFunc(s.handlePerUserBackup)).ServeHTTP)
	api.HandleFunc("/api/v1/backups/per-mailbox/", s.adminMiddleware(http.HandlerFunc(s.handlePerMailboxBackup)).ServeHTTP)
	api.HandleFunc("/api/v1/backup-jobs", s.adminMiddleware(http.HandlerFunc(s.handleBackupJobList)).ServeHTTP)
	api.HandleFunc("/api/v1/backup-jobs/", s.adminMiddleware(http.HandlerFunc(s.handleBackupJobPath)).ServeHTTP)

	// Cluster management (HA) is an infrastructure surface: super-admin only,
	// served from the admin router so the admin SPA reaches it same-origin on a
	// separate admin listener.
	api.HandleFunc("/api/v1/cluster/status", s.adminMiddleware(http.HandlerFunc(s.handleClusterStatus)).ServeHTTP)
	api.HandleFunc("/api/v1/cluster/instances", s.adminMiddleware(http.HandlerFunc(s.handleClusterInstances)).ServeHTTP)
	api.HandleFunc("/api/v1/cluster/failover", s.adminMiddleware(http.HandlerFunc(s.handleClusterFailover)).ServeHTTP)
	api.HandleFunc("/api/v1/cluster/heartbeat", s.adminMiddleware(http.HandlerFunc(s.handleClusterHeartbeat)).ServeHTTP)
	api.HandleFunc("/api/v1/metrics", s.adminMiddleware(http.HandlerFunc(s.handleMetrics)).ServeHTTP)
	api.HandleFunc("/api/v1/stats", s.tenantAdminMiddleware(http.HandlerFunc(s.handleStats)).ServeHTTP)
	// Rate-limit CONFIG lives in the settings DTO (PUT /api/v1/admin/config); a
	// separate runtime-only write endpoint here would not persist and would be a
	// second source of truth. Only the read-only per-IP/per-user stats remain.
	api.HandleFunc("/api/v1/admin/ratelimits/ip/", s.adminMiddleware(http.HandlerFunc(s.handleRateLimitIPStats)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/ratelimits/user/", s.adminMiddleware(http.HandlerFunc(s.handleRateLimitUserStats)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/vacations", s.adminMiddleware(http.HandlerFunc(s.handleAdminVacations)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/push/stats", s.adminMiddleware(http.HandlerFunc(s.handleAdminPushStats)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/jwt/rotate", s.adminMiddleware(http.HandlerFunc(s.handleJWTRotate)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/jwt/status", s.adminMiddleware(http.HandlerFunc(s.handleJWTStatus)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/queue", s.adminMiddleware(http.HandlerFunc(s.handleQueue)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/queue/", s.adminMiddleware(http.HandlerFunc(s.handleQueueDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/delegations", s.adminMiddleware(http.HandlerFunc(s.handleDelegations)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/delegations/", s.adminMiddleware(http.HandlerFunc(s.handleDelegationDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/directory", s.adminMiddleware(http.HandlerFunc(s.handleDirectory)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/directory/", s.adminMiddleware(http.HandlerFunc(s.handleDirectoryDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/rules", s.adminMiddleware(http.HandlerFunc(s.handleAdminRules)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/rules/", s.adminMiddleware(http.HandlerFunc(s.handleAdminRuleDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/diagnostics", s.adminMiddleware(http.HandlerFunc(s.handleAdminDiagnostics)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/diagnostics/", s.adminMiddleware(http.HandlerFunc(s.handleAdminDiagnosticsDetail)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/domains/", s.adminMiddleware(http.HandlerFunc(s.handleAdminDNSHealth)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/sync/activity", s.adminMiddleware(http.HandlerFunc(s.handleAdminSyncActivity)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/logs", s.adminMiddleware(http.HandlerFunc(s.handleAdminLogs)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/logs/tail", s.adminMiddleware(http.HandlerFunc(s.handleAdminLogsTail)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/jobs", s.adminMiddleware(http.HandlerFunc(s.handleAdminJobs)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/config", s.adminMiddleware(http.HandlerFunc(s.handleConfig)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/roles", s.adminMiddleware(http.HandlerFunc(s.handleAdminRoles)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/roles/permissions", s.adminMiddleware(http.HandlerFunc(s.handleAdminRolePermissions)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/roles/", s.adminMiddleware(http.HandlerFunc(s.handleAdminRoleByID)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/accounts/", s.adminMiddleware(http.HandlerFunc(s.handleAdminAccountRoles)).ServeHTTP)
	api.HandleFunc("/api/v1/admin/ldap/", s.adminMiddleware(http.HandlerFunc(s.handleLDAPSync)).ServeHTTP)
}

// limitBodyMiddleware restricts request body size to prevent DoS.
func (s *Server) limitBodyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			limit := int64(4 << 20)
			if r.URL.Path == "/api/v1/mail/send" {
				limit = 32 << 20
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware enforces API rate limiting per IP.
// Exempts health check and authentication endpoints.
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip rate limiting for health checks (probes must not be throttled)
		if r.URL.Path == "/health" || r.URL.Path == "/health/ready" {
			next.ServeHTTP(w, r)
			return
		}

		// Get client IP (respects X-Forwarded-For from trusted proxies)
		ip := getClientIP(r, s.config.TrustedProxies)

		if !s.checkAPIRateLimit(ip) {
			s.logger.Warn("API rate limit exceeded",
				slog.String("ip", ip),
				slog.String("path", r.URL.Path),
			)
			s.sendError(w, http.StatusTooManyRequests, "rate limit exceeded, try again later")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// SetSearchService injects the search service into the API server
func (s *Server) SetSearchService(svc *search.Service) {
	s.searchSvc = svc
}

// SetRateLimitManager injects the rate limit manager into the API server
func (s *Server) SetRateLimitManager(mgr RateLimitManager) {
	s.rateLimitMgr = mgr
}

// SetTracingProvider sets the OpenTelemetry tracing provider
func (s *Server) SetTracingProvider(provider *tracing.Provider) {
	s.tracingProvider = provider
}

// SetClusterManager injects the cluster manager into the API server
func (s *Server) SetClusterManager(mgr *cluster.ClusterManager, cfg *ClusterConfig) {
	s.clusterMgr = mgr
	s.clusterConfig = cfg
}

// SetBackupManager injects the backup manager into the API server
func (s *Server) SetBackupManager(mgr *backup.Manager) {
	s.backupMgr = mgr
}

// SetSemcoreStore injects the canonical semantic-core store (as the SemanticStore
// interface) and the canonical mutation pipeline so admin surfaces (delegation,
// directory/resources, rules, jobs) and the notes handler can reach the persisted
// domain models. The store is bridged from the bbolt-backed *semcore.Store via
// BoltSemanticStore; a relational aggregate provides its own SemanticStore.
func (s *Server) SetSemcoreStore(store SemanticStore, pipe *semcore.MutationPipeline) {
	s.semStore = store
	s.mutationPipe = pipe
	// Build a read-only view over the durable-job bucket so the admin Jobs
	// endpoint can list job records. This creates the bucket if absent; jobs
	// remain empty until a scheduler populates them.
	if store != nil {
		if js, err := store.NewJobStore(); err == nil {
			s.jobStore = js
		}
	}
	// Point the webmail calendar and contacts at the canonical collaboration
	// store so they share one source of truth with EWS and CalDAV/CardDAV.
	s.wireCollabCalendarStore()
	s.wireCollabContactsStore()
	s.wireCollabTaskStore()
}

// SetSieveManager injects the runtime Sieve manager so the webmail filter
// endpoints can recompile and install a user's active Sieve script after they
// mutate canonical inbox rules. When nil, rule mutations are persisted but no
// Sieve recompile occurs.
func (s *Server) SetSieveManager(mgr *sieve.Manager) {
	s.sieveManager = mgr
}

// SetACMEChallengeHandler injects the ACME HTTP-01 challenge handler.
func (s *Server) SetACMEChallengeHandler(handler http.Handler) {
	s.acmeChallengeHandler = handler
	s.router = nil
}

// SetTLSConfig makes the primary listener serve HTTPS using cfg (whose
// GetCertificate callback resolves certificates live). Passing nil keeps the
// listener on plain HTTP. Must be called before Start.
func (s *Server) SetTLSConfig(cfg *tls.Config) {
	s.tlsConfig = cfg
}

// serveMaybeTLS serves srv over TLS when tlsCfg is non-nil, otherwise over plain
// HTTP. On the TLS path HTTP/2 is deliberately left disabled (TLSNextProto is an
// empty, non-nil map): the EWS/MAPI surfaces carry connection-oriented HTTP-layer
// NTLM whose 401→401→200 handshake assumes one auth exchange per TCP connection,
// and HTTP/2 stream multiplexing over a single connection would break that.
func serveMaybeTLS(srv *http.Server, tlsCfg *tls.Config) error {
	if tlsCfg != nil {
		srv.TLSConfig = tlsCfg
		srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
		return srv.ListenAndServeTLS("", "")
	}
	return srv.ListenAndServe()
}

// AuditLogger exposes the underlying audit logger so other subsystems
// (SMTP/IMAP/POP3) can record protocol-level auth events into the same sink.
func (s *Server) AuditLogger() *audit.Logger {
	return s.auditLogger
}

// Start starts the API server
func (s *Server) Start(addr string) error {
	s.config.Addr = addr

	// HTTP-layer NTLM on the RPC proxy is connection-oriented, so every
	// connection carries its own NTLM challenge state (see ntlmConnState).
	ntlmConnContext := func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, ntlmConnKey{}, &ntlmConnState{})
	}

	primaryHTTPServer := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ConnContext:       ntlmConnContext,
	}

	var plainHTTPServer *http.Server
	if s.config.PlainAddr != "" {
		plainHTTPServer = &http.Server{
			Addr:              s.config.PlainAddr,
			Handler:           s,
			ReadTimeout:       30 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			ConnContext:       ntlmConnContext,
		}
	}

	s.serverMu.Lock()
	s.httpServer = primaryHTTPServer
	s.plainHTTPServer = plainHTTPServer
	s.serverMu.Unlock()

	// Start background token blacklist cleanup
	go s.tokenBlacklistCleanup()

	if plainHTTPServer != nil {
		go func() {
			s.logger.Info("Plain API server starting", "addr", plainHTTPServer.Addr)
			if err := plainHTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				s.logger.Error("Plain API server error", "error", err, "addr", plainHTTPServer.Addr)
			}
		}()
	}

	if s.tlsConfig != nil {
		s.logger.Info("API server serving HTTPS", "addr", addr)
	} else {
		s.logger.Info("Admin API server starting", "addr", addr)
	}
	return serveMaybeTLS(primaryHTTPServer, s.tlsConfig)
}

// tokenBlacklistCleanup periodically removes expired entries from the token blacklist
func (s *Server) tokenBlacklistCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.CleanupExpiredTokens()
		}
	}
}

// Stop gracefully stops the API server
func (s *Server) Stop() error {
	// Signal background tasks to stop (sync.Once ensures only one call)
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})

	s.StartDrain()
	s.DrainWait(s.config.DrainTimeout)

	s.serverMu.Lock()
	httpServer := s.httpServer
	plainHTTPServer := s.plainHTTPServer
	s.serverMu.Unlock()

	shutdownServer := func(server *http.Server) error {
		if server == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()
		return server.Shutdown(ctx)
	}

	if err := shutdownServer(plainHTTPServer); err != nil {
		return err
	}
	if err := shutdownServer(httpServer); err != nil {
		return err
	}
	return nil
}

// StartDrain initiates graceful draining mode.
// After this is called, /health/ready returns 503 and new requests are rejected.
// Returns a function that waits for all active requests to complete.
// Call this before Stop() for zero-downtime deployments.
func (s *Server) StartDrain() func() {
	s.draining.Store(true)
	return func() {
		// Wait for in-flight requests to complete
		// This is a simple implementation - for production, track active connections
		// The actual connection tracking would use middleware that increments/decrements a counter
	}
}

// DrainWait waits for all active requests to complete.
// timeout is the maximum time to wait before forcing close.
func (s *Server) DrainWait(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for s.activeRequests() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
}

// activeRequests returns the number of currently active requests
// This is a placeholder - real implementation would use atomic counter
func (s *Server) activeRequests() int {
	return 0
}

// decodeJSON decodes JSON from request body with DisallowUnknownFields
// to reject requests with unknown fields
func decodeJSON(r *http.Request, v interface{}) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

// Middleware

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := ""
		for _, o := range s.config.CorsOrigins {
			if o == origin {
				allowed = o
				break
			}
		}
		if allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// authResult carries the validated identity extracted from a request's JWT.
type authResult struct {
	user               string
	isAdmin            bool
	mustChangePassword bool
	tokenHash          string
	// tenantID is the tenant that owns the caller's domain; isTenantAdmin marks
	// a self-service admin scoped to that tenant. Both are empty/false on legacy
	// tokens minted before multi-tenancy and for the global super-admin (isAdmin).
	tenantID      string
	isTenantAdmin bool
}

// authenticateRequest extracts and validates the request's JWT (the HttpOnly
// "jwt" cookie is preferred, then a Bearer Authorization header). On success it
// returns the identity and ok=true; on any failure it returns ok=false with a
// reason string suitable for a 401 body. It writes nothing to the response, so
// callers decide how to react: authMiddleware rejects with the reason, while
// handleMe answers a soft 200 so the login screen never logs a 401.
func (s *Server) authenticateRequest(r *http.Request) (authResult, string, bool) {
	// Get token from HttpOnly cookie first (preferred for web clients)
	var tokenStr string
	if cookie, err := r.Cookie("jwt"); err == nil && cookie.Value != "" {
		tokenStr = cookie.Value
	} else {
		// Fall back to Authorization header (for API clients)
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			return authResult{}, "missing authorization", false
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			return authResult{}, "invalid authorization header format", false
		}
		tokenStr = parts[1]
	}

	if tokenStr == "" {
		return authResult{}, "missing token", false
	}

	// Validate token
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		// Try kid-based secret lookup first
		if kid, ok := token.Header["kid"].(string); ok && kid != "" {
			if kidSecret, ok := s.jwtSecrets[kid]; ok {
				return []byte(kidSecret), nil
			}
		}
		// Fall back to current kid
		if secret, ok := s.jwtSecrets[s.currentKid]; ok {
			return []byte(secret), nil
		}
		// Last resort: try legacy JWTSecret only if not disabled
		if !s.config.DisableLegacyJWT {
			return []byte(s.config.JWTSecret), nil
		}
		return nil, fmt.Errorf("unknown signing key")
	}, jwt.WithValidMethods([]string{"HS256"}))

	if err != nil || !token.Valid {
		return authResult{}, "invalid token", false
	}

	// Check if token is revoked (logout)
	tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(tokenStr)))
	if s.IsTokenRevoked(tokenHash) {
		return authResult{}, "token has been revoked", false
	}

	// Get claims
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return authResult{}, "invalid token claims", false
	}

	user, hasSub := claims["sub"].(string)
	if !hasSub || user == "" {
		return authResult{}, "invalid token claims", false
	}
	// admin and the password-change marker are optional claims: absent or of an
	// unexpected type means false, so the assertion's ok result is handled here
	// rather than discarded.
	isAdmin := false
	if v, ok := claims["admin"].(bool); ok {
		isAdmin = v
	}
	mustChangePasswordClaim := false
	if v, ok := claims[passwordChangeRequiredClaim].(bool); ok {
		mustChangePasswordClaim = v
	}
	mustChangePassword, err := enforceAuthenticatedAccount(s.db, user, mustChangePasswordClaim)
	if err != nil {
		return authResult{}, "invalid token", false
	}

	// Tenant scope (optional claims; absent on legacy/super-admin tokens).
	tenantID := ""
	if v, ok := claims["tenant"].(string); ok {
		tenantID = v
	}
	isTenantAdmin := false
	if v, ok := claims["tenant_admin"].(bool); ok {
		isTenantAdmin = v
	}

	return authResult{
		user:               user,
		isAdmin:            isAdmin,
		mustChangePassword: mustChangePassword,
		tokenHash:          tokenHash,
		tenantID:           tenantID,
		isTenantAdmin:      isTenantAdmin,
	}, "", true
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health, login, and the soft session-check endpoint.
		// /auth/me validates the token itself and answers 200 either way, so it
		// must not be rejected here (that would log a 401 on the login screen).
		if r.URL.Path == "/health" || r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/me" {
			next.ServeHTTP(w, r)
			return
		}

		res, reason, ok := s.authenticateRequest(r)
		if !ok {
			s.sendError(w, http.StatusUnauthorized, reason)
			return
		}

		// Add claims to context. These string keys are read by every downstream
		// handler via r.Context().Value("user"/"isAdmin"/"mustChangePassword"),
		// so the key type must stay a plain string for compatibility.
		ctx := context.WithValue(r.Context(), "user", res.user)                    //nolint:staticcheck // shared string context key read across all handlers
		ctx = context.WithValue(ctx, "isAdmin", res.isAdmin)                       //nolint:staticcheck // shared string context key read across all handlers
		ctx = context.WithValue(ctx, "mustChangePassword", res.mustChangePassword) //nolint:staticcheck // shared string context key read across all handlers
		ctx = context.WithValue(ctx, contextKeyTokenHash, res.tokenHash)
		ctx = context.WithValue(ctx, contextKeyTenantID, res.tenantID)
		ctx = context.WithValue(ctx, contextKeyTenantAdmin, res.isTenantAdmin)

		if res.mustChangePassword && !isPasswordChangeOnlyRoute(r, res.user) {
			s.sendJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":                "password_change_required",
				"must_change_password": true,
			})
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// adminMiddleware wraps a handler to require admin role.
// Must be used after authMiddleware so that "isAdmin" is in context.
func (s *Server) adminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isAdmin, ok := r.Context().Value("isAdmin").(bool)
		if !ok || !isAdmin {
			s.sendJSON(w, http.StatusForbidden, map[string]string{
				"error": "admin access required",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tenantAdminMiddleware allows a global super-admin OR a self-service
// tenant-admin through. Handlers behind it MUST still scope every resource to
// the caller's tenant (callerTenantScope/allowsDomain) — this gate only
// establishes that the caller holds some admin authority, not which tenant's
// data they may touch. Must run after authMiddleware.
func (s *Server) tenantAdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts := s.callerTenantScope(r)
		if !ts.isSuperAdmin && !ts.isTenantAdmin {
			s.sendJSON(w, http.StatusForbidden, map[string]string{
				"error": "admin access required",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// permissionMiddleware returns a middleware that requires the caller to have a specific
// permission through an RBAC role. It must run after authMiddleware so that the
// caller's user ID is in context.
//
//nolint:unused
func (s *Server) permissionMiddleware(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := r.Context().Value("user").(string)
			if !ok || user == "" {
				s.sendError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			roles, err := s.db.GetUserRoles(user)
			if err != nil {
				s.sendError(w, http.StatusInternalServerError, "failed to load roles")
				return
			}
			for _, role := range roles {
				perms, err := s.db.GetRolePermissions(role.ID)
				if err != nil {
					continue
				}
				for _, p := range perms {
					if p.Permission == permission {
						next.ServeHTTP(w, r)
						return
					}
				}
			}
			s.sendJSON(w, http.StatusForbidden, map[string]string{
				"error": "insufficient permissions",
			})
		})
	}
}

// Handlers

func (s *Server) handleWebmail(w http.ResponseWriter, r *http.Request) {
	// Use injected webmail FS
	webmailFS := s.webmailFS
	if webmailFS == nil {
		webmailFS = NewEmbedFSAdapter(umailserver.WebmailFS)
	}

	// Also use admin FS for fallback (shared /assets/ paths)
	adminFS := s.adminFS
	if adminFS == nil {
		adminFS = NewEmbedFSAdapter(umailserver.AdminFS)
	}

	// Handle SPA routing - serve index.html for non-existent paths
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}

	// Try to open the file from webmail FS
	file, err := webmailFS.Open(path)
	if err != nil {
		// Fallback: check admin FS for shared assets (e.g., /assets/...)
		// Close the webmail file first if it was opened
		if file != nil {
			_ = file.Close()
		}
		file, err = adminFS.Open(path)
		if err != nil {
			// If file not found, serve index.html for SPA routing
			// Close the admin file first if it was opened
			if file != nil {
				_ = file.Close()
			}
			file, err = webmailFS.Open("index.html")
			if err != nil {
				s.logger.Error("Failed to open index.html", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			path = "index.html"
		}
	}
	defer file.Close()

	// Set content type based on file extension
	if strings.HasSuffix(path, ".html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	} else if strings.HasSuffix(path, ".js") {
		w.Header().Set("Content-Type", "application/javascript")
	} else if strings.HasSuffix(path, ".css") {
		w.Header().Set("Content-Type", "text/css")
	} else if strings.HasSuffix(path, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
	}

	// Serve file content with cache headers (ServeContent handles If-None-Match
	// 304, Range, and HEAD).
	serveStaticContent(w, r, path, file)
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	// Use injected admin FS
	adminFS := s.adminFS
	if adminFS == nil {
		adminFS = NewEmbedFSAdapter(umailserver.AdminFS)
	}

	// Handle SPA routing - serve index.html for non-existent paths
	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	if path == "" {
		path = "index.html"
	}

	// Try to open the file
	file, err := adminFS.Open(path)
	if err != nil {
		// If file not found, serve index.html for SPA routing
		// Close the first file if it was opened
		if file != nil {
			_ = file.Close()
		}
		file, err = adminFS.Open("index.html")
		if err != nil {
			s.logger.Error("Failed to open index.html", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		path = "index.html"
	}
	defer file.Close()

	// Set content type based on file extension
	if strings.HasSuffix(path, ".html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	} else if strings.HasSuffix(path, ".js") {
		w.Header().Set("Content-Type", "application/javascript")
	} else if strings.HasSuffix(path, ".css") {
		w.Header().Set("Content-Type", "text/css")
	} else if strings.HasSuffix(path, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
	}

	// Serve file content with cache headers (ServeContent handles If-None-Match
	// 304, Range, and HEAD).
	serveStaticContent(w, r, path, file)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get actual metrics from metrics collector
	stats := metrics.Get().GetStats()

	// Add queue stats if queue manager is available
	if s.queueMgr != nil {
		if queueStats, err := s.queueMgr.GetStats(); err == nil {
			stats["queue"] = map[string]int{
				"pending":   queueStats.Pending,
				"sending":   queueStats.Sending,
				"failed":    queueStats.Failed,
				"delivered": queueStats.Delivered,
				"bounced":   queueStats.Bounced,
				"total":     queueStats.Total,
			}
		}
	}

	s.sendJSON(w, http.StatusOK, stats)
}

// SetQueueManager injects the queue manager for stats
func (s *Server) SetQueueManager(qm *queue.Manager) {
	s.queueMgr = qm
}

// SetHealthMonitor sets the health monitor for health endpoints
func (s *Server) SetHealthMonitor(mon HealthMonitor) {
	s.healthMon = mon
}

// SetMailDB sets the mail database for email operations
func (s *Server) SetMailDB(db MailStore) {
	s.mailDB = db
	s.initMailHandler()
}

// SetPublicFoldersEnabled wires the live gate for the per-domain public-folder
// tree onto the API server and its mail handler, so the discovery endpoint and
// the per-folder access check honor a hot-reload toggle without a restart.
func (s *Server) SetPublicFoldersEnabled(fn func() bool) {
	s.publicFoldersEnabled = fn
	if s.mailHandler != nil {
		s.mailHandler.SetPublicFoldersEnabled(fn)
	}
}

// SetNTLMEnabled wires the live MAPI/HTTP NTLM gate onto the API server and the
// MCP server, so capturing the per-account NT hash at password-set and login
// time honors a hot-reload toggle without a restart.
func (s *Server) SetNTLMEnabled(fn func() bool) {
	s.ntlmEnabled = fn
	if s.mcpServer != nil {
		s.mcpServer.SetNTLMEnabled(fn)
	}
}

// ntlmHashEnabled reports whether the NT hash should be captured for stored
// accounts, defaulting to off when no gate is wired.
func (s *Server) ntlmHashEnabled() bool {
	return s.ntlmEnabled != nil && s.ntlmEnabled()
}

// SetMsgStore sets the message store for email operations
func (s *Server) SetMsgStore(msgStore *storage.MessageStore) {
	s.msgStore = msgStore
	s.initMailHandler()
}

// SetMailDeliveryFunc wires the shared outbound delivery path so webmail send
// actually delivers to recipients (local + relay), not just files a Sent copy.
func (s *Server) SetMailDeliveryFunc(fn func(from string, to []string, data []byte) error) {
	s.mailDeliver = fn
	s.initMailHandler()
}

// SetMailCrossProtocolFuncs wires the tri-store filer + idempotent semcore
// remover so webmail mail mutations stay cross-protocol consistent (visible in
// and removed from EWS, not just IMAP/webmail).
func (s *Server) SetMailCrossProtocolFuncs(
	file func(owner, folder string, raw []byte, flags []string) (uint32, string, error),
	remove func(owner, folder, blobKey string),
) {
	s.mailFileCopy = file
	s.mailRemoveCopy = remove
	s.initMailHandler()
}

// SetRecoverableCaptureFunc wires the soft-delete dumpster capture used by a
// webmail permanent delete: when it captures the message into Recoverable Items,
// the handler retains the shared blob for restore.
func (s *Server) SetRecoverableCaptureFunc(capture func(owner, srcFolder string, raw []byte) bool) {
	s.mailRecoverCapture = capture
	s.initMailHandler()
}

// SetRecoverFunc wires the dumpster restore path used by POST /api/v1/mail/recover.
func (s *Server) SetRecoverFunc(fn func(owner, id string) (string, error)) {
	s.mailRecover = fn
	s.initMailHandler()
}

// SetScheduledFuncs wires the "send later" hooks webmail uses: schedule a future
// send, list the caller's scheduled messages, and cancel one by id.
func (s *Server) SetScheduledFuncs(
	schedule func(owner, from string, to []string, data []byte, sendAt time.Time) (string, error),
	list func(owner string) ([]ScheduledMailItem, error),
	cancel func(owner, id string) error,
) {
	s.mailSchedule = schedule
	s.mailScheduledList = list
	s.mailScheduledCancel = cancel
	s.initMailHandler()
}

// initMailHandler initializes the mail handler with storage backends
func (s *Server) initMailHandler() {
	if s.mailHandler == nil && (s.msgStore != nil || s.mailDB != nil) {
		s.mailHandler = NewMailHandler()
		s.mailHandler.SetStorage(s.msgStore, s.mailDB)
	} else if s.mailHandler != nil {
		s.mailHandler.SetStorage(s.msgStore, s.mailDB)
	}
	if s.mailHandler != nil && s.mailDeliver != nil {
		s.mailHandler.SetDeliveryFunc(s.mailDeliver)
	}
	if s.mailHandler != nil && s.publicFoldersEnabled != nil {
		s.mailHandler.SetPublicFoldersEnabled(s.publicFoldersEnabled)
	}
	s.applyScheduledFuncs()
}

// applyScheduledFuncs binds the injected "send later" hooks onto the mail
// handler. Safe to call repeatedly; each is applied only when set.
func (s *Server) applyScheduledFuncs() {
	if s.mailHandler == nil {
		return
	}
	if s.mailSchedule != nil {
		s.mailHandler.SetScheduleFunc(s.mailSchedule)
	}
	if s.mailScheduledList != nil {
		s.mailHandler.SetScheduledListFunc(s.mailScheduledList)
	}
	if s.mailScheduledCancel != nil {
		s.mailHandler.SetScheduledCancelFunc(s.mailScheduledCancel)
	}
	if s.mailFileCopy != nil || s.mailRemoveCopy != nil {
		s.mailHandler.SetCrossProtocolFuncs(s.mailFileCopy, s.mailRemoveCopy)
	}
	if s.mailRecoverCapture != nil {
		s.mailHandler.SetRecoverableCaptureFunc(s.mailRecoverCapture)
	}
	if s.mailRecover != nil {
		s.mailHandler.SetRecoverFunc(s.mailRecover)
	}
}

// SetContactsDataDir initializes the contacts handler with the data directory
func (s *Server) SetContactsDataDir(dataDir string) {
	s.contactsHandler = NewContactsHandler(dataDir)
	s.wireCollabContactsStore()
}

// SetCalendarDataDir initializes the calendar handler with the data directory.
func (s *Server) SetCalendarDataDir(dataDir string) {
	fn := s.calendarDeliver
	s.calendarHandler = NewCalendarHandler(dataDir)
	if fn != nil {
		s.calendarHandler.SetDeliveryFunc(fn)
	}
	s.calendarHandler.SetRoomLookup(s.roomLookup)
	s.wireCollabCalendarStore()
}

// SetCalendarDeliveryFunc wires the outbound delivery path the calendar uses to
// email meeting invitations to attendees.
func (s *Server) SetCalendarDeliveryFunc(fn func(from string, to []string, data []byte) error) {
	s.calendarDeliver = fn
	if s.calendarHandler != nil {
		s.calendarHandler.SetDeliveryFunc(fn)
	}
}

// SetTaskDataDir initializes the task handler with the data directory.
func (s *Server) SetTaskDataDir(dataDir string) {
	s.taskHandler = NewTaskHandler(dataDir)
}

// SetAPIRateLimit sets the HTTP API rate limit (requests per minute, 0 = disabled)
func (s *Server) SetAPIRateLimit(limit int) {
	s.apiRateMu.Lock()
	defer s.apiRateMu.Unlock()
	s.apiRateLimit = limit
}

// checkAPIRateLimit returns true if the IP is allowed to make API requests.
// Uses sliding window based on HTTPRequestsPerMinute config.
func (s *Server) checkAPIRateLimit(ip string) bool {
	// Snapshot the configured limit under its mutex (SetAPIRateLimit writes it
	// under the same lock); the cluster path must not read it unguarded.
	s.apiRateMu.Lock()
	limit := s.apiRateLimit
	if limit <= 0 {
		s.apiRateMu.Unlock()
		return true // rate limiting disabled
	}

	if cs := s.clusterCounters(); cs != nil {
		s.apiRateMu.Unlock()
		ctx, cancel := rlCtx()
		defer cancel()
		// Fixed 1-minute window: the TTL is set only on the first request of
		// the window, so the count resets when the window expires.
		n, err := cs.IncrFixed(ctx, rlAPIKey(ip), time.Minute)
		if err != nil {
			s.logger.Warn("cluster rate-limit: api incr failed; allowing", "error", err)
			return true
		}
		return n <= int64(limit)
	}

	defer s.apiRateMu.Unlock()

	if s.apiRateAttempts == nil {
		s.apiRateAttempts = make(map[string]*apiRateAttempt)
	}

	now := time.Now()
	attempt, exists := s.apiRateAttempts[ip]

	// Check if window has expired (1 minute window)
	if !exists || now.Sub(attempt.windowStart) > time.Minute {
		s.apiRateAttempts[ip] = &apiRateAttempt{count: 1, windowStart: now}
		return true
	}

	// Check if limit exceeded
	if attempt.count >= s.apiRateLimit {
		return false
	}

	attempt.count++
	return true
}

// Helpers

func (s *Server) sendJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("Failed to encode JSON response", "error", err)
	}
}

func (s *Server) sendError(w http.ResponseWriter, status int, message string) {
	s.sendJSON(w, status, map[string]string{"error": message})
}

// SetEWSHandler configures the EWS SOAP handler on the API server.
// The handler requires the server to have a non-nil *db.DB for Basic Auth validation.
func (s *Server) SetEWSHandler(handler http.Handler) {
	s.ewsHandler = handler
}

// resolveDisplayName returns the configured display name for a local account
// address, or "" when the address is non-local, unknown, or has no display name
// set. It backs the mail API's sender/recipient name resolution.
func (s *Server) resolveDisplayName(email string) string {
	if s.db == nil {
		return ""
	}
	localPart, domain, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || localPart == "" || domain == "" {
		return ""
	}
	account, err := s.db.GetAccount(domain, localPart)
	if err != nil || account == nil {
		return ""
	}
	return account.DisplayName
}

// resolveTimezone returns the IANA timezone the account chose for time
// rendering, or "" when the address is non-local/unknown or the user has not
// set one (caller then falls back to server/UTC time). It backs the outgoing
// mail Date header so a sent message carries the sender's local offset.
func (s *Server) resolveTimezone(email string) string {
	if s.db == nil {
		return ""
	}
	localPart, domain, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || localPart == "" || domain == "" {
		return ""
	}
	account, err := s.db.GetAccount(domain, localPart)
	if err != nil || account == nil {
		return ""
	}
	return account.Timezone
}

// ewsBasicAuth performs HTTP Basic Auth validation against the database.
// It decodes the Authorization header, parses the email and password credentials,
// retrieves the account from the database, and verifies the password hash.
// Returns the authenticated email or an empty string on failure.
func (s *Server) ewsBasicAuth(w http.ResponseWriter, r *http.Request) string {
	if s.db == nil {
		return ""
	}
	authHdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "Basic ") {
		return ""
	}
	encoded := strings.TrimPrefix(authHdr, "Basic ")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return ""
	}
	email, password := parts[0], parts[1]
	localPart, domain, ok := strings.Cut(email, "@")
	if !ok {
		return ""
	}
	account, err := s.db.GetAccount(domain, localPart)
	if err != nil {
		return ""
	}
	if !account.IsActive {
		return ""
	}
	// Block accounts flagged for required password change. This enforces the
	// account-state gate at the EWS entry point so that inactive or
	// password-change-required principals cannot access Exchange-facing
	// surfaces even when delegate rights exist (VAL-DIR-012).
	if account.MustChangePassword {
		return ""
	}
	// A suspended tenant blocks its accounts at the EWS entry point too.
	if s.tenantSuspendedForDomain(domain) {
		return ""
	}
	matches, _ := s.verifyPassword(password, account.PasswordHash)
	if !matches {
		return ""
	}
	return email
}

// SetOABHandler configures the binary MAPI/HTTP Offline Address Book handler on
// the API server, served under /mapi/oab/ behind Basic auth like the other MAPI
// surfaces.
func (s *Server) SetOABHandler(handler http.Handler) {
	s.oabHandler = handler
}

// SetNSPIHandler configures the binary MAPI/HTTP (NSPI) address-book handler on
// the API server, served at /mapi/nspi behind Basic auth like the other MAPI
// surfaces.
func (s *Server) SetNSPIHandler(handler http.Handler) {
	s.nspiHandler = handler
}

// SetEMSMDBHandler configures the binary MAPI/HTTP (emsmdb) mailbox handler on the
// API server, served at /mapi/emsmdb behind Basic auth like the other MAPI
// surfaces.
func (s *Server) SetEMSMDBHandler(handler http.Handler) {
	s.emsmdbHandler = handler
}

// SetRPCHHandler configures the RPC-over-HTTP (Outlook Anywhere) tunnel handler,
// served at /rpc/rpcproxy.dll behind Basic auth like the other MAPI surfaces.
func (s *Server) SetRPCHHandler(handler http.Handler) {
	s.rpchHandler = handler
}

// mapiBasicAuth performs HTTP Basic Auth validation for MAPI/HTTP endpoints.
// It validates credentials and enforces account-state gates (inactive, password-change-required)
// before allowing access to NSPI or OAB surfaces. This satisfies VAL-OUTLOOK-008 by making
// account-state failures explicit at the MAPI/HTTP entry point.
//
// The function returns the authenticated email or an empty string on failure.
func (s *Server) mapiBasicAuth(w http.ResponseWriter, r *http.Request) string {
	if s.db == nil {
		return ""
	}
	authHdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "Basic ") {
		return ""
	}
	encoded := strings.TrimPrefix(authHdr, "Basic ")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return ""
	}
	email, password := parts[0], parts[1]
	localPart, domain, ok := strings.Cut(email, "@")
	if !ok {
		return ""
	}
	account, err := s.db.GetAccount(domain, localPart)
	if err != nil {
		return ""
	}

	// VAL-OUTLOOK-008: Inactive accounts fail explicitly before any mailbox data
	// is returned through MAPI/HTTP.
	if !account.IsActive {
		return ""
	}

	// VAL-OUTLOOK-008: Accounts flagged for required password change fail explicitly.
	// This blocks Outlook reconnection or resumed sessions after admin password reset.
	if account.MustChangePassword {
		return ""
	}

	// A suspended tenant blocks its accounts at the MAPI/HTTP entry point too.
	if s.tenantSuspendedForDomain(domain) {
		return ""
	}

	matches, _ := s.verifyPassword(password, account.PasswordHash)
	if !matches {
		return ""
	}

	// Also enforce the Outlook-tier policy gate: if MAPI/HTTP gate is not enabled,
	// reject at the auth layer so Outlook cannot create a half-working MAPI/HTTP profile.
	tier := semcore.AccountCompatibilityTier(account.CompatibilityTier)
	if tier != semcore.TierOutlook || !semcore.Gate().IsEnabled(semcore.FeatureMAPIHTTP) {
		// Not in Outlook tier with MAPI/HTTP enabled.
		return ""
	}

	return email
}

// mapiNTLMCredential resolves an NTLM (user, domain) identity to the mailbox
// email and its stored NT hash. It applies the same authorization gate as
// mapiBasicAuth minus the password check — NTLM proves the password itself via
// the NT hash — so the RPC-proxy NTLM path cannot bypass the active,
// password-change, tenant-suspension and Outlook-tier policies. It is the NTLM
// counterpart of mapiBasicAuth; keep the two gates aligned. ok is false when the
// account is absent, fails the gate, or has no stored NT hash.
func (s *Server) mapiNTLMCredential(user, domain string) (string, [16]byte, bool) {
	var nt [16]byte
	if s.db == nil {
		return "", nt, false
	}
	email := user
	if !strings.Contains(email, "@") && domain != "" {
		email = user + "@" + domain
	}
	localPart, dom, ok := strings.Cut(email, "@")
	if !ok {
		return "", nt, false
	}
	account, err := s.db.GetAccount(dom, localPart)
	if err != nil || account == nil || account.NTHash == "" {
		return "", nt, false
	}
	if !account.IsActive || account.MustChangePassword || s.tenantSuspendedForDomain(dom) {
		return "", nt, false
	}
	tier := semcore.AccountCompatibilityTier(account.CompatibilityTier)
	if tier != semcore.TierOutlook || !semcore.Gate().IsEnabled(semcore.FeatureMAPIHTTP) {
		return "", nt, false
	}
	raw, err := hex.DecodeString(account.NTHash)
	if err != nil || len(raw) != 16 {
		return "", nt, false
	}
	copy(nt[:], raw)
	return account.Email, nt, true
}

// ntlmTargetName is the server name advertised in the NTLM CHALLENGE target info.
const ntlmTargetName = "UMAILSERVER"

// ntlmFileTimeNow returns the current time as a Windows FILETIME (100ns ticks
// since 1601-01-01), placed in the CHALLENGE target-info timestamp.
func ntlmFileTimeNow() uint64 {
	const epochOffset = 11644473600 // seconds between 1601-01-01 and 1970-01-01
	return uint64(time.Now().Unix()+epochOffset) * 10000000
}

// mapiRPCProxyAuth authenticates an Outlook Anywhere RPC-proxy request at
// /rpc/rpcproxy.dll, returning the resolved mailbox email with ok=true when the
// channel may be tunneled. On failure it has already written a 401 challenge (or
// rejection) and ok is false. Basic-over-TLS is always accepted; HTTP-layer NTLM
// (NTLMSSP carried in the WWW-Authenticate/Authorization headers, RFC 4559) is
// offered and verified only when the NTLM opt-in is live — that is the scheme
// Outlook's and impacket's RPC-proxy clients drive, so the mount cannot be
// anonymous.
func (s *Server) mapiRPCProxyAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	authz := r.Header.Get("Authorization")
	ntlmOn := s.ntlmHashEnabled()

	if ntlmOn && strings.HasPrefix(authz, "NTLM ") {
		return s.mapiNTLMAuth(w, r, strings.TrimPrefix(authz, "NTLM "))
	}
	if email := s.mapiBasicAuth(w, r); email != "" {
		return email, true
	}
	// No usable identity: challenge. Offer NTLM first (preferred by Outlook) when
	// enabled, then Basic so existing Basic-over-TLS clients still authenticate.
	if ntlmOn {
		w.Header().Add("WWW-Authenticate", "NTLM")
	}
	w.Header().Add("WWW-Authenticate", `Basic realm="MAPI/HTTP"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return "", false
}

// mapiNTLMAuth drives one step of the HTTP-layer NTLM handshake for an RPC-proxy
// request. A NEGOTIATE (type 1) is answered with a fresh per-connection CHALLENGE
// in a keep-alive 401, so the AUTHENTICATE arrives on the same connection. An
// AUTHENTICATE (type 3) is verified against the stored NT hash via
// mapiNTLMCredential using that connection's challenge; on success the resolved
// email is returned with ok=true. Any malformed or failed exchange yields a
// connection-closing 401.
func (s *Server) mapiNTLMAuth(w http.ResponseWriter, r *http.Request, token string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return s.rejectNTLM(w), false
	}
	cs := ntlmConnFromContext(r.Context())
	if cs == nil {
		return s.rejectNTLM(w), false
	}
	msgType, ok := ntlmssp.MessageType(raw)
	if !ok {
		return s.rejectNTLM(w), false
	}

	switch msgType {
	case 1: // NEGOTIATE -> issue a CHALLENGE in a keep-alive 401
		var challenge [8]byte
		if _, err := rand.Read(challenge[:]); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return "", false
		}
		cs.mu.Lock()
		cs.challenge = challenge
		cs.have = true
		cs.mu.Unlock()

		type2 := ntlmssp.BuildChallenge(challenge, ntlmTargetName, ntlmFileTimeNow())
		w.Header().Set("WWW-Authenticate", "NTLM "+base64.StdEncoding.EncodeToString(type2))
		// Zero-length, keep-alive: the AUTHENTICATE must reuse this connection.
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusUnauthorized)
		return "", false

	case 3: // AUTHENTICATE -> verify against this connection's challenge
		cs.mu.Lock()
		challenge, have := cs.challenge, cs.have
		cs.have = false // single-use: a fresh handshake must renegotiate
		cs.mu.Unlock()
		if !have {
			return s.rejectNTLM(w), false
		}
		auth, err := ntlmssp.ParseAuthenticate(raw)
		if err != nil {
			return s.rejectNTLM(w), false
		}
		email, ntHash, ok := s.mapiNTLMCredential(auth.User, auth.DomainName())
		if !ok || !ntlmssp.VerifyNTLMv2(auth, challenge, ntHash) {
			return s.rejectNTLM(w), false
		}
		return email, true

	default:
		return s.rejectNTLM(w), false
	}
}

// rejectNTLM writes a connection-closing 401 after a malformed or failed NTLM
// exchange, so a half-open handshake cannot be reused on the connection. It
// returns "" so callers can write `return s.rejectNTLM(w), false`.
func (s *Server) rejectNTLM(w http.ResponseWriter) string {
	w.Header().Set("Connection", "close")
	w.Header().Set("WWW-Authenticate", "NTLM")
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return ""
}

// SetActiveSyncHandler wires the Exchange ActiveSync endpoint handler.
func (s *Server) SetActiveSyncHandler(h http.Handler) { s.activesyncHandler = h }

// SetActiveSyncEnabled wires the live ActiveSync opt-in gate.
func (s *Server) SetActiveSyncEnabled(fn func() bool) { s.activeSyncEnabled = fn }

// activeSyncOn reports whether the EAS surface is enabled, read live.
func (s *Server) activeSyncOn() bool {
	return s.activeSyncEnabled != nil && s.activeSyncEnabled()
}

// ActiveSyncBasicAuth authenticates an Exchange ActiveSync request via HTTP
// Basic and enforces the same account-state gate as the other Exchange surfaces
// — active account, no forced password change, tenant not suspended, password
// verified. It is the authenticate func injected into the ActiveSync handler,
// returning the mailbox email with ok=true, or ("", false) on any failure; it
// never writes to the response (the handler emits the 401).
func (s *Server) ActiveSyncBasicAuth(r *http.Request) (string, bool) {
	if s.db == nil {
		return "", false
	}
	authHdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "Basic ") {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHdr, "Basic "))
	if err != nil {
		return "", false
	}
	email, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return "", false
	}
	localPart, domain, ok := strings.Cut(email, "@")
	if !ok {
		return "", false
	}
	account, err := s.db.GetAccount(domain, localPart)
	if err != nil || account == nil {
		return "", false
	}
	if !account.IsActive || account.MustChangePassword || s.tenantSuspendedForDomain(domain) {
		return "", false
	}
	if matches, _ := s.verifyPassword(password, account.PasswordHash); !matches {
		return "", false
	}
	return account.Email, true
}
