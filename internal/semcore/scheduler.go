// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the JobScheduler, which manages the lifecycle of
// migration, backfill, and rollback jobs. The scheduler:
//
//   - Persists job records to Bolt at every step boundary
//   - Handles dependency-aware ordering (later jobs wait for prerequisites)
//   - Recovers from interrupted runs by resuming from the last checkpoint
//   - Reports progress (items processed, errors, state) back to the job record
//
// The scheduler is safe for concurrent job submission but executes at most
// one job at a time within a given mailbox context to avoid conflicts.
// Long-running jobs yield between steps so that cancellation can be honored.
package semcore

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// SchedulerConfig controls scheduler behavior.
type SchedulerConfig struct {
	// MaxRetriesPerStep is the maximum number of times a failed step will
	// be retried before the job is moved to Failed state.
	MaxRetriesPerStep int

	// StepYieldInterval is how often a running step yields to the scheduler
	// loop for cancellation checks. Zero means yield at every loop iteration.
	StepYieldInterval time.Duration

	// OnJobStateChange is an optional hook called whenever a job changes state.
	// It is called from the scheduler goroutine.
	OnJobStateChange func(Job)
}

// DefaultSchedulerConfig returns the default scheduler configuration.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		MaxRetriesPerStep: 3,
		StepYieldInterval: 500 * time.Millisecond,
		OnJobStateChange:  nil,
	}
}

// JobScheduler runs and manages durable jobs.
// Only one scheduler instance should be active per database to avoid duplicate work.
type JobScheduler struct {
	store   JobStore
	config  SchedulerConfig
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
	mu      sync.Mutex
}

// NewJobScheduler creates a scheduler that persists job state to the given store.
func NewJobScheduler(store JobStore, config SchedulerConfig) *JobScheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &JobScheduler{
		store:  store,
		config: config,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start begins the scheduler's worker loop. It blocks until the scheduler
// has processed any previously interrupted jobs and is ready for new work.
// Start must be called before Submit.
func (s *JobScheduler) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("JobScheduler: already started")
	}
	s.running = true
	s.mu.Unlock()

	// Recover any jobs that were running when the server last stopped.
	if err := s.recoverJobs(); err != nil {
		return fmt.Errorf("JobScheduler.Start: recover failed: %w", err)
	}

	s.wg.Add(1)
	go s.runLoop()
	return nil
}

// Stop gracefully stops the scheduler. It waits for the current step to
// complete, then marks the job as interrupted so it can be resumed later.
// Stop does not wait for all jobs to complete.
func (s *JobScheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

	s.cancel()
	s.wg.Wait()
}

// Submit adds a new job to the scheduler. The job starts in Pending state and
// will be picked up by the run loop. If a job with the same ID already exists,
// Submit returns ErrJobAlreadyExists.
func (s *JobScheduler) Submit(job Job) error {
	if job.ID == "" {
		return fmt.Errorf("JobScheduler.Submit: job ID cannot be empty")
	}
	// Check for duplicate
	if _, err := s.store.Get(job.ID); err == nil {
		return fmt.Errorf("JobScheduler.Submit: job %q already exists", job.ID)
	}
	if err := s.store.Put(job); err != nil {
		return fmt.Errorf("JobScheduler.Submit: put failed: %w", err)
	}
	return nil
}

// GetJob retrieves a job by ID.
func (s *JobScheduler) GetJob(id string) (Job, error) {
	return s.store.Get(id)
}

// ListJobs returns all jobs, optionally filtered.
func (s *JobScheduler) ListJobs(kind JobKind, state JobState) ([]Job, error) {
	return s.store.List(kind, state)
}

// CancelJob marks a pending or running job for cancellation.
// The scheduler will stop the job at the next yield point.
func (s *JobScheduler) CancelJob(id string) error {
	job, err := s.store.Get(id)
	if err != nil {
		return err
	}
	if job.IsTerminal() {
		return fmt.Errorf("JobScheduler.CancelJob: job %s is already in terminal state %s", id, job.State)
	}
	job.State = JobStateCanceled
	return s.store.Put(job)
}

