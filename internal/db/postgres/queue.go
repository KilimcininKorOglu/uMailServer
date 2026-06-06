package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// Enqueue inserts (or overwrites) a queue entry and its recipients, stamping
// CreatedAt when unset, mirroring db.DB.Enqueue.
func (d *DB) Enqueue(entry *db.QueueEntry) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin enqueue: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if err := writeQueueEntry(ctx, tx, entry); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit enqueue: %w", err)
	}
	return nil
}

// EnqueueWithLimit refuses to insert when the queue already holds maxSize
// entries, matching db.DB.EnqueueWithLimit. The count and insert run in one
// transaction so the limit holds under concurrent submitters.
func (d *DB) EnqueueWithLimit(entry *db.QueueEntry, maxSize int) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin enqueue-with-limit: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM mail_queue`).Scan(&count); err != nil {
		return fmt.Errorf("postgres: count queue: %w", err)
	}
	if count >= maxSize {
		return fmt.Errorf("queue is full (max %d entries)", maxSize)
	}
	if err := writeQueueEntry(ctx, tx, entry); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit enqueue-with-limit: %w", err)
	}
	return nil
}

// Dequeue removes a queue entry; its recipients cascade.
func (d *DB) Dequeue(id string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM mail_queue WHERE id=$1`, id); err != nil {
		return fmt.Errorf("postgres: dequeue %q: %w", id, err)
	}
	return nil
}

