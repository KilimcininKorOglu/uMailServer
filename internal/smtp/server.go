package smtp

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/umailserver/umailserver/internal/metrics"
	"github.com/umailserver/umailserver/internal/tracing"
)

// ErrSessionQuit is returned when the session handles a QUIT command
var ErrSessionQuit = errors.New("QUIT")

// Server represents an SMTP server
type Server struct {
	config      *Config
	listener    net.Listener
	listeners   []net.Listener
	connections map[string]*Session
	connMu      sync.RWMutex
	listenersMu sync.Mutex // protects listeners slice
	running     atomic.Bool
	shutdown    chan struct{}
	stopOnce    sync.Once
	logger      *slog.Logger

	// Hooks for message processing
	onAuth             func(username, password string) (bool, error)
	onValidate         func(from string, to []string) error
	onDeliver          func(from string, to []string, data []byte) error
	onDeliverWithSieve func(from string, to []string, data []byte, sieveActions []string) error
	// onSchedule, when set, records a FUTURERELEASE (RFC 4865) submission for
	// future delivery instead of delivering now, returning the scheduled id.
	onSchedule      func(from string, to []string, data []byte, sendAt time.Time) (string, error)
	onGetUserSecret func(username string) (string, error) // Get user's shared secret for CRAM-MD5
	onGetPassword   func(username string) (string, error) // Get user's password for SCRAM-SHA-256
	onLoginResult   func(username string, success bool, ip, reason string)
	isLocalDomain   func(domain string) bool // reports whether a domain is locally hosted (anti-relay)
	pipeline        *Pipeline

	// Rate limiting
	rateLimiter ConnectionRateLimiter

	// Auth brute-force protection
	maxLoginAttempts int
	lockoutDuration  time.Duration
	authFailures     map[string][]time.Time // IP -> failure timestamps
	authFailuresMu   sync.Mutex

	legacyAuthPerMinute  int
	legacyConnPerHour    int
	legacyAuthAttempts   map[string][]time.Time
	legacyConnAttempts   map[string][]time.Time
	legacyAuthAttemptsMu sync.Mutex
	legacyConnAttemptsMu sync.Mutex

	// Tracing provider for OpenTelemetry
	tracingProvider *tracing.Provider
}

// ConnectionRateLimiter checks if a connection is allowed
type ConnectionRateLimiter interface {
	Allow(key string, limitType string) bool
}

// SetRateLimiter sets the rate limiter for the server
func (s *Server) SetRateLimiter(rl ConnectionRateLimiter) {
	s.rateLimiter = rl
}

// SetAuthLimits configures brute-force protection for SMTP AUTH
func (s *Server) SetAuthLimits(maxAttempts int, lockoutDuration time.Duration) {
	s.maxLoginAttempts = maxAttempts
	s.lockoutDuration = lockoutDuration
}

// SetLegacyRateLimits configures compatibility limits for legacy YAML keys.
func (s *Server) SetLegacyRateLimits(authPerMinute, connPerHour int) {
	s.legacyAuthPerMinute = authPerMinute
	s.legacyConnPerHour = connPerHour
}

// isAuthLockedOut returns true if the given IP is temporarily locked out
func (s *Server) isAuthLockedOut(ip string) bool {
	if s.maxLoginAttempts <= 0 {
		return false
	}
	s.authFailuresMu.Lock()
	defer s.authFailuresMu.Unlock()

	cutoff := time.Now().Add(-s.lockoutDuration)
	var recent []time.Time
	for _, t := range s.authFailures[ip] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	s.authFailures[ip] = recent
	return len(recent) >= s.maxLoginAttempts
}

// recordAuthFailure records a failed authentication attempt from the given IP
func (s *Server) recordAuthFailure(ip string) {
	if s.maxLoginAttempts <= 0 {
		return
	}
	s.authFailuresMu.Lock()
	defer s.authFailuresMu.Unlock()
	s.authFailures[ip] = append(s.authFailures[ip], time.Now())

	// Periodic cleanup when map reaches threshold
	if len(s.authFailures) >= 100 {
		s.cleanupAuthFailuresLocked()
	}
}

