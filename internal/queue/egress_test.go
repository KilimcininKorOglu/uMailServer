package queue

import (
	"net"
	"testing"
)

// TestMxDialerBindsLocalAddr verifies the outbound dialer binds a valid egress
// IP as its source address, and leaves LocalAddr unset (default route) for an
// empty or malformed IP.
func TestMxDialerBindsLocalAddr(t *testing.T) {
	d := mxDialer("203.0.113.10")
	la, ok := d.LocalAddr.(*net.TCPAddr)
	if !ok || la == nil {
		t.Fatalf("expected *net.TCPAddr LocalAddr, got %#v", d.LocalAddr)
	}
	if la.IP.String() != "203.0.113.10" {
		t.Errorf("LocalAddr IP = %q, want 203.0.113.10", la.IP)
	}
	if d.Timeout == 0 {
		t.Error("dialer must keep a connect timeout")
	}

	for _, bad := range []string{"", "not-an-ip", "203.0.113.999"} {
		if got := mxDialer(bad); got.LocalAddr != nil {
			t.Errorf("mxDialer(%q).LocalAddr = %v, want nil (default route)", bad, got.LocalAddr)
		}
	}
}

// TestMxPoolKey verifies the pool key separates connections by egress IP so a
// connection bound to one source IP is never reused for another, while the
// default-route case keeps the bare MX host as the key (backward compatible).
func TestMxPoolKey(t *testing.T) {
	if got := mxPoolKey("mx.example.com", ""); got != "mx.example.com" {
		t.Errorf("default-route key = %q, want bare host", got)
	}
	a := mxPoolKey("mx.example.com", "203.0.113.10")
	b := mxPoolKey("mx.example.com", "203.0.113.11")
	if a == b {
		t.Errorf("different egress IPs must yield different pool keys, both %q", a)
	}
	if a == "mx.example.com" {
		t.Errorf("bound key must differ from the default-route key")
	}
}
