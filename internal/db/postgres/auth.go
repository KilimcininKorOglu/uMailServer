package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// StoreRevokedToken records a token hash as revoked until expiry, mirroring
// db.DB.StoreRevokedToken (overwrite on repeat).
func (d *DB) StoreRevokedToken(tokenHash string, expiry time.Time) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO revoked_tokens (token_hash, expires_at) VALUES ($1,$2)
		ON CONFLICT (token_hash) DO UPDATE SET expires_at=EXCLUDED.expires_at,
			revoked_at=now()`,
		tokenHash, expiry,
	); err != nil {
		return fmt.Errorf("postgres: store revoked token: %w", err)
	}
	return nil
}

// IsTokenRevoked reports whether the token is currently revoked, lazily deleting
// an entry that has expired, matching db.DB.IsTokenRevoked.
func (d *DB) IsTokenRevoked(tokenHash string) (bool, error) {
	ctx := context.Background()
	var expiry time.Time
	err := d.pool.QueryRow(ctx,
		`SELECT expires_at FROM revoked_tokens WHERE token_hash=$1`, tokenHash,
	).Scan(&expiry)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("postgres: is token revoked: %w", err)
	}
	if time.Now().After(expiry) {
		if _, derr := d.pool.Exec(ctx, `DELETE FROM revoked_tokens WHERE token_hash=$1`, tokenHash); derr != nil {
			return false, fmt.Errorf("postgres: prune expired token: %w", derr)
		}
		return false, nil
	}
	return true, nil
}

// CleanupRevokedTokens deletes expired blacklist entries.
func (d *DB) CleanupRevokedTokens() error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM revoked_tokens WHERE expires_at < now()`); err != nil {
		return fmt.Errorf("postgres: cleanup revoked tokens: %w", err)
	}
	return nil
}

// CreateClientSession inserts a portal session, setting LastActive to CreatedAt
// like db.DB.CreateClientSession.
func (d *DB) CreateClientSession(session *db.ClientSession) error {
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now()
	}
	session.LastActive = session.CreatedAt
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO client_sessions (id, email, token_hash, device_type, client_ip,
			user_agent, created_at, last_active, revoked)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET email=EXCLUDED.email,
			token_hash=EXCLUDED.token_hash, device_type=EXCLUDED.device_type,
			client_ip=EXCLUDED.client_ip, user_agent=EXCLUDED.user_agent,
			created_at=EXCLUDED.created_at, last_active=EXCLUDED.last_active,
			revoked=EXCLUDED.revoked`,
		session.ID, session.Email, session.TokenHash, session.DeviceType, session.ClientIP,
		session.UserAgent, session.CreatedAt, session.LastActive, session.Revoked,
	); err != nil {
		return fmt.Errorf("postgres: create client session %q: %w", session.ID, err)
	}
	return nil
}

// GetClientSession returns the session by id; it errors when absent.
func (d *DB) GetClientSession(id string) (*db.ClientSession, error) {
	ctx := context.Background()
	s, err := scanSession(d.pool.QueryRow(ctx, sessionSelect+` WHERE id=$1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: client session %q not found: %w", id, db.ErrNotFound)
		}
		return nil, fmt.Errorf("postgres: get client session %q: %w", id, err)
	}
	return s, nil
}

// UpdateClientSession overwrites the session row.
func (d *DB) UpdateClientSession(session *db.ClientSession) error {
	ctx := context.Background()
	ct, err := d.pool.Exec(ctx, `
		UPDATE client_sessions SET email=$2, token_hash=$3, device_type=$4,
			client_ip=$5, user_agent=$6, created_at=$7, last_active=$8, revoked=$9
		WHERE id=$1`,
		session.ID, session.Email, session.TokenHash, session.DeviceType, session.ClientIP,
		session.UserAgent, session.CreatedAt, session.LastActive, session.Revoked,
	)
	if err != nil {
		return fmt.Errorf("postgres: update client session %q: %w", session.ID, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("postgres: client session %q not found: %w", session.ID, db.ErrNotFound)
	}
	return nil
}

// DeleteClientSession removes the session by id.
func (d *DB) DeleteClientSession(id string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM client_sessions WHERE id=$1`, id); err != nil {
		return fmt.Errorf("postgres: delete client session %q: %w", id, err)
	}
	return nil
}

// ListClientSessionsByEmail returns the non-revoked sessions for an email,
// matching db.DB.ListClientSessionsByEmail (revoked rows are filtered out).
func (d *DB) ListClientSessionsByEmail(email string) ([]*db.ClientSession, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, sessionSelect+` WHERE email=$1 AND revoked=FALSE ORDER BY created_at`, email)
	if err != nil {
		return nil, fmt.Errorf("postgres: list client sessions for %q: %w", email, err)
	}
	defer rows.Close()
	var sessions []*db.ClientSession
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan client session: %w", err)
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list client sessions for %q: %w", email, err)
	}
	return sessions, nil
}

// RevokeClientSession marks the session revoked.
func (d *DB) RevokeClientSession(id string) error {
	ctx := context.Background()
	ct, err := d.pool.Exec(ctx, `UPDATE client_sessions SET revoked=TRUE WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("postgres: revoke client session %q: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("postgres: client session %q not found: %w", id, db.ErrNotFound)
	}
	return nil
}

// CleanupExpiredSessions deletes revoked sessions and those inactive past
// maxAge, matching db.DB.CleanupExpiredSessions.
func (d *DB) CleanupExpiredSessions(maxAge time.Duration) error {
	ctx := context.Background()
	cutoff := time.Now().Add(-maxAge)
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM client_sessions WHERE revoked=TRUE OR last_active < $1`, cutoff,
	); err != nil {
		return fmt.Errorf("postgres: cleanup expired sessions: %w", err)
	}
	return nil
}

const sessionSelect = `
	SELECT id, email, token_hash, device_type, client_ip, user_agent,
		created_at, last_active, revoked
	FROM client_sessions`

func scanSession(row rowScanner) (*db.ClientSession, error) {
	var s db.ClientSession
	if err := row.Scan(&s.ID, &s.Email, &s.TokenHash, &s.DeviceType, &s.ClientIP,
		&s.UserAgent, &s.CreatedAt, &s.LastActive, &s.Revoked); err != nil {
		return nil, err
	}
	return &s, nil
}