// cleanupAuthFailuresLocked removes old entries from authFailures map
// Must be called with authFailuresMu held
func (s *Server) cleanupAuthFailuresLocked() {
	cutoff := time.Now().Add(-s.lockoutDuration)
	for ip, times := range s.authFailures {
		var recent []time.Time
		for _, t := range times {
			if t.After(cutoff) {
				recent = append(recent, t)
			}
		}
		if len(recent) > 0 {
			s.authFailures[ip] = recent
		} else {
			delete(s.authFailures, ip)
		}
	}
}

// clearAuthFailures removes recorded failures for the given IP
func (s *Server) clearAuthFailures(ip string) {
	s.authFailuresMu.Lock()
	defer s.authFailuresMu.Unlock()
	delete(s.authFailures, ip)
}

func (s *Server) isLegacyAuthRateLimited(ip string) bool {
	if s.legacyAuthPerMinute <= 0 {
		return false
	}

	s.legacyAuthAttemptsMu.Lock()
	defer s.legacyAuthAttemptsMu.Unlock()

	if s.legacyAuthAttempts == nil {
		s.legacyAuthAttempts = make(map[string][]time.Time)
	}

	cutoff := time.Now().Add(-time.Minute)
	var recent []time.Time
	for _, attempt := range s.legacyAuthAttempts[ip] {
		if attempt.After(cutoff) {
			recent = append(recent, attempt)
		}
	}
	s.legacyAuthAttempts[ip] = recent
	return len(recent) >= s.legacyAuthPerMinute
}

func (s *Server) recordLegacyAuthAttempt(ip string) {
	if s.legacyAuthPerMinute <= 0 {
		return
	}

	s.legacyAuthAttemptsMu.Lock()
	defer s.legacyAuthAttemptsMu.Unlock()

	if s.legacyAuthAttempts == nil {
		s.legacyAuthAttempts = make(map[string][]time.Time)
	}

	cutoff := time.Now().Add(-time.Minute)
	var recent []time.Time
	for _, attempt := range s.legacyAuthAttempts[ip] {
		if attempt.After(cutoff) {
			recent = append(recent, attempt)
		}
	}
	s.legacyAuthAttempts[ip] = append(recent, time.Now())
}

func (s *Server) allowLegacyConnection(ip string) bool {
	if s.legacyConnPerHour <= 0 {
		return true
	}

	s.legacyConnAttemptsMu.Lock()
	defer s.legacyConnAttemptsMu.Unlock()

	if s.legacyConnAttempts == nil {
		s.legacyConnAttempts = make(map[string][]time.Time)
	}

	cutoff := time.Now().Add(-time.Hour)
	var recent []time.Time
	for _, attempt := range s.legacyConnAttempts[ip] {
		if attempt.After(cutoff) {
			recent = append(recent, attempt)
		}
	}
	if len(recent) >= s.legacyConnPerHour {
		s.legacyConnAttempts[ip] = recent
		return false
	}

	s.legacyConnAttempts[ip] = append(recent, time.Now())
	return true
}

// Config holds SMTP server configuration
type Config struct {
	Hostname       string
	MaxMessageSize int64
	MaxRecipients  int
	MaxConnections int
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	AllowInsecure  bool
	TLSConfig      *tls.Config

	// Submission mode settings
	RequireAuth  bool // Reject MAIL FROM if not authenticated (submission mode)
	RequireTLS   bool // Require TLS before AUTH
	IsSubmission bool // Submission server mode (port 587/465)

	// FUTURERELEASE (RFC 4865) advertisement. When enabled on a submission
	// listener, EHLO advertises FUTURERELEASE and MAIL FROM accepts HOLDFOR/
	// HOLDUNTIL up to FutureReleaseMaxSeconds in the future.
	FutureReleaseEnabled    bool
	FutureReleaseMaxSeconds int
}

// NewServer creates a new SMTP server
func NewServer(config *Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	return &Server{
		config:             config,
		connections:        make(map[string]*Session),
		shutdown:           make(chan struct{}),
		logger:             logger,
		authFailures:       make(map[string][]time.Time),
		legacyAuthAttempts: make(map[string][]time.Time),
		legacyConnAttempts: make(map[string][]time.Time),
	}
}

