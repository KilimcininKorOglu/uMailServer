package tls

import (
	"context"

	"golang.org/x/crypto/acme/autocert"
)

// CacheStore is the minimal keyed-blob storage the ACME certificate cache adapts
// onto. A missing key MUST be reported by Get as (nil, nil) — never as an error —
// so the adapter can translate it to autocert.ErrCacheMiss. The tls package stays
// free of any storage backend; the server injects an implementation backed by the
// canonical store, which is what lets active-active nodes share certificates.
type CacheStore interface {
	Get(key string) ([]byte, error)
	Put(key string, data []byte) error
	Delete(key string) error
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
