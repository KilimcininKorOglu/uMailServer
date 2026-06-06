package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core item and conversation identities. Mirrors the item surface of
// *semcore.BoltIdentityStore. Items are keyed by a storage key (the bbolt bucket
// key) and carry the canonical ItemId, which is indexed so id-based lookups and
// updates do not scan. PutChangeKey keeps the bbolt store's optimistic-concurrency
// check (the new ChangeKey only advances when the caller's currentCK matches).

// itemStorageKeyFor mirrors the bbolt itemStorageKey: "email\x00msgKey", or just
// msgKey when email is empty.
func itemStorageKeyFor(msgKey, email string) string {
	if email == "" {
		return msgKey
	}
	return email + "\x00" + msgKey
}

func parseConversationID(s string) semcore.ConversationId {
	if s == "" {
		return semcore.ConversationId{}
	}
	id, _ := semcore.NewConversationId(s) //nolint:errcheck // non-empty by guard
	return id
}

func parseMailboxID(s string) semcore.MailboxId {
	if s == "" {
		return semcore.MailboxId{}
	}
	id, _ := semcore.NewMailboxId(s) //nolint:errcheck // non-empty by guard
	return id
}

// PutItemIdentity stores a new item identity under the default storage key
// (email + msgKey). Returns semcore.ErrIdentityExists if that key is taken.
func (d *DB) PutItemIdentity(msgKey, email string, id semcore.ItemId, mailboxID semcore.MailboxId, folderID semcore.FolderId, ck semcore.ChangeKey, convID semcore.ConversationId, isRead bool) error {
	return d.putItem(itemStorageKeyFor(msgKey, email), msgKey, email, id, mailboxID, folderID, ck, convID, isRead)
}

// PutItemIdentityWithKey stores a new item identity under an explicit storage
// key (used to register the same content in different folders).
func (d *DB) PutItemIdentityWithKey(storageKey, msgKey, email string, id semcore.ItemId, mailboxID semcore.MailboxId, folderID semcore.FolderId, ck semcore.ChangeKey, convID semcore.ConversationId, isRead bool) error {
	return d.putItem(storageKey, msgKey, email, id, mailboxID, folderID, ck, convID, isRead)
}

