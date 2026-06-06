package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core collaboration identities: calendar items, tasks, and contacts.
// Mirrors the consumer surface of *semcore.BoltCollaborationStore. Each record
// is keyed by the writer-chosen msg_key and located by (folder, ical_uid). Only
// the "Unsafe" put (no version check), folder listing, and UID find/delete are
// exposed — exactly what the API/CalDAV/CardDAV bridges use.

func parseCalendarItemID(s string) semcore.CalendarItemId {
	if s == "" {
		return semcore.CalendarItemId{}
	}
	id, _ := semcore.NewCalendarItemId(s) //nolint:errcheck // non-empty by guard
	return id
}

func parseCalendarChangeKey(s string) semcore.CalendarChangeKey {
	if s == "" {
		return semcore.CalendarChangeKey{}
	}
	ck, _ := semcore.NewCalendarChangeKey(s) //nolint:errcheck // non-empty by guard
	return ck
}

func parseTaskID(s string) semcore.TaskId {
	if s == "" {
		return semcore.TaskId{}
	}
	id, _ := semcore.NewTaskId(s) //nolint:errcheck // non-empty by guard
	return id
}

func parseTaskChangeKey(s string) semcore.TaskChangeKey {
	if s == "" {
		return semcore.TaskChangeKey{}
	}
	ck, _ := semcore.NewTaskChangeKey(s) //nolint:errcheck // non-empty by guard
	return ck
}

func parseContactID(s string) semcore.ContactId {
	if s == "" {
		return semcore.ContactId{}
	}
	id, _ := semcore.NewContactId(s) //nolint:errcheck // non-empty by guard
	return id
}

func parseContactChangeKey(s string) semcore.ContactChangeKey {
	if s == "" {
		return semcore.ContactChangeKey{}
	}
	ck, _ := semcore.NewContactChangeKey(s) //nolint:errcheck // non-empty by guard
	return ck
}

// --- calendar items ---

