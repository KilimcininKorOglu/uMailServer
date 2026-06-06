package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Semantic-core job store (migration/backfill/rollback scheduler). Mirrors
// *semcore.BoltJobStore. Scalar fields are typed columns; the variant step list
// is a JSONB payload. NewJobStore returns a handle satisfying semcore.JobStore.

// pgJobStore is the relational semcore.JobStore implementation.
type pgJobStore struct{ pool *pgxpool.Pool }

// NewJobStore returns a relational job store sharing this DB's pool, satisfying
// api.SemanticStore's NewJobStore accessor and semcore.JobStore.
func (d *DB) NewJobStore() (semcore.JobStore, error) {
	return &pgJobStore{pool: d.pool}, nil
}

// Put upserts a job.
func (s *pgJobStore) Put(job semcore.Job) error {
	steps, err := json.Marshal(job.Steps)
	if err != nil {
		return fmt.Errorf("postgres: marshal job steps: %w", err)
	}
	if _, err := s.pool.Exec(context.Background(), `
		INSERT INTO semcore_job (id, kind, target, mailbox_id, state, priority, steps, cursor,
			errors, last_error, created_at, started_at, checkpoint_at, completed_at, actor)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET kind=EXCLUDED.kind, target=EXCLUDED.target,
			mailbox_id=EXCLUDED.mailbox_id, state=EXCLUDED.state, priority=EXCLUDED.priority,
			steps=EXCLUDED.steps, cursor=EXCLUDED.cursor, errors=EXCLUDED.errors,
			last_error=EXCLUDED.last_error, created_at=EXCLUDED.created_at, started_at=EXCLUDED.started_at,
			checkpoint_at=EXCLUDED.checkpoint_at, completed_at=EXCLUDED.completed_at, actor=EXCLUDED.actor`,
		job.ID, string(job.Kind), job.Target, job.MailboxID.String(), string(job.State), job.Priority,
		steps, job.Cursor, job.Errors, job.LastError, nullTime(job.CreatedAt), nullTime(job.StartedAt),
		nullTime(job.CheckpointAt), nullTime(job.CompletedAt), job.Actor,
	); err != nil {
		return fmt.Errorf("postgres: put job %s: %w", job.ID, err)
	}
	return nil
}

// Get returns a job by id, or semcore.ErrJobNotFound when absent.
func (s *pgJobStore) Get(id string) (semcore.Job, error) {
	job, err := scanJob(s.pool.QueryRow(context.Background(),
		`SELECT id, kind, target, mailbox_id, state, priority, steps, cursor, errors, last_error,
			created_at, started_at, checkpoint_at, completed_at, actor
		 FROM semcore_job WHERE id=$1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return semcore.Job{}, semcore.ErrJobNotFound
		}
		return semcore.Job{}, fmt.Errorf("postgres: get job %s: %w", id, err)
	}
	return job, nil
}

// List returns jobs filtered by kind and/or state (empty = no filter).
func (s *pgJobStore) List(kind semcore.JobKind, state semcore.JobState) ([]semcore.Job, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, kind, target, mailbox_id, state, priority, steps, cursor, errors, last_error,
			created_at, started_at, checkpoint_at, completed_at, actor
		FROM semcore_job
		WHERE ($1='' OR kind=$1) AND ($2='' OR state=$2)`,
		string(kind), string(state))
	if err != nil {
		return nil, fmt.Errorf("postgres: list jobs: %w", err)
	}
	defer rows.Close()
	var jobs []semcore.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list jobs: %w", err)
	}
	return jobs, nil
}

// Delete removes a job, returning semcore.ErrJobNotFound when absent.
func (s *pgJobStore) Delete(id string) error {
	tag, err := s.pool.Exec(context.Background(), `DELETE FROM semcore_job WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete job %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return semcore.ErrJobNotFound
	}
	return nil
}

func scanJob(row rowScanner) (semcore.Job, error) {
	var id, kind, target, mailboxID, state, cursor, lastError, actor string
	var priority, errCount int
	var steps []byte
	var createdAt, startedAt, checkpointAt, completedAt *time.Time
	if err := row.Scan(&id, &kind, &target, &mailboxID, &state, &priority, &steps, &cursor,
		&errCount, &lastError, &createdAt, &startedAt, &checkpointAt, &completedAt, &actor); err != nil {
		return semcore.Job{}, err
	}
	job := semcore.Job{
		ID: id, Kind: semcore.JobKind(kind), Target: target, MailboxID: parseMailboxID(mailboxID),
		State: semcore.JobState(state), Priority: priority, Cursor: cursor, Errors: errCount,
		LastError: lastError, Actor: actor,
	}
	if len(steps) > 0 {
		if err := json.Unmarshal(steps, &job.Steps); err != nil {
			return semcore.Job{}, fmt.Errorf("postgres: unmarshal job steps: %w", err)
		}
	}
	if createdAt != nil {
		job.CreatedAt = *createdAt
	}
	if startedAt != nil {
		job.StartedAt = *startedAt
	}
	if checkpointAt != nil {
		job.CheckpointAt = *checkpointAt
	}
	if completedAt != nil {
		job.CompletedAt = *completedAt
	}
	return job, nil
}
