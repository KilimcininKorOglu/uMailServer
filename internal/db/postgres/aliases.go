package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// CreateAlias inserts (or, matching the bbolt store's overwrite-on-Put, updates)
// an alias keyed case-insensitively by (domain, local part). CreatedAt is
// stamped when unset, exactly like db.DB.CreateAlias.
func (d *DB) CreateAlias(alias *db.AliasData) error {
	if alias.CreatedAt.IsZero() {
		alias.CreatedAt = time.Now()
	}
	return d.upsertAlias(alias)
}

// UpdateAlias writes the alias, overwriting any existing row, mirroring the
// bbolt Put semantics db.DB.UpdateAlias relies on.
func (d *DB) UpdateAlias(alias *db.AliasData) error {
	return d.upsertAlias(alias)
}

func (d *DB) upsertAlias(alias *db.AliasData) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO aliases (domain, alias, target, is_active, created_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (domain, lower(alias))
		DO UPDATE SET alias=EXCLUDED.alias, target=EXCLUDED.target,
			is_active=EXCLUDED.is_active, created_at=EXCLUDED.created_at`,
		alias.Domain, alias.Alias, alias.Target, alias.IsActive, alias.CreatedAt,
	); err != nil {
		return fmt.Errorf("postgres: upsert alias %s/%s: %w", alias.Domain, alias.Alias, err)
	}
	return nil
}

// GetAlias returns the alias at (domain, local part). The lookup is
// case-insensitive on the local part, matching the bbolt key, and returns an
// error when absent.
func (d *DB) GetAlias(domain, localPart string) (*db.AliasData, error) {
	ctx := context.Background()
	var a db.AliasData
	err := d.pool.QueryRow(ctx,
		`SELECT domain, alias, target, is_active, created_at
		 FROM aliases WHERE domain=$1 AND lower(alias)=lower($2)`,
		domain, localPart,
	).Scan(&a.Domain, &a.Alias, &a.Target, &a.IsActive, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: alias %s/%s not found", domain, localPart)
		}
		return nil, fmt.Errorf("postgres: get alias %s/%s: %w", domain, localPart, err)
	}
	return &a, nil
}

// DeleteAlias removes the alias at (domain, local part), case-insensitively.
func (d *DB) DeleteAlias(domain, localPart string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM aliases WHERE domain=$1 AND lower(alias)=lower($2)`,
		domain, localPart,
	); err != nil {
		return fmt.Errorf("postgres: delete alias %s/%s: %w", domain, localPart, err)
	}
	return nil
}

// ListAliases returns every alias, ordered by domain then local part.
func (d *DB) ListAliases() ([]*db.AliasData, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT domain, alias, target, is_active, created_at
		 FROM aliases ORDER BY domain, lower(alias)`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list aliases: %w", err)
	}
	defer rows.Close()

	var aliases []*db.AliasData
	for rows.Next() {
		var a db.AliasData
		if err := rows.Scan(&a.Domain, &a.Alias, &a.Target, &a.IsActive, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan alias: %w", err)
		}
		aliases = append(aliases, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list aliases: %w", err)
	}
	return aliases, nil
}
