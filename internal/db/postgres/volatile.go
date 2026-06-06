package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
)

// Auxiliary volatile stores co-located with the metadata backend: the Bayesian
// spam token counts (spam.Store) and the per-user daily-send quota counters
// (ratelimit.QuotaStore). On bbolt these live in the shared mail.db; here they
// are plain relational tables created by Migrate, so Initialize is a no-op.

// Initialize satisfies both spam.Store and ratelimit.QuotaStore. The backing
// tables (spam_tokens, spam_stats, ratelimit_quota) are created by Migrate, so
// there is nothing to do here.
func (d *DB) Initialize() error { return nil }

// --- spam.Store ---

// IncrementToken adds delta to a token's count in the given class bucket
// ('spam_tokens' or 'ham_tokens'), matching the bbolt store's bucket+token key.
func (d *DB) IncrementToken(bucketName, token string, delta uint32) error {
	ctx := context.Background()
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO spam_tokens (class, token, count) VALUES ($1,$2,$3)
		ON CONFLICT (class, token) DO UPDATE SET count = spam_tokens.count + EXCLUDED.count`,
		bucketName, token, int64(delta),
	); err != nil {
		return fmt.Errorf("postgres: increment spam token %s/%s: %w", bucketName, token, err)
	}
	return nil
}

// GetTotalCounts returns the corpus ham/spam totals. The persisted spam_stats
// row is authoritative when present (the classifier writes it via SetTotals);
// otherwise the live token sums are returned, mirroring the bbolt store.
func (d *DB) GetTotalCounts() (totalHam, totalSpam uint64, err error) {
	ctx := context.Background()
	// Base: live token sums per class.
	rows, err := d.pool.Query(ctx,
		`SELECT class, COALESCE(SUM(count),0) FROM spam_tokens GROUP BY class`)
	if err != nil {
		return 0, 0, fmt.Errorf("postgres: sum spam tokens: %w", err)
	}
	for rows.Next() {
		var class string
		var sum int64
		if err := rows.Scan(&class, &sum); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("postgres: scan spam token sum: %w", err)
		}
		switch class {
		case "ham_tokens":
			totalHam = uint64(sum)
		case "spam_tokens":
			totalSpam = uint64(sum)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("postgres: sum spam tokens: %w", err)
	}
	// Override with the persisted totals when the stats row exists.
	var statHam, statSpam int64
	statErr := d.pool.QueryRow(ctx,
		`SELECT total_ham, total_spam FROM spam_stats WHERE id=1`).Scan(&statHam, &statSpam)
	if statErr == nil {
		totalHam = uint64(statHam)
		totalSpam = uint64(statSpam)
	} else if !errors.Is(statErr, pgx.ErrNoRows) {
		return 0, 0, fmt.Errorf("postgres: read spam stats: %w", statErr)
	}
	return totalHam, totalSpam, nil
}

// SetTotals persists the corpus ham/spam totals into the singleton stats row.
func (d *DB) SetTotals(totalHam, totalSpam uint64) error {
	ctx := context.Background()
	clamp := func(v uint64) int64 {
		if v > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(v)
	}
	if _, err := d.pool.Exec(ctx, `
		INSERT INTO spam_stats (id, total_ham, total_spam) VALUES (1,$1,$2)
		ON CONFLICT (id) DO UPDATE SET total_ham=EXCLUDED.total_ham, total_spam=EXCLUDED.total_spam`,
		clamp(totalHam), clamp(totalSpam),
	); err != nil {
		return fmt.Errorf("postgres: set spam totals: %w", err)
	}
	return nil
}

// GetTokenFrequency returns the ham and spam counts for a single token.
func (d *DB) GetTokenFrequency(token string) (hamCount, spamCount uint32, err error) {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx,
		`SELECT class, count FROM spam_tokens WHERE token=$1 AND class IN ('ham_tokens','spam_tokens')`, token)
	if err != nil {
		return 0, 0, fmt.Errorf("postgres: token frequency %q: %w", token, err)
	}
	defer rows.Close()
	clamp := func(v int64) uint32 {
		if v < 0 {
			return 0
		}
		if v > math.MaxUint32 {
			return math.MaxUint32
		}
		return uint32(v)
	}
	for rows.Next() {
		var class string
		var count int64
		if err := rows.Scan(&class, &count); err != nil {
			return 0, 0, fmt.Errorf("postgres: scan token frequency: %w", err)
		}
		switch class {
		case "ham_tokens":
			hamCount = clamp(count)
		case "spam_tokens":
			spamCount = clamp(count)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("postgres: token frequency %q: %w", token, err)
	}
	return hamCount, spamCount, nil
}

// --- ratelimit.QuotaStore ---

// GetUserSentToday returns the persisted daily-sent count for a user, or 0 when
// absent or on a read error (persistence is best-effort, matching bbolt).
func (d *DB) GetUserSentToday(user string) int64 {
	var count int64
	if err := d.pool.QueryRow(context.Background(),
		`SELECT sent_today FROM ratelimit_quota WHERE user_email=$1`, user,
	).Scan(&count); err != nil {
		return 0
	}
	return count
}

// SetUserSentToday persists the daily-sent count for a user. Best-effort: a
// write error is swallowed so rate limiting keeps working in memory.
func (d *DB) SetUserSentToday(user string, count int64) {
	if count < 0 {
		count = 0
	}
	// Best-effort persistence: a write error is swallowed so rate limiting keeps
	// working in memory (matches the bbolt store).
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO ratelimit_quota (user_email, sent_today) VALUES ($1,$2)
		ON CONFLICT (user_email) DO UPDATE SET sent_today=EXCLUDED.sent_today`,
		user, count); err != nil {
		return
	}
}
