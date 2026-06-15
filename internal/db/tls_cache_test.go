package db

import (
	"bytes"
	"errors"
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
