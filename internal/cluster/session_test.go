package cluster

import "testing"

// TestParseSentinelURL covers the redis-sentinel:// form used for HA Redis:
// comma-separated Sentinel addresses, an optional password, the master name,
// and an optional DB number. These are what NewFailoverClient needs to follow a
// Redis master failover.
func TestParseSentinelURL(t *testing.T) {
	opt, err := parseSentinelURL("redis-sentinel://s1:26379,s2:26379,s3:26379/mymaster")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opt.MasterName != "mymaster" {
		t.Errorf("master = %q, want mymaster", opt.MasterName)
	}
	if len(opt.SentinelAddrs) != 3 || opt.SentinelAddrs[0] != "s1:26379" || opt.SentinelAddrs[2] != "s3:26379" {
		t.Errorf("sentinel addrs = %v", opt.SentinelAddrs)
	}
	if opt.DB != 0 || opt.Password != "" {
		t.Errorf("unexpected db/password: db=%d pw=%q", opt.DB, opt.Password)
	}

	// Password + DB.
	opt, err = parseSentinelURL("redis-sentinel://:s3cret@h1:26379,h2:26379/cluster-master/2")
	if err != nil {
		t.Fatalf("parse with auth+db: %v", err)
	}
	if opt.MasterName != "cluster-master" || opt.DB != 2 {
		t.Errorf("master/db = %q/%d, want cluster-master/2", opt.MasterName, opt.DB)
	}
	if opt.Password != "s3cret" || opt.SentinelPassword != "s3cret" {
		t.Errorf("password not parsed: %q / %q", opt.Password, opt.SentinelPassword)
	}
	if len(opt.SentinelAddrs) != 2 {
		t.Errorf("sentinel addrs = %v, want 2", opt.SentinelAddrs)
	}

	// Errors: missing master, invalid db.
	if _, err := parseSentinelURL("redis-sentinel://s1:26379"); err == nil {
		t.Error("expected error for missing /masterName")
	}
	if _, err := parseSentinelURL("redis-sentinel://s1:26379/m/notanumber"); err == nil {
		t.Error("expected error for non-numeric db")
	}
}

// TestNewRedisClientForms verifies newRedisClient accepts a Sentinel URL, a
// plain redis:// URL, and a bare host:port without error (it builds the client
// lazily, so no live server is needed).
func TestNewRedisClientForms(t *testing.T) {
	for _, url := range []string{
		"redis-sentinel://s1:26379,s2:26379/mymaster/0",
		"redis://:pw@localhost:6379/0",
		"localhost:6379",
	} {
		c, err := newRedisClient(url)
		if err != nil {
			t.Fatalf("newRedisClient(%q): %v", url, err)
		}
		if c == nil {
			t.Fatalf("newRedisClient(%q) returned nil", url)
		}
		if err := c.Close(); err != nil {
			t.Errorf("close(%q): %v", url, err)
		}
	}
}
