package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/vacation"
)

// GetUIPrefs returns the user's webmail toggle map (empty when none are set),
// the typed replacement for the map[string]bool blob the bbolt store kept under
// the bare user key in BucketPreferences.
func (d *DB) GetUIPrefs(user string) (map[string]bool, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT key, enabled FROM user_ui_prefs WHERE user_email=$1`, user)
	if err != nil {
		return nil, fmt.Errorf("postgres: get ui prefs %q: %w", user, err)
	}
	defer rows.Close()
	prefs := map[string]bool{}
	for rows.Next() {
		var k string
		var v bool
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("postgres: scan ui pref %q: %w", user, err)
		}
		prefs[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: get ui prefs %q: %w", user, err)
	}
	return prefs, nil
}

// PutUIPrefs replaces the user's toggle map.
func (d *DB) PutUIPrefs(user string, prefs map[string]bool) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin put ui prefs: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.Exec(ctx, `DELETE FROM user_ui_prefs WHERE user_email=$1`, user); err != nil {
		return fmt.Errorf("postgres: clear ui prefs %q: %w", user, err)
	}
	for k, v := range prefs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_ui_prefs (user_email, key, enabled) VALUES ($1,$2,$3)`, user, k, v,
		); err != nil {
			return fmt.Errorf("postgres: insert ui pref %q/%q: %w", user, k, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit put ui prefs: %w", err)
	}
	return nil
}

// GetSignature returns the user's outgoing-mail signature, or "" when unset.
func (d *DB) GetSignature(user string) (string, error) {
	ctx := context.Background()
	var sig string
	err := d.pool.QueryRow(ctx, `SELECT signature FROM user_signatures WHERE user_email=$1`, user).Scan(&sig)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("postgres: get signature %q: %w", user, err)
	}
	return sig, nil
}

// PutSignature upserts the user's signature.
func (d *DB) PutSignature(user, signature string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO user_signatures (user_email, signature) VALUES ($1,$2)
		ON CONFLICT (user_email) DO UPDATE SET signature=EXCLUDED.signature`,
		user, signature,
	); err != nil {
		return fmt.Errorf("postgres: put signature %q: %w", user, err)
	}
	return nil
}

// GetCategories returns the user's ordered webmail categories (nil when none).
func (d *DB) GetCategories(user string) ([]db.Category, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT name, color FROM user_categories WHERE user_email=$1 ORDER BY ord`, user)
	if err != nil {
		return nil, fmt.Errorf("postgres: get categories %q: %w", user, err)
	}
	defer rows.Close()
	var cats []db.Category
	for rows.Next() {
		var c db.Category
		if err := rows.Scan(&c.Name, &c.Color); err != nil {
			return nil, fmt.Errorf("postgres: scan category %q: %w", user, err)
		}
		cats = append(cats, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: get categories %q: %w", user, err)
	}
	return cats, nil
}

