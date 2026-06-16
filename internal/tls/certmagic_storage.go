package tls

import (
	"context"
	"errors"
	"io/fs"

	"github.com/caddyserver/certmagic"
)

// certmagicStorage adapts the injected CacheStore + Locker to certmagic.Storage
// so issued certificates, account keys, and coordination locks all live in the
// canonical shared store. With certmagic, "instances sharing the same storage
// are the same cluster": active-active nodes that share the db-backed store
// coordinate issuance via the lock rather than a leader, and any node can
// obtain a certificate on-demand at handshake. A missing key maps to
// fs.ErrNotExist on Load/Stat as certmagic requires.
type certmagicStorage struct {
	store  CacheStore
	locker Locker
}

// newCertmagicStorage wires the shared keyed-blob store and distributed locker
// behind certmagic.Storage. Both are injected so this package stays free of
// db/cluster imports.
func newCertmagicStorage(store CacheStore, locker Locker) certmagicStorage {
	return certmagicStorage{store: store, locker: locker}
}

func (s certmagicStorage) Store(_ context.Context, key string, value []byte) error {
	return s.store.Put(key, value)
}

// Load maps a CacheStore absent key (Get returns nil,nil) to fs.ErrNotExist;
// any real storage error is returned verbatim.
func (s certmagicStorage) Load(_ context.Context, key string) ([]byte, error) {
	data, err := s.store.Get(key)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fs.ErrNotExist
	}
	return data, nil
}

func (s certmagicStorage) Delete(_ context.Context, key string) error {
	return s.store.Delete(key)
}

// Exists reports whether key is present. A Get miss is (nil,nil); a storage
// fault also reads as absent (best effort, matching certmagic.FileStorage).
func (s certmagicStorage) Exists(_ context.Context, key string) bool {
	data, err := s.store.Get(key)
	return err == nil && data != nil
}

// List returns every key under prefix. certmagic's recursive flag is not
// honored separately: keys are path-like and the store already returns the full
// sorted prefix subtree, which serves both recursive and the shallow list.
func (s certmagicStorage) List(_ context.Context, prefix string, _ bool) ([]string, error) {
	return s.store.List(prefix)
}

// Stat maps a CacheStore absent key (ErrNotFound) to fs.ErrNotExist.
func (s certmagicStorage) Stat(_ context.Context, key string) (certmagic.KeyInfo, error) {
	size, modified, err := s.store.Stat(key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return certmagic.KeyInfo{}, fs.ErrNotExist
		}
		return certmagic.KeyInfo{}, err
	}
	return certmagic.KeyInfo{Key: key, Size: size, Modified: modified, IsTerminal: true}, nil
}

func (s certmagicStorage) Lock(ctx context.Context, name string) error {
	return s.locker.Lock(ctx, name)
}

func (s certmagicStorage) Unlock(ctx context.Context, name string) error {
	return s.locker.Unlock(ctx, name)
}

// Compile-time assertion that certmagicStorage satisfies certmagic.Storage.
var _ certmagic.Storage = certmagicStorage{}
