package sieve

import "testing"

func TestManageSieveServerSetListenAddr(t *testing.T) {
	srv := NewManageSieveServer(nil, nil)

	srv.SetListenAddr("127.0.0.1:14190")
	if srv.listenAddr != "127.0.0.1:14190" {
		t.Fatalf("expected custom listen addr, got %s", srv.listenAddr)
	}

	srv.SetListenAddr("")
	if srv.listenAddr != "127.0.0.1:14190" {
		t.Fatalf("expected listen addr to remain unchanged, got %s", srv.listenAddr)
	}
}
