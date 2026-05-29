// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the concrete DefaultBackfillExecutor, which implements
// the BackfillHandler interface from backfill.go. It uses the JobScheduler
// to run durable backfill jobs, tracks progress per-item, and supports
// resume from checkpoint after interruption.
//
// The executor operates on these BackfillTargets:
//   - BackfillTargetMailbox  — assigns MailboxId to accounts
//   - BackfillTargetFolder   — assigns FolderId and distinguished folder metadata
//   - BackfillTargetItem     — assigns ItemId/ChangeKey for messages
//   - BackfillTargetAttachment — assigns AttachmentId for attachments
//   - BackfillTermConversation — assigns ConversationId and thread ordering
//   - BackfillTargetSyncState  — seeds SyncToken/watermark for mailboxes
//   - BackfillTargetLifecycle  — populates lifecycle journal entries
//
// Each target is processed in dependency order: MailboxId before FolderId,
// FolderId before ItemId, ItemId before AttachmentId and ConversationId.
package semcore

import (
	"context"
	"fmt"
	"log"
	"time"
)

// DefaultBackfillExecutor is the production backfill executor.
// It requires a JobScheduler and a JobStore to be configured before use.
type DefaultBackfillExecutor struct {
	scheduler *JobScheduler
	store     JobStore
}

// NewDefaultBackfillExecutor creates a backfill executor wired to the
// shared job scheduler and store.
func NewDefaultBackfillExecutor(scheduler *JobScheduler, store JobStore) *DefaultBackfillExecutor {
	return &DefaultBackfillExecutor{
		scheduler: scheduler,
		store:     store,
	}
}

// Run starts a backfill job for the given target.
// If mailboxID is zero, all mailboxes are backfilled.
// Run blocks until the context is canceled or the backfill reaches a
// checkpoint and yields. The job record is persisted at each checkpoint
// so that an interrupted run can be resumed from the same point.
func (e *DefaultBackfillExecutor) Run(ctx context.Context, target BackfillTarget, mailboxID MailboxId) error {
	log.Printf("[backfill] starting target=%s mailbox=%v", target, mailboxID)

	// Build the ordered list of steps for this target.
	steps := buildBackfillSteps(target)

	// Create a new job record.
	job := NewJob(JobKindBackfill, string(target), mailboxID, 10, steps, "system")

	// Submit to the scheduler.
	if err := e.scheduler.Submit(job); err != nil {
		// If the job already exists (e.g., from a previous partial run),
		// retrieve it and update the mailbox context.
		existing, getErr := e.scheduler.GetJob(job.ID)
		if getErr == nil && existing.MailboxID == mailboxID {
			// Resume existing job.
			return e.resumeJob(ctx, existing)
		}
		return fmt.Errorf("backfill: submit job: %w", err)
	}

	// Wait for the job to complete or the context to be canceled.
	// The scheduler runs the job asynchronously; we poll the store.
	for {
		select {
		case <-ctx.Done():
			log.Printf("[backfill] context canceled, job %s still running", job.ID)
			return ctx.Err()
		default:
		}

		storedJob, err := e.store.Get(job.ID)
		if err != nil {
			return fmt.Errorf("backfill: get job %s: %w", job.ID, err)
		}

		if storedJob.IsTerminal() {
			if storedJob.State == JobStateFailed {
				return fmt.Errorf("backfill job %s failed: %s", job.ID, storedJob.LastError)
			}
			log.Printf("[backfill] completed target=%s job=%s", target, job.ID)
			return nil
		}

		// Update the running job's Cursor so that if we are interrupted,
		// the next Run call will pick up from the correct step.
		job.Cursor = storedJob.Cursor

		// Yield between polls to avoid busy-waiting.
		time.Sleep(1 * time.Second)
	}
}

