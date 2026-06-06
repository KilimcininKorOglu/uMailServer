package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core subscriptions. Mirrors *semcore.BoltSubscriptionStore: a
// push/pull notification subscription with a random "sub-..." id, a default
// 30-minute expiry, and a drained flag that permanently invalidates it.

const subscriptionTTL = 30 * time.Minute

// foldersToStrings / stringsToFolders convert the FolderIDs slice to/from the
// stored TEXT[] form.
func foldersToStrings(ids []semcore.FolderId) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

func stringsToFolders(ss []string) []semcore.FolderId {
	if len(ss) == 0 {
		return nil
	}
	out := make([]semcore.FolderId, 0, len(ss))
	for _, s := range ss {
		out = append(out, parseFolderID(s))
	}
	return out
}

// CreateSubscription stores a new subscription with a fresh id, defaulting the
// expiry to 30 minutes out when unset (bbolt parity).
func (d *DB) CreateSubscription(sub semcore.Subscription) (semcore.SubscriptionId, error) {
	if sub.MailboxID.IsZero() {
		return semcore.SubscriptionId{}, fmt.Errorf("CreateSubscription: MailboxID is required")
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return semcore.SubscriptionId{}, fmt.Errorf("generate subscription id: %w", err)
	}
	sid := fmt.Sprintf("sub-%x", idBytes)
	if sub.ExpiresAt.IsZero() {
		sub.ExpiresAt = time.Now().Add(subscriptionTTL)
	}
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_subscription
			(id, mailbox_id, kind, folder_ids, last_seq, push_url, expires_at, drained_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())`,
		sid, sub.MailboxID.String(), int16(sub.Kind), foldersToStrings(sub.FolderIDs),
		int64(sub.LastSeq), sub.PushURL, nullTime(sub.ExpiresAt), nullTime(sub.DrainedAt),
	); err != nil {
		return semcore.SubscriptionId{}, fmt.Errorf("postgres: create subscription: %w", err)
	}
	return semcore.SubscriptionId{ID: sid}, nil
}

// GetSubscription returns a subscription by id. A drained subscription is
// returned together with semcore.ErrSubscriptionDrained (bbolt parity).
func (d *DB) GetSubscription(id semcore.SubscriptionId) (*semcore.Subscription, error) {
	sub, err := d.scanSubscription(context.Background(),
		`SELECT id, mailbox_id, kind, folder_ids, last_seq, push_url, expires_at, drained_at, created_at
		 FROM semcore_subscription WHERE id=$1`, id.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("subscription %q not found", id.ID)
		}
		return nil, err
	}
	if sub.IsDrained() {
		return sub, semcore.ErrSubscriptionDrained
	}
	return sub, nil
}

// RenewSubscription extends a subscription's expiry by the default TTL.
func (d *DB) RenewSubscription(id semcore.SubscriptionId) error {
	tag, err := d.pool.Exec(context.Background(),
		`UPDATE semcore_subscription SET expires_at=$2 WHERE id=$1`,
		id.ID, time.Now().Add(subscriptionTTL))
	if err != nil {
		return fmt.Errorf("postgres: renew subscription %q: %w", id.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("subscription %q not found", id.ID)
	}
	return nil
}

// ListSubscriptionsByMailbox returns all subscriptions owned by a mailbox.
func (d *DB) ListSubscriptionsByMailbox(mboxID semcore.MailboxId) ([]semcore.Subscription, error) {
	rows, err := d.pool.Query(context.Background(),
		`SELECT id, mailbox_id, kind, folder_ids, last_seq, push_url, expires_at, drained_at, created_at
		 FROM semcore_subscription WHERE mailbox_id=$1`, mboxID.String())
	if err != nil {
		return nil, fmt.Errorf("postgres: list subscriptions %s: %w", mboxID, err)
	}
	defer rows.Close()
	var result []semcore.Subscription
	for rows.Next() {
		sub, err := subscriptionFromRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list subscriptions %s: %w", mboxID, err)
	}
	return result, nil
}

// RemoveSubscription deletes a subscription (no error when absent, like bbolt).
func (d *DB) RemoveSubscription(id semcore.SubscriptionId) error {
	if _, err := d.pool.Exec(context.Background(),
		`DELETE FROM semcore_subscription WHERE id=$1`, id.ID); err != nil {
		return fmt.Errorf("postgres: remove subscription %q: %w", id.ID, err)
	}
	return nil
}

func (d *DB) scanSubscription(ctx context.Context, sql string, args ...any) (*semcore.Subscription, error) {
	return subscriptionFromRow(d.pool.QueryRow(ctx, sql, args...))
}

func subscriptionFromRow(row rowScanner) (*semcore.Subscription, error) {
	var id, mailboxID, pushURL string
	var kind int16
	var folderIDs []string
	var lastSeq int64
	var expiresAt, drainedAt *time.Time
	var createdAt time.Time
	if err := row.Scan(&id, &mailboxID, &kind, &folderIDs, &lastSeq, &pushURL, &expiresAt, &drainedAt, &createdAt); err != nil {
		return nil, err
	}
	sub := &semcore.Subscription{
		ID:        semcore.SubscriptionId{ID: id},
		MailboxID: parseMailboxID(mailboxID),
		Kind:      semcore.SubscriptionKind(uint8(kind)),
		FolderIDs: stringsToFolders(folderIDs),
		LastSeq:   uint64(lastSeq),
		PushURL:   pushURL,
		CreatedAt: createdAt,
	}
	if expiresAt != nil {
		sub.ExpiresAt = *expiresAt
	}
	if drainedAt != nil {
		sub.DrainedAt = *drainedAt
	}
	return sub, nil
}
