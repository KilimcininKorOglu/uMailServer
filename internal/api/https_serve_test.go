package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// selfSignedTLSConfig builds a *tls.Config that resolves a freshly generated
// self-signed certificate through GetCertificate, mirroring how the TLS manager
// hands the API server a live callback (no NextProtos, so the server offers no
// ALPN protocol — exactly the production config from GetTLSConfig).
func selfSignedTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil },
		MinVersion:     tls.VersionTLS12,
	}
}

// TestAPIServerServesHTTPSWithoutHTTP2 asserts that SetTLSConfig makes the API
// listener serve HTTPS, and that HTTP/2 stays disabled even when the client
// offers it. HTTP/2 must not auto-enable here: the EWS/MAPI surfaces on this mux
// carry connection-oriented HTTP-layer NTLM whose challenge dance assumes one
// auth exchange per TCP connection, which h2 stream multiplexing would break.
func TestAPIServerServesHTTPSWithoutHTTP2(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	}()

	server := NewServer(database, nil, Config{JWTSecret: "test-secret"})
	server.SetTLSConfig(selfSignedTLSConfig(t))

	// Reserve an ephemeral port, then release it so Start can rebind the address.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	if cerr := ln.Close(); cerr != nil {
		t.Fatalf("release port: %v", cerr)
	}

	go func() { _ = server.Start(addr) }() //nolint:errcheck // serves until Stop returns ErrServerClosed
	defer func() {
		if serr := server.Stop(); serr != nil {
			t.Errorf("stop server: %v", serr)
		}
	}()

	// The client offers BOTH h2 and http/1.1; a correct server selects http/1.1
	// because its TLS config advertises no ALPN protocol and h2 auto-setup is off.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // self-signed cert under test
			NextProtos:         []string{"h2", "http/1.1"},
		},
	}}

	var resp *http.Response
	for range 100 {
		resp, err = client.Get("https://" + addr + "/")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("HTTPS request never succeeded: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close body: %v", cerr)
		}
	}()

	if resp.TLS == nil {
		t.Fatal("response was not served over TLS")
	}
	if resp.Proto != "HTTP/1.1" {
		t.Fatalf("expected HTTP/1.1 (h2 disabled to preserve connection-oriented NTLM), got %q", resp.Proto)
	}
	if resp.TLS.NegotiatedProtocol == "h2" {
		t.Fatal("server negotiated HTTP/2; connection-oriented NTLM on EWS/MAPI would break")
	}
}
