package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/storage"
)

// Message-metadata methods of the relational storage layer. Message BODIES stay
// as Maildir files; this is only the per-message metadata + the search vector
// (generated from the indexed headers) + the counts/flag operations over it.

const messageColumns = `message_id, flags, labels, internal_date, size, mod_seq,
	subject, date_hdr, from_addr, to_addr, in_reply_to, refs, thread_id, is_thread_root`

// StoreMessageMetadata upserts a message and records the email (and thread)
// change, matching the bbolt store: a new uid is "created", an existing one
// "updated". The mailbox row is ensured first so the foreign key holds (the
// bbolt store auto-creates the messages bucket).
func (d *DB) StoreMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error {
	if meta == nil {
		return nil
	}
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin store message: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if err := ensureMailboxTx(ctx, tx, user, mailbox); err != nil {
		return err
	}

	// RFC 7162: a newly arrived message gets a nonzero mod-sequence from the same
	// per-mailbox counter as flag changes, so CONDSTORE/QRESYNC can report it.
	// Updates and restores (modseq already set, e.g. migration) are left as-is.
	if meta.ModSeq == 0 {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM messages WHERE user_email=$1 AND mailbox=$2 AND uid=$3)`,
			user, mailbox, int64(uid)).Scan(&exists); err != nil {
			return fmt.Errorf("postgres: check message exists %s/%s/%d: %w", user, mailbox, uid, err)
		}
		if !exists {
			var ms int64
			if err := tx.QueryRow(ctx,
				`UPDATE mailboxes SET highest_modseq = highest_modseq + 1
				 WHERE user_email=$1 AND name=$2 RETURNING highest_modseq`,
				user, mailbox).Scan(&ms); err != nil {
				return fmt.Errorf("postgres: assign arrival modseq %s/%s: %w", user, mailbox, err)
			}
			meta.ModSeq = uint64(ms)
		}
	}

	var preexisting bool
	// xmax is non-zero on a row updated by ON CONFLICT, zero on a fresh insert.
	err = tx.QueryRow(ctx, `
		INSERT INTO messages (user_email, mailbox, uid, message_id, flags, labels,
			internal_date, size, mod_seq, subject, date_hdr, from_addr, to_addr,
			in_reply_to, refs, thread_id, is_thread_root)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (user_email, mailbox, uid) DO UPDATE SET
			message_id=EXCLUDED.message_id, flags=EXCLUDED.flags, labels=EXCLUDED.labels,
			internal_date=EXCLUDED.internal_date, size=EXCLUDED.size, mod_seq=EXCLUDED.mod_seq,
			subject=EXCLUDED.subject, date_hdr=EXCLUDED.date_hdr, from_addr=EXCLUDED.from_addr,
			to_addr=EXCLUDED.to_addr, in_reply_to=EXCLUDED.in_reply_to, refs=EXCLUDED.refs,
			thread_id=EXCLUDED.thread_id, is_thread_root=EXCLUDED.is_thread_root
		RETURNING xmax <> 0`,
		user, mailbox, int64(uid), meta.MessageID, flagsArg(meta.Flags), flagsArg(meta.Labels),
		meta.InternalDate, meta.Size, int64(meta.ModSeq), meta.Subject, meta.Date, meta.From, meta.To,
		meta.InReplyTo, flagsArg(meta.References), meta.ThreadID, meta.IsThreadRoot,
	).Scan(&preexisting)
	if err != nil {
		return fmt.Errorf("postgres: store message %s/%s/%d: %w", user, mailbox, uid, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit store message: %w", err)
	}

	if meta.MessageID != "" {
		kind := storage.ChangeKindCreated
		if preexisting {
			kind = storage.ChangeKindUpdated
		}
		//nolint:errcheck // best-effort journal
		_ = d.RecordChange(user, storage.ChangeTypeEmail, kind, meta.MessageID, mailbox)
		if meta.ThreadID != "" {
			tk := storage.ChangeKindUpdated
			if !preexisting && meta.IsThreadRoot {
				tk = storage.ChangeKindCreated
			}
			//nolint:errcheck // best-effort journal
			_ = d.RecordChange(user, storage.ChangeTypeThread, tk, meta.ThreadID, "")
		}
	}
	return nil
}

// UpdateMessageMetadata is an alias for StoreMessageMetadata (bbolt parity).
func (d *DB) UpdateMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error {
	return d.StoreMessageMetadata(user, mailbox, uid, meta)
}

// GetMessageMetadata returns the message metadata, or an empty struct when the
// message or mailbox does not exist (matching the bbolt store, which returns
// empty metadata rather than an error).
func (d *DB) GetMessageMetadata(user, mailbox string, uid uint32) (*storage.MessageMetadata, error) {
	ctx := context.Background()
	meta, err := scanMessage(d.pool.QueryRow(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE user_email=$1 AND mailbox=$2 AND uid=$3`,
		user, mailbox, int64(uid)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &storage.MessageMetadata{}, nil
		}
		return nil, fmt.Errorf("postgres: get message %s/%s/%d: %w", user, mailbox, uid, err)
	}
	meta.UID = uid
	return meta, nil
}