// SetAuthHandler sets the authentication handler
func (s *Server) SetAuthHandler(handler func(username, password string) (bool, error)) {
	s.onAuth = handler
}

// SetValidateHandler sets the message validation handler
func (s *Server) SetValidateHandler(handler func(from string, to []string) error) {
	s.onValidate = handler
}

// SetDeliveryHandler sets the message delivery handler
func (s *Server) SetDeliveryHandler(handler func(from string, to []string, data []byte) error) {
	s.onDeliver = handler
}

// SetDeliveryHandlerWithSieve sets the message delivery handler with sieve action support
func (s *Server) SetDeliveryHandlerWithSieve(handler func(from string, to []string, data []byte, sieveActions []string) error) {
	s.onDeliverWithSieve = handler
}

// SetScheduleHandler sets the FUTURERELEASE (RFC 4865) handler: a submission
// carrying a future HOLDFOR/HOLDUNTIL is recorded for scheduled delivery instead
// of being delivered now.
func (s *Server) SetScheduleHandler(handler func(from string, to []string, data []byte, sendAt time.Time) (string, error)) {
	s.onSchedule = handler
}

// SetLocalDomainFunc wires the local-domain check used for anti-relay policy.
// When set, an unauthenticated session may only address recipients in locally
// hosted domains; relaying to external domains requires authentication. This
// closes the open-relay hole on the inbound (port 25) listener.
func (s *Server) SetLocalDomainFunc(fn func(domain string) bool) {
	s.isLocalDomain = fn
}

// SetPipeline sets the message processing pipeline
func (s *Server) SetPipeline(p *Pipeline) {
	s.pipeline = p
}

// SetUserSecretHandler sets the handler for retrieving a user's shared secret for CRAM-MD5 auth
func (s *Server) SetUserSecretHandler(handler func(username string) (string, error)) {
	s.onGetUserSecret = handler
}

// SetPasswordHandler sets the handler for retrieving a user's password for SCRAM-SHA-256 auth
func (s *Server) SetPasswordHandler(handler func(username string) (string, error)) {
	s.onGetPassword = handler
}

// SetLoginResultHandler sets the handler for login result events. The reason
// argument is empty on success and populated with a short tag on failure
// (e.g. "invalid_credentials") so consumers can record audit trails.
func (s *Server) SetLoginResultHandler(handler func(username string, success bool, ip, reason string)) {
	s.onLoginResult = handler
}

// SetTracingProvider sets the OpenTelemetry tracing provider
func (s *Server) SetTracingProvider(provider *tracing.Provider) {
	s.tracingProvider = provider
}

// ListenAndServe starts listening on the specified address
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	return s.Serve(ln)
}

// ListenAndServeTLS starts listening with TLS on the specified address
func (s *Server) ListenAndServeTLS(addr string, tlsConfig *tls.Config) error {
	ln, err := tls.Listen("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	return s.Serve(ln)
}

// Serve accepts connections on the listener
func (s *Server) Serve(listener net.Listener) error {
	s.listener = listener
	s.listenersMu.Lock()
	s.listeners = append(s.listeners, listener)
	s.listenersMu.Unlock()
	s.running.Store(true)

	s.logger.Info("SMTP server listening",
		slog.String("address", listener.Addr().String()),
		slog.String("hostname", s.config.Hostname),
	)

	for {
		select {
		case <-s.shutdown:
			return nil
		default:
		}

		if tl, ok := listener.(interface{ SetDeadline(time.Time) error }); ok {
			_ = tl.SetDeadline(time.Now().Add(time.Second))
		}
		conn, err := listener.Accept()
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok && opErr.Timeout() {
				continue
			}
			if s.running.Load() {
				s.logger.Error("accept error", slog.Any("error", err))
			}
			continue
		}

		go s.handleConnection(conn)
	}
}

