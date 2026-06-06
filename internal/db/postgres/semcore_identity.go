package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core identity stores (mailbox / folder / item identities). Mirrors
// *semcore.BoltIdentityStore. Canonical ids (MailboxId/FolderId/ItemId) are
// cryptographically random opaque strings — NOT monotonic counters — so there is
// no sequence-allocation race; the only concurrency concern is create-if-absent,
// handled by INSERT ... ON CONFLICT DO NOTHING plus a re-read on conflict.

// newSemcoreID mints a fresh random canonical id, identical in shape to the
// semcore package's internal generator (16 random bytes, hex-encoded).
func newSemcoreID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// --- mailbox identities ---

// EnsureMailboxId returns the MailboxId for an email, creating a stable random
// identity when none exists (idempotent under races). Mirrors the bbolt store.
func (d *DB) EnsureMailboxId(email string) (semcore.MailboxId, error) {
	ctx := context.Background()
	// Fast path: existing identity.
	if id, err := d.GetMailboxIDByEmail(email); err == nil {
		return id, nil
	}
	// Slow path: insert a fresh id, but keep the existing one on a concurrent race.
	var raw string
	err := d.pool.QueryRow(ctx, `
		INSERT INTO semcore_mailbox_identity (email, mailbox_id, uid_validity)
		VALUES ($1, $2, 1)
		ON CONFLICT (email) DO UPDATE SET email = semcore_mailbox_identity.email
		RETURNING mailbox_id`,
		email, newSemcoreID(),
	).Scan(&raw)
	if err != nil {
		return semcore.MailboxId{}, fmt.Errorf("postgres: ensure mailbox id %q: %w", email, err)
	}
	id, err := semcore.NewMailboxId(raw)
	if err != nil {
		return semcore.MailboxId{}, fmt.Errorf("postgres: ensure mailbox id %q: %w", email, err)
	}
	return id, nil
}

// GetMailboxIDByEmail returns the MailboxId for an email, or
// semcore.ErrMailboxNotFound when absent (bbolt parity).
func (d *DB) GetMailboxIDByEmail(email string) (semcore.MailboxId, error) {
	var raw string
	err := d.pool.QueryRow(context.Background(),
		`SELECT mailbox_id FROM semcore_mailbox_identity WHERE email=$1`, email,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return semcore.MailboxId{}, semcore.ErrMailboxNotFound
		}
		return semcore.MailboxId{}, fmt.Errorf("postgres: get mailbox id %q: %w", email, err)
	}
	return semcore.NewMailboxId(raw)
}

// MailboxEmailsByID returns a map of MailboxId string -> account email for every
// registered mailbox identity (used by admin surfaces that persist a MailboxId
// but display the email).
func (d *DB) MailboxEmailsByID() (map[string]string, error) {
	rows, err := d.pool.Query(context.Background(),
		`SELECT mailbox_id, email FROM semcore_mailbox_identity`)
	if err != nil {
		return nil, fmt.Errorf("postgres: mailbox emails by id: %w", err)
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, fmt.Errorf("postgres: scan mailbox identity: %w", err)
		}
		m[id] = email
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: mailbox emails by id: %w", err)
	}
	return m, nil
}