// PutCalendarItemIdentityUnsafe upserts a calendar item identity by msg_key.
func (d *DB) PutCalendarItemIdentityUnsafe(msgKey string, rec *semcore.StoredCalendarItemIdentity) error {
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_calendar_item (msg_key, id, master_id, folder_id, mailbox_id, change_key, kind, ical_uid, raw_hash, etag, raw_data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (msg_key) DO UPDATE SET id=EXCLUDED.id, master_id=EXCLUDED.master_id,
			folder_id=EXCLUDED.folder_id, mailbox_id=EXCLUDED.mailbox_id, change_key=EXCLUDED.change_key,
			kind=EXCLUDED.kind, ical_uid=EXCLUDED.ical_uid, raw_hash=EXCLUDED.raw_hash,
			etag=EXCLUDED.etag, raw_data=EXCLUDED.raw_data`,
		msgKey, rec.ID.String(), rec.MasterID.String(), rec.FolderID.String(), rec.MailboxID.String(),
		rec.ChangeKey.String(), int16(rec.Kind), rec.IcalUID, rec.RawHash, rec.ETag, rec.RawData,
	); err != nil {
		return fmt.Errorf("postgres: put calendar item %s: %w", msgKey, err)
	}
	return nil
}

// FindCalendarItemByUID locates a calendar item by folder + iCal UID.
func (d *DB) FindCalendarItemByUID(folderID semcore.FolderId, icalUID string) (string, *semcore.StoredCalendarItemIdentity, bool, error) {
	var msgKey, id, masterID, mailboxID, changeKey, rawHash, etag, rawData string
	var kind int16
	err := d.pool.QueryRow(context.Background(), `
		SELECT msg_key, id, master_id, mailbox_id, change_key, kind, raw_hash, etag, raw_data
		FROM semcore_calendar_item WHERE folder_id=$1 AND ical_uid=$2 LIMIT 1`,
		folderID.String(), icalUID,
	).Scan(&msgKey, &id, &masterID, &mailboxID, &changeKey, &kind, &rawHash, &etag, &rawData)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, false, nil
		}
		return "", nil, false, fmt.Errorf("postgres: find calendar item %s/%s: %w", folderID, icalUID, err)
	}
	return msgKey, &semcore.StoredCalendarItemIdentity{
		ID: parseCalendarItemID(id), MasterID: parseCalendarItemID(masterID), FolderID: folderID,
		MailboxID: parseMailboxID(mailboxID), ChangeKey: parseCalendarChangeKey(changeKey),
		Kind: semcore.CollabKind(uint8(kind)), IcalUID: icalUID, RawHash: rawHash, ETag: etag, RawData: rawData,
	}, true, nil
}

// ListCalendarItemsByFolder returns all calendar items in a folder.
func (d *DB) ListCalendarItemsByFolder(folderID semcore.FolderId) ([]semcore.StoredCalendarItemIdentity, error) {
	rows, err := d.pool.Query(context.Background(), `
		SELECT id, master_id, mailbox_id, change_key, kind, ical_uid, raw_hash, etag, raw_data
		FROM semcore_calendar_item WHERE folder_id=$1`, folderID.String())
	if err != nil {
		return nil, fmt.Errorf("postgres: list calendar items %s: %w", folderID, err)
	}
	defer rows.Close()
	var out []semcore.StoredCalendarItemIdentity
	for rows.Next() {
		var id, masterID, mailboxID, changeKey, icalUID, rawHash, etag, rawData string
		var kind int16
		if err := rows.Scan(&id, &masterID, &mailboxID, &changeKey, &kind, &icalUID, &rawHash, &etag, &rawData); err != nil {
			return nil, fmt.Errorf("postgres: scan calendar item: %w", err)
		}
		out = append(out, semcore.StoredCalendarItemIdentity{
			ID: parseCalendarItemID(id), MasterID: parseCalendarItemID(masterID), FolderID: folderID,
			MailboxID: parseMailboxID(mailboxID), ChangeKey: parseCalendarChangeKey(changeKey),
			Kind: semcore.CollabKind(uint8(kind)), IcalUID: icalUID, RawHash: rawHash, ETag: etag, RawData: rawData,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list calendar items %s: %w", folderID, err)
	}
	return out, nil
}

// DeleteCalendarItemByUID removes a calendar item by folder + iCal UID (no-op
// when absent, matching the bbolt store).
func (d *DB) DeleteCalendarItemByUID(folderID semcore.FolderId, icalUID string) error {
	if _, err := d.pool.Exec(context.Background(),
		`DELETE FROM semcore_calendar_item WHERE folder_id=$1 AND ical_uid=$2`, folderID.String(), icalUID); err != nil {
		return fmt.Errorf("postgres: delete calendar item %s/%s: %w", folderID, icalUID, err)
	}
	return nil
}

// --- tasks ---

// PutTaskIdentityUnsafe upserts a task identity by msg_key.
func (d *DB) PutTaskIdentityUnsafe(msgKey string, rec *semcore.StoredTaskIdentity) error {
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_task (msg_key, id, folder_id, mailbox_id, change_key, ical_uid, raw_hash, etag, raw_data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (msg_key) DO UPDATE SET id=EXCLUDED.id, folder_id=EXCLUDED.folder_id,
			mailbox_id=EXCLUDED.mailbox_id, change_key=EXCLUDED.change_key, ical_uid=EXCLUDED.ical_uid,
			raw_hash=EXCLUDED.raw_hash, etag=EXCLUDED.etag, raw_data=EXCLUDED.raw_data`,
		msgKey, rec.ID.String(), rec.FolderID.String(), rec.MailboxID.String(), rec.ChangeKey.String(),
		rec.IcalUID, rec.RawHash, rec.ETag, rec.RawData,
	); err != nil {
		return fmt.Errorf("postgres: put task %s: %w", msgKey, err)
	}
	return nil
}

// FindTaskByUID locates a task by folder + iCal UID.
func (d *DB) FindTaskByUID(folderID semcore.FolderId, icalUID string) (string, *semcore.StoredTaskIdentity, bool, error) {
	var msgKey, id, mailboxID, changeKey, rawHash, etag, rawData string
	err := d.pool.QueryRow(context.Background(), `
		SELECT msg_key, id, mailbox_id, change_key, raw_hash, etag, raw_data
		FROM semcore_task WHERE folder_id=$1 AND ical_uid=$2 LIMIT 1`, folderID.String(), icalUID,
	).Scan(&msgKey, &id, &mailboxID, &changeKey, &rawHash, &etag, &rawData)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, false, nil
		}
		return "", nil, false, fmt.Errorf("postgres: find task %s/%s: %w", folderID, icalUID, err)
	}
	return msgKey, &semcore.StoredTaskIdentity{
		ID: parseTaskID(id), FolderID: folderID, MailboxID: parseMailboxID(mailboxID),
		ChangeKey: parseTaskChangeKey(changeKey), IcalUID: icalUID, RawHash: rawHash, ETag: etag, RawData: rawData,
	}, true, nil
}

