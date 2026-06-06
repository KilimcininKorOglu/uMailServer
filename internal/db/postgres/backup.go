package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umailserver/umailserver/internal/storage"
)

// Backup jobs and manifests for the admin backup API. The bbolt store kept each
// record as a JSON blob keyed by ID; here they are typed rows. A missing record
// returns the same "not found" error text the bbolt store used, since callers
// only test for a non-nil error.

// CreateBackupJob stores a backup job.
func (d *DB) CreateBackupJob(job *storage.BackupJob) error {
	return d.upsertBackupJob(job)
}

// UpdateBackupJob updates an existing backup job (upsert, matching bbolt).
func (d *DB) UpdateBackupJob(job *storage.BackupJob) error {
	return d.upsertBackupJob(job)
}

func (d *DB) upsertBackupJob(job *storage.BackupJob) error {
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO backup_jobs (id, name, type, target, schedule, retention, enabled,
			last_run, next_run, destinations, options, status, last_error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, type=EXCLUDED.type,
			target=EXCLUDED.target, schedule=EXCLUDED.schedule, retention=EXCLUDED.retention,
			enabled=EXCLUDED.enabled, last_run=EXCLUDED.last_run, next_run=EXCLUDED.next_run,
			destinations=EXCLUDED.destinations, options=EXCLUDED.options, status=EXCLUDED.status,
			last_error=EXCLUDED.last_error`,
		job.ID, job.Name, job.Type, job.Target, job.Schedule, job.Retention, job.Enabled,
		job.LastRun, job.NextRun, job.Destinations, job.Options, job.Status, job.LastError,
	); err != nil {
		return fmt.Errorf("postgres: upsert backup job %s: %w", job.ID, err)
	}
	return nil
}

// GetBackupJob retrieves a backup job by ID.
func (d *DB) GetBackupJob(id string) (*storage.BackupJob, error) {
	var job storage.BackupJob
	err := d.pool.QueryRow(context.Background(), `
		SELECT id, name, type, target, schedule, retention, enabled,
			last_run, next_run, destinations, options, status, last_error
		FROM backup_jobs WHERE id=$1`, id,
	).Scan(&job.ID, &job.Name, &job.Type, &job.Target, &job.Schedule, &job.Retention,
		&job.Enabled, &job.LastRun, &job.NextRun, &job.Destinations, &job.Options,
		&job.Status, &job.LastError)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("backup job not found")
		}
		return nil, fmt.Errorf("postgres: get backup job %s: %w", id, err)
	}
	return &job, nil
}

// DeleteBackupJob deletes a backup job (no error when absent, matching bbolt).
func (d *DB) DeleteBackupJob(id string) error {
	if _, err := d.pool.Exec(context.Background(), `DELETE FROM backup_jobs WHERE id=$1`, id); err != nil {
		return fmt.Errorf("postgres: delete backup job %s: %w", id, err)
	}
	return nil
}

// ListBackupJobs returns all backup jobs, optionally filtered to enabled ones.
func (d *DB) ListBackupJobs(enabledOnly bool) ([]storage.BackupJob, error) {
	ctx := context.Background()
	query := `SELECT id, name, type, target, schedule, retention, enabled,
			last_run, next_run, destinations, options, status, last_error
		FROM backup_jobs`
	if enabledOnly {
		query += ` WHERE enabled=true`
	}
	query += ` ORDER BY id`
	rows, err := d.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("postgres: list backup jobs: %w", err)
	}
	defer rows.Close()
	var jobs []storage.BackupJob
	for rows.Next() {
		var job storage.BackupJob
		if err := rows.Scan(&job.ID, &job.Name, &job.Type, &job.Target, &job.Schedule,
			&job.Retention, &job.Enabled, &job.LastRun, &job.NextRun, &job.Destinations,
			&job.Options, &job.Status, &job.LastError); err != nil {
			return nil, fmt.Errorf("postgres: scan backup job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list backup jobs: %w", err)
	}
	return jobs, nil
}

// CreateBackupManifest stores a backup manifest.
func (d *DB) CreateBackupManifest(manifest *storage.BackupManifest) error {
	if _, err := d.pool.Exec(context.Background(), `
		INSERT INTO backup_manifests (id, filename, size, created_at, type, target,
			checksum, encrypted, retention_until, destination, path, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET filename=EXCLUDED.filename, size=EXCLUDED.size,
			created_at=EXCLUDED.created_at, type=EXCLUDED.type, target=EXCLUDED.target,
			checksum=EXCLUDED.checksum, encrypted=EXCLUDED.encrypted,
			retention_until=EXCLUDED.retention_until, destination=EXCLUDED.destination,
			path=EXCLUDED.path, metadata=EXCLUDED.metadata`,
		manifest.ID, manifest.Filename, manifest.Size, manifest.CreatedAt, manifest.Type,
		manifest.Target, manifest.Checksum, manifest.Encrypted, nullTime(manifest.RetentionUntil),
		manifest.Destination, manifest.Path, manifest.Metadata,
	); err != nil {
		return fmt.Errorf("postgres: create backup manifest %s: %w", manifest.ID, err)
	}
	return nil
}

// GetBackupManifest retrieves a backup manifest by ID.
func (d *DB) GetBackupManifest(id string) (*storage.BackupManifest, error) {
	var m storage.BackupManifest
	var retentionUntil *time.Time
	err := d.pool.QueryRow(context.Background(), `
		SELECT id, filename, size, created_at, type, target, checksum, encrypted,
			retention_until, destination, path, metadata
		FROM backup_manifests WHERE id=$1`, id,
	).Scan(&m.ID, &m.Filename, &m.Size, &m.CreatedAt, &m.Type, &m.Target, &m.Checksum,
		&m.Encrypted, &retentionUntil, &m.Destination, &m.Path, &m.Metadata)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("backup manifest not found")
		}
		return nil, fmt.Errorf("postgres: get backup manifest %s: %w", id, err)
	}
	if retentionUntil != nil {
		m.RetentionUntil = *retentionUntil
	}
	return &m, nil
}

// DeleteBackupManifest deletes a backup manifest (no error when absent).
func (d *DB) DeleteBackupManifest(id string) error {
	if _, err := d.pool.Exec(context.Background(), `DELETE FROM backup_manifests WHERE id=$1`, id); err != nil {
		return fmt.Errorf("postgres: delete backup manifest %s: %w", id, err)
	}
	return nil
}

// ListBackupManifests returns all manifests, optionally filtered by target.
func (d *DB) ListBackupManifests(target string) ([]storage.BackupManifest, error) {
	ctx := context.Background()
	query := `SELECT id, filename, size, created_at, type, target, checksum, encrypted,
			retention_until, destination, path, metadata FROM backup_manifests`
	args := []any{}
	if target != "" {
		query += ` WHERE target=$1`
		args = append(args, target)
	}
	query += ` ORDER BY created_at DESC, id`
	rows, err := d.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list backup manifests: %w", err)
	}
	defer rows.Close()
	var manifests []storage.BackupManifest
	for rows.Next() {
		var m storage.BackupManifest
		var retentionUntil *time.Time
		if err := rows.Scan(&m.ID, &m.Filename, &m.Size, &m.CreatedAt, &m.Type, &m.Target,
			&m.Checksum, &m.Encrypted, &retentionUntil, &m.Destination, &m.Path, &m.Metadata); err != nil {
			return nil, fmt.Errorf("postgres: scan backup manifest: %w", err)
		}
		if retentionUntil != nil {
			m.RetentionUntil = *retentionUntil
		}
		manifests = append(manifests, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list backup manifests: %w", err)
	}
	return manifests, nil
}
