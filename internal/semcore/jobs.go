// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file defines the durable job record types used by the migration and
// backfill scheduler. Every run of a migration or backfill produces one Job
// record that is persisted in Bolt. The record captures the full execution
// state so that interrupted runs can be resumed without re-running completed
// steps or skipping failed ones.
//
// # Job lifecycle
//
//  1. Pending → Running (scheduler picks it up)
//  2. Running → Completed (all steps finished successfully)
//  3. Running → Failed (unrecoverable error; can be retried from failed step)
//  4. Running → Canceled (explicit cancellation request)
//  5. Failed job can be re-enqueued (restart from step 0 or from last checkpoint)
package semcore

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// JobKind classifies the type of durable job.
type JobKind string

const (
	JobKindMigration JobKind = "migration"
	JobKindBackfill  JobKind = "backfill"
	JobKindRollback  JobKind = "rollback"
)

// JobState is the current state of a durable job.
type JobState string

const (
	JobStatePending   JobState = "pending"
	JobStateRunning   JobState = "running"
	JobStateCompleted JobState = "completed"
	JobStateFailed    JobState = "failed"
	JobStateCanceled JobState = "canceled"
)

func (s JobState) String() string { return string(s) }

// Job represents a durable, resumable job record.
// One Job record exists per logical operation (migration run, backfill run).
// The scheduler updates the record in Bolt after every step boundary so that
// an interrupted job can be resumed from the last successful checkpoint.
type Job struct {
	// ID is the globally unique identifier for this job run.
	ID string

	// Kind distinguishes migration, backfill, and rollback jobs.
	Kind JobKind

	// Target is the semantic object being acted on (e.g., BackfillTargetMailbox).
	// For migration jobs this is the migration version string.
	Target string

	// MailboxID restricts the job to a single mailbox (zero = all mailboxes).
	MailboxID MailboxId

	// State is the current execution state.
	State JobState

	// Priority for scheduling (lower = higher priority). 0 is highest.
	Priority int

	// Steps are the ordered steps for this job. Each step is executed in order.
	// A step is complete when its State is StepStateCompleted or StepStateFailed.
	Steps []JobStep

	// Cursor is an opaque resume token for the scheduler. It encodes the
	// next step to run and any intermediate state needed to continue.
	Cursor string

	// Errors is the count of non-fatal errors encountered during execution.
	Errors int

	// LastError is the most recent error message for display/debugging.
	LastError string

	// CreatedAt is when the job was first created.
	CreatedAt time.Time

	// StartedAt is when execution first began (zero if still pending).
	StartedAt time.Time

	// CheckpointAt is the time of the last successful step completion.
	CheckpointAt time.Time

	// CompletedAt is when execution finished (zero if still running).
	CompletedAt time.Time

	// Actor is the user or system that initiated the job.
	Actor string
}

// CurrentStep returns the first non-terminal step in the job, or nil if all
// steps are complete or the job has no steps.
func (j *Job) CurrentStep() *JobStep {
	for i := range j.Steps {
		if j.Steps[i].State != StepStateCompleted && j.Steps[i].State != StepStateSkipped {
			return &j.Steps[i]
		}
	}
	return nil
}

// Progress returns the number of completed steps and the total step count.
func (j *Job) Progress() (done, total int) {
	for _, s := range j.Steps {
		if s.State == StepStateCompleted {
			done++
		}
	}
	return done, len(j.Steps)
}

// IsTerminal returns true when the job is in a final state and cannot proceed.
func (j *Job) IsTerminal() bool {
	return j.State == JobStateCompleted ||
		j.State == JobStateFailed ||
		j.State == JobStateCanceled
}

// ResumeCursor encodes the job's next executable step index into Cursor.
func (j *Job) EncodeResumeCursor() {
	if j.CurrentStep() == nil {
		j.Cursor = "done"
		return
	}
	// Find index of first non-terminal step
	for i, s := range j.Steps {
		if s.State != StepStateCompleted && s.State != StepStateSkipped {
			j.Cursor = fmt.Sprintf("step=%d", i)
			return
		}
	}
}

