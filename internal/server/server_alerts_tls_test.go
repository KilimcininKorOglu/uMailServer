package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/alert"
	tlspkg "github.com/umailserver/umailserver/internal/tls"
)

// alertCaptureServer starts a webhook endpoint that appends every delivered
// alert to sink, so a test can assert which alerts fired.
func alertCaptureServer(t *testing.T, sink *[]alert.Alert) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var a alert.Alert
		if jerr := json.Unmarshal(body, &a); jerr == nil {
			*sink = append(*sink, a)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// memCacheStore is an in-memory tlspkg.CacheStore for seeding the autocert cache.
type memCacheStore struct{ m map[string][]byte }

func (s *memCacheStore) Get(key string) ([]byte, error) { return s.m[key], nil }
func (s *memCacheStore) Put(key string, data []byte) error {
	s.m[key] = data
	return nil
}
func (s *memCacheStore) Delete(key string) error {
	delete(s.m, key)
	return nil
}

// expiringCertBundle builds a cert+key PEM bundle (key block first, as autocert
// stores it) for domain, expiring after the given duration.
func expiringCertBundle(t *testing.T, domain string, validFor time.Duration) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(validFor),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return append(keyPEM, certPEM...)
}

// TestCheckAlertsTLSExpiringFires is the end-to-end proof that the expiry alert
// — the only reliable signal that an ACME certificate's renewal has silently
// stalled, since autocert renews lazily — now actually fires. A near-expiry
// certificate seeded into the autocert cache must drive checkAlerts ->
// GetCertificateStatus -> CheckTLSCertificate -> a delivered webhook alert.
// Before the status read was fixed it reported Valid=false and this never fired.
func TestCheckAlertsTLSExpiringFires(t *testing.T) {
	var alerts []alert.Alert
	webhook := alertCaptureServer(t, &alerts)
	defer webhook.Close()

	store := &memCacheStore{m: map[string][]byte{}}
	tlsMgr, err := tlspkg.NewManager(tlspkg.Config{
		AutoTLS:      true,
		Domains:      []string{"mail.example.com"},
		CacheBackend: "store",
		CacheStore:   store,
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// Seed a certificate expiring in 5 days — inside the 7-day warning window.
	store.m["mail.example.com"] = expiringCertBundle(t, "mail.example.com", 5*24*time.Hour)

	alertCfg := alert.DefaultConfig()
	alertCfg.Enabled = true
	alertCfg.WebhookURL = webhook.URL
	alertCfg.TLSWarningDays = 7
	alertCfg.MinInterval = 0
	alertMgr := alert.NewManager(alertCfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	alertMgr.SetAllowPrivateIP(true)

	srv := &Server{tlsManager: tlsMgr, alertMgr: alertMgr}
	srv.checkAlerts()

	var got *alert.Alert
	for i := range alerts {
		if alerts[i].Name == "tls_certificate_expiring" {
			got = &alerts[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected a tls_certificate_expiring alert, got %d alerts: %+v", len(alerts), alerts)
	}
	if got.Details["domain"] != "mail.example.com" {
		t.Fatalf("alert domain = %v, want mail.example.com", got.Details["domain"])
	}
}

// TestCheckAlertsTLSHealthyQuiet verifies a certificate with plenty of validity
// raises no alert — the warning must not fire for a cert renewing normally.
func TestCheckAlertsTLSHealthyQuiet(t *testing.T) {
	var alerts []alert.Alert
	webhook := alertCaptureServer(t, &alerts)
	defer webhook.Close()

	store := &memCacheStore{m: map[string][]byte{}}
	tlsMgr, err := tlspkg.NewManager(tlspkg.Config{
		AutoTLS:      true,
		Domains:      []string{"mail.example.com"},
		CacheBackend: "store",
		CacheStore:   store,
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	store.m["mail.example.com"] = expiringCertBundle(t, "mail.example.com", 60*24*time.Hour)

	alertCfg := alert.DefaultConfig()
	alertCfg.Enabled = true
	alertCfg.WebhookURL = webhook.URL
	alertCfg.TLSWarningDays = 7
	alertCfg.MinInterval = 0
	alertMgr := alert.NewManager(alertCfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	alertMgr.SetAllowPrivateIP(true)

	srv := &Server{tlsManager: tlsMgr, alertMgr: alertMgr}
	srv.checkAlerts()

	for _, a := range alerts {
		if a.Name == "tls_certificate_expiring" || a.Name == "tls_certificate_expired" {
			t.Fatalf("a healthy certificate must not raise %q", a.Name)
		}
	}
}