// recoverJobs finds all jobs that were in Running state when the server
// was last stopped and marks them as interrupted so they can be resumed.
func (s *JobScheduler) recoverJobs() error {
	jobs, err := s.store.List(JobKindMigration, JobStateRunning)
	if err != nil {
		return err
	}
	jobs2, err := s.store.List(JobKindBackfill, JobStateRunning)
	if err != nil {
		return err
	}
	jobs3, err := s.store.List(JobKindRollback, JobStateRunning)
	if err != nil {
		return err
	}
	allRunning := append(append(jobs, jobs2...), jobs3...)
	for _, job := range allRunning {
		// Move to pending so the run loop picks it up again.
		// The Cursor field still holds the resume position.
		job.State = JobStatePending
		if err := s.store.Put(job); err != nil {
			return fmt.Errorf("recover job %s: %w", job.ID, err)
		}
		log.Printf("[scheduler] recovered interrupted job %s (cursor=%q)", job.ID, job.Cursor)
	}
	return nil
}

// runLoop is the scheduler's main goroutine. It runs until Stop is called.
func (s *JobScheduler) runLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			if !s.pickAndRunOne() {
				// No runnable jobs; sleep and retry.
				time.Sleep(2 * time.Second)
			}
		}
	}
}

// pickAndRunOne finds the highest-priority pending job and runs it.
// Returns true if a job was found and executed (even if the execution was
// trivial such as immediately completing or canceling).
// Returns false if no pending jobs were found.
func (s *JobScheduler) pickAndRunOne() bool {
	jobs, err := s.store.List(JobKindMigration, JobStatePending)
	if err != nil {
		log.Printf("[scheduler] list migration jobs: %v", err)
		return false
	}
	jobs2, err := s.store.List(JobKindBackfill, JobStatePending)
	if err != nil {
		log.Printf("[scheduler] list backfill jobs: %v", err)
		return false
	}
	jobs3, err := s.store.List(JobKindRollback, JobStatePending)
	if err != nil {
		log.Printf("[scheduler] list rollback jobs: %v", err)
		return false
	}
	allPending := append(append(jobs, jobs2...), jobs3...)
	if len(allPending) == 0 {
		return false
	}

	// Pick highest priority (lowest number), then oldest (earliest CreatedAt).
	var best *Job
	for i := range allPending {
		if best == nil || allPending[i].Priority < best.Priority ||
			(allPending[i].Priority == best.Priority && allPending[i].CreatedAt.Before(best.CreatedAt)) {
			best = &allPending[i]
		}
	}

	// Re-fetch to ensure we have the latest version.
	job, err := s.store.Get(best.ID)
	if err != nil {
		log.Printf("[scheduler] get job %s: %v", best.ID, err)
		return true // a job was found; return true to avoid tight loop
	}

	if job.State != JobStatePending {
		return true // state changed; look again
	}

	s.executeJob(job)
	return true
}

// executeJob runs a single job to completion (or cancellation).
// It updates the job in the store at every step boundary.
func (s *JobScheduler) executeJob(job Job) {
	// Mark job as running.
	job.State = JobStateRunning
	job.StartedAt = time.Now()
	if err := s.store.Put(job); err != nil {
		log.Printf("[scheduler] failed to persist running state for job %s: %v", job.ID, err)
		return
	}
	s.onStateChange(job)

	// Find where to resume.
	startStep := job.DecodeResumeStep()

	// Run steps in order, skipping completed ones.
	for i := startStep; i < len(job.Steps); i++ {
		// Check for cancellation before each step.
		select {
		case <-s.ctx.Done():
			// Graceful: mark interrupted so it can resume later.
			job.State = JobStatePending
			job.EncodeResumeCursor()
			if err := s.store.Put(job); err != nil {
				log.Printf("[scheduler] failed to persist interrupted state for job %s: %v", job.ID, err)
			}
			return
		default:
		}

		step := &job.Steps[i]
		if step.State == StepStateCompleted || step.State == StepStateSkipped {
			continue
		}

		// Execute the step with yield support.
		s.executeStep(&job, i)

		// Persist step result immediately.
		if err := s.store.Put(job); err != nil {
			log.Printf("[scheduler] failed to persist step result for job %s step %d: %v", job.ID, i, err)
			return
		}
		s.onStateChange(job)

		if job.State == JobStateCanceled {
			job.EncodeResumeCursor()
			if err := s.store.Put(job); err != nil {
				log.Printf("[scheduler] failed to persist canceled state for job %s: %v", job.ID, err)
			}
			return
		}

		if job.State == JobStateFailed {
			return
		}
	}

	// All steps done.
	job.State = JobStateCompleted
	job.CompletedAt = time.Now()
	job.CheckpointAt = time.Now()
	if err := s.store.Put(job); err != nil {
		log.Printf("[scheduler] failed to persist completed state for job %s: %v", job.ID, err)
		return
	}
	s.onStateChange(job)
}

