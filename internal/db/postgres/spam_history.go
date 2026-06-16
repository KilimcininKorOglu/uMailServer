package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// LogSpamEvent records a spam check event.
func (d *DB) LogSpamEvent(entry *db.SpamHistoryEntry) error {
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	entry.Timestamp = time.Now()
	reasonsJSON, err := json.Marshal(entry.Reasons)
	if err != nil {
		return fmt.Errorf("failed to marshal reasons: %w", err)
	}
	_, err = d.pool.Exec(context.Background(), `
		INSERT INTO spam_history
			(id, mail_from, rcpt_to, from_header, subject, score, verdict,
			 reasons, client_ip, helo, message_id, size, timestamp)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		entry.ID, entry.MailFrom, entry.RcptTo, entry.FromHeader,
		entry.Subject, entry.Score, entry.Verdict,
		reasonsJSON, entry.ClientIP, entry.Helo,
		entry.MessageID, entry.Size, entry.Timestamp,
	)
	return err
}

// ListSpamHistory returns spam events matching the given filters, and the total
// count before pagination.
func (d *DB) ListSpamHistory(opts db.SpamHistoryListOptions) ([]*db.SpamHistoryEntry, int, error) {
	if opts.Limit <= 0 {
		opts.Limit = 100
	}
	var conditions []string
	var args []interface{}
	argIdx := 1

	if opts.Domain != "" {
		conditions = append(conditions, fmt.Sprintf("rcpt_to LIKE $%d", argIdx))
		args = append(args, "%@"+opts.Domain)
		argIdx++
	}
	if opts.Verdict != "" {
		conditions = append(conditions, fmt.Sprintf("verdict = $%d", argIdx))
		args = append(args, opts.Verdict)
		argIdx++
	}
	if !opts.Start.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", argIdx))
		args = append(args, opts.Start)
		argIdx++
	}
	if !opts.End.IsZero() {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argIdx))
		args = append(args, opts.End)
		argIdx++
	}
	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Count total
	var total int
	countQuery := "SELECT COUNT(*) FROM spam_history " + where
	if err := d.pool.QueryRow(context.Background(), countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count spam history: %w", err)
	}

	// Fetch page
	query := fmt.Sprintf(`
		SELECT id, mail_from, rcpt_to, from_header, subject, score, verdict,
			   reasons, client_ip, helo, message_id, size, timestamp
		FROM spam_history %s
		ORDER BY timestamp DESC
		LIMIT $%d OFFSET $%d`,
		where, argIdx, argIdx+1)
	args = append(args, opts.Limit, opts.Offset)

	rows, err := d.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list spam history: %w", err)
	}
	defer rows.Close()

	var entries []*db.SpamHistoryEntry
	for rows.Next() {
		var e db.SpamHistoryEntry
		var reasonsJSON []byte
		if err := rows.Scan(
			&e.ID, &e.MailFrom, &e.RcptTo, &e.FromHeader,
			&e.Subject, &e.Score, &e.Verdict,
			&reasonsJSON, &e.ClientIP, &e.Helo,
			&e.MessageID, &e.Size, &e.Timestamp,
		); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(reasonsJSON, &e.Reasons); err != nil {
			e.Reasons = nil
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}
