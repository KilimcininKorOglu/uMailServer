package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// CreateScheduledMessage inserts (or overwrites) a scheduled message and its
// recipients, stamping CreatedAt when unset. Mirrors db.DB.CreateScheduledMessage.
func (d *DB) CreateScheduledMessage(m *db.ScheduledMessage) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin create scheduled: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if err := writeScheduledMessage(ctx, tx, m); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit create scheduled: %w", err)
	}
	return nil
}

// CreateScheduledMessageWithLimit refuses to insert when the owner already holds
// maxPerOwner pending scheduled messages. The count and insert run in one
// transaction so the limit holds under concurrent submitters.
func (d *DB) CreateScheduledMessageWithLimit(m *db.ScheduledMessage, maxPerOwner int) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin create-scheduled-with-limit: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM scheduled_messages WHERE owner=$1 AND status='pending'`, m.Owner,
	).Scan(&count); err != nil {
		return fmt.Errorf("postgres: count scheduled: %w", err)
	}
	if count >= maxPerOwner {
		return fmt.Errorf("too many scheduled messages (max %d per user)", maxPerOwner)
	}
	if err := writeScheduledMessage(ctx, tx, m); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit create-scheduled-with-limit: %w", err)
	}
	return nil
}

// GetScheduledMessage returns the scheduled message by id, including recipients.
func (d *DB) GetScheduledMessage(id string) (*db.ScheduledMessage, error) {
	ctx := context.Background()
	m, err := scanScheduledMessage(d.pool.QueryRow(ctx, scheduledSelect+` WHERE id=$1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: scheduled message %q not found: %w", id, db.ErrNotFound)
		}
		return nil, fmt.Errorf("postgres: get scheduled message %q: %w", id, err)
	}
	if err := d.loadScheduledRecipients(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// UpdateScheduledMessage overwrites the message and its recipients.
func (d *DB) UpdateScheduledMessage(m *db.ScheduledMessage) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin update scheduled: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if err := writeScheduledMessage(ctx, tx, m); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit update scheduled: %w", err)
	}
	return nil
}