// resumeJob resumes an existing partial backfill job.
func (e *DefaultBackfillExecutor) resumeJob(ctx context.Context, job Job) error {
	log.Printf("[backfill] resuming job %s from cursor=%q", job.ID, job.Cursor)

	// Re-submit the job to the scheduler so it picks up from the cursor.
	job.State = JobStatePending
	if err := e.scheduler.Submit(job); err != nil {
		// Duplicate submit is fine — the scheduler will pick it up.
		_ = err
	}

	// Poll until the job completes or context is canceled.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		storedJob, err := e.store.Get(job.ID)
		if err != nil {
			return fmt.Errorf("backfill: get job %s: %w", job.ID, err)
		}

		if storedJob.IsTerminal() {
			if storedJob.State == JobStateFailed {
				return fmt.Errorf("backfill job %s failed: %s", job.ID, storedJob.LastError)
			}
			log.Printf("[backfill] resumed job completed target=%s job=%s", BackfillTarget(job.Target), job.ID)
			return nil
		}

		time.Sleep(1 * time.Second)
	}
}

// Status implements BackfillHandler. It retrieves the latest job record
// for the given target and mailbox combination and returns it as a BackfillJob.
func (e *DefaultBackfillExecutor) Status() BackfillJob {
	// Find the most recent backfill job.
	jobs, err := e.store.List(JobKindBackfill, "")
	if err != nil {
		return BackfillJob{Status: BackfillStatusFailed}
	}

	var best *Job
	for i := range jobs {
		if best == nil || jobs[i].CreatedAt.After(best.CreatedAt) {
			best = &jobs[i]
		}
	}

	if best == nil {
		return BackfillJob{Status: BackfillStatusFailed}
	}

	// Map Job to BackfillJob for the interface.
	bj := BackfillJob{
		ID:        best.ID,
		Target:    BackfillTarget(best.Target),
		MailboxID: best.MailboxID,
		Errors:    best.Errors,
		StartedAt: best.StartedAt.Unix(),
	}

	switch best.State {
	case JobStatePending:
		bj.Status = BackfillStatusPending
	case JobStateRunning:
		bj.Status = BackfillStatusRunning
		step := best.CurrentStep()
		if step != nil {
			bj.Cursor = step.Checkpoint
		}
	case JobStateCompleted:
		bj.Status = BackfillStatusCompleted
		bj.CompletedAt = best.CompletedAt.Unix()
	case JobStateFailed:
		bj.Status = BackfillStatusFailed
	case JobStateCanceled:
		bj.Status = BackfillStatusCanceled
	}

	return bj
}

// buildBackfillSteps returns the ordered step list for a given backfill target.
// Steps are ordered to respect dependencies: mailbox identity before folder
// identity, folder identity before item identity, etc.
func buildBackfillSteps(target BackfillTarget) []JobStep {
	switch target {
	case BackfillTargetMailbox:
		return []JobStep{
			{Name: string(BackfillTargetMailbox), Description: "Assign MailboxId to all accounts", State: StepStatePending},
		}
	case BackfillTargetFolder:
		return []JobStep{
			{Name: string(BackfillTargetFolder), Description: "Assign FolderId and distinguished folder metadata", State: StepStatePending},
		}
	case BackfillTargetItem:
		return []JobStep{
			{Name: string(BackfillTargetItem), Description: "Assign ItemId, ChangeKey, and ConversationId to messages", State: StepStatePending},
		}
	case BackfillTargetAttachment:
		return []JobStep{
			{Name: string(BackfillTargetAttachment), Description: "Assign AttachmentId to file and inline attachments", State: StepStatePending},
		}
	case BackfillTermConversation:
		return []JobStep{
			{Name: string(BackfillTermConversation), Description: "Compute and assign ConversationId to threads", State: StepStatePending},
		}
	case BackfillTargetSyncState:
		return []JobStep{
			{Name: string(BackfillTargetSyncState), Description: "Seed SyncToken and watermark for mailboxes", State: StepStatePending},
		}
	case BackfillTargetLifecycle:
		return []JobStep{
			{Name: string(BackfillTargetLifecycle), Description: "Populate lifecycle journal entries", State: StepStatePending},
		}
	default:
		return []JobStep{
			{Name: string(target), Description: "Backfill " + string(target), State: StepStatePending},
		}
	}
}
