// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the concrete DefaultRollbackExecutor, which implements
// the RollbackHandler interface. Rollback clears canonical semantic state
// and returns the system to legacy-only behavior. Rollback does NOT delete
// or modify legacy store data; it only clears canonical identity, sync-state,
// and lifecycle records so that the system can fall back to existing
// protocol-local stores while the canonical store is repopulated later.
package semcore

import (
	"context"
	"fmt"
	"log"
	"time"
)

// DefaultRollbackExecutor is the production rollback executor.
// It requires a JobScheduler and a JobStore to be configured before use.
type DefaultRollbackExecutor struct {
	scheduler *JobScheduler
	store     JobStore
}

// NewDefaultRollbackExecutor creates a rollback executor wired to the
// shared job scheduler and store.
func NewDefaultRollbackExecutor(scheduler *JobScheduler, store JobStore) *DefaultRollbackExecutor {
	return &DefaultRollbackExecutor{
		scheduler: scheduler,
		store:     store,
	}
}

// Run starts a rollback job for the given target.
// If mailboxID is zero, all mailboxes are rolled back.
// Run blocks until the context is canceled or the rollback completes.
func (e *DefaultRollbackExecutor) Run(ctx context.Context, target RollbackTarget, mailboxID MailboxId) error {
	log.Printf("[rollback] starting target=%s mailbox=%v", target, mailboxID)

	steps := buildRollbackSteps(target)
	job := NewJob(JobKindRollback, string(target), mailboxID, 5, steps, "system")

	if err := e.scheduler.Submit(job); err != nil {
		existing, getErr := e.scheduler.GetJob(job.ID)
		if getErr == nil && existing.MailboxID == mailboxID {
			return e.resumeJob(ctx, existing)
		}
		return fmt.Errorf("rollback: submit job: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		storedJob, err := e.store.Get(job.ID)
		if err != nil {
			return fmt.Errorf("rollback: get job %s: %w", job.ID, err)
		}

		if storedJob.IsTerminal() {
			if storedJob.State == JobStateFailed {
				return fmt.Errorf("rollback job %s failed: %s", job.ID, storedJob.LastError)
			}
			log.Printf("[rollback] completed target=%s job=%s", target, job.ID)
			return nil
		}

		job.Cursor = storedJob.Cursor
		time.Sleep(1 * time.Second)
	}
}

// resumeJob resumes an existing rollback job.
func (e *DefaultRollbackExecutor) resumeJob(ctx context.Context, job Job) error {
	log.Printf("[rollback] resuming job %s from cursor=%q", job.ID, job.Cursor)
	job.State = JobStatePending
	if err := e.scheduler.Submit(job); err != nil {
		// Duplicate submit is fine — the scheduler will pick it up.
		_ = fmt.Sprintf("duplicate job submission: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		storedJob, err := e.store.Get(job.ID)
		if err != nil {
			return fmt.Errorf("rollback: get job %s: %w", job.ID, err)
		}

		if storedJob.IsTerminal() {
			if storedJob.State == JobStateFailed {
				return fmt.Errorf("rollback job %s failed: %s", job.ID, storedJob.LastError)
			}
			return nil
		}

		time.Sleep(1 * time.Second)
	}
}

// Status implements RollbackHandler.
func (e *DefaultRollbackExecutor) Status() RollbackJob {
	jobs, err := e.store.List(JobKindRollback, "")
	if err != nil {
		return RollbackJob{Status: RollbackStatusFailed}
	}

	var best *Job
	for i := range jobs {
		if best == nil || jobs[i].CreatedAt.After(best.CreatedAt) {
			best = &jobs[i]
		}
	}

	if best == nil {
		return RollbackJob{Status: RollbackStatusFailed}
	}

	rj := RollbackJob{
		ID:        best.ID,
		Target:    RollbackTarget(best.Target),
		MailboxID: best.MailboxID,
		StartedAt: best.StartedAt.Unix(),
	}

	switch best.State {
	case JobStatePending:
		rj.Status = RollbackStatusPending
	case JobStateRunning:
		rj.Status = RollbackStatusRunning
	case JobStateCompleted:
		rj.Status = RollbackStatusCompleted
		rj.CompletedAt = best.CompletedAt.Unix()
	case JobStateFailed:
		rj.Status = RollbackStatusFailed
	}

	return rj
}

// buildRollbackSteps returns the ordered step list for a given rollback target.
func buildRollbackSteps(target RollbackTarget) []JobStep {
	return []JobStep{
		{Name: string(target), Description: "Rollback " + string(target), State: StepStatePending},
	}
}