// DeleteScheduledMessage removes a scheduled message; its recipients cascade.
func (d *DB) DeleteScheduledMessage(id string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM scheduled_messages WHERE id=$1`, id); err != nil {
		return fmt.Errorf("postgres: delete scheduled %q: %w", id, err)
	}
	return nil
}

// ListScheduledByOwner returns all scheduled messages owned by the given mailbox.
func (d *DB) ListScheduledByOwner(owner string) ([]*db.ScheduledMessage, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, scheduledSelect+` WHERE owner=$1 ORDER BY send_at`, owner)
	if err != nil {
		return nil, fmt.Errorf("postgres: list scheduled by owner: %w", err)
	}
	return d.collectAndLoad(ctx, rows)
}

// ListDueScheduledMessages returns pending messages whose send time has arrived.
func (d *DB) ListDueScheduledMessages(now time.Time) ([]*db.ScheduledMessage, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		scheduledSelect+` WHERE status='pending' AND send_at <= $1 ORDER BY send_at`, now)
	if err != nil {
		return nil, fmt.Errorf("postgres: due scheduled: %w", err)
	}
	return d.collectAndLoad(ctx, rows)
}

// CancelScheduledByFolderRef deletes the scheduled message whose Scheduled-folder
// projection (owner + folder uid) was expunged, so canceling from any surface's
// folder view cancels the send. Returns true if a matching record was removed.
func (d *DB) CancelScheduledByFolderRef(owner string, uid uint32) (bool, error) {
	ctx := context.Background()
	tag, err := d.pool.Exec(ctx,
		`DELETE FROM scheduled_messages WHERE owner=$1 AND folder_uid=$2`, owner, int64(uid))
	if err != nil {
		return false, fmt.Errorf("postgres: cancel scheduled by folder ref: %w", err)
	}
	return tag.RowsAffected() >= 1, nil
}

// ClaimScheduledMessage atomically claims a due pending message for release,
// flipping it to 'sending' and stamping claimed_at, only if still pending and
// due. Returns true if THIS caller won the claim. This is the cluster-wide guard
// against a brief two-leader window during failover (the single-writer bbolt
// store relies on the leader gate instead); it is exposed via the optional
// scheduledClaimer interface the release loop type-asserts. Rows orphaned in
// 'sending' by a crashed node are recovered by ResetStaleScheduledMessages.
func (d *DB) ClaimScheduledMessage(id string, now time.Time) (bool, error) {
	ctx := context.Background()
	tag, err := d.pool.Exec(ctx, `
		UPDATE scheduled_messages SET status='sending', claimed_at=$2
		WHERE id=$1 AND status='pending' AND send_at <= $2`, id, now)
	if err != nil {
		return false, fmt.Errorf("postgres: claim scheduled message: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ResetStaleScheduledMessages flips messages stuck in 'sending' (claimed before
// the cutoff, e.g. by a node that crashed mid-release) back to 'pending'. Returns
// how many were reset.
func (d *DB) ResetStaleScheduledMessages(before time.Time) (int, error) {
	ctx := context.Background()
	tag, err := d.pool.Exec(ctx,
		`UPDATE scheduled_messages SET status='pending' WHERE status='sending' AND claimed_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("postgres: reset stale scheduled: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

const scheduledSelect = `
	SELECT id, owner, sender, message_path, send_at, created_at, claimed_at, status,
		source, file_sent, folder_uid, blob_key, retry_count, last_error
	FROM scheduled_messages`

func scanScheduledMessage(row rowScanner) (*db.ScheduledMessage, error) {
	var m db.ScheduledMessage
	var folderUID int64
	if err := row.Scan(&m.ID, &m.Owner, &m.From, &m.MessagePath, &m.SendAt, &m.CreatedAt,
		&m.ClaimedAt, &m.Status, &m.Source, &m.FileSent, &folderUID, &m.BlobKey,
		&m.RetryCount, &m.LastError); err != nil {
		return nil, err
	}
	m.FolderUID = uint32(folderUID)
	return &m, nil
}

// collectScheduled drains rows into messages. It closes rows.
func collectScheduled(rows pgx.Rows) ([]*db.ScheduledMessage, error) {
	defer rows.Close()
	var out []*db.ScheduledMessage
	for rows.Next() {
		m, err := scanScheduledMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan scheduled message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: read scheduled messages: %w", err)
	}
	return out, nil
}

// collectAndLoad drains rows and loads each message's recipients.
func (d *DB) collectAndLoad(ctx context.Context, rows pgx.Rows) ([]*db.ScheduledMessage, error) {
	msgs, err := collectScheduled(rows)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if err := d.loadScheduledRecipients(ctx, m); err != nil {
			return nil, err
		}
	}
	return msgs, nil
}

// writeScheduledMessage upserts the row and replaces the ordered recipients.
func writeScheduledMessage(ctx context.Context, tx pgx.Tx, m *db.ScheduledMessage) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO scheduled_messages (id, owner, sender, message_path, send_at,
			created_at, claimed_at, status, source, file_sent, folder_uid, blob_key, retry_count, last_error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (id) DO UPDATE SET owner=EXCLUDED.owner, sender=EXCLUDED.sender,
			message_path=EXCLUDED.message_path, send_at=EXCLUDED.send_at,
			created_at=EXCLUDED.created_at, claimed_at=EXCLUDED.claimed_at, status=EXCLUDED.status,
			source=EXCLUDED.source, file_sent=EXCLUDED.file_sent, folder_uid=EXCLUDED.folder_uid,
			blob_key=EXCLUDED.blob_key, retry_count=EXCLUDED.retry_count, last_error=EXCLUDED.last_error`,
		m.ID, m.Owner, m.From, m.MessagePath, m.SendAt, m.CreatedAt, m.ClaimedAt, m.Status,
		m.Source, m.FileSent, int64(m.FolderUID), m.BlobKey, m.RetryCount, m.LastError,
	); err != nil {
		return fmt.Errorf("postgres: write scheduled message %q: %w", m.ID, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM scheduled_message_recipients WHERE scheduled_id=$1`, m.ID); err != nil {
		return fmt.Errorf("postgres: clear scheduled recipients %q: %w", m.ID, err)
	}
	for i, rcpt := range m.To {
		if _, err := tx.Exec(ctx,
			`INSERT INTO scheduled_message_recipients (scheduled_id, ord, recipient) VALUES ($1,$2,$3)`,
			m.ID, i, rcpt,
		); err != nil {
			return fmt.Errorf("postgres: insert scheduled recipient %q[%d]: %w", m.ID, i, err)
		}
	}
	return nil
}

func (d *DB) loadScheduledRecipients(ctx context.Context, m *db.ScheduledMessage) error {
	rows, err := d.pool.Query(ctx,
		`SELECT recipient FROM scheduled_message_recipients WHERE scheduled_id=$1 ORDER BY ord`, m.ID)
	if err != nil {
		return fmt.Errorf("postgres: load scheduled recipients %q: %w", m.ID, err)
	}
	defer rows.Close()
	var to []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return fmt.Errorf("postgres: scan scheduled recipient %q: %w", m.ID, err)
		}
		to = append(to, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: load scheduled recipients %q: %w", m.ID, err)
	}
	m.To = to
	return nil
}
