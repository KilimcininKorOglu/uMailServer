package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/db"
)

// CreateRecoverableItem inserts (or overwrites) a recoverable item, stamping
// DeletedAt when unset. Mirrors db.DB.CreateRecoverableItem.
func (d *DB) CreateRecoverableItem(m *db.RecoverableItem) error {
	if m.DeletedAt.IsZero() {
		m.DeletedAt = time.Now().UTC()
	}
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO recoverable_items (id, owner, original_folder, blob_key, folder_uid, deleted_at, size, subject)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET owner=EXCLUDED.owner, original_folder=EXCLUDED.original_folder,
			blob_key=EXCLUDED.blob_key, folder_uid=EXCLUDED.folder_uid, deleted_at=EXCLUDED.deleted_at,
			size=EXCLUDED.size, subject=EXCLUDED.subject`,
		m.ID, m.Owner, m.OriginalFolder, m.BlobKey, int64(m.FolderUID), m.DeletedAt, m.Size, m.Subject,
	); err != nil {
		return fmt.Errorf("postgres: create recoverable item %q: %w", m.ID, err)
	}
	return nil
}

// GetRecoverableItem returns the recoverable item by id.
func (d *DB) GetRecoverableItem(id string) (*db.RecoverableItem, error) {
	ctx := context.Background()
	m, err := scanRecoverableItem(d.pool.QueryRow(ctx, recoverableSelect+` WHERE id=$1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres: recoverable item %q not found: %w", id, db.ErrNotFound)
		}
		return nil, fmt.Errorf("postgres: get recoverable item %q: %w", id, err)
	}
	return m, nil
}

// DeleteRecoverableItem removes a recoverable item record.
func (d *DB) DeleteRecoverableItem(id string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `DELETE FROM recoverable_items WHERE id=$1`, id); err != nil {
		return fmt.Errorf("postgres: delete recoverable item %q: %w", id, err)
	}
	return nil
}

// ListRecoverableByOwner returns all recoverable items owned by the given mailbox.
func (d *DB) ListRecoverableByOwner(owner string) ([]*db.RecoverableItem, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, recoverableSelect+` WHERE owner=$1 ORDER BY deleted_at`, owner)
	if err != nil {
		return nil, fmt.Errorf("postgres: list recoverable by owner: %w", err)
	}
	return collectRecoverable(rows)
}

// ListExpiredRecoverableItems returns items deleted at or before the cutoff, so
// the retention cleaner can purge them.
func (d *DB) ListExpiredRecoverableItems(cutoff time.Time) ([]*db.RecoverableItem, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, recoverableSelect+` WHERE deleted_at <= $1 ORDER BY deleted_at`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("postgres: expired recoverable: %w", err)
	}
	return collectRecoverable(rows)
}

// FindRecoverableByFolderRef returns the recoverable item whose Recoverable-Items
// projection (owner + folder uid) matches, or nil when none does.
func (d *DB) FindRecoverableByFolderRef(owner string, uid uint32) (*db.RecoverableItem, error) {
	ctx := context.Background()
	m, err := scanRecoverableItem(d.pool.QueryRow(ctx,
		recoverableSelect+` WHERE owner=$1 AND folder_uid=$2`, owner, int64(uid)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres: find recoverable by folder ref: %w", err)
	}
	return m, nil
}

const recoverableSelect = `
	SELECT id, owner, original_folder, blob_key, folder_uid, deleted_at, size, subject
	FROM recoverable_items`

func scanRecoverableItem(row rowScanner) (*db.RecoverableItem, error) {
	var m db.RecoverableItem
	var folderUID int64
	if err := row.Scan(&m.ID, &m.Owner, &m.OriginalFolder, &m.BlobKey, &folderUID,
		&m.DeletedAt, &m.Size, &m.Subject); err != nil {
		return nil, err
	}
	m.FolderUID = uint32(folderUID)
	return &m, nil
}

// collectRecoverable drains rows into items. It closes rows.
func collectRecoverable(rows pgx.Rows) ([]*db.RecoverableItem, error) {
	defer rows.Close()
	var out []*db.RecoverableItem
	for rows.Next() {
		m, err := scanRecoverableItem(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan recoverable item: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: read recoverable items: %w", err)
	}
	return out, nil
}
