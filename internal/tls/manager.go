package tls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
)

// Manager handles TLS certificate management
type Manager struct {
	config     Config
	logger     *slog.Logger
	certMagic  *certmagic.Config
	acmeIssuer *certmagic.ACMEIssuer
	certCache  map[string]*tls.Certificate
	certMu     sync.RWMutex
	certDir    string

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

	// CacheBackend selects where the issuer persists certificates: "store" uses the
	// injected CacheStore (shared across active-active nodes); anything else uses
	// the local filesystem. CacheStore must be non-nil for "store".
	CacheBackend string
	CacheStore   CacheStore
	// Locker is the distributed lock certmagic serializes issuance/renewal across
	// active-active nodes over (instances sharing the same storage are one cluster).
	// Required when AutoTLS is enabled with CacheBackend "store"; ignored otherwise.
	Locker Locker
	// CacheDir overrides the filesystem certificate-cache directory (default ./certs).
	CacheDir string
	// RenewBeforeDays overrides the renewal lead time in days (0 = certmagic default).
	RenewBeforeDays int
	// ACMECACertFile is a PEM file of CA certificate(s) the ACME client trusts when
	// connecting to ACMEEndpoint. Set this for a private or test ACME CA (e.g. a
	// local Pebble server) whose directory endpoint is not signed by a public CA.
	// Empty uses the system trust store.
	ACMECACertFile string
}

// NewManager creates a new TLS certificate manager
func NewManager(config Config, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}

	certDir := config.CacheDir
	if certDir == "" {
		certDir = "./certs"
	}
	m := &Manager{
		config:    config,
		logger:    logger,
		certCache: make(map[string]*tls.Certificate),
		certDir:   certDir,
	}

	// Ensure cert directory exists
	if err := os.MkdirAll(m.certDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create cert directory: %w", err)
	}

	// Setup the ACME issuer if auto TLS is enabled
	if config.AutoTLS {
		if err := m.setupCertmagic(); err != nil {
			return nil, fmt.Errorf("failed to setup ACME issuer: %w", err)
		}
	}

	return m, nil
}

// setupCertmagic builds the certmagic issuer/manager. certmagic coordinates
// issuance across active-active nodes through a shared Storage + lock rather than
// a leader: any node may on-demand a certificate at handshake, and a contended
// issuance serializes on the lock. HTTP-01 is the default challenge; DNS-01
// (wildcard) is wired in a follow-up.
func (m *Manager) setupCertmagic() error {
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

	// Storage: shared db-backed store (active-active) or the local filesystem.
	var storage certmagic.Storage
	if m.config.CacheBackend == "store" && m.config.CacheStore != nil {
		if m.config.Locker == nil {
			return fmt.Errorf("cache_backend \"store\" requires a distributed Locker for active-active issuance")
		}
		storage = newCertmagicStorage(m.config.CacheStore, m.config.Locker)
		m.logger.Info("ACME issuer using shared db-backed certificate storage")
	} else {
		storage = &certmagic.FileStorage{Path: m.certDir}
	}

	// ACME directory URL: Let's Encrypt production (default), staging, or a custom
	// endpoint (e.g. a local Pebble CA).
	ca := certmagic.LetsEncryptProductionCA
	if m.config.UseStaging {
		ca = certmagic.LetsEncryptStagingCA
	}
	if m.config.ACMEEndpoint != "" {
		ca = m.config.ACMEEndpoint
	}

	issuerCfg := certmagic.ACMEIssuer{
		CA:     ca,
		Email:  m.config.Email,
		Agreed: true,
	}
	// Trust a private/test ACME CA (e.g. local Pebble) for the directory endpoint
	// without weakening verification: restrict the ACME client's trust to the
	// configured root, leaving the system trust store untouched everywhere else.
	if m.config.ACMECACertFile != "" {
		caData, err := os.ReadFile(m.config.ACMECACertFile)
		if err != nil {
			return fmt.Errorf("read acme ca cert %q: %w", m.config.ACMECACertFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return fmt.Errorf("acme ca cert %q contains no usable certificate", m.config.ACMECACertFile)
		}
		issuerCfg.TrustedRoots = pool
		m.logger.Info("ACME issuer trusting custom CA for directory endpoint", "ca_file", m.config.ACMECACertFile)
	}

	magicCfg := certmagic.Config{
		Storage:  storage,
		OnDemand: &certmagic.OnDemandConfig{DecisionFunc: m.decideOnDemand},
	}
	if m.config.RenewBeforeDays > 0 {
		// certmagic renews within a window that is a fraction of the cert's total
		// validity; convert a lead-time in days (on a 90-day LE cert) to that ratio.
		magicCfg.RenewalWindowRatio = float64(m.config.RenewBeforeDays) / 90.0
	}

	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return m.certMagic, nil
		},
	})
	m.certMagic = certmagic.New(cache, magicCfg)
	m.acmeIssuer = certmagic.NewACMEIssuer(m.certMagic, issuerCfg)
	m.certMagic.Issuers = []certmagic.Issuer{m.acmeIssuer}

	m.logger.Info("ACME issuer configured",
		"email", m.config.Email,
		"domains", m.config.Domains,
		"endpoint", ca,
		"challenge", challenge,
	)

	return nil
}