// handleConnection handles a new SMTP connection
func (s *Server) handleConnection(conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Panic in SMTP connection handler", "error", r)
			_ = conn.Close()
		}
	}()

	// Enforce global connection limit
	s.connMu.RLock()
	atLimit := s.config.MaxConnections > 0 && len(s.connections) >= s.config.MaxConnections
	s.connMu.RUnlock()
	if atLimit {
		if _, err := conn.Write([]byte("421 4.7.0 Too many connections, try again later\r\n")); err != nil {
			s.logger.Debug("failed to write rate limit response", "error", err)
		}
		_ = conn.Close()
		return
	}

	// Check rate limit
	ip := getIPFromAddr(conn.RemoteAddr().String())
	if !s.allowLegacyConnection(ip) {
		s.logger.Warn("SMTP connection hourly rate limited",
			slog.String("remote_addr", conn.RemoteAddr().String()),
		)
		if _, err := conn.Write([]byte("421 4.7.0 Too many SMTP connections from this IP, try again later\r\n")); err != nil {
			s.logger.Debug("failed to write rate limit response", "error", err)
		}
		_ = conn.Close()
		return
	}
	if s.rateLimiter != nil {
		if !s.rateLimiter.Allow(ip, "smtp_connection") {
			s.logger.Warn("SMTP connection rate limited",
				slog.String("remote_addr", conn.RemoteAddr().String()),
			)
			if _, err := conn.Write([]byte("421 4.7.0 Rate limit exceeded, try again later\r\n")); err != nil {
				s.logger.Debug("failed to write rate limit response", "error", err)
			}
			_ = conn.Close()
			return
		}
	}

	session := NewSession(conn, s)
	metrics.Get().SMTPConnection()

	s.connMu.Lock()
	s.connections[session.ID()] = session
	s.connMu.Unlock()

	s.logger.Debug("SMTP connection established",
		slog.String("remote_addr", conn.RemoteAddr().String()),
		slog.String("session_id", session.ID()),
	)

	defer func() {
		s.connMu.Lock()
		delete(s.connections, session.ID())
		s.connMu.Unlock()

		_ = conn.Close()

		s.logger.Debug("SMTP connection closed",
			slog.String("remote_addr", conn.RemoteAddr().String()),
			slog.String("session_id", session.ID()),
		)
	}()

	// Send greeting
	_ = session.WriteResponse(220, fmt.Sprintf("%s ESMTP uMailServer", s.config.Hostname))

	// Handle commands
	reader := bufio.NewReader(conn)
	session.reader = reader
	for {
		if s.config.ReadTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(s.config.ReadTimeout))
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.logger.Debug("read error", slog.Any("error", err))
			}
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		s.logger.Debug("SMTP command",
			slog.String("session_id", session.ID()),
			slog.String("command", truncate(line, 50)),
		)

		if err := session.HandleCommand(line); err != nil {
			if errors.Is(err, ErrSessionQuit) {
				return
			}
			s.logger.Debug("command error",
				slog.String("session_id", session.ID()),
				slog.Any("error", err),
			)
		}
	}
}

// Stop gracefully shuts down the server
func (s *Server) Stop() error {
	s.running.Store(false)
	s.stopOnce.Do(func() {
		close(s.shutdown)
	})

	// Close all listeners
	s.listenersMu.Lock()
	for _, ln := range s.listeners {
		_ = ln.Close()
	}
	s.listenersMu.Unlock()

	// Close all connections
	s.connMu.Lock()
	for _, session := range s.connections {
		_ = session.Close()
	}
	s.connMu.Unlock()

	return nil
}

// getIPFromAddr extracts IP from an address string
func getIPFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// Helper function
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ValidateEmail validates an email address
func ValidateEmail(email string) (string, error) {
	addr, err := mail.ParseAddress(email)
	if err != nil {
		// Try to handle international addresses (SMTPUTF8)
		// Some UTF-8 addresses may not parse with the strict parser
		// Basic validation: check for non-ASCII and @ sign
		if strings.Contains(email, "@") && !strings.HasPrefix(email, "@") && !strings.HasSuffix(email, "@") {
			return email, nil
		}
		return "", err
	}
	return addr.Address, nil
}
