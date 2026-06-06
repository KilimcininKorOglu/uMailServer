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

	"github.com/jackc/pgx/v5/pgxpool"
)

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

// Migrate applies the embedded schema. Every statement is idempotent
// (CREATE ... IF NOT EXISTS), so Migrate is safe to call on every start. The
// whole file is sent in one Exec, which pgx runs over the simple query protocol
// (no bind parameters), allowing the multiple statements in schema.sql.
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("postgres: apply schema: %w", err)
	}
	return nil
}

// Pool exposes the underlying connection pool for the typed store methods built
// on top of this package.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Close releases the connection pool. It returns an error to match the
// db.Store contract (the bbolt store's Close can fail); the pgx pool close
// itself does not report one.
func (d *DB) Close() error {
	if d.pool != nil {
		d.pool.Close()
	}
	return nil
}