// DeleteMessage removes a message and records the email (and thread) change.
func (d *DB) DeleteMessage(user, mailbox string, uid uint32) error {
	ctx := context.Background()
	var messageID, threadID string
	err := d.pool.QueryRow(ctx,
		`DELETE FROM messages WHERE user_email=$1 AND mailbox=$2 AND uid=$3
		 RETURNING message_id, thread_id`,
		user, mailbox, int64(uid),
	).Scan(&messageID, &threadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // nothing to delete, matching bbolt's silent no-op
		}
		return fmt.Errorf("postgres: delete message %s/%s/%d: %w", user, mailbox, uid, err)
	}
	if messageID != "" {
		//nolint:errcheck // best-effort journal
		_ = d.RecordChange(user, storage.ChangeTypeEmail, storage.ChangeKindDestroyed, messageID, mailbox)
		if threadID != "" {
			//nolint:errcheck // best-effort journal
			_ = d.RecordChange(user, storage.ChangeTypeThread, storage.ChangeKindUpdated, threadID, "")
		}
	}
	return nil
}

// RecordExpungedUIDs stores an RFC 7162 expunge tombstone (uid -> modSeq) for
// each expunged UID so a later QRESYNC SELECT can report them as VANISHED
// (EARLIER). The rows cascade with their mailbox on delete/rename.
func (d *DB) RecordExpungedUIDs(user, mailbox string, uids []uint32, modSeq uint64) error {
	if len(uids) == 0 {
		return nil
	}
	ctx := context.Background()
	batch := &pgx.Batch{}
	for _, uid := range uids {
		batch.Queue(
			`INSERT INTO expunged_messages (user_email, mailbox, uid, mod_seq)
			 VALUES ($1,$2,$3,$4)
			 ON CONFLICT (user_email, mailbox, uid) DO UPDATE SET mod_seq=EXCLUDED.mod_seq`,
			user, mailbox, int64(uid), int64(modSeq))
	}
	br := d.pool.SendBatch(ctx, batch)
	defer br.Close() //nolint:errcheck // close reports the first exec error below
	for range uids {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres: record expunged %s/%s: %w", user, mailbox, err)
		}
	}
	return nil
}

// ExpungedUIDsSince returns, in ascending UID order, the UIDs expunged from the
// mailbox at a mod-sequence greater than sinceModSeq (RFC 7162 QRESYNC VANISHED
// EARLIER).
func (d *DB) ExpungedUIDsSince(user, mailbox string, sinceModSeq uint64) ([]uint32, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT uid FROM expunged_messages
		 WHERE user_email=$1 AND mailbox=$2 AND mod_seq > $3
		 ORDER BY uid`,
		user, mailbox, int64(sinceModSeq))
	if err != nil {
		return nil, fmt.Errorf("postgres: expunged since %s/%s: %w", user, mailbox, err)
	}
	defer rows.Close()
	var uids []uint32
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("postgres: scan expunged uid: %w", err)
		}
		uids = append(uids, uint32(uid))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate expunged: %w", err)
	}
	return uids, nil
}

// GetMessageUIDs returns the mailbox's UIDs in ascending order.
func (d *DB) GetMessageUIDs(user, mailbox string) ([]uint32, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT uid FROM messages WHERE user_email=$1 AND mailbox=$2 ORDER BY uid`, user, mailbox)
	if err != nil {
		return nil, fmt.Errorf("postgres: list message uids %s/%s: %w", user, mailbox, err)
	}
	defer rows.Close()
	uids := []uint32{}
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("postgres: scan uid: %w", err)
		}
		uids = append(uids, uint32(uid))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list message uids %s/%s: %w", user, mailbox, err)
	}
	return uids, nil
}

