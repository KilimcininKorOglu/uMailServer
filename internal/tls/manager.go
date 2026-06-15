package tls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// Manager handles TLS certificate management
type Manager struct {
	config      Config
	logger      *slog.Logger
	certManager *autocert.Manager
	certCache   map[string]*tls.Certificate
	certMu      sync.RWMutex
	certDir     string

	// domainSource, when set, lists the base domains the server is authoritative
	// for so the ACME host policy can accept tenant domains discovered at runtime.
	// It is consulted through a short-lived cache to keep the TLS handshake fast.
	domainSource  func() ([]string, error)
	domainCache   []string
	domainCacheAt time.Time
	domainMu      sync.Mutex
}

// domainCacheTTL bounds how stale the dynamic ACME host-policy domain set may be.
// The handshake path reads it, so it trades a few minutes of propagation delay
// for not hitting the store on every ClientHello.
const domainCacheTTL = 5 * time.Minute

// acmeServicePrefixes are the well-known service hostnames accepted for each
// authoritative base domain (e.g. mail.example.com for example.com).
var acmeServicePrefixes = []string{"mail.", "autodiscover.", "smtp.", "imap."}

// Config holds TLS manager configuration
type Config struct {
	Enabled           bool
	AutoTLS           bool
	CertFile          string
	KeyFile           string
	Email             string
	Domains           []string
	ACMEEndpoint      string
	UseStaging        bool
	Challenge         string
	DNSProvider       string
	MinVersion        uint16 // TLS version (e.g., tls.VersionTLS12, tls.VersionTLS13). Default: TLS 1.2
	ClientAuth        bool   // Enable client certificate authentication
	RequireClientCert bool   // Require client certificate (mTLS)
	ClientCAFile      string // CA file for client certificate verification
	ClientAuthMode    tls.ClientAuthType

	// CacheBackend selects where autocert persists certificates: "store" uses the
	// injected CacheStore (shared across active-active nodes); anything else uses
	// the local filesystem DirCache. CacheStore must be non-nil for "store".
	CacheBackend string
	CacheStore   CacheStore
}

// NewManager creates a new TLS certificate manager
func NewManager(config Config, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}

	m := &Manager{
		config:    config,
		logger:    logger,
		certCache: make(map[string]*tls.Certificate),
		certDir:   "./certs",
	}

	// Ensure cert directory exists
	if err := os.MkdirAll(m.certDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create cert directory: %w", err)
	}

	// Setup autocert if auto TLS is enabled
	if config.AutoTLS {
		if err := m.setupAutocert(); err != nil {
			return nil, fmt.Errorf("failed to setup autocert: %w", err)
		}
	}

	return m, nil
}

// setupAutocert configures the autocert manager for Let's Encrypt
func (m *Manager) setupAutocert() error {
	challenge := m.config.Challenge
	if challenge == "" {
		challenge = "http-01"
	}
	if challenge == "dns-01" {
		if m.config.DNSProvider == "" {
			return fmt.Errorf("dns-01 challenge requires tls.acme.dns_provider")
		}
		return fmt.Errorf("dns-01 challenge with provider %q is not implemented", m.config.DNSProvider)
	}
	if challenge != "http-01" {
		return fmt.Errorf("unsupported ACME challenge %q", challenge)
	}

	// Use staging environment if configured
	acmeEndpoint := acme.LetsEncryptURL
	if m.config.UseStaging {
		acmeEndpoint = "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	if m.config.ACMEEndpoint != "" {
		acmeEndpoint = m.config.ACMEEndpoint
	}

	var cache autocert.Cache
	if m.config.CacheBackend == "store" && m.config.CacheStore != nil {
		cache = storeCache{store: m.config.CacheStore}
		m.logger.Info("Autocert using shared db-backed certificate cache")
	} else {
		cache = autocert.DirCache(m.certDir)
	}

	m.certManager = &autocert.Manager{
		Client:     &acme.Client{DirectoryURL: acmeEndpoint},
		Cache:      cache,
		Prompt:     autocert.AcceptTOS,
		Email:      m.config.Email,
		HostPolicy: m.hostPolicy,
	}

	m.logger.Info("Autocert configured",
		"email", m.config.Email,
		"domains", m.config.Domains,
		"endpoint", acmeEndpoint,
		"challenge", challenge,
	)

	return nil
}

// SetDomainSource injects a callback listing the base domains the server is
// authoritative for. The ACME host policy then accepts those domains and their
// mail./autodiscover./smtp./imap. service hostnames in addition to the statically
// configured Domains. Without a source, only the static Domains are allowed. Safe
// to call after NewManager: the policy reads the source live, behind a short cache.
func (m *Manager) SetDomainSource(lister func() ([]string, error)) {
	m.domainMu.Lock()
	m.domainSource = lister
	m.domainCacheAt = time.Time{} // force a refresh on the next lookup
	m.domainMu.Unlock()
}

