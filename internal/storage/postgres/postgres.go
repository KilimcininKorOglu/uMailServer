// Package postgres holds the relational (PostgreSQL) backend for the message
// metadata + search index — the Faz 4 storage-layer counterpart to
// internal/db/postgres. Message bodies stay as Maildir files; only the
// per-mailbox state, per-message metadata, threads, and search index are
// relational.
//
// This first slice provides the connection pool and idempotent schema apply.
// The typed methods that satisfy the imap.MetadataStore / search.MetadataStore /
// jmap.MailStore consumer interfaces are layered on top in subsequent slices,
// starting with mailbox state and the atomic UID/mod-seq counters.
package postgres

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// DB is the PostgreSQL-backed message-metadata store handle.
type DB struct {
	pool *pgxpool.Pool
}

// Open creates a connection pool for dsn and verifies connectivity.
func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("storage/postgres: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("storage/postgres: ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Migrate applies the embedded schema idempotently (CREATE ... IF NOT EXISTS),
// safe to call on every start.
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("storage/postgres: apply schema: %w", err)
	}
	return nil
}

// Pool exposes the connection pool for the typed methods built on this package.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Close releases the connection pool.
func (d *DB) Close() error {
	if d.pool != nil {
		d.pool.Close()
	}
	return nil
}