func (d *DB) putItem(storageKey, msgKey, email string, id semcore.ItemId, mailboxID semcore.MailboxId, folderID semcore.FolderId, ck semcore.ChangeKey, convID semcore.ConversationId, isRead bool) error {
	if id.IsZero() {
		return errors.New("PutItemIdentity: zero ItemId")
	}
	tag, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_item_identity
			(storage_key, item_id, mailbox_id, folder_id, change_key, conversation_id, msg_key, email, is_read, categories)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'{}')
		ON CONFLICT (storage_key) DO NOTHING`,
		[]byte(storageKey), id.String(), mailboxID.String(), folderID.String(), ck.String(),
		convID.String(), msgKey, email, isRead,
	)
	if err != nil {
		return fmt.Errorf("postgres: put item identity %s: %w", storageKey, err)
	}
	if tag.RowsAffected() == 0 {
		return semcore.ErrIdentityExists
	}
	return nil
}

// GetItemIDByKey returns the ItemId of the item stored under the given message
// key (the msg_key the writer recorded), or ErrItemNotFound. Mirrors the bbolt
// store's key/suffix lookup, which resolves to the same record.
func (d *DB) GetItemIDByKey(msgKey string) (semcore.ItemId, error) {
	var raw string
	err := d.pool.QueryRow(context.Background(),
		`SELECT item_id FROM semcore_item_identity WHERE msg_key=$1 LIMIT 1`, msgKey,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return semcore.ItemId{}, semcore.ErrItemNotFound
		}
		return semcore.ItemId{}, fmt.Errorf("postgres: get item id by key %q: %w", msgKey, err)
	}
	return semcore.NewItemId(raw)
}

// GetItemIdentity returns the item identity for an ItemId, or ErrItemNotFound.
func (d *DB) GetItemIdentity(id semcore.ItemId) (*semcore.StoredItemIdentity, error) {
	var itemID, mailboxID, folderID, changeKey, convID, msgKey, email string
	var isRead bool
	var categories []string
	err := d.pool.QueryRow(context.Background(), `
		SELECT item_id, mailbox_id, folder_id, change_key, conversation_id, msg_key, email, is_read, categories
		FROM semcore_item_identity WHERE item_id=$1 LIMIT 1`, id.String(),
	).Scan(&itemID, &mailboxID, &folderID, &changeKey, &convID, &msgKey, &email, &isRead, &categories)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, semcore.ErrItemNotFound
		}
		return nil, fmt.Errorf("postgres: get item identity %s: %w", id, err)
	}
	rec := &semcore.StoredItemIdentity{
		ItemID:         id,
		MailboxID:      parseMailboxID(mailboxID),
		FolderID:       parseFolderID(folderID),
		ChangeKey:      parseChangeKey(changeKey),
		ConversationID: parseConversationID(convID),
		MsgKey:         msgKey,
		Email:          email,
		IsRead:         isRead,
		Categories:     categories,
	}
	return rec, nil
}

// ListItemIdentitiesByFolder returns all item identities in a folder.
func (d *DB) ListItemIdentitiesByFolder(folderID semcore.FolderId) ([]semcore.StoredItemIdentity, error) {
	rows, err := d.pool.Query(context.Background(), `
		SELECT item_id, mailbox_id, change_key, conversation_id, msg_key, email, is_read, categories
		FROM semcore_item_identity WHERE folder_id=$1`, folderID.String())
	if err != nil {
		return nil, fmt.Errorf("postgres: list items by folder %s: %w", folderID, err)
	}
	defer rows.Close()
	var result []semcore.StoredItemIdentity
	for rows.Next() {
		var itemID, mailboxID, changeKey, convID, msgKey, email string
		var isRead bool
		var categories []string
		if err := rows.Scan(&itemID, &mailboxID, &changeKey, &convID, &msgKey, &email, &isRead, &categories); err != nil {
			return nil, fmt.Errorf("postgres: scan item identity: %w", err)
		}
		iid, err := semcore.NewItemId(itemID)
		if err != nil {
			return nil, fmt.Errorf("postgres: invalid item id %q: %w", itemID, err)
		}
		result = append(result, semcore.StoredItemIdentity{
			ItemID:         iid,
			MailboxID:      parseMailboxID(mailboxID),
			FolderID:       folderID,
			ChangeKey:      parseChangeKey(changeKey),
			ConversationID: parseConversationID(convID),
			MsgKey:         msgKey,
			Email:          email,
			IsRead:         isRead,
			Categories:     categories,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list items by folder %s: %w", folderID, err)
	}
	return result, nil
}

// SetItemFolder moves an item to another folder, or ErrItemNotFound when absent.
func (d *DB) SetItemFolder(id semcore.ItemId, folderID semcore.FolderId) error {
	if id.IsZero() {
		return errors.New("SetItemFolder: zero ItemId")
	}
	if folderID.IsZero() {
		return errors.New("SetItemFolder: zero FolderId")
	}
	return d.execItemUpdate(`UPDATE semcore_item_identity SET folder_id=$2 WHERE item_id=$1`,
		id.String(), folderID.String())
}

// SetItemMsgKey updates an item's message-store blob key.
func (d *DB) SetItemMsgKey(id semcore.ItemId, msgKey string) error {
	if id.IsZero() {
		return errors.New("SetItemMsgKey: zero ItemId")
	}
	if msgKey == "" {
		return errors.New("SetItemMsgKey: empty msgKey")
	}
	return d.execItemUpdate(`UPDATE semcore_item_identity SET msg_key=$2 WHERE item_id=$1`,
		id.String(), msgKey)
}

// UpdateItemState stores read/category state, updating only the supplied fields.
func (d *DB) UpdateItemState(id semcore.ItemId, isRead *bool, categories []string) error {
	if id.IsZero() {
		return errors.New("UpdateItemState: zero ItemId")
	}
	setRead := isRead != nil
	readVal := false
	if isRead != nil {
		readVal = *isRead
	}
	setCats := categories != nil
	if categories == nil {
		categories = []string{}
	}
	return d.execItemUpdate(`
		UPDATE semcore_item_identity
		SET is_read = CASE WHEN $2 THEN $3 ELSE is_read END,
		    categories = CASE WHEN $4 THEN $5 ELSE categories END
		WHERE item_id=$1`,
		id.String(), setRead, readVal, setCats, categories)
}

// execItemUpdate runs an UPDATE keyed by item_id and maps zero rows to
// ErrItemNotFound.
func (d *DB) execItemUpdate(sql string, args ...any) error {
	tag, err := d.pool.Exec(context.Background(), sql, args...)
	if err != nil {
		return fmt.Errorf("postgres: update item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return semcore.ErrItemNotFound
	}
	return nil
}

// PutChangeKey advances an item's ChangeKey, rejecting stale writes: the new key
// only applies when the caller's currentCK matches the stored one (or both are
// zero on first write), mirroring the bbolt store's optimistic concurrency.
func (d *DB) PutChangeKey(id semcore.ItemId, currentCK, newCK semcore.ChangeKey) error {
	if id.IsZero() {
		return errors.New("PutChangeKey: zero ItemId")
	}
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: put change key %s: %w", id, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var stored string
	err = tx.QueryRow(ctx,
		`SELECT change_key FROM semcore_item_identity WHERE item_id=$1 LIMIT 1 FOR UPDATE`, id.String(),
	).Scan(&stored)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return semcore.ErrItemNotFound
		}
		return fmt.Errorf("postgres: put change key %s: %w", id, err)
	}
	if !(currentCK.IsZero() && stored == "") && stored != currentCK.String() {
		return fmt.Errorf("stale ChangeKey: expected %v, found %v", currentCK, stored)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE semcore_item_identity SET change_key=$2 WHERE item_id=$1`, id.String(), newCK.String(),
	); err != nil {
		return fmt.Errorf("postgres: put change key %s: %w", id, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: put change key %s: %w", id, err)
	}
	return nil
}

// DeleteItemIdentity removes an item identity, or ErrItemNotFound when absent.
func (d *DB) DeleteItemIdentity(id semcore.ItemId) error {
	tag, err := d.pool.Exec(context.Background(),
		`DELETE FROM semcore_item_identity WHERE item_id=$1`, id.String())
	if err != nil {
		return fmt.Errorf("postgres: delete item identity %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return semcore.ErrItemNotFound
	}
	return nil
}

// PutConversationIdentity registers a conversation's owning mailbox. Returns
// semcore.ErrIdentityExists when the conversation is already registered (the
// bbolt store does not overwrite).
func (d *DB) PutConversationIdentity(id semcore.ConversationId, mailboxID semcore.MailboxId) error {
	if id.IsZero() {
		return errors.New("PutConversationIdentity: zero ConversationId")
	}
	tag, err := d.pool.Exec(context.Background(), `
		INSERT INTO semcore_conversation_identity (conversation_id, mailbox_id)
		VALUES ($1, $2) ON CONFLICT (conversation_id) DO NOTHING`,
		id.String(), mailboxID.String(),
	)
	if err != nil {
		return fmt.Errorf("postgres: put conversation identity %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return semcore.ErrIdentityExists
	}
	return nil
}
