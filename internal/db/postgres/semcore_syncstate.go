package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core per-client sync state. Mirrors *semcore.BoltSyncStateStore: a
// (mailbox, folder, client) tuple holds an opaque watermark, a monotonic version
// bumped on every write, and a folder_gone tombstone flag.

// PutSyncState upserts the watermark for a (mailbox, folder, client) tuple,
// bumping the version and clearing folder_gone, matching the bbolt store.
func (d *DB) PutSyncState(mboxID semcore.MailboxId, folderID semcore.FolderId, clientID, watermark string) error {
	_, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_sync_state (mailbox_id, folder_id, client_id, watermark, version, updated_at, folder_gone)
		VALUES ($1,$2,$3,$4,1,now(),false)
		ON CONFLICT (mailbox_id, folder_id, client_id) DO UPDATE SET
			watermark=EXCLUDED.watermark,
			version=semcore_sync_state.version + 1,
			updated_at=now(),
			folder_gone=false`,
		mboxID.String(), folderID.String(), clientID, watermark,
	)
	if err != nil {
		return fmt.Errorf("postgres: put sync state %s/%s/%s: %w", mboxID, folderID, clientID, err)
	}
	return nil
}

// GetSyncState returns the sync state for the tuple, or
// semcore.ErrSyncStateNotFound when absent.
func (d *DB) GetSyncState(mboxID semcore.MailboxId, folderID semcore.FolderId, clientID string) (*semcore.StoredSyncState, error) {
	var watermark string
	var version int64
	var updatedAt time.Time
	var folderGone bool
	err := d.pool.QueryRow(context.Background(), `
		SELECT watermark, version, updated_at, folder_gone
		FROM semcore_sync_state WHERE mailbox_id=$1 AND folder_id=$2 AND client_id=$3`,
		mboxID.String(), folderID.String(), clientID,
	).Scan(&watermark, &version, &updatedAt, &folderGone)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, semcore.ErrSyncStateNotFound
		}
		return nil, fmt.Errorf("postgres: get sync state %s/%s/%s: %w", mboxID, folderID, clientID, err)
	}
	return &semcore.StoredSyncState{
		MailboxID:  mboxID,
		FolderID:   folderID,
		ClientID:   clientID,
		Watermark:  watermark,
		Version:    uint64(version),
		UpdatedAt:  updatedAt,
		FolderGone: folderGone,
	}, nil
}

// MarkFolderGone flags every sync state for the folder as gone (no error when
// none exist, matching the bbolt store).
func (d *DB) MarkFolderGone(folderID semcore.FolderId) error {
	if _, err := d.pool.Exec(context.Background(),
		`UPDATE semcore_sync_state SET folder_gone=true WHERE folder_id=$1`, folderID.String()); err != nil {
		return fmt.Errorf("postgres: mark folder gone %s: %w", folderID, err)
	}
	return nil
}
