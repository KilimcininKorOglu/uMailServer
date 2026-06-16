package tls

import (
	"context"
	"errors"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// ErrNotFound is the sentinel a CacheStore.Stat returns for an absent key. It is
// distinct from a real storage fault so the certmagic adapter can map it to
// fs.ErrNotExist; Get keeps its own (nil, nil) absent contract for the autocert
// adapter.
var ErrNotFound = errors.New("tls: cache entry not found")

// CacheStore is the minimal keyed-blob storage the certificate cache adapts
// onto. A missing key MUST be reported by Get as (nil, nil) — never as an error —
// so the adapter can translate it to autocert.ErrCacheMiss. Stat instead reports
// an absent key as ErrNotFound (it has no nil-value escape hatch). The tls
// package stays free of any storage backend; the server injects an
// implementation backed by the canonical store, which is what lets active-active
// nodes share certificates.
type CacheStore interface {
	Get(key string) ([]byte, error)
	Put(key string, data []byte) error
	Delete(key string) error
	// List returns every key with the given prefix (empty = all) in ascending
	// key order.
	List(prefix string) ([]string, error)
	// Stat returns the byte size and last-modified time of the value under key,
	// or ErrNotFound when the key is absent.
	Stat(key string) (size int64, modified time.Time, err error)
}

// Locker is the distributed-lock contract certmagic.Storage.Lock/Unlock adapt
// onto. Lock blocks until name is acquired or ctx is canceled; Unlock releases
// it. The implementation lives outside the tls package (in internal/server) so
// the tls package stays free of db/cluster imports — a Locker is injected the
// same way a CacheStore is.
type Locker interface {
	Lock(ctx context.Context, name string) error
	Unlock(ctx context.Context, name string) error
}

// storeCache adapts a CacheStore to autocert.Cache so issued certificates and the
// ACME account key persist in the shared store instead of the local filesystem.
type storeCache struct {
	store CacheStore
}

// Get returns the cached bytes, mapping an absent key to autocert.ErrCacheMiss as
// the autocert.Cache contract requires.
func (c storeCache) Get(_ context.Context, key string) ([]byte, error) {
	data, err := c.store.Get(key)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, autocert.ErrCacheMiss
	}
	return data, nil
}

func (c storeCache) Put(_ context.Context, key string, data []byte) error {
	return c.store.Put(key, data)
}

func (c storeCache) Delete(_ context.Context, key string) error {
	return c.store.Delete(key)
}