// PutCategories replaces the user's categories.
func (d *DB) PutCategories(user string, categories []db.Category) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin put categories: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.Exec(ctx, `DELETE FROM user_categories WHERE user_email=$1`, user); err != nil {
		return fmt.Errorf("postgres: clear categories %q: %w", user, err)
	}
	for i, c := range categories {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_categories (user_email, ord, name, color) VALUES ($1,$2,$3,$4)`,
			user, i, c.Name, c.Color,
		); err != nil {
			return fmt.Errorf("postgres: insert category %q[%d]: %w", user, i, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit put categories: %w", err)
	}
	return nil
}

// GetVacation returns the user's vacation config, erroring when none is stored
// (the caller falls back to its default), including the exclude list.
func (d *DB) GetVacation(user string) (*vacation.Config, error) {
	ctx := context.Background()
	var c vacation.Config
	var start, end *time.Time
	var interval int64
	err := d.pool.QueryRow(ctx, `
		SELECT enabled, start_date, end_date, subject, message, html_message,
			send_interval, ignore_lists, ignore_bulk
		FROM user_vacation WHERE user_email=$1`, user,
	).Scan(&c.Enabled, &start, &end, &c.Subject, &c.Message, &c.HTMLMessage,
		&interval, &c.IgnoreLists, &c.IgnoreBulk)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: vacation config for %q not found: %w", user, db.ErrNotFound)
		}
		return nil, fmt.Errorf("postgres: get vacation %q: %w", user, err)
	}
	c.SendInterval = time.Duration(interval)
	if start != nil {
		c.StartDate = *start
	}
	if end != nil {
		c.EndDate = *end
	}

	rows, err := d.pool.Query(ctx, `SELECT address FROM user_vacation_excludes WHERE user_email=$1 ORDER BY ord`, user)
	if err != nil {
		return nil, fmt.Errorf("postgres: get vacation excludes %q: %w", user, err)
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("postgres: scan vacation exclude %q: %w", user, err)
		}
		c.ExcludeAddresses = append(c.ExcludeAddresses, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: get vacation excludes %q: %w", user, err)
	}
	return &c, nil
}

// PutVacation upserts the user's vacation config and exclude list.
func (d *DB) PutVacation(user string, c *vacation.Config) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin put vacation: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_vacation (user_email, enabled, start_date, end_date, subject,
			message, html_message, send_interval, ignore_lists, ignore_bulk)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (user_email) DO UPDATE SET enabled=EXCLUDED.enabled,
			start_date=EXCLUDED.start_date, end_date=EXCLUDED.end_date,
			subject=EXCLUDED.subject, message=EXCLUDED.message,
			html_message=EXCLUDED.html_message, send_interval=EXCLUDED.send_interval,
			ignore_lists=EXCLUDED.ignore_lists, ignore_bulk=EXCLUDED.ignore_bulk`,
		user, c.Enabled, nullTime(c.StartDate), nullTime(c.EndDate), c.Subject,
		c.Message, c.HTMLMessage, int64(c.SendInterval), c.IgnoreLists, c.IgnoreBulk,
	); err != nil {
		return fmt.Errorf("postgres: upsert vacation %q: %w", user, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_vacation_excludes WHERE user_email=$1`, user); err != nil {
		return fmt.Errorf("postgres: clear vacation excludes %q: %w", user, err)
	}
	for i, addr := range c.ExcludeAddresses {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_vacation_excludes (user_email, ord, address) VALUES ($1,$2,$3)`, user, i, addr,
		); err != nil {
			return fmt.Errorf("postgres: insert vacation exclude %q[%d]: %w", user, i, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit put vacation: %w", err)
	}
	return nil
}

// DeleteVacation removes the user's vacation config; excludes cascade.
func (d *DB) DeleteVacation(user string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM user_vacation WHERE user_email=$1`, user); err != nil {
		return fmt.Errorf("postgres: delete vacation %q: %w", user, err)
	}
	return nil
}

// GetUserConfig returns the Outlook EWS UserConfiguration at (owner, name),
// erroring when absent.
func (d *DB) GetUserConfig(owner, name string) (*db.UserConfigBlob, error) {
	ctx := context.Background()
	var b db.UserConfigBlob
	err := d.pool.QueryRow(ctx,
		`SELECT dictionary, xml_data, binary_data FROM ews_user_config WHERE owner=$1 AND name=$2`,
		owner, name,
	).Scan(&b.Dictionary, &b.XMLData, &b.BinaryData)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: user config %s/%s not found: %w", owner, name, db.ErrNotFound)
		}
		return nil, fmt.Errorf("postgres: get user config %s/%s: %w", owner, name, err)
	}
	return &b, nil
}

// PutUserConfig upserts the EWS UserConfiguration at (owner, name).
func (d *DB) PutUserConfig(owner, name string, b *db.UserConfigBlob) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO ews_user_config (owner, name, dictionary, xml_data, binary_data)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (owner, name) DO UPDATE SET dictionary=EXCLUDED.dictionary,
			xml_data=EXCLUDED.xml_data, binary_data=EXCLUDED.binary_data`,
		owner, name, b.Dictionary, b.XMLData, b.BinaryData,
	); err != nil {
		return fmt.Errorf("postgres: put user config %s/%s: %w", owner, name, err)
	}
	return nil
}

// DeleteUserConfig removes the EWS UserConfiguration at (owner, name).
func (d *DB) DeleteUserConfig(owner, name string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM ews_user_config WHERE owner=$1 AND name=$2`, owner, name); err != nil {
		return fmt.Errorf("postgres: delete user config %s/%s: %w", owner, name, err)
	}
	return nil
}