// GetQueueEntry returns the entry by id, including recipients. It errors when
// absent.
func (d *DB) GetQueueEntry(id string) (*db.QueueEntry, error) {
	ctx := context.Background()
	entry, err := scanQueueEntry(d.pool.QueryRow(ctx, queueSelect+` WHERE id=$1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: queue entry %q not found: %w", id, db.ErrNotFound)
		}
		return nil, fmt.Errorf("postgres: get queue entry %q: %w", id, err)
	}
	if err := d.loadRecipients(ctx, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// UpdateQueueEntry overwrites the entry and its recipients.
func (d *DB) UpdateQueueEntry(entry *db.QueueEntry) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin update queue entry: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if err := writeQueueEntry(ctx, tx, entry); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit update queue entry: %w", err)
	}
	return nil
}

// GetPendingQueue returns entries that are pending and due (next_retry < now),
// ordered for the retry worker. It does not claim them — matching the bbolt
// read; the multi-node, per-entry claim happens at delivery via ClaimQueueEntry.
func (d *DB) GetPendingQueue(now time.Time) ([]*db.QueueEntry, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		queueSelect+` WHERE status='pending' AND next_retry < $1 ORDER BY priority DESC, next_retry`, now)
	if err != nil {
		return nil, fmt.Errorf("postgres: pending queue: %w", err)
	}
	entries, err := collectQueueEntries(rows)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err := d.loadRecipients(ctx, e); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// queueClaimLease is how long a claimed ('sending') entry is reserved for the
// claiming node. next_retry is pushed this far into the future on claim; if the
// node delivers (success → 'delivered', failure → 'pending' + backoff) before it
// expires, the lease is moot. If the node dies mid-delivery, another node
// reclaims the entry once the lease elapses. It must comfortably exceed a single
// delivery attempt (SMTP connect + send across MX hosts).
const queueClaimLease = 10 * time.Minute

// ClaimQueueEntry atomically claims a single entry for delivery, flipping it to
// 'sending' and taking a lease (next_retry → now+lease) — but only if the entry
// is still due (next_retry <= now) and not already delivered. It returns true if
// THIS caller won the claim, false if another node already holds a fresh lease
// (or the row is gone/delivered). This is the cluster-wide guard that ensures an
// entry is delivered by at most one worker at a time, regardless of whether it
// reached delivery via the immediate enqueue path or a sweeper: both funnel
// through deliver(), which claims here before sending. A 'sending' row whose
// lease has expired (next_retry <= now) is reclaimable, so an entry orphaned by a
// crashed node is retried instead of stranded. This is the relational HA guard
// the single-writer bbolt store could not provide.
func (d *DB) ClaimQueueEntry(id string, now time.Time) (bool, error) {
	ctx := context.Background()
	lease := now.Add(queueClaimLease)
	tag, err := d.pool.Exec(ctx, `
		UPDATE mail_queue SET status='sending', next_retry=$3
		WHERE id=$1 AND status <> 'delivered' AND next_retry <= $2`, id, now, lease)
	if err != nil {
		return false, fmt.Errorf("postgres: claim queue entry: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ForEachQueueEntry invokes fn for every queue entry (recipients loaded).
func (d *DB) ForEachQueueEntry(fn func(*db.QueueEntry) error) error {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, queueSelect)
	if err != nil {
		return fmt.Errorf("postgres: iterate queue: %w", err)
	}
	entries, err := collectQueueEntries(rows)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := d.loadRecipients(ctx, e); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

const queueSelect = `
	SELECT id, sender, message_path, created_at, next_retry, retry_count,
		last_error, status, priority, notify, ret
	FROM mail_queue`

func scanQueueEntry(row rowScanner) (*db.QueueEntry, error) {
	var e db.QueueEntry
	var priority int16
	var notify, ret int32
	if err := row.Scan(&e.ID, &e.From, &e.MessagePath, &e.CreatedAt, &e.NextRetry,
		&e.RetryCount, &e.LastError, &e.Status, &priority, &notify, &ret); err != nil {
		return nil, err
	}
	e.Priority = db.QueuePriority(priority)
	e.Notify = db.DSNNotify(notify)
	e.Ret = db.DSNRet(ret)
	return &e, nil
}

// collectQueueEntries drains rows into entries. It closes rows.
func collectQueueEntries(rows pgx.Rows) ([]*db.QueueEntry, error) {
	defer rows.Close()
	var entries []*db.QueueEntry
	for rows.Next() {
		e, err := scanQueueEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan queue entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: read queue entries: %w", err)
	}
	return entries, nil
}

// writeQueueEntry upserts the row and replaces the ordered recipients.
func writeQueueEntry(ctx context.Context, tx pgx.Tx, e *db.QueueEntry) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO mail_queue (id, sender, message_path, created_at, next_retry,
			retry_count, last_error, status, priority, notify, ret)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO UPDATE SET sender=EXCLUDED.sender,
			message_path=EXCLUDED.message_path, created_at=EXCLUDED.created_at,
			next_retry=EXCLUDED.next_retry, retry_count=EXCLUDED.retry_count,
			last_error=EXCLUDED.last_error, status=EXCLUDED.status,
			priority=EXCLUDED.priority, notify=EXCLUDED.notify, ret=EXCLUDED.ret`,
		e.ID, e.From, e.MessagePath, e.CreatedAt, e.NextRetry, e.RetryCount,
		e.LastError, e.Status, int16(e.Priority), int32(e.Notify), int32(e.Ret),
	); err != nil {
		return fmt.Errorf("postgres: write queue entry %q: %w", e.ID, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mail_queue_recipients WHERE queue_id=$1`, e.ID); err != nil {
		return fmt.Errorf("postgres: clear recipients %q: %w", e.ID, err)
	}
	for i, rcpt := range e.To {
		if _, err := tx.Exec(ctx,
			`INSERT INTO mail_queue_recipients (queue_id, ord, recipient) VALUES ($1,$2,$3)`,
			e.ID, i, rcpt,
		); err != nil {
			return fmt.Errorf("postgres: insert recipient %q[%d]: %w", e.ID, i, err)
		}
	}
	return nil
}

func (d *DB) loadRecipients(ctx context.Context, e *db.QueueEntry) error {
	rows, err := d.pool.Query(ctx,
		`SELECT recipient FROM mail_queue_recipients WHERE queue_id=$1 ORDER BY ord`, e.ID)
	if err != nil {
		return fmt.Errorf("postgres: load recipients %q: %w", e.ID, err)
	}
	defer rows.Close()
	var to []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return fmt.Errorf("postgres: scan recipient %q: %w", e.ID, err)
		}
		to = append(to, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: load recipients %q: %w", e.ID, err)
	}
	e.To = to
	return nil
}
