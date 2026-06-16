package tls

import (
	"context"
	"testing"
)

// TestDecideOnDemandStripsPort asserts the on-demand authorization gate compares
// hostnames, not host:port. The HTTP-01 challenge may carry r.Host with a port
// when validated on a non-default port (for example a local Pebble on :5002).
// Without stripping the port the gate refuses the challenge and issuance fails,
// even though the hostname is authorized.
func TestDecideOnDemandStripsPort(t *testing.T) {
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
		if err := m.decideOnDemand(context.Background(), host); err != nil {
			t.Errorf("host %q should be allowed, got: %v", host, err)
		}
	}

	// An unauthorized host must still be refused, port or not, so the port-strip
	// does not widen what can be issued.
	for _, host := range []string{"evil.test", "evil.test:5002"} {
		if err := m.decideOnDemand(context.Background(), host); err == nil {
			t.Errorf("host %q should be refused", host)
		}
	}
}
