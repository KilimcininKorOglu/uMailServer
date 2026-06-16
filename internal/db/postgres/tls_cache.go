package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// ListTLSCacheKeys returns every TLS-cache key with the given prefix (empty
// prefix = all keys) in ascending key order. starts_with avoids LIKE wildcard
// escaping for keys that may contain % or _.
func (d *DB) ListTLSCacheKeys(prefix string) ([]string, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT key FROM tls_cache WHERE starts_with(key, $1) ORDER BY key`, prefix)
	if err != nil {
		return nil, fmt.Errorf("postgres: list tls cache keys %q: %w", prefix, err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("postgres: scan tls cache key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list tls cache keys %q: %w", prefix, err)
	}
	return keys, nil
}

// StatTLSCacheEntry returns the byte size and last-modified time of the value
// under key, or a wrapped ErrNotFound when the key is absent.
func (d *DB) StatTLSCacheEntry(key string) (int64, time.Time, error) {
	ctx := context.Background()
	var size int64
	var modified time.Time
	err := d.pool.QueryRow(ctx, `SELECT length(data), updated_at FROM tls_cache WHERE key=$1`, key).Scan(&size, &modified)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, time.Time{}, fmt.Errorf("postgres: tls cache key %q not found: %w", key, db.ErrNotFound)
		}
		return 0, time.Time{}, fmt.Errorf("postgres: stat tls cache key %q: %w", key, err)
	}
	return size, modified, nil
}
