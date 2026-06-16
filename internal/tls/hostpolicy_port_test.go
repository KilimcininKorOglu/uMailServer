package tls

import (
	"context"
	"testing"
)

// TestHostPolicyStripsPort asserts the ACME host policy compares hostnames, not
// host:port. autocert's HTTP-01 handler passes r.Host, which carries a port when
// the challenge is validated on a non-default port (for example a local Pebble
// validating on :5002). Without stripping the port the policy refuses the
// challenge with 403 and issuance fails, even though the hostname is authorized.
func TestHostPolicyStripsPort(t *testing.T) {
	m, err := NewManager(Config{Domains: []string{"mail.example.com"}}, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	allowed := []string{
		"mail.example.com",      // bare host (SNI path)
		"mail.example.com:5002", // host:port (HTTP-01 handler path, non-default port)
		"mail.example.com:80",   // host:port (default challenge port)
		"MAIL.example.com:5002", // case-insensitive with port
	}
	for _, host := range allowed {
		if err := m.hostPolicy(context.Background(), host); err != nil {
			t.Errorf("host %q should be allowed, got: %v", host, err)
		}
	}

	// An unauthorized host must still be refused, port or not, so the port-strip
	// does not widen what can be issued.
	for _, host := range []string{"evil.test", "evil.test:5002"} {
		if err := m.hostPolicy(context.Background(), host); err == nil {
			t.Errorf("host %q should be refused", host)
		}
	}
}
