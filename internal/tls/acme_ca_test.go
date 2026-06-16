package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCAPEM generates a self-signed CA certificate and returns the path to
// its PEM file. It stands in for a private/test ACME CA such as Pebble's
// pebble.minica.pem.
func writeTestCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-acme-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write CA pem: %v", err)
	}
	return path
}

// TestACMECACertFileTrusted asserts that a configured ACME CA file is loaded into
// the issuer's TrustedRoots (so a private/test ACME directory endpoint is
// trusted) without touching the system trust store. This is the wiring that lets
// the server talk to a local Pebble whose directory cert is not publicly signed.
func TestACMECACertFileTrusted(t *testing.T) {
	caPath := writeTestCAPEM(t)
	m, err := NewManager(Config{
		Enabled:        true,
		AutoTLS:        true,
		Challenge:      "http-01",
		ACMEEndpoint:   "https://pebble.test:14000/dir",
		ACMECACertFile: caPath,
		CacheDir:       t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if m.acmeIssuer == nil {
		t.Fatal("ACME issuer not configured")
	}
	if m.acmeIssuer.TrustedRoots == nil {
		t.Fatal("ACME issuer TrustedRoots is nil; custom CA was not wired and the system trust store would be used")
	}
}

// TestACMECACertFileMissingErrors asserts that a configured-but-unreadable ACME CA
// file fails construction loudly instead of silently falling back to system roots
// (which would then fail every ACME handshake against a private CA).
func TestACMECACertFileMissingErrors(t *testing.T) {
	_, err := NewManager(Config{
		Enabled:        true,
		AutoTLS:        true,
		Challenge:      "http-01",
		ACMEEndpoint:   "https://pebble.test:14000/dir",
		ACMECACertFile: filepath.Join(t.TempDir(), "does-not-exist.pem"),
		CacheDir:       t.TempDir(),
	}, nil)
	if err == nil {
		t.Fatal("expected NewManager to fail when the ACME CA file is missing")
	}
}