// hostPolicy is the autocert HostPolicy. It permits ACME issuance only for the
// configured Domains, the dynamically discovered tenant domains, and their
// well-known service hostnames. An unrecognized SNI is refused so a probe for an
// arbitrary name cannot trigger a doomed ACME order and burn the CA rate limit.
func (m *Manager) hostPolicy(_ context.Context, host string) error {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if m.hostAllowed(host) {
		return nil
	}
	return fmt.Errorf("acme/autocert: host %q not configured for issuance", host)
}

// hostAllowed reports whether host is an authoritative base domain or one of its
// accepted service hostnames.
func (m *Manager) hostAllowed(host string) bool {
	for _, base := range m.allowedDomains() {
		base = strings.TrimSuffix(strings.ToLower(base), ".")
		if base == "" {
			continue
		}
		if host == base {
			return true
		}
		for _, prefix := range acmeServicePrefixes {
			if host == prefix+base {
				return true
			}
		}
	}
	return false
}

// allowedDomains unions the static Domains with the cached dynamic domain set,
// refreshing the latter from the injected source once its TTL has elapsed. A
// failing source keeps the previous cache (logged) rather than dropping domains.
func (m *Manager) allowedDomains() []string {
	domains := append([]string(nil), m.config.Domains...)

	m.domainMu.Lock()
	defer m.domainMu.Unlock()
	if m.domainSource != nil {
		if time.Since(m.domainCacheAt) > domainCacheTTL {
			if dynamic, err := m.domainSource(); err != nil {
				m.logger.Warn("TLS host policy: domain source failed, using cached set", "error", err)
			} else {
				m.domainCache = dynamic
				m.domainCacheAt = time.Now()
			}
		}
		domains = append(domains, m.domainCache...)
	}
	return domains
}

// GetCertificate returns a TLS certificate for the given hello info
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	// First try autocert if enabled
	if m.certManager != nil {
		cert, err := m.certManager.GetCertificate(hello)
		if err == nil && cert != nil {
			return cert, nil
		}
		// Fall through to manual certs on error
		m.logger.Debug("Autocert failed, trying manual certs", "error", err)
	}

	// Try manual certificates
	return m.getManualCertificate(hello.ServerName)
}

// getManualCertificate loads a certificate from file
func (m *Manager) getManualCertificate(serverName string) (*tls.Certificate, error) {
	// Check cache first (read lock)
	m.certMu.RLock()
	if cert, ok := m.certCache[serverName]; ok {
		m.certMu.RUnlock()
		return cert, nil
	}
	m.certMu.RUnlock()

	// Determine cert paths
	certPath := m.config.CertFile
	keyPath := m.config.KeyFile

	// If server-specific certs exist, use those
	if serverName != "" {
		specificCert := filepath.Join(m.certDir, serverName+".crt")
		specificKey := filepath.Join(m.certDir, serverName+".key")

		if _, err := os.Stat(specificCert); err == nil {
			if _, err := os.Stat(specificKey); err == nil {
				certPath = specificCert
				keyPath = specificKey
			}
		}
	}

	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("no certificate configured for %s", serverName)
	}

	// Load certificate
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load certificate: %w", err)
	}

	// Cache certificate (write lock)
	m.certMu.Lock()
	m.certCache[serverName] = &cert
	m.certMu.Unlock()

	return &cert, nil
}

// GetTLSConfig returns a TLS configuration
func (m *Manager) GetTLSConfig() *tls.Config {
	return m.GetTLSConfigWithClientAuth(false)
}

// GetTLSConfigWithClientAuth returns a TLS configuration with optional client certificate auth
func (m *Manager) GetTLSConfigWithClientAuth(requireClientCert bool) *tls.Config {
	// Use configured min version or default to TLS 1.2
	minVersion := m.config.MinVersion
	if minVersion < tls.VersionTLS12 {
		minVersion = tls.VersionTLS12
	}

	// Determine client auth mode
	clientAuth := tls.NoClientCert
	if m.config.ClientAuth || requireClientCert {
		if m.config.RequireClientCert || requireClientCert {
			clientAuth = tls.RequireAndVerifyClientCert
		} else {
			clientAuth = tls.VerifyClientCertIfGiven
		}
	}
	// Allow override from config
	if m.config.ClientAuthMode != 0 {
		clientAuth = m.config.ClientAuthMode
	}

	var clientCAs *x509.CertPool
	if m.config.ClientCAFile != "" {
		clientCAs = x509.NewCertPool()
		caData, err := os.ReadFile(m.config.ClientCAFile)
		if err == nil {
			clientCAs.AppendCertsFromPEM(caData)
		} else {
			m.logger.Warn("Failed to load client CA file", "file", m.config.ClientCAFile, "error", err)
		}
	}

	// #nosec G402 -- MinVersion is runtime-validated to be >= TLS 1.2 above
	return &tls.Config{
		GetCertificate: m.GetCertificate,
		MinVersion:     minVersion,
		MaxVersion:     tls.VersionTLS13,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		},
		PreferServerCipherSuites: true,
		ClientAuth:               clientAuth,
		ClientCAs:                clientCAs,
	}
}

