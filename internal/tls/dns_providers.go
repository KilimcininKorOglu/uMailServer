package tls

import (
	"fmt"
	"os"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"github.com/libdns/rfc2136"
)

// envGetter is the indirection used to read provider credentials. The
// production code path pulls from os.Getenv; tests inject a map-backed stub
// to assert which credentials are required for each provider without
// touching the real environment.
type envGetter func(string) string

// osEnv is the default envGetter reading the process environment.
func osEnv(key string) string { return os.Getenv(key) }

// DNSProviderNames returns the registry of supported DNS-01 providers. It
// exists so an operator-facing error (an unsupported name in config) and the
// test that locks the set can be driven from one source.
func DNSProviderNames() []string {
	return []string{"cloudflare", "rfc2136"}
}

// newDNSProvider builds a certmagic.DNSProvider for the named DNS-01 driver,
// sourcing its credentials from env. The challenge type is fixed to TXT: the
// provider only ever has to satisfy libdns.RecordAppender + libdns.RecordDeleter
// for the _acme-challenge subdomain.
//
// Each provider fails loud when its required env vars are missing — silent
// fallback to the wrong credential set (e.g. an empty API token) is a security
// hazard: it would burn the CA rate limit and leak the issuance intent into
// the zone of whoever owns the token.
func newDNSProvider(name string, getenv envGetter) (certmagic.DNSProvider, error) {
	switch name {
	case "cloudflare":
		token := getenv("CLOUDFLARE_API_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("dns-01 provider %q requires CLOUDFLARE_API_TOKEN env var", name)
		}
		return &cloudflare.Provider{APIToken: token}, nil
	case "rfc2136":
		// TSIG-authenticated dynamic updates. Every field is required — RFC 2845
		// silently substituting empty secrets produces unsigned updates that the
		// authoritative server rejects with EKEYUNKNOWN, which certmagic then
		// reports as a generic timeout; failing loud here is the right level.
		server := getenv("RFC2136_SERVER")
		keyName := getenv("RFC2136_TSIG_KEYNAME")
		keyAlg := getenv("RFC2136_TSIG_ALGORITHM")
		key := getenv("RFC2136_TSIG_SECRET")
		missing := []string{}
		if server == "" {
			missing = append(missing, "RFC2136_SERVER")
		}
		if keyName == "" {
			missing = append(missing, "RFC2136_TSIG_KEYNAME")
		}
		if keyAlg == "" {
			missing = append(missing, "RFC2136_TSIG_ALGORITHM")
		}
		if key == "" {
			missing = append(missing, "RFC2136_TSIG_SECRET")
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("dns-01 provider %q requires env vars: %v", name, missing)
		}
		return &rfc2136.Provider{
			Server:  server,
			KeyName: keyName,
			KeyAlg:  keyAlg,
			Key:     key,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported dns-01 provider %q (supported: %v)", name, DNSProviderNames())
	}
}
