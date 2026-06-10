package server

import (
	"log/slog"
	"net"
	"testing"

	"github.com/umailserver/umailserver/internal/config"
	"github.com/umailserver/umailserver/internal/db"
)

// TestResolveEgressIP encodes the per-domain egress-IP contract: a domain
// assigned to a Relay IP group egresses from one of that group's IPs (stably,
// so its sending reputation stays anchored), while unassigned domains, unknown
// groups, and unknown domains fall back to the default route ("").
func TestResolveEgressIP(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := d.Close(); cerr != nil {
			t.Errorf("db close: %v", cerr)
		}
	})

	s := &Server{database: d, logger: slog.Default()}
	s.config.Store(&config.Config{Relay: config.RelayConfig{IPGroups: []config.IPGroupConfig{
		{Name: "marketing", IPs: []string{"203.0.113.10", "203.0.113.11"}},
		{Name: "transactional", IPs: []string{"203.0.113.20"}},
		{Name: "broken", IPs: []string{"not-an-ip"}},
	}}})

	mkDomain := func(name, group string) {
		dom := &db.DomainData{Name: name, IsActive: true}
		if group != "" {
			dom.Settings = map[string]string{domainEgressIPGroupKey: group}
		}
		if err := d.CreateDomain(dom); err != nil {
			t.Fatalf("CreateDomain %s: %v", name, err)
		}
	}
	mkDomain("marketing.test", "marketing")
	mkDomain("tx.test", "transactional")
	mkDomain("plain.test", "")
	mkDomain("badgroup.test", "nonexistent")
	mkDomain("emptyips.test", "broken")

	marketingIPs := map[string]bool{"203.0.113.10": true, "203.0.113.11": true}

	// resolveEgressIP receives the already-extracted sender domain (the queue
	// strips the local part before calling it).
	got := s.resolveEgressIP("marketing.test")
	if !marketingIPs[got] {
		t.Errorf("marketing.test egress = %q, want one of %v", got, marketingIPs)
	}
	// ...and the pick is stable across calls (reputation anchoring).
	if again := s.resolveEgressIP("marketing.test"); again != got {
		t.Errorf("egress not stable: %q then %q", got, again)
	}

	// A single-IP group always yields that IP.
	if got := s.resolveEgressIP("tx.test"); got != "203.0.113.20" {
		t.Errorf("tx.test egress = %q, want 203.0.113.20", got)
	}

	// Unassigned domain, unknown group, group with no valid IPs, and unknown
	// domain all fall back to the default route.
	for _, domain := range []string{"plain.test", "badgroup.test", "emptyips.test", "unknown.test", ""} {
		if got := s.resolveEgressIP(domain); got != "" {
			t.Errorf("resolveEgressIP(%q) = %q, want \"\" (default route)", domain, got)
		}
	}
}

// TestMxDialerBindsEgress is a queue-package concern but the binding contract is
// visible here too: a valid egress IP becomes the dialer's LocalAddr, anything
// else leaves it on the default route. (The queue package owns the dialer; this
// asserts the net.IP parse boundary the server feeds it.)
func TestEgressIPParseBoundary(t *testing.T) {
	for _, ip := range []string{"203.0.113.10", "::1", "127.0.0.1"} {
		if net.ParseIP(ip) == nil {
			t.Errorf("expected %q to parse as an IP", ip)
		}
	}
	for _, bad := range []string{"", "not-an-ip", "203.0.113.999"} {
		if net.ParseIP(bad) != nil {
			t.Errorf("expected %q to be rejected as an IP", bad)
		}
	}
}
