package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core tombstones. Mirrors *semcore.BoltTombstoneStore: a deletion
// record keyed by (mailbox, folder, item, kind); a newer DeletedAt supersedes an
// older record for the same key (idempotent newest-wins upsert). Satisfies
// semcore.TombstoneWriter and the EWS tombstone surface.

// PutTombstone records a deletion, keeping the newest DeletedAt for the key.
func (d *DB) PutTombstone(t semcore.Tombstone) error {
	if t.MailboxID.IsZero() {
		return fmt.Errorf("PutTombstone: MailboxID is required")
	}
	_, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_tombstone (mailbox_id, folder_id, item_id, kind, deleted_at, actor)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (mailbox_id, folder_id, item_id, kind)
		DO UPDATE SET deleted_at=EXCLUDED.deleted_at, actor=EXCLUDED.actor
		WHERE EXCLUDED.deleted_at > semcore_tombstone.deleted_at`,
		t.MailboxID.String(), t.FolderID.String(), t.ItemID.String(),
		int16(t.Kind), t.DeletedAt, t.Actor,
	)
	if err != nil {
		return fmt.Errorf("postgres: put tombstone %s: %w", t.MailboxID, err)
	}
	return nil
}

// ListTombstonesSince returns tombstones for the mailbox (and folder, when
// folderID is non-zero) deleted at or after since.
func (d *DB) ListTombstonesSince(mboxID semcore.MailboxId, folderID semcore.FolderId, since time.Time) ([]semcore.Tombstone, error) {
	rows, err := d.pool.Query(context.Background(), `
		SELECT folder_id, item_id, kind, deleted_at, actor
		FROM semcore_tombstone
		WHERE mailbox_id=$1 AND ($2='' OR folder_id=$2) AND deleted_at >= $3`,
		mboxID.String(), folderID.String(), since,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list tombstones since %s: %w", mboxID, err)
	}
	defer rows.Close()
	var result []semcore.Tombstone
	for rows.Next() {
		var fID, iID, actor string
		var kind int16
		var deletedAt time.Time
		if err := rows.Scan(&fID, &iID, &kind, &deletedAt, &actor); err != nil {
			return nil, fmt.Errorf("postgres: scan tombstone: %w", err)
		}
		result = append(result, semcore.Tombstone{
			MailboxID: mboxID,
			FolderID:  parseFolderID(fID),
			ItemID:    parseItemID(iID),
			Kind:      semcore.LifecycleKind(uint8(kind)),
			DeletedAt: deletedAt,
			Actor:     actor,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list tombstones since %s: %w", mboxID, err)
	}
	return result, nil
}