// UpdateMessageMetadataFunc applies fn to the message under a row lock and, for
// a pre-existing message, bumps the mailbox mod-sequence into the result (RFC
// 7162) — the atomic read-modify-write the bbolt store did in one transaction.
func (d *DB) UpdateMessageMetadataFunc(user, mailbox string, uid uint32, fn func(*storage.MessageMetadata) error) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin update-func: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if err := ensureMailboxTx(ctx, tx, user, mailbox); err != nil {
		return err
	}

	meta, scanErr := scanMessage(tx.QueryRow(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE user_email=$1 AND mailbox=$2 AND uid=$3 FOR UPDATE`,
		user, mailbox, int64(uid)))
	preexisting := scanErr == nil
	if scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows) {
		return fmt.Errorf("postgres: lock message %s/%s/%d: %w", user, mailbox, uid, scanErr)
	}
	if meta == nil {
		meta = &storage.MessageMetadata{}
	}
	meta.UID = uid

	if err := fn(meta); err != nil {
		return err
	}

	if preexisting {
		var modSeq int64
		if err := tx.QueryRow(ctx,
			`UPDATE mailboxes SET highest_modseq = highest_modseq + 1
			 WHERE user_email=$1 AND name=$2 RETURNING highest_modseq`,
			user, mailbox,
		).Scan(&modSeq); err != nil {
			return fmt.Errorf("postgres: bump modseq %s/%s: %w", user, mailbox, err)
		}
		meta.ModSeq = uint64(modSeq)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (user_email, mailbox, uid, message_id, flags, labels,
			internal_date, size, mod_seq, subject, date_hdr, from_addr, to_addr,
			in_reply_to, refs, thread_id, is_thread_root)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (user_email, mailbox, uid) DO UPDATE SET
			message_id=EXCLUDED.message_id, flags=EXCLUDED.flags, labels=EXCLUDED.labels,
			internal_date=EXCLUDED.internal_date, size=EXCLUDED.size, mod_seq=EXCLUDED.mod_seq,
			subject=EXCLUDED.subject, date_hdr=EXCLUDED.date_hdr, from_addr=EXCLUDED.from_addr,
			to_addr=EXCLUDED.to_addr, in_reply_to=EXCLUDED.in_reply_to, refs=EXCLUDED.refs,
			thread_id=EXCLUDED.thread_id, is_thread_root=EXCLUDED.is_thread_root`,
		user, mailbox, int64(uid), meta.MessageID, flagsArg(meta.Flags), flagsArg(meta.Labels),
		meta.InternalDate, meta.Size, int64(meta.ModSeq), meta.Subject, meta.Date, meta.From, meta.To,
		meta.InReplyTo, flagsArg(meta.References), meta.ThreadID, meta.IsThreadRoot,
	); err != nil {
		return fmt.Errorf("postgres: write message %s/%s/%d: %w", user, mailbox, uid, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit update-func: %w", err)
	}
	return nil
}

// GetMailboxCounts returns EXISTS/RECENT/UNSEEN over the mailbox's messages.
func (d *DB) GetMailboxCounts(user, mailbox string) (exists, recent, unseen int, err error) {
	ctx := context.Background()
	err = d.pool.QueryRow(ctx, `
		SELECT count(*),
			count(*) FILTER (WHERE '\Recent' = ANY(flags)),
			count(*) FILTER (WHERE NOT ('\Seen' = ANY(flags)))
		FROM messages WHERE user_email=$1 AND mailbox=$2`,
		user, mailbox,
	).Scan(&exists, &recent, &unseen)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("postgres: mailbox counts %s/%s: %w", user, mailbox, err)
	}
	return exists, recent, unseen, nil
}

// ClearRecent removes the \Recent flag from every message in the mailbox.
func (d *DB) ClearRecent(user, mailbox string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx,
		`UPDATE messages SET flags = array_remove(flags, '\Recent')
		 WHERE user_email=$1 AND mailbox=$2 AND '\Recent' = ANY(flags)`,
		user, mailbox,
	); err != nil {
		return fmt.Errorf("postgres: clear recent %s/%s: %w", user, mailbox, err)
	}
	return nil
}

// ReconcileUIDNext raises uid_next to max(uid)+1 when it has fallen behind the
// stored messages (e.g. after a restore), matching the bbolt store.
func (d *DB) ReconcileUIDNext(user, mailbox string) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `
		UPDATE mailboxes SET uid_next = GREATEST(uid_next,
			coalesce((SELECT max(uid) FROM messages WHERE user_email=$1 AND mailbox=$2), 0) + 1)
		WHERE user_email=$1 AND name=$2`,
		user, mailbox,
	); err != nil {
		return fmt.Errorf("postgres: reconcile uidnext %s/%s: %w", user, mailbox, err)
	}
	return nil
}

// ensureMailboxTx creates the mailbox row if absent (so a message insert's
// foreign key holds), mirroring the bbolt auto-create of the messages bucket.
func ensureMailboxTx(ctx context.Context, tx pgx.Tx, user, mailbox string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO mailboxes (user_email, name, uid_next) VALUES ($1,$2,1)
		ON CONFLICT (user_email, name) DO NOTHING`, user, mailbox); err != nil {
		return fmt.Errorf("postgres: ensure mailbox %s/%s: %w", user, mailbox, err)
	}
	return nil
}

func scanMessage(row rowScanner) (*storage.MessageMetadata, error) {
	var m storage.MessageMetadata
	var modSeq int64
	if err := row.Scan(&m.MessageID, &m.Flags, &m.Labels, &m.InternalDate, &m.Size, &modSeq,
		&m.Subject, &m.Date, &m.From, &m.To, &m.InReplyTo, &m.References, &m.ThreadID, &m.IsThreadRoot); err != nil {
		return nil, err
	}
	m.ModSeq = uint64(modSeq)
	return &m, nil
}

// flagsArg normalizes a nil slice to an empty (non-null) array for the TEXT[]
// columns, which are NOT NULL.
func flagsArg(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
