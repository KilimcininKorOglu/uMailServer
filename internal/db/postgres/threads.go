package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/mailthread"
	"github.com/umailserver/umailserver/internal/storage"
)

// Conversation threading. Thread IDs are computed with the SAME shared helpers
// the bbolt store uses (mailthread.Root + storage.DeterministicThreadID /
// GenerateThreadID / NormalizeSubject), so a message threads to the same id on
// either backend.

// GetOrCreateThreadID returns the conversation id: deterministic from the
// threading headers when present, else the existing subject-grouped thread
// (within 30 days), else a fresh id — mirroring the bbolt store.
func (d *DB) GetOrCreateThreadID(user, mailbox, subject, ownMessageID, inReplyTo string, references []string) (string, error) {
	if root, _ := mailthread.Root(ownMessageID, inReplyTo, references); root != "" {
		return storage.DeterministicThreadID(root), nil
	}
	if threadID, err := d.findThreadBySubject(user, mailbox, storage.NormalizeSubject(subject)); err == nil && threadID != "" {
		return threadID, nil
	}
	return storage.GenerateThreadID(subject), nil
}

// findThreadBySubject returns the thread id of the oldest message in the mailbox
// whose normalized subject matches, when that thread is newer than 30 days
// (matching the bbolt store's recency gate). Normalization runs in Go so it is
// identical to the bbolt path.
func (d *DB) findThreadBySubject(user, mailbox, normalizedSubject string) (string, error) {
	if normalizedSubject == "" {
		return "", nil
	}
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT subject, internal_date, thread_id FROM messages WHERE user_email=$1 AND mailbox=$2`,
		user, mailbox)
	if err != nil {
		return "", fmt.Errorf("postgres: find thread by subject %s/%s: %w", user, mailbox, err)
	}
	defer rows.Close()

	threadID := ""
	oldest := time.Now()
	for rows.Next() {
		var subj, tid string
		var internalDate time.Time
		if err := rows.Scan(&subj, &internalDate, &tid); err != nil {
			return "", fmt.Errorf("postgres: scan thread candidate: %w", err)
		}
		if storage.NormalizeSubject(subj) == normalizedSubject && internalDate.Before(oldest) {
			oldest = internalDate
			threadID = tid
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("postgres: find thread by subject %s/%s: %w", user, mailbox, err)
	}
	if threadID != "" && time.Since(oldest) > 30*24*time.Hour {
		return "", nil
	}
	return threadID, nil
}

// GetThread returns the thread record, erroring when absent (bbolt parity).
func (d *DB) GetThread(user, threadID string) (*storage.Thread, error) {
	ctx := context.Background()
	var th storage.Thread
	var lastActivity *time.Time
	err := d.pool.QueryRow(ctx, `
		SELECT thread_id, subject, participants, message_count, unread_count,
			last_activity, created_at
		FROM threads WHERE user_email=$1 AND thread_id=$2`,
		user, threadID,
	).Scan(&th.ThreadID, &th.Subject, &th.Participants, &th.MessageCount, &th.UnreadCount,
		&lastActivity, &th.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("thread not found")
		}
		return nil, fmt.Errorf("postgres: get thread %s/%s: %w", user, threadID, err)
	}
	if lastActivity != nil {
		th.LastActivity = *lastActivity
	}
	return &th, nil
}

// GetThreadMessages returns the messages of a thread within one mailbox, built
// from message metadata (IsRead from the \Seen flag).
func (d *DB) GetThreadMessages(user, mailbox, threadID string) ([]*storage.ThreadMessage, error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `
		SELECT message_id, uid, from_addr, to_addr, subject, internal_date, flags, in_reply_to
		FROM messages WHERE user_email=$1 AND mailbox=$2 AND thread_id=$3 ORDER BY uid`,
		user, mailbox, threadID)
	if err != nil {
		return nil, fmt.Errorf("postgres: thread messages %s/%s/%s: %w", user, mailbox, threadID, err)
	}
	defer rows.Close()
	messages := []*storage.ThreadMessage{}
	for rows.Next() {
		var tm storage.ThreadMessage
		var uid int64
		var flags []string
		if err := rows.Scan(&tm.MessageID, &uid, &tm.From, &tm.To, &tm.Subject, &tm.Date, &flags, &tm.InReplyTo); err != nil {
			return nil, fmt.Errorf("postgres: scan thread message: %w", err)
		}
		tm.UID = uint32(uid)
		tm.Mailbox = mailbox
		tm.Flags = flags
		tm.IsRead = storage.HasFlag(flags, "\\Seen")
		messages = append(messages, &tm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: thread messages %s/%s/%s: %w", user, mailbox, threadID, err)
	}
	return messages, nil
}

// UpdateThread upserts a thread record.
func (d *DB) UpdateThread(user string, thread *storage.Thread) error {
	ctx := context.Background()
	participants := thread.Participants
	if participants == nil {
		participants = []string{}
	}
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO threads (user_email, thread_id, subject, participants,
			message_count, unread_count, last_activity, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (user_email, thread_id) DO UPDATE SET subject=EXCLUDED.subject,
			participants=EXCLUDED.participants, message_count=EXCLUDED.message_count,
			unread_count=EXCLUDED.unread_count, last_activity=EXCLUDED.last_activity`,
		user, thread.ThreadID, thread.Subject, participants,
		thread.MessageCount, thread.UnreadCount, nullTime(thread.LastActivity), thread.CreatedAt,
	); err != nil {
		return fmt.Errorf("postgres: update thread %s/%s: %w", user, thread.ThreadID, err)
	}
	return nil
}