// ListTasksByFolder returns all tasks in a folder.
func (d *DB) ListTasksByFolder(folderID semcore.FolderId) ([]semcore.StoredTaskIdentity, error) {
	rows, err := d.pool.Query(context.Background(), `
		SELECT id, mailbox_id, change_key, ical_uid, raw_hash, etag, raw_data
		FROM semcore_task WHERE folder_id=$1`, folderID.String())
	if err != nil {
		return nil, fmt.Errorf("postgres: list tasks %s: %w", folderID, err)
	}
	defer rows.Close()
	var out []semcore.StoredTaskIdentity
	for rows.Next() {
		var id, mailboxID, changeKey, icalUID, rawHash, etag, rawData string
		if err := rows.Scan(&id, &mailboxID, &changeKey, &icalUID, &rawHash, &etag, &rawData); err != nil {
			return nil, fmt.Errorf("postgres: scan task: %w", err)
		}
		out = append(out, semcore.StoredTaskIdentity{
			ID: parseTaskID(id), FolderID: folderID, MailboxID: parseMailboxID(mailboxID),
			ChangeKey: parseTaskChangeKey(changeKey), IcalUID: icalUID, RawHash: rawHash, ETag: etag, RawData: rawData,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list tasks %s: %w", folderID, err)
	}
	return out, nil
}

// DeleteTaskByUID removes a task by folder + iCal UID (no-op when absent).
func (d *DB) DeleteTaskByUID(folderID semcore.FolderId, icalUID string) error {
	if _, err := d.pool.Exec(context.Background(),
		`DELETE FROM semcore_task WHERE folder_id=$1 AND ical_uid=$2`, folderID.String(), icalUID); err != nil {
		return fmt.Errorf("postgres: delete task %s/%s: %w", folderID, icalUID, err)
	}
	return nil
}

// --- contacts ---

// PutContactIdentityUnsafe upserts a contact identity by msg_key.
func (d *DB) PutContactIdentityUnsafe(msgKey string, rec *semcore.StoredContactIdentity) error {
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_contact (msg_key, id, folder_id, mailbox_id, change_key, ical_uid, raw_hash, etag, raw_data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (msg_key) DO UPDATE SET id=EXCLUDED.id, folder_id=EXCLUDED.folder_id,
			mailbox_id=EXCLUDED.mailbox_id, change_key=EXCLUDED.change_key, ical_uid=EXCLUDED.ical_uid,
			raw_hash=EXCLUDED.raw_hash, etag=EXCLUDED.etag, raw_data=EXCLUDED.raw_data`,
		msgKey, rec.ID.String(), rec.FolderID.String(), rec.MailboxID.String(), rec.ChangeKey.String(),
		rec.IcalUID, rec.RawHash, rec.ETag, rec.RawData,
	); err != nil {
		return fmt.Errorf("postgres: put contact %s: %w", msgKey, err)
	}
	return nil
}

// FindContactByUID locates a contact by folder + UID.
func (d *DB) FindContactByUID(folderID semcore.FolderId, icalUID string) (string, *semcore.StoredContactIdentity, bool, error) {
	var msgKey, id, mailboxID, changeKey, rawHash, etag, rawData string
	err := d.pool.QueryRow(context.Background(), `
		SELECT msg_key, id, mailbox_id, change_key, raw_hash, etag, raw_data
		FROM semcore_contact WHERE folder_id=$1 AND ical_uid=$2 LIMIT 1`, folderID.String(), icalUID,
	).Scan(&msgKey, &id, &mailboxID, &changeKey, &rawHash, &etag, &rawData)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, false, nil
		}
		return "", nil, false, fmt.Errorf("postgres: find contact %s/%s: %w", folderID, icalUID, err)
	}
	return msgKey, &semcore.StoredContactIdentity{
		ID: parseContactID(id), FolderID: folderID, MailboxID: parseMailboxID(mailboxID),
		ChangeKey: parseContactChangeKey(changeKey), IcalUID: icalUID, RawHash: rawHash, ETag: etag, RawData: rawData,
	}, true, nil
}

// ListContactsByFolder returns all contacts in a folder.
func (d *DB) ListContactsByFolder(folderID semcore.FolderId) ([]semcore.StoredContactIdentity, error) {
	rows, err := d.pool.Query(context.Background(), `
		SELECT id, mailbox_id, change_key, ical_uid, raw_hash, etag, raw_data
		FROM semcore_contact WHERE folder_id=$1`, folderID.String())
	if err != nil {
		return nil, fmt.Errorf("postgres: list contacts %s: %w", folderID, err)
	}
	defer rows.Close()
	var out []semcore.StoredContactIdentity
	for rows.Next() {
		var id, mailboxID, changeKey, icalUID, rawHash, etag, rawData string
		if err := rows.Scan(&id, &mailboxID, &changeKey, &icalUID, &rawHash, &etag, &rawData); err != nil {
			return nil, fmt.Errorf("postgres: scan contact: %w", err)
		}
		out = append(out, semcore.StoredContactIdentity{
			ID: parseContactID(id), FolderID: folderID, MailboxID: parseMailboxID(mailboxID),
			ChangeKey: parseContactChangeKey(changeKey), IcalUID: icalUID, RawHash: rawHash, ETag: etag, RawData: rawData,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list contacts %s: %w", folderID, err)
	}
	return out, nil
}

// DeleteContactByUID removes a contact by folder + UID (no-op when absent).
func (d *DB) DeleteContactByUID(folderID semcore.FolderId, icalUID string) error {
	if _, err := d.pool.Exec(context.Background(),
		`DELETE FROM semcore_contact WHERE folder_id=$1 AND ical_uid=$2`, folderID.String(), icalUID); err != nil {
		return fmt.Errorf("postgres: delete contact %s/%s: %w", folderID, icalUID, err)
	}
	return nil
}

// --- ews.CollabStore extras: by-id lookups + change-key-checked put/delete ---

// GetCalendarItemByID returns a calendar item by its CalendarItemId.
func (d *DB) GetCalendarItemByID(id semcore.CalendarItemId) (*semcore.StoredCalendarItemIdentity, error) {
	var msgKey, masterID, folderID, mailboxID, changeKey, icalUID, rawHash, etag, rawData string
	var kind int16
	err := d.pool.QueryRow(context.Background(), `
		SELECT msg_key, master_id, folder_id, mailbox_id, change_key, kind, ical_uid, raw_hash, etag, raw_data
		FROM semcore_calendar_item WHERE id=$1 LIMIT 1`, id.String(),
	).Scan(&msgKey, &masterID, &folderID, &mailboxID, &changeKey, &kind, &icalUID, &rawHash, &etag, &rawData)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, semcore.ErrCalendarItemNotFound
		}
		return nil, fmt.Errorf("postgres: get calendar item by id %s: %w", id, err)
	}
	_ = msgKey
	return &semcore.StoredCalendarItemIdentity{
		ID: id, MasterID: parseCalendarItemID(masterID), FolderID: parseFolderID(folderID),
		MailboxID: parseMailboxID(mailboxID), ChangeKey: parseCalendarChangeKey(changeKey),
		Kind: semcore.CollabKind(uint8(kind)), IcalUID: icalUID, RawHash: rawHash, ETag: etag, RawData: rawData,
	}, nil
}

// PutCalendarItemIdentity upserts with an optimistic change-key check on update.
func (d *DB) PutCalendarItemIdentity(msgKey string, rec *semcore.StoredCalendarItemIdentity, currentChangeKey semcore.CalendarChangeKey) error {
	if err := d.checkCollabChangeKey(context.Background(), "semcore_calendar_item", msgKey, currentChangeKey.String(), currentChangeKey.IsZero()); err != nil {
		return err
	}
	return d.PutCalendarItemIdentityUnsafe(msgKey, rec)
}

// DeleteCalendarItemIdentity removes by msg_key with an optimistic change-key check.
func (d *DB) DeleteCalendarItemIdentity(msgKey string, currentChangeKey semcore.CalendarChangeKey) error {
	return d.deleteCollabChecked(context.Background(), "semcore_calendar_item", msgKey, currentChangeKey.String(), semcore.ErrCalendarItemNotFound)
}

// GetContactByID returns a contact by its ContactId.
func (d *DB) GetContactByID(id semcore.ContactId) (*semcore.StoredContactIdentity, error) {
	var msgKey, folderID, mailboxID, changeKey, icalUID, rawHash, etag, rawData string
	err := d.pool.QueryRow(context.Background(), `
		SELECT msg_key, folder_id, mailbox_id, change_key, ical_uid, raw_hash, etag, raw_data
		FROM semcore_contact WHERE id=$1 LIMIT 1`, id.String(),
	).Scan(&msgKey, &folderID, &mailboxID, &changeKey, &icalUID, &rawHash, &etag, &rawData)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, semcore.ErrContactNotFound
		}
		return nil, fmt.Errorf("postgres: get contact by id %s: %w", id, err)
	}
	_ = msgKey
	return &semcore.StoredContactIdentity{
		ID: id, FolderID: parseFolderID(folderID), MailboxID: parseMailboxID(mailboxID),
		ChangeKey: parseContactChangeKey(changeKey), IcalUID: icalUID, RawHash: rawHash, ETag: etag, RawData: rawData,
	}, nil
}

// PutContactIdentity upserts with an optimistic change-key check on update.
func (d *DB) PutContactIdentity(msgKey string, rec *semcore.StoredContactIdentity, currentChangeKey semcore.ContactChangeKey) error {
	if err := d.checkCollabChangeKey(context.Background(), "semcore_contact", msgKey, currentChangeKey.String(), currentChangeKey.IsZero()); err != nil {
		return err
	}
	return d.PutContactIdentityUnsafe(msgKey, rec)
}

// DeleteContactIdentity removes by msg_key with an optimistic change-key check.
func (d *DB) DeleteContactIdentity(msgKey string, currentChangeKey semcore.ContactChangeKey) error {
	return d.deleteCollabChecked(context.Background(), "semcore_contact", msgKey, currentChangeKey.String(), semcore.ErrContactNotFound)
}

// GetTaskByID returns a task by its TaskId.
func (d *DB) GetTaskByID(id semcore.TaskId) (*semcore.StoredTaskIdentity, error) {
	var msgKey, folderID, mailboxID, changeKey, icalUID, rawHash, etag, rawData string
	err := d.pool.QueryRow(context.Background(), `
		SELECT msg_key, folder_id, mailbox_id, change_key, ical_uid, raw_hash, etag, raw_data
		FROM semcore_task WHERE id=$1 LIMIT 1`, id.String(),
	).Scan(&msgKey, &folderID, &mailboxID, &changeKey, &icalUID, &rawHash, &etag, &rawData)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, semcore.ErrTaskNotFound
		}
		return nil, fmt.Errorf("postgres: get task by id %s: %w", id, err)
	}
	_ = msgKey
	return &semcore.StoredTaskIdentity{
		ID: id, FolderID: parseFolderID(folderID), MailboxID: parseMailboxID(mailboxID),
		ChangeKey: parseTaskChangeKey(changeKey), IcalUID: icalUID, RawHash: rawHash, ETag: etag, RawData: rawData,
	}, nil
}

// PutTaskIdentity upserts with an optimistic change-key check on update.
func (d *DB) PutTaskIdentity(msgKey string, rec *semcore.StoredTaskIdentity, currentChangeKey semcore.TaskChangeKey) error {
	if err := d.checkCollabChangeKey(context.Background(), "semcore_task", msgKey, currentChangeKey.String(), currentChangeKey.IsZero()); err != nil {
		return err
	}
	return d.PutTaskIdentityUnsafe(msgKey, rec)
}

// DeleteTaskIdentity removes by msg_key with an optimistic change-key check.
func (d *DB) DeleteTaskIdentity(msgKey string, currentChangeKey semcore.TaskChangeKey) error {
	return d.deleteCollabChecked(context.Background(), "semcore_task", msgKey, currentChangeKey.String(), semcore.ErrTaskNotFound)
}

// checkCollabChangeKey enforces the optimistic-concurrency rule the bbolt store
// applies on a checked Put: if a row already exists at msgKey, currentChangeKey
// must be non-zero and match the stored change_key, else ErrCollabVersionConflict.
// A fresh insert (no existing row) passes without a check.
func (d *DB) checkCollabChangeKey(ctx context.Context, table, msgKey, currentChangeKey string, currentZero bool) error {
	var stored string
	err := d.pool.QueryRow(ctx, `SELECT change_key FROM `+table+` WHERE msg_key=$1`, msgKey).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // fresh insert
	}
	if err != nil {
		return fmt.Errorf("postgres: check change key %s/%s: %w", table, msgKey, err)
	}
	if currentZero {
		return fmt.Errorf("put %s: update requires currentChangeKey", table)
	}
	if stored != currentChangeKey {
		return semcore.ErrCollabVersionConflict
	}
	return nil
}

// deleteCollabChecked deletes a collab row by msg_key, enforcing the change-key
// check: absent -> notFound, mismatch -> ErrCollabVersionConflict.
func (d *DB) deleteCollabChecked(ctx context.Context, table, msgKey, currentChangeKey string, notFound error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: delete %s: %w", table, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	var stored string
	err = tx.QueryRow(ctx, `SELECT change_key FROM `+table+` WHERE msg_key=$1 FOR UPDATE`, msgKey).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound
	}
	if err != nil {
		return fmt.Errorf("postgres: delete %s/%s: %w", table, msgKey, err)
	}
	if stored != currentChangeKey {
		return semcore.ErrCollabVersionConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE msg_key=$1`, msgKey); err != nil {
		return fmt.Errorf("postgres: delete %s/%s: %w", table, msgKey, err)
	}
	return tx.Commit(ctx)
}
