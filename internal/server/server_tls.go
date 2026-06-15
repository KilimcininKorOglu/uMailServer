package server

import (
	"errors"

	"github.com/umailserver/umailserver/internal/db"
)

// tlsCacheStore adapts the canonical db.Store to the tls package's CacheStore
// (the keyed-blob contract the ACME certificate cache needs). It maps a missing
// key to (nil, nil) — never an error — so the autocert adapter can translate it
// to a cache miss rather than aborting issuance.
type tlsCacheStore struct{ store db.Store }

func (c tlsCacheStore) Get(key string) ([]byte, error) {
	data, err := c.store.GetTLSCacheEntry(key)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	return data, err
}

func (c tlsCacheStore) Put(key string, data []byte) error {
	return c.store.PutTLSCacheEntry(key, data)
}

func (c tlsCacheStore) Delete(key string) error {
	return c.store.DeleteTLSCacheEntry(key)
}
