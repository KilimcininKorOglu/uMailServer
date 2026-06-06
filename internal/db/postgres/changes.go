package postgres

import (
	"context"
	"fmt"
	"strconv"

	"github.com/umailserver/umailserver/internal/storage"
)

// Change journal (JMAP incremental sync). Mirrors the bbolt journal: an
// append-only per-user log keyed by a monotonic seq, queried by type since a
// state token. The seq is a global BIGSERIAL here — tokens are opaque and each
// user's entries still get strictly increasing seq, so GetChangesSince is
// monotonic per user.

// RecordChange appends a change entry for the user. Like the bbolt store this is
// best-effort at the call site (a journal failure must not roll back the
// mutation), so callers log and continue.
func (d *DB) RecordChange(user string, ct storage.ChangeType, ck storage.ChangeKind, id, mailbox string) error {
	if user == "" {
		return nil
	}
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx,
		`INSERT INTO changes (user_email, type, kind, id, mailbox)
		 VALUES ($1,$2,$3,$4,$5)`,
		user, string(ct), string(ck), id, mailbox,
	); err != nil {
		return fmt.Errorf("postgres: record change %s/%s: %w", user, ct, err)
	}
	return nil
}

// CurrentChangeState returns the user's latest change sequence as an opaque
// JMAP state token ("0" when there are no changes).
func (d *DB) CurrentChangeState(user string) (string, error) {
	ctx := context.Background()
	var seq int64
	if err := d.pool.QueryRow(ctx,
		`SELECT coalesce(max(seq), 0) FROM changes WHERE user_email=$1`, user,
	).Scan(&seq); err != nil {
		return "0", fmt.Errorf("postgres: current change state %q: %w", user, err)
	}
	return strconv.FormatInt(seq, 10), nil
}

// GetChangesSince returns up to max change entries of the given type with
// seq > sinceSeq, in seq order, plus whether more remain and the last seq seen.
func (d *DB) GetChangesSince(user string, ct storage.ChangeType, sinceSeq uint64, max int) (entries []storage.ChangeEntry, hasMore bool, lastSeq uint64, err error) {
	if max <= 0 {
		max = 256
	}
	lastSeq = sinceSeq
	ctx := context.Background()
	// Fetch one extra row to detect hasMore without a second query.
	rows, qerr := d.pool.Query(ctx, `
		SELECT seq, type, kind, id, mailbox, at
		FROM changes
		WHERE user_email=$1 AND type=$2 AND seq > $3
		ORDER BY seq
		LIMIT $4`,
		user, string(ct), int64(sinceSeq), max+1,
	)
	if qerr != nil {
		return nil, false, sinceSeq, fmt.Errorf("postgres: get changes %q: %w", user, qerr)
	}
	defer rows.Close()
	for rows.Next() {
		var e storage.ChangeEntry
		var seq int64
		var typ, kind string
		if err := rows.Scan(&seq, &typ, &kind, &e.ID, &e.Mailbox, &e.At); err != nil {
			return nil, false, sinceSeq, fmt.Errorf("postgres: scan change %q: %w", user, err)
		}
		if len(entries) >= max {
			hasMore = true
			break
		}
		e.Seq = uint64(seq)
		e.Type = storage.ChangeType(typ)
		e.Kind = storage.ChangeKind(kind)
		entries = append(entries, e)
		lastSeq = e.Seq
	}
	if err := rows.Err(); err != nil {
		return nil, false, sinceSeq, fmt.Errorf("postgres: get changes %q: %w", user, err)
	}
	return entries, hasMore, lastSeq, nil
}
