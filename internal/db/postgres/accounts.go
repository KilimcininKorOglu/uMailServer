package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// CreateAccount inserts an account, stamping CreatedAt/UpdatedAt on the passed
// struct exactly like db.DB.CreateAccount. The relational primary key is the
// email; the (domain, local_part) lookup used by GetAccount is served by the
// domain index plus the stored local_part.
func (d *DB) CreateAccount(account *db.AccountData) error {
	ctx := context.Background()
	now := time.Now()
	if account.CreatedAt.IsZero() {
		account.CreatedAt = now
	}
	account.UpdatedAt = now

	if _, err := d.pool.Exec(ctx, `
		INSERT INTO accounts (email, local_part, domain, password_hash, apop_hash,
			totp_secret, totp_enabled, totp_last_used_step, quota_used, quota_limit,
			max_message_size, forward_to, forward_keep_copy, sieve_script,
			vacation_settings, must_change_password, is_admin, is_tenant_admin,
			is_active, compatibility_tier, created_at, updated_at, last_login_at,
			avatar, avatar_type, display_name, title, department, phone)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)`,
		account.Email, account.LocalPart, account.Domain, account.PasswordHash, account.APOPHash,
		account.TOTPSecret, account.TOTPEnabled, account.TOTPLastUsedStep, account.QuotaUsed, account.QuotaLimit,
		account.MaxMessageSize, account.ForwardTo, account.ForwardKeepCopy, account.SieveScript,
		account.VacationSettings, account.MustChangePassword, account.IsAdmin, account.IsTenantAdmin,
		account.IsActive, account.CompatibilityTier, account.CreatedAt, account.UpdatedAt, nullTime(account.LastLoginAt),
		nullBytes(account.Avatar), account.AvatarType, account.DisplayName, account.Title, account.Department, account.Phone,
	); err != nil {
		return fmt.Errorf("postgres: insert account %q: %w", account.Email, err)
	}
	return nil
}

// GetAccount returns the account at (domain, local_part). It returns an error
// when absent, matching db.DB.GetAccount.
func (d *DB) GetAccount(domain, localPart string) (*db.AccountData, error) {
	ctx := context.Background()
	account, err := scanAccount(d.pool.QueryRow(ctx, accountSelect+` WHERE domain=$1 AND local_part=$2`, domain, localPart))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: account %s/%s not found", domain, localPart)
		}
		return nil, fmt.Errorf("postgres: get account %s/%s: %w", domain, localPart, err)
	}
	return account, nil
}

// ListAccountsByDomain returns every account in the domain, ordered by
// local_part for a stable result.
func (d *DB) ListAccountsByDomain(domain string) ([]*db.AccountData, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, accountSelect+` WHERE domain=$1 ORDER BY local_part`, domain)
	if err != nil {
		return nil, fmt.Errorf("postgres: list accounts in %q: %w", domain, err)
	}
	defer rows.Close()

	var accounts []*db.AccountData
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan account: %w", err)
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list accounts in %q: %w", domain, err)
	}
	return accounts, nil
}

const accountSelect = `
	SELECT email, local_part, domain, password_hash, apop_hash, totp_secret,
		totp_enabled, totp_last_used_step, quota_used, quota_limit,
		max_message_size, forward_to, forward_keep_copy, sieve_script,
		vacation_settings, must_change_password, is_admin, is_tenant_admin,
		is_active, compatibility_tier, created_at, updated_at, last_login_at,
		avatar, avatar_type, display_name, title, department, phone
	FROM accounts`

func scanAccount(row rowScanner) (*db.AccountData, error) {
	var a db.AccountData
	var lastLogin *time.Time
	if err := row.Scan(&a.Email, &a.LocalPart, &a.Domain, &a.PasswordHash, &a.APOPHash, &a.TOTPSecret,
		&a.TOTPEnabled, &a.TOTPLastUsedStep, &a.QuotaUsed, &a.QuotaLimit,
		&a.MaxMessageSize, &a.ForwardTo, &a.ForwardKeepCopy, &a.SieveScript,
		&a.VacationSettings, &a.MustChangePassword, &a.IsAdmin, &a.IsTenantAdmin,
		&a.IsActive, &a.CompatibilityTier, &a.CreatedAt, &a.UpdatedAt, &lastLogin,
		&a.Avatar, &a.AvatarType, &a.DisplayName, &a.Title, &a.Department, &a.Phone); err != nil {
		return nil, err
	}
	if lastLogin != nil {
		a.LastLoginAt = *lastLogin
	}
	return &a, nil
}

// nullTime maps a zero time to a SQL NULL so an unset LastLoginAt round-trips as
// a zero value rather than the epoch.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// nullBytes maps an empty slice to a SQL NULL so an unset avatar round-trips as
// a nil slice.
func nullBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}