// VerifyClientCert verifies a client certificate and returns the identity
func (m *Manager) VerifyClientCert(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", fmt.Errorf("no certificate provided")
	}

	// Extract email from certificate subject
	if len(cert.EmailAddresses) > 0 {
		return cert.EmailAddresses[0], nil
	}

	// Use common name if no email
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName, nil
	}

	return "", fmt.Errorf("no identity found in certificate")
}

// GenerateSelfSigned generates a self-signed certificate for testing
func (m *Manager) GenerateSelfSigned(_ []string) (string, string, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}

	// Generate certificate
	// Note: In production, use proper certificate generation
	template := &tls.Certificate{
		Certificate: [][]byte{},
		PrivateKey:  priv,
	}

	// For now, just create key file
	keyPath := filepath.Join(m.certDir, "selfsigned.key")
	certPath := filepath.Join(m.certDir, "selfsigned.crt")

	// In a real implementation, we'd generate a proper cert here
	// For now, return the paths and let the user generate them
	_ = template

	m.logger.Warn("Self-signed certificate generation not fully implemented")
	return certPath, keyPath, nil
}

// RenewCertificates manually triggers certificate renewal
func (m *Manager) RenewCertificates(ctx context.Context) error {
	if m.certManager == nil {
		return fmt.Errorf("autocert not configured")
	}

	// Force renewal by deleting cached certs
	for _, domain := range m.config.Domains {
		if err := m.certManager.Cache.Delete(ctx, domain); err != nil {
			m.logger.Warn("Failed to delete cached cert", "domain", domain, "error", err)
		}
	}

	m.logger.Info("Certificate renewal triggered", "domains", m.config.Domains)
	return nil
}

// GetCertificateStatus reports the certificate state for every domain the server
// is authoritative for (the static set plus the dynamically discovered tenant
// domains), so the expiry alert and admin panel see ACME-issued certificates —
// not just manually placed files.
func (m *Manager) GetCertificateStatus() []CertificateStatus {
	var statuses []CertificateStatus
	seen := make(map[string]bool)
	for _, domain := range m.allowedDomains() {
		domain = strings.TrimSuffix(strings.ToLower(domain), ".")
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		statuses = append(statuses, m.certificateStatus(domain))
	}
	return statuses
}

// certificateStatus parses the certificate currently held for domain and
// summarizes its validity and expiry.
func (m *Manager) certificateStatus(domain string) CertificateStatus {
	status := CertificateStatus{Domain: domain, Valid: false}

	data, err := m.loadCertPEM(domain)
	if err != nil {
		status.Error = err.Error()
		return status
	}

	cert, err := parseCertificate(data)
	if err != nil {
		status.Error = err.Error()
		return status
	}

	status.Valid = true
	status.ExpiresAt = cert.NotAfter
	status.Issuer = cert.Issuer.CommonName
	if time.Until(cert.NotAfter) < 7*24*time.Hour {
		status.Warning = "Certificate expires within 7 days"
	}
	return status
}

// loadCertPEM returns the certificate PEM held for domain. With autocert active
// it reads the ACME cache under the bare-domain key (the same key RenewCertificates
// uses); the bundle has the private key block first, then the chain. It is a pure
// cache read — never certManager.GetCertificate, which would trigger ACME issuance
// during a status check. Without autocert it reads the per-domain manual cert file.
func (m *Manager) loadCertPEM(domain string) ([]byte, error) {
	if m.certManager != nil {
		return m.certManager.Cache.Get(context.Background(), domain)
	}
	certPath := filepath.Join(m.certDir, domain+".crt")
	return os.ReadFile(filepath.Clean(certPath))
}

// CertificateStatus holds certificate status information
type CertificateStatus struct {
	Domain    string    `json:"domain"`
	Valid     bool      `json:"valid"`
	ExpiresAt time.Time `json:"expires_at"`
	Issuer    string    `json:"issuer"`
	Warning   string    `json:"warning,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// parseCertificate parses a certificate from PEM data
func parseCertificate(data []byte) (*x509.Certificate, error) {
	// Walk the PEM blocks and parse the first CERTIFICATE. The autocert cache
	// stores the private key block ahead of the certificate chain, so a bundle
	// must be scanned rather than assuming the leaf is the first block.
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("no CERTIFICATE block in PEM data")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate: %w", err)
		}
		return cert, nil
	}
}

// HTTPChallengeHandler returns the handler for ACME HTTP challenges
func (m *Manager) HTTPChallengeHandler() http.Handler {
	if m.certManager == nil {
		return nil
	}
	challenge := m.config.Challenge
	if challenge != "" && challenge != "http-01" {
		return nil
	}
	return m.certManager.HTTPHandler(nil)
}

// Close cleans up resources
func (m *Manager) Close() error {
	// Nothing to clean up currently
	return nil
}

// IsEnabled returns true if TLS is enabled
func (m *Manager) IsEnabled() bool {
	return m.config.Enabled
}

// IsAutoTLS returns true if auto TLS is enabled
func (m *Manager) IsAutoTLS() bool {
	return m.config.AutoTLS
}
