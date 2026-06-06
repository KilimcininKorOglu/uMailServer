// Package postgres holds the relational (PostgreSQL) backend for the canonical
// account/domain/alias/group/queue store — the Faz 4 replacement for the
// bbolt-backed internal/db.DB. It exists so a multi-node uMailServer deployment
// can share one source of truth across nodes instead of a per-node bbolt file.
//
// This first slice provides the connection pool and idempotent schema apply.
// The typed read/write methods that satisfy the internal/db consumer interfaces
// (queue.Store, mapi.Store, mcp.Store, and the wider account/domain surface)
// are layered on top in subsequent slices, table by table, starting with the
// unambiguous "net" surfaces.
package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// initAdvisoryLockKey serializes schema migration + bootstrap across nodes that
// boot against the same fresh database. Any fixed bigint shared by all nodes
// works; this is an arbitrary constant ("uMail init").
const initAdvisoryLockKey int64 = 0x756D61696C696E74

// schemaSQL is the relational schema applied by Migrate. It is embedded so the
// binary carries its own schema and no external migration tool is required to
// stand up a fresh database.
//
//go:embed schema.sql
var schemaSQL string

// DB is the PostgreSQL-backed store handle. It owns a connection pool shared by
// every protocol surface on this node.
type DB struct {
	pool *pgxpool.Pool

	// initMu/initConn hold the session advisory lock taken by Migrate so schema
	// apply + bootstrap is serialized across nodes booting on a fresh database.
	// The lock is released by ReleaseInitLock (server, after bootstrap) or by
	// Close (CLI/shutdown), whichever comes first.
	initMu   sync.Mutex
	initConn *pgxpool.Conn
}

// Open creates a connection pool for dsn and verifies connectivity. The dsn is a
// standard libpq/pgx connection string or URL (e.g.
// "postgres://user:pass@host:5432/umail?sslmode=disable"). The caller owns the
// returned DB and must Close it.
func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Migrate applies the embedded schema under a session advisory lock so that two
// nodes booting against the same fresh database cannot run concurrent DDL
// (which Postgres can fail with duplicate-object/deadlock errors) and cannot
// race the bootstrap inserts that follow. Every statement is idempotent
// (CREATE ... IF NOT EXISTS), so Migrate is safe to call on every start; the
// whole file is one Exec over the simple query protocol (multiple statements,
// no bind params). The lock is HELD on a dedicated connection after Migrate
// returns, spanning the caller's bootstrap, until ReleaseInitLock or Close.
func (d *DB) Migrate(ctx context.Context) error {
	// Re-entrant: if this handle already holds the init lock (Migrate called
	// again before release — e.g. an idempotency re-apply), reuse the locked
	// connection instead of acquiring a second one, which would deadlock against
	// our own session lock.
	d.initMu.Lock()
	held := d.initConn
	d.initMu.Unlock()
	if held != nil {
		if _, err := held.Exec(ctx, schemaSQL); err != nil {
			return fmt.Errorf("postgres: apply schema: %w", err)
		}
		return nil
	}

	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres: acquire init conn: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", initAdvisoryLockKey); err != nil {
		conn.Release()
		return fmt.Errorf("postgres: acquire init lock: %w", err)
	}
	if _, err := conn.Exec(ctx, schemaSQL); err != nil {
		// Release the lock + conn before surfacing the failure.
		//nolint:errcheck // best-effort unlock on the error path; conn release frees it anyway
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", initAdvisoryLockKey)
		conn.Release()
		return fmt.Errorf("postgres: apply schema: %w", err)
	}
	d.initMu.Lock()
	d.initConn = conn
	d.initMu.Unlock()
	return nil
}

// ReleaseInitLock releases the boot-time advisory lock taken by Migrate, letting
// the next node proceed with its own schema apply + bootstrap. The server calls
// it once bootstrap finishes. Idempotent and safe if Migrate was never called.
func (d *DB) ReleaseInitLock(ctx context.Context) {
	d.initMu.Lock()
	conn := d.initConn
	d.initConn = nil
	d.initMu.Unlock()
	if conn == nil {
		return
	}
	//nolint:errcheck // best-effort unlock; the conn release (and session end) frees it anyway
	_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", initAdvisoryLockKey)
	conn.Release()
}

// Pool exposes the underlying connection pool for the typed store methods built
// on top of this package.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Close releases the connection pool. It returns an error to match the
// db.Store contract (the bbolt store's Close can fail); the pgx pool close
// itself does not report one.
func (d *DB) Close() error {
	// Release the init advisory lock's connection first, else pool.Close() blocks
	// forever waiting for that acquired conn to return (the CLI never calls
	// ReleaseInitLock).
	d.ReleaseInitLock(context.Background())
	if d.pool != nil {
		d.pool.Close()
	}
	return nil
}
