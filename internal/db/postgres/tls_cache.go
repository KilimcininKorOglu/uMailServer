package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/umailserver/umailserver/internal/db"
)

// This file implements the db.Store TLS certificate-cache blob methods on the
// PostgreSQL backend — a generic keyed byte store, the counterpart of the bbolt
// implementation in internal/db/db.go.

// GetTLSCacheEntry returns the raw bytes stored under key, or a wrapped
// ErrNotFound when the key is absent.
func (d *DB) GetTLSCacheEntry(key string) ([]byte, error) {
	ctx := context.Background()
	var data []byte
	err := d.pool.QueryRow(ctx, `SELECT data FROM tls_cache WHERE key=$1`, key).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: tls cache key %q not found: %w", key, db.ErrNotFound)
		}
		return nil, fmt.Errorf("postgres: get tls cache key %q: %w", key, err)
	}
	return data, nil
}

// PutTLSCacheEntry stores raw bytes under key, overwriting any existing value.
func (d *DB) PutTLSCacheEntry(key string, data []byte) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO tls_cache (key, data, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET data=EXCLUDED.data, updated_at=now()`,
		key, data,
	); err != nil {
		return fmt.Errorf("postgres: put tls cache key %q: %w", key, err)
	}
	return nil
}

// DeleteTLSCacheEntry removes key from the TLS cache; absence is not an error.
func (d *DB) DeleteTLSCacheEntry(key string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM tls_cache WHERE key=$1`, key); err != nil {
		return fmt.Errorf("postgres: delete tls cache key %q: %w", key, err)
	}
	return nil
}
