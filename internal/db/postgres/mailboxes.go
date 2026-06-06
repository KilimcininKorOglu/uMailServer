package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/storage"
)

// Mailbox-state methods of the relational storage layer. These are the
// self-contained subset (no change-journal or message dependency): per-mailbox
// counters and the subscription set. Mailbox lifecycle (Create/Delete/Rename,
// which record change-journal entries) and message-derived reads (counts,
// ReconcileUIDNext) land with the change journal and message CRUD.

// GetMailbox returns the mailbox state, or a default (uid_validity/uid_next = 1)
// when the mailbox row does not exist yet — matching the bbolt store.
func (d *DB) GetMailbox(user, mailbox string) (*storage.Mailbox, error) {
	ctx := context.Background()
	mb := storage.Mailbox{Name: mailbox, UIDValidity: 1, UIDNext: 1}
	var uidValidity, uidNext, modSeq int64
	err := d.pool.QueryRow(ctx,
		`SELECT uid_validity, uid_next, highest_modseq FROM mailboxes WHERE user_email=$1 AND name=$2`,
		user, mailbox,
	).Scan(&uidValidity, &uidNext, &modSeq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &mb, nil
		}
		return nil, fmt.Errorf("postgres: get mailbox %s/%s: %w", user, mailbox, err)
	}
	mb.UIDValidity = uint32(uidValidity)
	mb.UIDNext = uint32(uidNext)
	mb.HighestModSeq = uint64(modSeq)
	return &mb, nil
}

// ListMailboxes returns the user's mailbox names, defaulting to ["INBOX"] when
// none exist yet (matching the bbolt store).
func (d *DB) ListMailboxes(user string) ([]string, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT name FROM mailboxes WHERE user_email=$1 ORDER BY name`, user)
	if err != nil {
		return nil, fmt.Errorf("postgres: list mailboxes %q: %w", user, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("postgres: scan mailbox %q: %w", user, err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list mailboxes %q: %w", user, err)
	}
	if len(names) == 0 {
		names = []string{"INBOX"}
	}
	return names, nil
}

// GetNextUID atomically claims and returns the next UID for the mailbox,
// creating the mailbox row on first use. It is a single statement
// (INSERT ... ON CONFLICT ... RETURNING) so the read-modify-write is atomic
// across concurrent nodes — the relational answer to the bbolt single-writer
// UID-monotonicity assumption. The claimed UID equals the pre-increment
// uid_next.
func (d *DB) GetNextUID(user, mailbox string) (uint32, error) {
	ctx := context.Background()
	var claimed int64
	// On insert: uid_next starts at 1, we store 2 and claim 1.
	// On conflict: bump uid_next and claim the previous value.
	err := d.pool.QueryRow(ctx, `
		INSERT INTO mailboxes (user_email, name, uid_validity, uid_next)
		VALUES ($1, $2, $3, 2)
		ON CONFLICT (user_email, name)
		DO UPDATE SET uid_next = mailboxes.uid_next + 1
		RETURNING uid_next - 1`,
		user, mailbox, time.Now().Unix(),
	).Scan(&claimed)
	if err != nil {
		return 0, fmt.Errorf("postgres: next uid %s/%s: %w", user, mailbox, err)
	}
	return uint32(claimed), nil
}

// GetHighestModSeq returns the mailbox's highest RFC 7162 mod-sequence, or 0
// when the mailbox does not exist.
func (d *DB) GetHighestModSeq(user, mailbox string) (uint64, error) {
	ctx := context.Background()
	var modSeq int64
	err := d.pool.QueryRow(ctx,
		`SELECT highest_modseq FROM mailboxes WHERE user_email=$1 AND name=$2`, user, mailbox,
	).Scan(&modSeq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("postgres: highest modseq %s/%s: %w", user, mailbox, err)
	}
	return uint64(modSeq), nil
}

// GetSubscribed reports whether the user is subscribed to the mailbox name
// (independent of whether the mailbox exists).
func (d *DB) GetSubscribed(user, mailbox string) (bool, error) {
	ctx := context.Background()
	var exists bool
	err := d.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM mailbox_subscriptions WHERE user_email=$1 AND mailbox=$2)`,
		user, mailbox,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("postgres: get subscribed %s/%s: %w", user, mailbox, err)
	}
	return exists, nil
}

// SetSubscribed adds or removes a subscription for the mailbox name.
func (d *DB) SetSubscribed(user, mailbox string, subscribed bool) error {
	ctx := context.Background()
	if subscribed {
		if _, err := d.pool.Exec(ctx,
			`INSERT INTO mailbox_subscriptions (user_email, mailbox) VALUES ($1,$2)
			 ON CONFLICT (user_email, mailbox) DO NOTHING`, user, mailbox,
		); err != nil {
			return fmt.Errorf("postgres: subscribe %s/%s: %w", user, mailbox, err)
		}
		return nil
	}
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM mailbox_subscriptions WHERE user_email=$1 AND mailbox=$2`, user, mailbox,
	); err != nil {
		return fmt.Errorf("postgres: unsubscribe %s/%s: %w", user, mailbox, err)
	}
	return nil
}

// ListSubscribed returns the user's subscribed mailbox names.
func (d *DB) ListSubscribed(user string) ([]string, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT mailbox FROM mailbox_subscriptions WHERE user_email=$1 ORDER BY mailbox`, user)
	if err != nil {
		return nil, fmt.Errorf("postgres: list subscribed %q: %w", user, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("postgres: scan subscribed %q: %w", user, err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list subscribed %q: %w", user, err)
	}
	return names, nil
}
