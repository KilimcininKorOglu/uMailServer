package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// CreateDomain inserts a domain and its settings. It mirrors the bbolt
// db.DB.CreateDomain semantics: timestamps are stamped on the passed struct,
// and a domain with no tenant gets its own single-domain tenant (id == name) so
// the every-domain-belongs-to-a-tenant invariant holds (the relational schema
// enforces it with a NOT-NULL-friendly RESTRICT foreign key).
func (d *DB) CreateDomain(domain *db.DomainData) error {
	ctx := context.Background()
	now := time.Now()
	if domain.CreatedAt.IsZero() {
		domain.CreatedAt = now
	}
	domain.UpdatedAt = now
	if domain.TenantID == "" {
		if err := d.ensureSelfTenant(ctx, domain.Name); err != nil {
			return err
		}
		domain.TenantID = domain.Name
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin create domain: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(ctx, `
		INSERT INTO domains (name, tenant_id, max_accounts, max_mailbox_size,
			dkim_selector, dkim_public_key, dkim_private_key, catch_all_target,
			company_name, from_template_internal, from_template_external,
			is_active, created_at, updated_at, quota_warn, quota_prohibit_send)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		domain.Name, domain.TenantID, domain.MaxAccounts, domain.MaxMailboxSize,
		domain.DKIMSelector, domain.DKIMPublicKey, domain.DKIMPrivateKey, domain.CatchAllTarget,
		domain.CompanyName, domain.FromTemplateInternal, domain.FromTemplateExternal,
		domain.IsActive, domain.CreatedAt, domain.UpdatedAt, domain.QuotaWarn, domain.QuotaProhibitSend,
	); err != nil {
		return fmt.Errorf("postgres: insert domain %q: %w", domain.Name, err)
	}
	if err := replaceDomainSettings(ctx, tx, domain.Name, domain.Settings); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit create domain: %w", err)
	}
	return nil
}

// GetDomain returns the domain by name, including its settings map. It returns
// an error when the domain does not exist, matching db.DB.GetDomain.
func (d *DB) GetDomain(name string) (*db.DomainData, error) {
	ctx := context.Background()
	domain, err := scanDomain(d.pool.QueryRow(ctx, domainSelect+` WHERE name=$1`, name))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: domain %q not found: %w", name, db.ErrNotFound)
		}
		return nil, fmt.Errorf("postgres: get domain %q: %w", name, err)
	}
	if err := loadDomainSettings(ctx, d.pool, domain); err != nil {
		return nil, err
	}
	return domain, nil
}

// UpdateDomain re-stamps UpdatedAt and writes the domain row and settings.
func (d *DB) UpdateDomain(domain *db.DomainData) error {
	ctx := context.Background()
	domain.UpdatedAt = time.Now()

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin update domain: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	ct, err := tx.Exec(ctx, `
		UPDATE domains SET tenant_id=$2, max_accounts=$3, max_mailbox_size=$4,
			dkim_selector=$5, dkim_public_key=$6, dkim_private_key=$7,
			catch_all_target=$8, company_name=$9, from_template_internal=$10,
			from_template_external=$11, is_active=$12, updated_at=$13,
			quota_warn=$14, quota_prohibit_send=$15
		WHERE name=$1`,
		domain.Name, domain.TenantID, domain.MaxAccounts, domain.MaxMailboxSize,
		domain.DKIMSelector, domain.DKIMPublicKey, domain.DKIMPrivateKey,
		domain.CatchAllTarget, domain.CompanyName, domain.FromTemplateInternal,
		domain.FromTemplateExternal, domain.IsActive, domain.UpdatedAt,
		domain.QuotaWarn, domain.QuotaProhibitSend,
	)
	if err != nil {
		return fmt.Errorf("postgres: update domain %q: %w", domain.Name, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("postgres: domain %q not found: %w", domain.Name, db.ErrNotFound)
	}
	if err := replaceDomainSettings(ctx, tx, domain.Name, domain.Settings); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit update domain: %w", err)
	}
	return nil
}

// DeleteDomain removes the domain. The settings child rows cascade.
func (d *DB) DeleteDomain(name string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM domains WHERE name=$1`, name); err != nil {
		return fmt.Errorf("postgres: delete domain %q: %w", name, err)
	}
	return nil
}

// ListDomains returns every domain with its settings, ordered by name.
func (d *DB) ListDomains() ([]*db.DomainData, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, domainSelect+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list domains: %w", err)
	}
	defer rows.Close()

	var domains []*db.DomainData
	for rows.Next() {
		domain, err := scanDomain(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan domain: %w", err)
		}
		domains = append(domains, domain)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list domains: %w", err)
	}
	for _, domain := range domains {
		if err := loadDomainSettings(ctx, d.pool, domain); err != nil {
			return nil, err
		}
	}
	return domains, nil
}

