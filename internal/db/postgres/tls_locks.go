package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file implements the db.Store TLS coordination-lock methods on the
// PostgreSQL backend — the multi-node counterpart of the in-process bbolt lease
// in internal/db/db.go. A lock is a row in tls_locks holding an owner and a
// TTL'd expiry; a node acquires by inserting a fresh row or stealing an expired
// one, so a crashed node cannot wedge certificate management for the cluster.

// LockTLSCache attempts a single, non-blocking acquisition of a named
// distributed lock held for ttl by owner. The upsert succeeds (and returns the
// row) only when there is no existing row, the existing lease is past its
// expiry (steal-on-stale), or the existing owner is the caller (re-acquire). A
// live lease owned by another node yields no row, which we report as
// acquired=false rather than an error.
func (d *DB) LockTLSCache(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	expiry := time.Now().Add(ttl)
	var got string
	err := d.pool.QueryRow(ctx, `
		INSERT INTO tls_locks (name, owner, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE
			SET owner = EXCLUDED.owner, expires_at = EXCLUDED.expires_at
			WHERE tls_locks.expires_at < now() OR tls_locks.owner = EXCLUDED.owner
		RETURNING name`,
		name, owner, expiry,
	).Scan(&got)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // live lease held by another owner; no row updated
		}
		return false, fmt.Errorf("postgres: lock tls cache %q: %w", name, err)
	}
	return true, nil
}

// UnlockTLSCache releases name when held by owner; a lock held by another owner
// (or nobody) is left untouched and is not an error.
func (d *DB) UnlockTLSCache(ctx context.Context, name, owner string) error {
	if _, err := d.pool.Exec(ctx, `DELETE FROM tls_locks WHERE name=$1 AND owner=$2`, name, owner); err != nil {
		return fmt.Errorf("postgres: unlock tls cache %q: %w", name, err)
	}
	return nil
}
