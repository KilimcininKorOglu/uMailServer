package db

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// TestTLSCacheStore covers the generic keyed-blob contract the ACME certificate
// cache relies on: an absent key is reported as ErrNotFound (so the adapter can
// translate it to a cache miss rather than a fatal error), bytes round-trip
// verbatim, Put overwrites, and Delete is idempotent.
func TestTLSCacheStore(t *testing.T) {
	database, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	}()

	// Absent key must surface as ErrNotFound, distinct from a storage fault.
	if _, err := database.GetTLSCacheEntry("acme.org"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTLSCacheEntry(absent) = %v, want ErrNotFound", err)
	}

	// Certificate bundles contain NUL bytes and binary DER; they must round-trip
	// byte-for-byte, so the store must not JSON-encode or otherwise transform them.
	want := []byte{0x00, 0x01, 'P', 'E', 'M', 0xff, 0x00, '\n'}
	if err := database.PutTLSCacheEntry("acme.org", want); err != nil {
		t.Fatalf("PutTLSCacheEntry: %v", err)
	}
	got, err := database.GetTLSCacheEntry("acme.org")
	if err != nil {
		t.Fatalf("GetTLSCacheEntry: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("GetTLSCacheEntry = %v, want %v (verbatim round-trip)", got, want)
	}

	// Put overwrites in place.
	want2 := []byte("renewed-bundle")
	if err := database.PutTLSCacheEntry("acme.org", want2); err != nil {
		t.Fatalf("PutTLSCacheEntry (overwrite): %v", err)
	}
	got, err = database.GetTLSCacheEntry("acme.org")
	if err != nil {
		t.Fatalf("GetTLSCacheEntry (after overwrite): %v", err)
	}
	if !bytes.Equal(got, want2) {
		t.Fatalf("after overwrite = %q, want %q", got, want2)
	}

	// Delete removes the key; a second delete of the same key is not an error.
	if err := database.DeleteTLSCacheEntry("acme.org"); err != nil {
		t.Fatalf("DeleteTLSCacheEntry: %v", err)
	}
	if _, err := database.GetTLSCacheEntry("acme.org"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete = %v, want ErrNotFound", err)
	}
	if err := database.DeleteTLSCacheEntry("acme.org"); err != nil {
		t.Fatalf("DeleteTLSCacheEntry (absent) should be a no-op, got: %v", err)
	}
}

// TestTLSCacheListStat covers the prefix-list and stat contract certmagic.Storage
// builds on: List backs Storage.List (a node enumerating which certs the cluster
// already holds under a key prefix), Stat backs Storage.Stat (size for asset
// maintenance), and an absent key must surface as ErrNotFound so the adapter can
// translate it to fs.ErrNotExist rather than a fatal error. If these returned the
// wrong key set or swallowed absence, a cluster node would mis-enumerate shared
// certificates or treat a missing asset as a storage fault.
func TestTLSCacheListStat(t *testing.T) {
	database, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	}()

	// Seed certmagic-shaped path keys under one prefix plus an unrelated key.
	seed := map[string][]byte{
		"certificates/acme/a.example.com.crt": {0x00, 'A', 0xff},
		"certificates/acme/b.example.com.crt": []byte("bbbb"),
		"certificates/acme/a.example.com.key": []byte("k"),
		"acme/account.json":                   []byte("{}"),
	}
	for k, v := range seed {
		if err := database.PutTLSCacheEntry(k, v); err != nil {
			t.Fatalf("PutTLSCacheEntry(%q): %v", k, err)
		}
	}

	// Prefix list returns ONLY the prefixed keys, in ascending order (the bbolt
	// cursor is byte-sorted) — never the unrelated acme/account.json.
	gotPrefix, err := database.ListTLSCacheKeys("certificates/acme/")
	if err != nil {
		t.Fatalf("ListTLSCacheKeys(prefix): %v", err)
	}
	wantPrefix := []string{
		"certificates/acme/a.example.com.crt",
		"certificates/acme/a.example.com.key",
		"certificates/acme/b.example.com.crt",
	}
	if !reflect.DeepEqual(gotPrefix, wantPrefix) {
		t.Fatalf("ListTLSCacheKeys(prefix) = %v, want %v", gotPrefix, wantPrefix)
	}

	// Empty prefix enumerates every key (still sorted), including the unrelated one.
	gotAll, err := database.ListTLSCacheKeys("")
	if err != nil {
		t.Fatalf("ListTLSCacheKeys(\"\"): %v", err)
	}
	if len(gotAll) != len(seed) {
		t.Fatalf("ListTLSCacheKeys(\"\") returned %d keys, want %d", len(gotAll), len(seed))
	}

	// Stat reports the exact stored byte length (3 bytes incl. the NUL).
	size, _, err := database.StatTLSCacheEntry("certificates/acme/a.example.com.crt")
	if err != nil {
		t.Fatalf("StatTLSCacheEntry: %v", err)
	}
	if size != 3 {
		t.Fatalf("StatTLSCacheEntry size = %d, want 3", size)
	}

	// An absent key must be ErrNotFound, not a zero-size success — otherwise the
	// adapter cannot distinguish "no such cert" from "empty cert".
	if _, _, err := database.StatTLSCacheEntry("certificates/acme/missing.crt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StatTLSCacheEntry(absent) = %v, want ErrNotFound", err)
	}
}
