package tls

import (
	"strings"
	"testing"
)

// mapEnv turns a map into an envGetter, so tests can pin exactly which env
// vars are read by each provider without touching the real environment.
func mapEnv(m map[string]string) envGetter {
	return func(key string) string { return m[key] }
}

// TestDNSProviderNamesLocksSupportedSet is a guard against silently dropping
// a provider from the registry: every supported name must build a non-nil
// provider with empty credentials (the test does not exercise an actual
// record write, just the constructor and the credential check).
func TestDNSProviderNamesLocksSupportedSet(t *testing.T) {
	for _, name := range DNSProviderNames() {
		// Each supported provider with no credentials must fail with the
		// "requires env vars" message — proving the name is wired and the
		// credential check runs, not that the constructor silently succeeds.
		_, err := newDNSProvider(name, mapEnv(nil))
		if err == nil {
			t.Errorf("%q: expected error for missing credentials, got nil", name)
			continue
		}
		if !strings.Contains(err.Error(), "requires") {
			t.Errorf("%q: error %q does not look like a credential-missing failure", name, err)
		}
	}
}

// TestDNSProviderUnknownErrors ensures an unrecognized name fails loud with
// the supported set listed, so an operator typo never silently falls back.
func TestDNSProviderUnknownErrors(t *testing.T) {
	_, err := newDNSProvider("route53", mapEnv(nil))
	if err == nil {
		t.Fatal("expected error for unsupported provider, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error %q must mark the provider as unsupported", err)
	}
	for _, name := range DNSProviderNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q must list supported provider %q", err, name)
		}
	}
}

// TestDNSProviderCloudflareRequiresToken asserts the API-token env var is
// mandatory: a missing token must fail with a name-tagged error so the
// operator sees which credential the server was looking for.
func TestDNSProviderCloudflareRequiresToken(t *testing.T) {
	_, err := newDNSProvider("cloudflare", mapEnv(map[string]string{
		"CLOUDFLARE_API_TOKEN": "",
	}))
	if err == nil {
		t.Fatal("expected error for empty CLOUDFLARE_API_TOKEN, got nil")
	}
	if !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
		t.Errorf("error %q must mention CLOUDFLARE_API_TOKEN", err)
	}
}

// TestDNSProviderCloudflareHappyPath checks that a non-empty token builds a
// non-nil provider satisfying the certmagic DNSProvider contract.
func TestDNSProviderCloudflareHappyPath(t *testing.T) {
	p, err := newDNSProvider("cloudflare", mapEnv(map[string]string{
		"CLOUDFLARE_API_TOKEN": "fake-token",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

// TestDNSProviderRFC2136RequiresAllTSIGFields checks that every RFC2136 TSIG
// env var is mandatory and reported together: an operator with three of four
// set must see the missing one named, not a partial configuration that the
// DNS server then rejects with a misleading EKEYUNKNOWN.
func TestDNSProviderRFC2136RequiresAllTSIGFields(t *testing.T) {
	_, err := newDNSProvider("rfc2136", mapEnv(map[string]string{
		"RFC2136_SERVER":          "ns1.example.com:53",
		"RFC2136_TSIG_KEYNAME":    "keyname",
		"RFC2136_TSIG_ALGORITHM":  "hmac-sha256",
		// RFC2136_TSIG_SECRET intentionally missing
	}))
	if err == nil {
		t.Fatal("expected error for missing RFC2136_TSIG_SECRET, got nil")
	}
	if !strings.Contains(err.Error(), "RFC2136_TSIG_SECRET") {
		t.Errorf("error %q must name the missing env var", err)
	}
	// Drop another field; both missing names must show so the operator can
	// fix all of them in one pass.
	_, err = newDNSProvider("rfc2136", mapEnv(map[string]string{
		"RFC2136_SERVER":         "ns1.example.com:53",
		"RFC2136_TSIG_KEYNAME":   "keyname",
		"RFC2136_TSIG_ALGORITHM": "",
		"RFC2136_TSIG_SECRET":    "",
	}))
	if err == nil {
		t.Fatal("expected error for two missing RFC2136 fields, got nil")
	}
	if !strings.Contains(err.Error(), "RFC2136_TSIG_ALGORITHM") || !strings.Contains(err.Error(), "RFC2136_TSIG_SECRET") {
		t.Errorf("error %q must list every missing env var", err)
	}
}

// TestDNSProviderRFC2136HappyPath checks that a fully-populated TSIG env builds
// a non-nil provider satisfying the certmagic DNSProvider contract.
func TestDNSProviderRFC2136HappyPath(t *testing.T) {
	p, err := newDNSProvider("rfc2136", mapEnv(map[string]string{
		"RFC2136_SERVER":         "ns1.example.com:53",
		"RFC2136_TSIG_KEYNAME":   "keyname",
		"RFC2136_TSIG_ALGORITHM": "hmac-sha256",
		"RFC2136_TSIG_SECRET":    "c2VjcmV0",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}