const domainSelect = `
	SELECT name, tenant_id, max_accounts, max_mailbox_size, dkim_selector,
		dkim_public_key, dkim_private_key, catch_all_target, company_name,
		from_template_internal, from_template_external, is_active,
		created_at, updated_at, quota_warn, quota_prohibit_send
	FROM domains`

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows, so scanDomain
// serves single-row and list reads.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDomain(row rowScanner) (*db.DomainData, error) {
	var d db.DomainData
	var tenantID *string
	if err := row.Scan(&d.Name, &tenantID, &d.MaxAccounts, &d.MaxMailboxSize,
		&d.DKIMSelector, &d.DKIMPublicKey, &d.DKIMPrivateKey, &d.CatchAllTarget,
		&d.CompanyName, &d.FromTemplateInternal, &d.FromTemplateExternal,
		&d.IsActive, &d.CreatedAt, &d.UpdatedAt, &d.QuotaWarn, &d.QuotaProhibitSend); err != nil {
		return nil, err
	}
	if tenantID != nil {
		d.TenantID = *tenantID
	}
	return &d, nil
}

// replaceDomainSettings rewrites a domain's settings child rows to match m.
func replaceDomainSettings(ctx context.Context, tx pgx.Tx, name string, m map[string]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM domain_settings WHERE domain=$1`, name); err != nil {
		return fmt.Errorf("postgres: clear domain settings %q: %w", name, err)
	}
	for k, v := range m {
		if _, err := tx.Exec(ctx,
			`INSERT INTO domain_settings (domain, key, value) VALUES ($1,$2,$3)`,
			name, k, v,
		); err != nil {
			return fmt.Errorf("postgres: insert domain setting %q/%q: %w", name, k, err)
		}
	}
	return nil
}

// loadDomainSettings fills d.Settings from the child table. The map stays nil
// when there are no settings, matching the bbolt round-trip (omitempty).
func loadDomainSettings(ctx context.Context, q querier, d *db.DomainData) error {
	rows, err := q.Query(ctx, `SELECT key, value FROM domain_settings WHERE domain=$1`, d.Name)
	if err != nil {
		return fmt.Errorf("postgres: load domain settings %q: %w", d.Name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return fmt.Errorf("postgres: scan domain setting %q: %w", d.Name, err)
		}
		if d.Settings == nil {
			d.Settings = make(map[string]string)
		}
		d.Settings[k] = v
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: load domain settings %q: %w", d.Name, err)
	}
	return nil
}

// ensureSelfTenant creates a single-domain tenant (id == name) when it is
// absent, mirroring db.DB.ensureSelfTenant so a runtime-created domain always
// satisfies the tenant foreign key.
func (d *DB) ensureSelfTenant(ctx context.Context, name string) error {
	now := time.Now()
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO tenants (id, name, is_active, created_at, updated_at)
		VALUES ($1,$1,TRUE,$2,$2)
		ON CONFLICT (id) DO NOTHING`,
		name, now,
	); err != nil {
		return fmt.Errorf("postgres: ensure self tenant %q: %w", name, err)
	}
	return nil
}

// querier abstracts *pgxpool.Pool and pgx.Tx for read helpers usable inside or
// outside a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
