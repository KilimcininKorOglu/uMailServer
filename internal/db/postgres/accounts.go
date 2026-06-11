package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
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
			avatar, avatar_type, display_name, title, department, phone,
			timezone, locale, theme, onboarded, send_policy, receive_policy,
			quota_warn, quota_prohibit_send, quota_warn_sent)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,
			$36,$37,$38)`,
		account.Email, account.LocalPart, account.Domain, account.PasswordHash, account.APOPHash,
		account.TOTPSecret, account.TOTPEnabled, account.TOTPLastUsedStep, account.QuotaUsed, account.QuotaLimit,
		account.MaxMessageSize, account.ForwardTo, account.ForwardKeepCopy, account.SieveScript,
		account.VacationSettings, account.MustChangePassword, account.IsAdmin, account.IsTenantAdmin,
		account.IsActive, account.CompatibilityTier, account.CreatedAt, account.UpdatedAt, nullTime(account.LastLoginAt),
		nullBytes(account.Avatar), account.AvatarType, account.DisplayName, account.Title, account.Department, account.Phone,
		account.Timezone, account.Locale, account.Theme, account.Onboarded, account.SendPolicy, account.ReceivePolicy,
		account.QuotaWarn, account.QuotaProhibitSend, account.QuotaWarnSent,
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
			return nil, fmt.Errorf("postgres: account %s/%s not found: %w", domain, localPart, db.ErrNotFound)
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

// UpdateAccount re-stamps UpdatedAt and overwrites the account row identified by
// email, mirroring db.DB.UpdateAccount's Put.
func (d *DB) UpdateAccount(account *db.AccountData) error {
	ctx := context.Background()
	account.UpdatedAt = time.Now()
	ct, err := d.pool.Exec(ctx, `
		UPDATE accounts SET local_part=$2, domain=$3, password_hash=$4, apop_hash=$5,
			totp_secret=$6, totp_enabled=$7, totp_last_used_step=$8, quota_used=$9,
			quota_limit=$10, max_message_size=$11, forward_to=$12, forward_keep_copy=$13,
			sieve_script=$14, vacation_settings=$15, must_change_password=$16, is_admin=$17,
			is_tenant_admin=$18, is_active=$19, compatibility_tier=$20, updated_at=$21,
			last_login_at=$22, avatar=$23, avatar_type=$24, display_name=$25, title=$26,
			department=$27, phone=$28, timezone=$29, locale=$30, theme=$31, onboarded=$32,
			send_policy=$33, receive_policy=$34,
			quota_warn=$35, quota_prohibit_send=$36, quota_warn_sent=$37
		WHERE email=$1`,
		account.Email, account.LocalPart, account.Domain, account.PasswordHash, account.APOPHash,
		account.TOTPSecret, account.TOTPEnabled, account.TOTPLastUsedStep, account.QuotaUsed,
		account.QuotaLimit, account.MaxMessageSize, account.ForwardTo, account.ForwardKeepCopy,
		account.SieveScript, account.VacationSettings, account.MustChangePassword, account.IsAdmin,
		account.IsTenantAdmin, account.IsActive, account.CompatibilityTier, account.UpdatedAt,
		nullTime(account.LastLoginAt), nullBytes(account.Avatar), account.AvatarType, account.DisplayName,
		account.Title, account.Department, account.Phone,
		account.Timezone, account.Locale, account.Theme, account.Onboarded,
		account.SendPolicy, account.ReceivePolicy,
		account.QuotaWarn, account.QuotaProhibitSend, account.QuotaWarnSent,
	)
	if err != nil {
		return fmt.Errorf("postgres: update account %q: %w", account.Email, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("postgres: account %q not found: %w", account.Email, db.ErrNotFound)
	}
	return nil
}

// SetQuotaWarnSent flips an account's quota-warning latch via a targeted column
// update, so a concurrent IncrementQuota's quota_used is never clobbered.
func (d *DB) SetQuotaWarnSent(domain, localPart string, sent bool) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx,
		`UPDATE accounts SET quota_warn_sent=$3, updated_at=now() WHERE domain=$1 AND local_part=$2`,
		domain, localPart, sent); err != nil {
		return fmt.Errorf("postgres: set quota warn sent %s/%s: %w", domain, localPart, err)
	}
	return nil
}

// DeleteAccount removes the account at (domain, local_part).
func (d *DB) DeleteAccount(domain, localPart string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM accounts WHERE domain=$1 AND local_part=$2`, domain, localPart,
	); err != nil {
		return fmt.Errorf("postgres: delete account %s/%s: %w", domain, localPart, err)
	}
	return nil
}