// Reload flushes the manager's cached certificate material and forces the next
// host-policy lookup to re-query the domain source. The next TLS handshake then
// re-reads a replaced manual certificate from disk and sees domain changes
// immediately, without restarting any listener — because each listener resolves
// its certificate and host policy through live callbacks, not a pinned cert.
// Structural settings (min TLS version, client-auth mode, listener ports) are
// baked into each listener's tls.Config at startup and still require a restart.
func (m *Manager) Reload() {
	m.certMu.Lock()
	m.certCache = make(map[string]*tls.Certificate)
	m.certMu.Unlock()

	m.domainMu.Lock()
	m.domainCacheAt = time.Time{} // force a domain-source refresh on the next lookup
	m.domainMu.Unlock()

	m.logger.Info("TLS manager reloaded: certificate and domain caches flushed")
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

// decideOnDemand is the certmagic on-demand authorization gate. It permits
// issuance only for the configured Domains, the dynamically discovered tenant
// domains, and their well-known service hostnames. An unrecognized SNI is refused
// so a probe for an arbitrary name cannot trigger a doomed ACME order and burn
// the CA rate limit. It is the on-demand replacement for the autocert HostPolicy.
func (m *Manager) decideOnDemand(_ context.Context, host string) error {
	// The HTTP-01 challenge may carry host:port (e.g. a local Pebble validating
	// on 5002); the SNI path arrives without one. Strip it so the check compares
	// hostnames, not host:port.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if m.hostAllowed(host) {
		return nil
	}
	return fmt.Errorf("acme: host %q not configured for issuance", host)
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
	// First try the ACME issuer (certmagic on-demand) if enabled
	if m.certMagic != nil {
		cert, err := m.certMagic.GetCertificate(hello)
		if err == nil && cert != nil {
			return cert, nil
		}
		// Fall through to manual certs on error
		m.logger.Debug("ACME issuer failed, trying manual certs", "error", err)
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

// GenerateSelfSigned writes a self-signed ECDSA P-256 certificate covering the
// given domains (plus their autodiscover. hostnames) and its key to the cert
// directory, returning the cert and key paths. It is the bootstrap fallback for
// when ACME is off and no manual certificate is configured, so STARTTLS/HTTPS can
// still come up; clients must explicitly trust it. With no domains it covers the
// full authoritative set.
func (m *Manager) GenerateSelfSigned(domains []string) (string, string, error) {
	if len(domains) == 0 {
		domains = m.allowedDomains()
	}
	sans := selfSignedSANs(domains)
	if len(sans) == 0 {
		// A freshly installed server may have no domains yet; cover localhost so
		// the bootstrap certificate is still usable for local administration.
		sans = []string{"localhost"}
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("failed to generate serial number: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: sans[0], Organization: []string{"uMailServer self-signed"}},
		DNSNames:              sans,
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(825 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return "", "", fmt.Errorf("failed to create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal private key: %w", err)
	}

	certPath := filepath.Join(m.certDir, "selfsigned.crt")
	keyPath := filepath.Join(m.certDir, "selfsigned.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return "", "", fmt.Errorf("failed to write certificate: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", fmt.Errorf("failed to write key: %w", err)
	}

	m.logger.Info("Generated self-signed certificate", "domains", sans, "cert", certPath)
	return certPath, keyPath, nil
}

// selfSignedSANs expands the base domains into the SAN set: each domain plus its
// autodiscover. hostname (so Outlook/Exchange clients reach a covered name),
// lowercased and deduplicated, preserving first-seen order.
func selfSignedSANs(domains []string) []string {
	seen := make(map[string]bool)
	var sans []string
	add := func(name string) {
		name = strings.TrimSuffix(strings.ToLower(name), ".")
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		sans = append(sans, name)
	}
	for _, d := range domains {
		d = strings.TrimSuffix(strings.ToLower(d), ".")
		if d == "" {
			continue
		}
		add(d)
		add("autodiscover." + d)
	}
	return sans
}

// RenewCertificates manually triggers certificate renewal by dropping the cached
// certificate assets (cert, key, meta) so certmagic re-issues on the next
// handshake. It is a maintenance hook with no production caller today.
func (m *Manager) RenewCertificates(ctx context.Context) error {
	if m.certMagic == nil || m.acmeIssuer == nil {
		return fmt.Errorf("ACME issuer not configured")
	}

	issuerKey := m.acmeIssuer.IssuerKey()
	storage := m.certMagic.Storage
	for _, domain := range m.config.Domains {
		for _, key := range []string{
			certmagic.StorageKeys.SiteCert(issuerKey, domain),
			certmagic.StorageKeys.SitePrivateKey(issuerKey, domain),
			certmagic.StorageKeys.SiteMeta(issuerKey, domain),
		} {
			if err := storage.Delete(ctx, key); err != nil {
				m.logger.Warn("Failed to delete cached cert asset", "key", key, "error", err)
			}
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

// loadCertPEM returns the certificate PEM held for domain. With the ACME issuer
// active it reads the cert asset from certmagic's storage under the issuer's
// SiteCert key; the bundle has the private key block first, then the chain. It is
// a pure storage read — never GetCertificate, which would trigger ACME issuance
// during a status check. Without the issuer it reads the per-domain manual cert
// file.
func (m *Manager) loadCertPEM(domain string) ([]byte, error) {
	if m.certMagic != nil && m.acmeIssuer != nil {
		key := certmagic.StorageKeys.SiteCert(m.acmeIssuer.IssuerKey(), domain)
		return m.certMagic.Storage.Load(context.Background(), key)
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

// HTTPChallengeHandler returns the handler for ACME HTTP-01 challenges, served on
// /.well-known/acme-challenge/. It is nil (unmounted) when the issuer is off or a
// non-HTTP challenge is configured.
func (m *Manager) HTTPChallengeHandler() http.Handler {
	if m.acmeIssuer == nil {
		return nil
	}
	challenge := m.config.Challenge
	if challenge != "" && challenge != "http-01" {
		return nil
	}
	return m.acmeIssuer.HTTPChallengeHandler(nil)
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
