package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core lifecycle journal. Mirrors *semcore.BoltLifecycleStore: a
// strictly increasing per-mailbox seq (allocated atomically via the
// semcore_lifecycle_seq counter, the same INSERT ... ON CONFLICT ... RETURNING
// trick as GetNextUID) plus an append-only event log queried by seq.
//
// The id fields (FolderId/ItemId/ChangeKey/DelegateId) are opaque string
// wrappers; zero values round-trip as the empty string (their String() returns
// "" and we skip reconstruction for empty columns).

// lcID builds the storable string for an opaque id whose String() method yields
// "" when zero.
func parseFolderID(s string) semcore.FolderId {
	if s == "" {
		return semcore.FolderId{}
	}
	id, _ := semcore.NewFolderId(s) //nolint:errcheck // non-empty by guard
	return id
}

func parseItemID(s string) semcore.ItemId {
	if s == "" {
		return semcore.ItemId{}
	}
	id, _ := semcore.NewItemId(s) //nolint:errcheck // non-empty by guard
	return id
}

func parseChangeKey(s string) semcore.ChangeKey {
	if s == "" {
		return semcore.ChangeKey{}
	}
	ck, _ := semcore.NewChangeKey(s) //nolint:errcheck // non-empty by guard
	return ck
}

func parseDelegateID(s string) semcore.DelegateId {
	if s == "" {
		return semcore.DelegateId{}
	}
	id, _ := semcore.NewDelegateId(s) //nolint:errcheck // non-empty by guard
	return id
}

// AppendLifecycle allocates the next per-mailbox seq and stores the event in one
// atomic statement. Satisfies semcore.PipelineLifecycleStore and the lifecycle
// surface the EWS server holds.
func (d *DB) AppendLifecycle(event semcore.Lifecycle) error {
	if event.MailboxID.IsZero() {
		return fmt.Errorf("AppendLifecycle: MailboxID is required")
	}
	// A zero At falls back to the column default now() via COALESCE (the bbolt
	// store stored At verbatim, but a zero TIMESTAMPTZ is meaningless).
	_, err := d.pool.Exec(context.Background(), `
		WITH s AS (
			INSERT INTO semcore_lifecycle_seq (mailbox_id, seq_next) VALUES ($1, 2)
			ON CONFLICT (mailbox_id) DO UPDATE SET seq_next = semcore_lifecycle_seq.seq_next + 1
			RETURNING seq_next - 1 AS seq
		)
		INSERT INTO semcore_lifecycle
			(mailbox_id, seq, folder_id, item_id, kind, at, actor, change_key, delegate_email, delegate_id)
		SELECT $1, s.seq, $2, $3, $4, COALESCE($5, now()), $6, $7, $8, $9 FROM s`,
		event.MailboxID.String(), event.FolderID.String(), event.ItemID.String(),
		int16(event.Kind), nullTime(event.At), event.Actor, event.ChangeKey.String(),
		event.DelegateEmail, event.DelegateID.String(),
	)
	if err != nil {
		return fmt.Errorf("postgres: append lifecycle %s: %w", event.MailboxID, err)
	}
	return nil
}

// PollEvents returns events for the mailbox with seq greater than sinceSeq, in
// seq order, up to limit, and the highest seq returned.
func (d *DB) PollEvents(mboxID semcore.MailboxId, sinceSeq uint64, limit int) ([]semcore.Lifecycle, uint64, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.pool.Query(context.Background(), `
		SELECT seq, folder_id, item_id, kind, at, actor, change_key, delegate_email, delegate_id
		FROM semcore_lifecycle
		WHERE mailbox_id=$1 AND seq>$2
		ORDER BY seq LIMIT $3`,
		mboxID.String(), int64(sinceSeq), limit,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: poll lifecycle %s: %w", mboxID, err)
	}
	defer rows.Close()
	var result []semcore.Lifecycle
	var highest uint64
	for rows.Next() {
		var seq int64
		var folderID, itemID, actor, changeKey, delEmail, delID string
		var kind int16
		var at time.Time
		if err := rows.Scan(&seq, &folderID, &itemID, &kind, &at, &actor, &changeKey, &delEmail, &delID); err != nil {
			return nil, 0, fmt.Errorf("postgres: scan lifecycle: %w", err)
		}
		result = append(result, semcore.Lifecycle{
			MailboxID:     mboxID,
			FolderID:      parseFolderID(folderID),
			ItemID:        parseItemID(itemID),
			Kind:          semcore.LifecycleKind(uint8(kind)),
			At:            at,
			Actor:         actor,
			ChangeKey:     parseChangeKey(changeKey),
			DelegateEmail: delEmail,
			DelegateID:    parseDelegateID(delID),
		})
		if uint64(seq) > highest {
			highest = uint64(seq)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres: poll lifecycle %s: %w", mboxID, err)
	}
	return result, highest, nil
}

// FoldersSince mirrors the bbolt store, which aliases PollEvents.
func (d *DB) FoldersSince(mboxID semcore.MailboxId, sinceSeq uint64, limit int) ([]semcore.Lifecycle, uint64, error) {
	return d.PollEvents(mboxID, sinceSeq, limit)
}

// HighestSequence returns the last allocated seq for the mailbox (0 when none).
func (d *DB) HighestSequence(mboxID semcore.MailboxId) (uint64, error) {
	var seqNext int64
	err := d.pool.QueryRow(context.Background(),
		`SELECT seq_next FROM semcore_lifecycle_seq WHERE mailbox_id=$1`, mboxID.String(),
	).Scan(&seqNext)
	if err != nil {
		// No counter row yet → no events appended.
		return 0, nil //nolint:nilerr // absent counter means zero, matching bbolt
	}
	if seqNext <= 1 {
		return 0, nil
	}
	return uint64(seqNext - 1), nil
}

// PruneEvents deletes events older than maxAge and returns the count removed.
func (d *DB) PruneEvents(maxAge time.Duration) (int, error) {
	tag, err := d.pool.Exec(context.Background(),
		`DELETE FROM semcore_lifecycle WHERE at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int64(maxAge.Seconds())),
	)
	if err != nil {
		return 0, fmt.Errorf("postgres: prune lifecycle: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