// IncrementQuota atomically adds delta to an account's quota_used, enforcing the
// same effective ceiling as db.DB.IncrementQuota: the tighter of the account's
// own quota_limit and the domain's max_mailbox_size (0 = unlimited on either
// side), checked only on growth. The account row is locked FOR UPDATE so the
// read-modify-write is safe under concurrent writers across nodes — the reason
// this moves off the single-writer bbolt store.
func (d *DB) IncrementQuota(domain, localPart string, delta int64) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin increment quota: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	var used, limit int64
	err = tx.QueryRow(ctx,
		`SELECT quota_used, quota_limit FROM accounts
		 WHERE domain=$1 AND local_part=$2 FOR UPDATE`,
		domain, localPart,
	).Scan(&used, &limit)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("postgres: account %s/%s not found: %w", domain, localPart, db.ErrNotFound)
		}
		return fmt.Errorf("postgres: lock account %s/%s: %w", domain, localPart, err)
	}

	effectiveLimit := limit
	if delta > 0 {
		var domLimit int64
		derr := tx.QueryRow(ctx, `SELECT max_mailbox_size FROM domains WHERE name=$1`, domain).Scan(&domLimit)
		if derr != nil && !errors.Is(derr, pgx.ErrNoRows) {
			return fmt.Errorf("postgres: read domain ceiling %q: %w", domain, derr)
		}
		if domLimit > 0 && (effectiveLimit == 0 || domLimit < effectiveLimit) {
			effectiveLimit = domLimit
		}
	}
	if effectiveLimit > 0 && used+delta > effectiveLimit {
		return fmt.Errorf("quota exceeded for user: %s/%s", domain, localPart)
	}
	if delta > 0 && used > math.MaxInt64-delta {
		return fmt.Errorf("quota overflow for user: %s/%s", domain, localPart)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE accounts SET quota_used=$3, updated_at=now()
		 WHERE domain=$1 AND local_part=$2`,
		domain, localPart, used+delta,
	); err != nil {
		return fmt.Errorf("postgres: update quota %s/%s: %w", domain, localPart, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit increment quota: %w", err)
	}
	return nil
}

// SetQuotaUsed sets an account's quota_used to an absolute value. Unlike
// IncrementQuota it applies NO cap check — it reconciles the counter to the
// canonical mailbox size (which may legitimately already exceed the limit), so
// it must never reject.
func (d *DB) SetQuotaUsed(domain, localPart string, used int64) error {
	ctx := context.Background()
	ct, err := d.pool.Exec(ctx,
		`UPDATE accounts SET quota_used=$3, updated_at=now()
		 WHERE domain=$1 AND local_part=$2`,
		domain, localPart, used)
	if err != nil {
		return fmt.Errorf("postgres: set quota used %s/%s: %w", domain, localPart, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("postgres: account %s/%s not found: %w", domain, localPart, db.ErrNotFound)
	}
	return nil
}

const accountSelect = `
	SELECT email, local_part, domain, password_hash, apop_hash, totp_secret,
		totp_enabled, totp_last_used_step, quota_used, quota_limit,
		max_message_size, forward_to, forward_keep_copy, sieve_script,
		vacation_settings, must_change_password, is_admin, is_tenant_admin,
		is_active, compatibility_tier, created_at, updated_at, last_login_at,
		avatar, avatar_type, display_name, title, department, phone,
		timezone, locale, theme, onboarded, send_policy, receive_policy,
		quota_warn, quota_prohibit_send, quota_warn_sent
	FROM accounts`

func scanAccount(row rowScanner) (*db.AccountData, error) {
	var a db.AccountData
	var lastLogin *time.Time
	if err := row.Scan(&a.Email, &a.LocalPart, &a.Domain, &a.PasswordHash, &a.APOPHash, &a.TOTPSecret,
		&a.TOTPEnabled, &a.TOTPLastUsedStep, &a.QuotaUsed, &a.QuotaLimit,
		&a.MaxMessageSize, &a.ForwardTo, &a.ForwardKeepCopy, &a.SieveScript,
		&a.VacationSettings, &a.MustChangePassword, &a.IsAdmin, &a.IsTenantAdmin,
		&a.IsActive, &a.CompatibilityTier, &a.CreatedAt, &a.UpdatedAt, &lastLogin,
		&a.Avatar, &a.AvatarType, &a.DisplayName, &a.Title, &a.Department, &a.Phone,
		&a.Timezone, &a.Locale, &a.Theme, &a.Onboarded, &a.SendPolicy, &a.ReceivePolicy,
		&a.QuotaWarn, &a.QuotaProhibitSend, &a.QuotaWarnSent); err != nil {
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