// DecodeResumeStep returns the step index encoded in the Cursor field, or 0
// if the cursor is empty or malformed.
func (j *Job) DecodeResumeStep() int {
	if j.Cursor == "" || j.Cursor == "done" {
		return 0
	}
	if strings.HasPrefix(j.Cursor, "step=") {
		var idx int
		n, err := fmt.Sscanf(j.Cursor[5:], "%d", &idx)
		if err != nil || n != 1 {
			return 0
		}
		return idx
	}
	return 0
}

// JobStep represents one atomic unit of a job. A step either succeeds or fails;
// a failed step can be retried. Steps are executed in order; later steps wait
// for earlier steps to complete.
type JobStep struct {
	// Name is a short human-readable identifier for this step.
	Name string

	// Description explains what this step does.
	Description string

	// State is the step's current execution state.
	State StepState

	// StartedAt is when the step began execution.
	StartedAt time.Time

	// CompletedAt is when the step finished (success or failure).
	CompletedAt time.Time

	// Checkpoint is an opaque value written by the step executor when it
	// reaches a resumable boundary (e.g., "uid=1234" or "page=5"). The
	// scheduler stores this in Bolt so that a restarted job can continue
	// from this point rather than repeating work.
	Checkpoint string

	// Error is the error message if the step failed.
	Error string

	// Retries is the number of times this step has been attempted.
	Retries int
}

// StepState is the state of an individual job step.
type StepState string

const (
	StepStatePending   StepState = "pending"
	StepStateRunning   StepState = "running"
	StepStateCompleted StepState = "completed"
	StepStateFailed    StepState = "failed"
	StepStateSkipped   StepState = "skipped"
)

func (s StepState) String() string { return string(s) }

// MarshalJSON serializes a Job for Bolt storage.
func (j Job) MarshalJSON() ([]byte, error) {
	type alias Job
	return json.Marshal(alias(j))
}

// UnmarshalJob deserializes a Job from Bolt storage.
func UnmarshalJob(data []byte) (Job, error) {
	var j Job
	err := json.Unmarshal(data, &j)
	return j, err
}

// JobStore is the interface for persisting and querying Job records.
// The implementation must store jobs durably (Bolt-backed).
type JobStore interface {
	// Put persists a job. If the job already exists, it is updated.
	Put(job Job) error

	// Get retrieves a job by ID. Returns ErrJobNotFound if not found.
	Get(id string) (Job, error)

	// List returns all jobs, optionally filtered by kind and state.
	// If kind is empty, all kinds are returned. If state is empty, all states.
	List(kind JobKind, state JobState) ([]Job, error)

	// Delete removes a job. Returns ErrJobNotFound if not found.
	Delete(id string) error
}

// ErrJobNotFound is returned by JobStore operations when a job does not exist.
var ErrJobNotFound = fmt.Errorf("job not found")

// NewJob creates a new job with the given kind, target, and steps.
// The job starts in Pending state with CreatedAt set to now.
func NewJob(kind JobKind, target string, mailboxID MailboxId, priority int, steps []JobStep, actor string) Job {
	return Job{
		ID:        newJobID(),
		Kind:      kind,
		Target:    target,
		MailboxID: mailboxID,
		State:     JobStatePending,
		Priority:  priority,
		Steps:     steps,
		Actor:     actor,
		CreatedAt: time.Now(),
	}
}

// newJobID generates a unique job identifier.
// Format: {kind}-{timestamp}-{random}
// Uses crypto/rand to ensure uniqueness even when multiple jobs are
// created within the same nanosecond window.
func newJobID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // best-effort entropy; error is ignored
	return fmt.Sprintf("job-%d-%x", time.Now().UnixNano(), b)
}
