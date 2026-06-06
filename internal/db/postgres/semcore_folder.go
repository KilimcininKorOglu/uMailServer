package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core folder identities. Mirrors the folder surface of
// *semcore.BoltIdentityStore. Folders are keyed by (mbox_key, folder_name); the
// folder's MailboxID is the mailbox key itself (an email), matching the bbolt
// store. The mbox_key argument callers pass is the account email.

// scanFolder builds a StoredFolderIdentity from a row's columns.
func scanFolder(mboxKey, folderID, parentID, role string, sortOrder int, highestModSeq int64, subscribed bool) (semcore.StoredFolderIdentity, error) {
	fid, err := semcore.NewFolderId(folderID)
	if err != nil {
		return semcore.StoredFolderIdentity{}, fmt.Errorf("invalid folder id %q: %w", folderID, err)
	}
	mid, err := semcore.NewMailboxId(mboxKey)
	if err != nil {
		return semcore.StoredFolderIdentity{}, fmt.Errorf("invalid mailbox key %q: %w", mboxKey, err)
	}
	return semcore.StoredFolderIdentity{
		FolderID:      fid,
		MailboxID:     mid,
		ParentID:      parseFolderID(parentID),
		Role:          role,
		SortOrder:     sortOrder,
		HighestModSeq: uint64(highestModSeq),
		IsSubscribed:  subscribed,
	}, nil
}

// EnsureFolderId returns the FolderId for a mailbox+folder, creating a stable
// random identity when none exists. When role is set and a folder already holds
// that role, the existing folder's id is returned (bbolt parity).
func (d *DB) EnsureFolderId(mboxKey, folderName, role string) (semcore.FolderId, error) {
	if id, err := d.GetFolderID(mboxKey, folderName); err == nil {
		return id, nil
	}
	if role != "" {
		if existing, err := d.getFolderByRole(mboxKey, role); err == nil {
			return existing.FolderID, nil
		}
	}
	var raw string
	err := d.pool.QueryRow(context.Background(), `
		INSERT INTO semcore_folder_identity (mbox_key, folder_name, folder_id, role, is_subscribed)
		VALUES ($1, $2, $3, $4, true)
		ON CONFLICT (mbox_key, folder_name) DO UPDATE SET mbox_key = semcore_folder_identity.mbox_key
		RETURNING folder_id`,
		mboxKey, folderName, newSemcoreID(), role,
	).Scan(&raw)
	if err != nil {
		return semcore.FolderId{}, fmt.Errorf("postgres: ensure folder id %s/%s: %w", mboxKey, folderName, err)
	}
	return semcore.NewFolderId(raw)
}

