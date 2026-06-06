package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// CreateTenant inserts a tenant and its settings, stamping timestamps like
// db.DB.CreateTenant. An empty id is rejected.
func (d *DB) CreateTenant(t *db.TenantData) error {
	if t.ID == "" {
		return fmt.Errorf("tenant id is required")
	}
	now := time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	return d.upsertTenant(t)
}

// UpdateTenant re-stamps UpdatedAt and writes the tenant row and settings.
func (d *DB) UpdateTenant(t *db.TenantData) error {
	t.UpdatedAt = time.Now()
	return d.upsertTenant(t)
}

func (d *DB) upsertTenant(t *db.TenantData) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin upsert tenant: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(ctx, `
		INSERT INTO tenants (id, name, is_active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name,
			is_active=EXCLUDED.is_active, updated_at=EXCLUDED.updated_at`,
		t.ID, t.Name, t.IsActive, t.CreatedAt, t.UpdatedAt,
	); err != nil {
		return fmt.Errorf("postgres: upsert tenant %q: %w", t.ID, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tenant_settings WHERE tenant_id=$1`, t.ID); err != nil {
		return fmt.Errorf("postgres: clear tenant settings %q: %w", t.ID, err)
	}
	for k, v := range t.Settings {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tenant_settings (tenant_id, key, value) VALUES ($1,$2,$3)`,
			t.ID, k, v,
		); err != nil {
			return fmt.Errorf("postgres: insert tenant setting %q/%q: %w", t.ID, k, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit upsert tenant: %w", err)
	}
	return nil
}

// GetTenant returns the tenant by id, including its settings. It returns an
// error when absent.
func (d *DB) GetTenant(id string) (*db.TenantData, error) {
	ctx := context.Background()
	var t db.TenantData
	err := d.pool.QueryRow(ctx,
		`SELECT id, name, is_active, created_at, updated_at FROM tenants WHERE id=$1`, id,
	).Scan(&t.ID, &t.Name, &t.IsActive, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: tenant %q not found", id)
		}
		return nil, fmt.Errorf("postgres: get tenant %q: %w", id, err)
	}
	if err := d.loadTenantSettings(ctx, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// DeleteTenant removes a tenant. Settings cascade; domains are the caller's
// concern, matching db.DB.DeleteTenant.
func (d *DB) DeleteTenant(id string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, id); err != nil {
		return fmt.Errorf("postgres: delete tenant %q: %w", id, err)
	}
	return nil
}

// ListTenants returns every tenant with its settings, ordered by id.
func (d *DB) ListTenants() ([]*db.TenantData, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT id, name, is_active, created_at, updated_at FROM tenants ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list tenants: %w", err)
	}
	defer rows.Close()

	var tenants []*db.TenantData
	for rows.Next() {
		var t db.TenantData
		if err := rows.Scan(&t.ID, &t.Name, &t.IsActive, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan tenant: %w", err)
		}
		tenants = append(tenants, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list tenants: %w", err)
	}
	for _, t := range tenants {
		if err := d.loadTenantSettings(ctx, t); err != nil {
			return nil, err
		}
	}
	return tenants, nil
}

// ListDomainsByTenant returns the domains owned by the tenant.
func (d *DB) ListDomainsByTenant(tenantID string) ([]*db.DomainData, error) {
	all, err := d.ListDomains()
	if err != nil {
		return nil, err
	}
	var out []*db.DomainData
	for _, dom := range all {
		if dom.TenantID == tenantID {
			out = append(out, dom)
		}
	}
	return out, nil
}

// EnsureTenantsForDomains backfills self-tenants for domains missing a tenant
// (or referencing a vanished one). Idempotent; returns the count backfilled.
func (d *DB) EnsureTenantsForDomains() (int, error) {
	domains, err := d.ListDomains()
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	backfilled := 0
	for _, dom := range domains {
		if dom.TenantID != "" {
			if _, gerr := d.GetTenant(dom.TenantID); gerr == nil {
				continue
			}
		}
		if err := d.ensureSelfTenant(ctx, dom.Name); err != nil {
			return backfilled, err
		}
		dom.TenantID = dom.Name
		if err := d.UpdateDomain(dom); err != nil {
			return backfilled, err
		}
		backfilled++
	}
	return backfilled, nil
}

func (d *DB) loadTenantSettings(ctx context.Context, t *db.TenantData) error {
	rows, err := d.pool.Query(ctx, `SELECT key, value FROM tenant_settings WHERE tenant_id=$1`, t.ID)
	if err != nil {
		return fmt.Errorf("postgres: load tenant settings %q: %w", t.ID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return fmt.Errorf("postgres: scan tenant setting %q: %w", t.ID, err)
		}
		if t.Settings == nil {
			t.Settings = make(map[string]string)
		}
		t.Settings[k] = v
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: load tenant settings %q: %w", t.ID, err)
	}
	return nil
}
