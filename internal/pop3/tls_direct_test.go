package pop3

import (
	"crypto/tls"
	"testing"
)

// TestSetTLSConfigDirect_ServedWithoutFiles asserts that a pre-built *tls.Config
// (such as the TLS manager's live GetCertificate callback used under ACME, where
// no static cert/key file paths exist) is both advertised and negotiated by
// POP3. This matters because POP3 previously loaded a static key pair from fixed
// file paths only; an ACME-only deployment has empty CertFile/KeyFile, so STLS
// was permanently broken on POP3 even though SMTP/IMAP/ManageSieve served the
// issued certificate through the same callback.
func TestSetTLSConfigDirect_ServedWithoutFiles(t *testing.T) {
	srv := NewServer("127.0.0.1:0", nil, nil)

	// No file-based TLSConfig is ever set: this mirrors ACME-only config.
	if srv.tlsAvailable() {
		t.Fatal("TLS reported available before any config is set")
	}

	live := &tls.Config{MinVersion: tls.VersionTLS13}
	srv.SetTLSConfigDirect(live)

	if !srv.tlsAvailable() {
		t.Fatal("TLS not advertised after SetTLSConfigDirect; STLS would be refused")
	}

	got, err := srv.getTLSConfig()
	if err != nil {
		t.Fatalf("getTLSConfig returned an error for a pre-built config: %v", err)
	}
	if got != live {
		t.Fatal("getTLSConfig did not return the pre-built config; POP3 would negotiate a different certificate than the other listeners")
	}
}

// TestSetTLSConfigDirect_PrecedenceOverFiles asserts the pre-built config wins
// over file paths, so wiring the live callback cannot be silently shadowed by a
// stale file-based config that would fail to load.
func TestSetTLSConfigDirect_PrecedenceOverFiles(t *testing.T) {
	srv := NewServer("127.0.0.1:0", nil, nil)
	srv.SetTLSConfig(&TLSConfig{CertFile: "does-not-exist.pem", KeyFile: "missing.pem"})

	live := &tls.Config{MinVersion: tls.VersionTLS12}
	srv.SetTLSConfigDirect(live)

	got, err := srv.getTLSConfig()
	if err != nil {
		t.Fatalf("getTLSConfig errored despite the pre-built config taking precedence: %v", err)
	}
	if got != live {
		t.Fatal("file-based config shadowed the pre-built live config")
	}
}