// RestoreFolderIdentity inserts a folder identity with the exact FolderId and
// all fields supplied, for the bbolt→Postgres migration. Unlike EnsureFolderId
// it preserves the source's canonical id (items and collaboration records
// reference it). StoredFolderIdentity carries no folder name, so the caller
// resolves it from the source via FolderNameByID and passes it here.
func (d *DB) RestoreFolderIdentity(folderName string, f semcore.StoredFolderIdentity) error {
	_, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_folder_identity
			(mbox_key, folder_name, folder_id, parent_id, role, sort_order, highest_modseq, is_subscribed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (mbox_key, folder_name) DO UPDATE SET
			folder_id=EXCLUDED.folder_id, parent_id=EXCLUDED.parent_id, role=EXCLUDED.role,
			sort_order=EXCLUDED.sort_order, highest_modseq=EXCLUDED.highest_modseq,
			is_subscribed=EXCLUDED.is_subscribed`,
		f.MailboxID.String(), folderName, f.FolderID.String(), f.ParentID.String(),
		f.Role, f.SortOrder, int64(f.HighestModSeq), f.IsSubscribed)
	if err != nil {
		return fmt.Errorf("postgres: restore folder identity %s/%s: %w", f.MailboxID.String(), folderName, err)
	}
	return nil
}

// GetFolderID returns the FolderId for a mailbox+folder, or ErrFolderNotFound.
func (d *DB) GetFolderID(mboxKey, folderName string) (semcore.FolderId, error) {
	var raw string
	err := d.pool.QueryRow(context.Background(),
		`SELECT folder_id FROM semcore_folder_identity WHERE mbox_key=$1 AND folder_name=$2`,
		mboxKey, folderName,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return semcore.FolderId{}, semcore.ErrFolderNotFound
		}
		return semcore.FolderId{}, fmt.Errorf("postgres: get folder id %s/%s: %w", mboxKey, folderName, err)
	}
	return semcore.NewFolderId(raw)
}

// GetFolderByID returns the folder identity for a FolderId, or ErrFolderNotFound.
func (d *DB) GetFolderByID(id semcore.FolderId) (*semcore.StoredFolderIdentity, error) {
	var mboxKey, parentID, role string
	var sortOrder int
	var highestModSeq int64
	var subscribed bool
	err := d.pool.QueryRow(context.Background(), `
		SELECT mbox_key, parent_id, role, sort_order, highest_modseq, is_subscribed
		FROM semcore_folder_identity WHERE folder_id=$1`, id.String(),
	).Scan(&mboxKey, &parentID, &role, &sortOrder, &highestModSeq, &subscribed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, semcore.ErrFolderNotFound
		}
		return nil, fmt.Errorf("postgres: get folder by id %s: %w", id, err)
	}
	rec, err := scanFolder(mboxKey, id.String(), parentID, role, sortOrder, highestModSeq, subscribed)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetFolderByMailbox returns the folder identity holding the given role in the
// mailbox (ErrFolderNotFound when none), preferring the canonically-named folder
// when several share the role.
func (d *DB) GetFolderByMailbox(mboxKey, role string) (*semcore.StoredFolderIdentity, error) {
	return d.getFolderByRole(mboxKey, role)
}

// getFolderByRole finds the mailbox folder with the role, preferring a folder
// whose name matches the canonical name for that role (mirrors the bbolt store).
func (d *DB) getFolderByRole(mboxKey, role string) (*semcore.StoredFolderIdentity, error) {
	rows, err := d.pool.Query(context.Background(), `
		SELECT folder_name, folder_id, parent_id, sort_order, highest_modseq, is_subscribed
		FROM semcore_folder_identity WHERE mbox_key=$1 AND role=$2`, mboxKey, role)
	if err != nil {
		return nil, fmt.Errorf("postgres: folder by role %s/%s: %w", mboxKey, role, err)
	}
	defer rows.Close()
	canonical := semcore.CanonicalFolderNameForRole(role)
	var result *semcore.StoredFolderIdentity
	var resultName string
	for rows.Next() {
		var name, folderID, parentID string
		var sortOrder int
		var highestModSeq int64
		var subscribed bool
		if err := rows.Scan(&name, &folderID, &parentID, &sortOrder, &highestModSeq, &subscribed); err != nil {
			return nil, fmt.Errorf("postgres: scan folder by role: %w", err)
		}
		rec, err := scanFolder(mboxKey, folderID, parentID, role, sortOrder, highestModSeq, subscribed)
		if err != nil {
			return nil, err
		}
		if result == nil {
			rc := rec
			result, resultName = &rc, name
			continue
		}
		if canonical != "" && strings.EqualFold(name, canonical) && !strings.EqualFold(resultName, canonical) {
			rc := rec
			result, resultName = &rc, name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: folder by role %s/%s: %w", mboxKey, role, err)
	}
	if result == nil {
		return nil, semcore.ErrFolderNotFound
	}
	return result, nil
}

// ListFolderIdentitiesForMailbox returns all folder identities for a mailbox.
func (d *DB) ListFolderIdentitiesForMailbox(mboxKey string) ([]semcore.StoredFolderIdentity, error) {
	rows, err := d.pool.Query(context.Background(), `
		SELECT folder_id, parent_id, role, sort_order, highest_modseq, is_subscribed
		FROM semcore_folder_identity WHERE mbox_key=$1`, mboxKey)
	if err != nil {
		return nil, fmt.Errorf("postgres: list folders %s: %w", mboxKey, err)
	}
	defer rows.Close()
	var result []semcore.StoredFolderIdentity
	for rows.Next() {
		var folderID, parentID, role string
		var sortOrder int
		var highestModSeq int64
		var subscribed bool
		if err := rows.Scan(&folderID, &parentID, &role, &sortOrder, &highestModSeq, &subscribed); err != nil {
			return nil, fmt.Errorf("postgres: scan folder: %w", err)
		}
		rec, err := scanFolder(mboxKey, folderID, parentID, role, sortOrder, highestModSeq, subscribed)
		if err != nil {
			return nil, err
		}
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list folders %s: %w", mboxKey, err)
	}
	return result, nil
}

// FolderNameByID returns the stored folder name for a folder id within the
// mailbox, or ErrFolderNotFound.
func (d *DB) FolderNameByID(mboxKey string, id semcore.FolderId) (string, error) {
	var name string
	err := d.pool.QueryRow(context.Background(),
		`SELECT folder_name FROM semcore_folder_identity WHERE mbox_key=$1 AND folder_id=$2`,
		mboxKey, id.String(),
	).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", semcore.ErrFolderNotFound
		}
		return "", fmt.Errorf("postgres: folder name by id %s/%s: %w", mboxKey, id, err)
	}
	return name, nil
}

// SetFolderParent sets a folder's parent, or ErrFolderNotFound when absent.
func (d *DB) SetFolderParent(id, parentID semcore.FolderId) error {
	tag, err := d.pool.Exec(context.Background(),
		`UPDATE semcore_folder_identity SET parent_id=$2 WHERE folder_id=$1`,
		id.String(), parentID.String())
	if err != nil {
		return fmt.Errorf("postgres: set folder parent %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return semcore.ErrFolderNotFound
	}
	return nil
}

// DeleteFolder removes a folder identity, or ErrFolderNotFound when absent.
func (d *DB) DeleteFolder(id semcore.FolderId) error {
	tag, err := d.pool.Exec(context.Background(),
		`DELETE FROM semcore_folder_identity WHERE folder_id=$1`, id.String())
	if err != nil {
		return fmt.Errorf("postgres: delete folder %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return semcore.ErrFolderNotFound
	}
	return nil
}