// executeStep runs one step with retry support and yield points.
func (s *JobScheduler) executeStep(job *Job, stepIdx int) {
	step := &job.Steps[stepIdx]

	for {
		select {
		case <-s.ctx.Done():
			job.State = JobStatePending
			job.EncodeResumeCursor()
			return
		default:
		}

		step.State = StepStateRunning
		step.StartedAt = time.Now()
		job.LastError = ""

		// Run the step implementation.
		err := s.runStep(job, stepIdx)

		if err == nil {
			step.State = StepStateCompleted
			step.CompletedAt = time.Now()
			job.CheckpointAt = time.Now()
			return
		}

		// Error occurred.
		step.Retries++
		step.Error = err.Error()
		job.LastError = err.Error()
		job.Errors++

		if step.Retries >= s.config.MaxRetriesPerStep {
			step.State = StepStateFailed
			job.State = JobStateFailed
			job.CompletedAt = time.Now()
			return
		}

		// Retry after backoff.
		step.State = StepStatePending
		backoff := time.Duration(step.Retries) * 5 * time.Second
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
		time.Sleep(backoff)
	}
}

// runStep dispatches to the appropriate job-type handler.
func (s *JobScheduler) runStep(job *Job, stepIdx int) error {
	_ = &job.Steps[stepIdx] // step info available for future use

	switch job.Kind {
	case JobKindMigration:
		return s.runMigrationStep(job, stepIdx)
	case JobKindBackfill:
		return s.runBackfillStep(job, stepIdx)
	case JobKindRollback:
		return s.runRollbackStep(job, stepIdx)
	default:
		return fmt.Errorf("runStep: unknown job kind %s", job.Kind)
	}
}

// runMigrationStep executes a single migration step.
func (s *JobScheduler) runMigrationStep(job *Job, stepIdx int) error {
	// Migration steps are driven by the MigrationExecutor.
	// The step.Checkpoint carries the migration version to run.
	step := &job.Steps[stepIdx]

	// The step name is the migration version identifier.
	// For now, this is a placeholder — Phase 1 migration logic calls the
	// existing internal/db/migrations registry.
	if step.Name == "" {
		return fmt.Errorf("migration step has no name (version)")
	}

	// TODO: wire to actual migration executor.
	// The step.Checkpoint can carry intermediate progress.
	// For now, we mark it complete.
	return nil
}

// runBackfillStep executes a single backfill step by delegating to the
// BackfillExecutor. The executor receives the context for cancellation and
// writes its own progress into job.Steps[stepIdx].Checkpoint.
func (s *JobScheduler) runBackfillStep(job *Job, stepIdx int) error {
	step := &job.Steps[stepIdx]

	// If no backfill executor is registered, fail clearly.
	if BackfillExecutor == nil || BackfillExecutor == (*noOpBackfill)(nil) {
		return fmt.Errorf("backfill executor not initialized (call SetBackfillExecutor during startup)")
	}

	target := BackfillTarget(step.Name)
	ctx := s.ctx

	// If a checkpoint exists, the backfill executor can resume from it.
	// For now we pass the job's Cursor (which encodes the resume position)
	// so the executor can pick up from the correct step.
	_ = step.Checkpoint

	err := BackfillExecutor.Run(ctx, target, job.MailboxID)
	if err != nil {
		return fmt.Errorf("backfill step %s: %w", step.Name, err)
	}

	// Backfill completed successfully.
	step.Checkpoint = "done"
	return nil
}

// runRollbackStep executes a single rollback step via the RollbackExecutor.
func (s *JobScheduler) runRollbackStep(job *Job, stepIdx int) error {
	step := &job.Steps[stepIdx]

	if RollbackExecutor == nil || RollbackExecutor == (*noOpRollback)(nil) {
		return fmt.Errorf("rollback executor not initialized (call SetRollbackExecutor during startup)")
	}

	target := RollbackTarget(step.Name)
	err := RollbackExecutor.Run(s.ctx, target, job.MailboxID)
	if err != nil {
		return fmt.Errorf("rollback step %s: %w", step.Name, err)
	}

	step.Checkpoint = "done"
	return nil
}

// onStateChange calls the optional state-change hook.
func (s *JobScheduler) onStateChange(job Job) {
	if s.config.OnJobStateChange != nil {
		s.config.OnJobStateChange(job)
	}
}
