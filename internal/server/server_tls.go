package server

import (
	"context"
	"errors"
	"time"

	"github.com/umailserver/umailserver/internal/db"
	umailTLS "github.com/umailserver/umailserver/internal/tls"
)

// tlsCacheStore adapts the canonical db.Store to the tls package's CacheStore
// (the keyed-blob contract the ACME certificate cache needs). It maps a missing
// key to (nil, nil) — never an error — so the cache adapter can translate it to
// a miss rather than aborting issuance. List/Stat expose the prefix-enumerate
// and stat the certmagic.Storage adapter builds on, so a node can enumerate
// which certificates the cluster already holds.
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

// List returns every TLS-cache key with the given prefix (empty = all), in
// ascending order, delegating to the store's prefix scan.
func (c tlsCacheStore) List(prefix string) ([]string, error) {
	return c.store.ListTLSCacheKeys(prefix)
}

// Stat returns the byte size and last-modified time of the value under key.
func (c tlsCacheStore) Stat(key string) (int64, time.Time, error) {
	return c.store.StatTLSCacheEntry(key)
}

// tlsLockTTL bounds how long a node may hold a TLS issuance/renewal lock. It is
// deliberately far larger than a worst-case ACME obtain so a slow-but-alive
// issuance is never stolen mid-flight; if a holder crashes, another node may
// steal the row once this TTL elapses.
const tlsLockTTL = 5 * time.Minute

// tlsLockRetry is the interval between non-blocking acquire attempts while
// waiting for a contended lock.
const tlsLockRetry = 1 * time.Second

// tlsLocker adapts db.Store's single-shot try-lock to the tls.Locker blocking
// contract: Lock spins on the try primitive (honoring ctx) until the lease is
// acquired, Unlock releases it when still owned. owner identifies the holder so
// a live lease held by another node is never released by the wrong node.
type tlsLocker struct {
	store db.Store
	owner string
	ttl   time.Duration
}

// newTLSLocker builds a tls.Locker backed by the shared db.Store. owner must be
// unique per node (so concurrent nodes' leases are distinguishable); the server
// generates it at startup and injects it here.
func newTLSLocker(store db.Store, owner string) umailTLS.Locker {
	return tlsLocker{store: store, owner: owner, ttl: tlsLockTTL}
}

func (l tlsLocker) Lock(ctx context.Context, name string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		acquired, err := l.store.LockTLSCache(ctx, name, l.owner, l.ttl)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(tlsLockRetry):
		}
	}
}

func (l tlsLocker) Unlock(ctx context.Context, name string) error {
	return l.store.UnlockTLSCache(ctx, name, l.owner)
}
