// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file defines typed entry points for backfill and rollback operations.
// Phase 1 will implement the actual backfill/rollback logic; these types
// provide the structured entry points so that code in later phases can
// call into a well-defined interface rather than working ad hoc.
//
// # Backfill
//
// Backfill is the process of populating canonical semantic-core state from
// existing legacy store data. For example, assigning MailboxId/FolderId/ItemId
// identities to messages that currently exist only with IMAP UIDs, or
// computing ConversationId for existing threads that are currently inferred
// only from In-Reply-To headers.
//
// Backfill is resumable. It must be safe to interrupt and restart without
// data loss or corruption.
//
// # Rollback
//
// Rollback undoes the effects of a failed or canceled migration. The semantic
// core is designed so that a rollback does not require rolling back legacy
// stores — the canonical store can be cleared and the system falls back to
// legacy-only behavior while the canonical data is repopulated later.
//
// # Entry Point Design
//
// These types are interfaces and structs, not implementations. Phase 1 will
// provide concrete implementations. The interfaces define the minimum contract
// that any backfill/rollback implementation must satisfy.
package semcore

import "context"

// ---------------------------------------------------------------------------
// Backfill entry points
// ---------------------------------------------------------------------------

// BackfillTarget identifies what kind of canonical state is being backfilled.
type BackfillTarget string

const (
	BackfillTargetMailbox     BackfillTarget = "mailbox"     // MailboxId assignment
	BackfillTargetFolder      BackfillTarget = "folder"      // FolderId assignment + distinguished folder metadata
	BackfillTargetItem        BackfillTarget = "item"        // ItemId/ChangeKey/ConversationId for messages
	BackfillTargetAttachment  BackfillTarget = "attachment" // AttachmentId for file/inline attachments
	BackfillTermConversation  BackfillTarget = "conversation" // ConversationId / thread ordering
	BackfillTargetSyncState   BackfillTarget = "sync_state"  // SyncToken / watermark seeding
	BackfillTargetLifecycle  BackfillTarget = "lifecycle"   // Lifecycle journal population
)

// BackfillJob represents a single backfill run for a target type.
// Phase 1 will add persistence, progress tracking, and resume support.
type BackfillJob struct {
	ID          string
	Target      BackfillTarget
	MailboxID   MailboxId // zero = all mailboxes; non-zero = single mailbox
	Status      BackfillStatus
	Cursor      string   // opaque resume cursor
	TotalItems  int      // estimated or counted total items to process
	Processed   int      // items processed so far
	Errors      int      // non-fatal errors encountered
	StartedAt   int64   // unix timestamp
	CompletedAt int64   // zero if not yet complete
}

// BackfillStatus describes the state of a backfill job.
type BackfillStatus string

const (
	BackfillStatusPending   BackfillStatus = "pending"
	BackfillStatusRunning   BackfillStatus = "running"
	BackfillStatusCompleted BackfillStatus = "completed"
	BackfillStatusFailed    BackfillStatus = "failed"
	BackfillStatusCanceled  BackfillStatus = "canceled"
)

func (s BackfillStatus) String() string { return string(s) }

// BackfillHandler is the interface that Phase 1 backfill implementations must satisfy.
// It receives a context (for cancellation), a target, and a mailbox ID (zero = all).
type BackfillHandler interface {
	// Run starts the backfill job. It may return before the job is complete;
	// callers can pass a cancellable context to interrupt.
	Run(ctx context.Context, target BackfillTarget, mailboxID MailboxId) error

	// Status returns the current status of a running or completed backfill job.
	Status() BackfillJob
}

// BackfillExecutor is the global backfill executor set by Phase 1.
// It defaults to a no-op that returns ErrBackfillNotReady.
var BackfillExecutor BackfillHandler = noOpBackfill{}

var ErrBackfillNotReady = &BackfillError{msg: "backfill: executor not yet initialized (Phase 1)"}

// SetBackfillExecutor sets the global backfill executor.
// It is only safe to call during server startup before protocol servers start.
func SetBackfillExecutor(h BackfillHandler) {
	BackfillExecutor = h
}

type noOpBackfill struct{}

func (noOpBackfill) Run(ctx context.Context, target BackfillTarget, mailboxID MailboxId) error {
	return ErrBackfillNotReady
}

func (noOpBackfill) Status() BackfillJob {
	return BackfillJob{Status: BackfillStatusFailed}
}

// ---------------------------------------------------------------------------
// Rollback entry points
// ---------------------------------------------------------------------------

// RollbackTarget identifies what kind of canonical state is being rolled back.
type RollbackTarget string

const (
	RollbackTargetIdentity  RollbackTarget = "identity"  // clear canonical IDs, fall back to legacy
	RollbackTargetSyncState RollbackTarget = "sync_state" // clear sync tokens and watermarks
	RollbackTargetLifecycle RollbackTarget = "lifecycle"  // clear lifecycle journal
	RollbackTargetAll       RollbackTarget = "all"        // clear all canonical state
)

// RollbackJob represents a single rollback run.
type RollbackJob struct {
	ID          string
	Target      RollbackTarget
	Status      RollbackStatus
	MailboxID   MailboxId // zero = all mailboxes
	Affected    int      // objects affected
	StartedAt   int64
	CompletedAt int64
}

// RollbackStatus describes the state of a rollback job.
type RollbackStatus string

const (
	RollbackStatusPending   RollbackStatus = "pending"
	RollbackStatusRunning   RollbackStatus = "running"
	RollbackStatusCompleted RollbackStatus = "completed"
	RollbackStatusFailed    RollbackStatus = "failed"
)

func (s RollbackStatus) String() string { return string(s) }

// RollbackHandler is the interface that Phase 1 rollback implementations must satisfy.
type RollbackHandler interface {
	// Run starts the rollback. It may return before the job is complete.
	Run(ctx context.Context, target RollbackTarget, mailboxID MailboxId) error

	// Status returns the current RPC status of the job.
	Status() RollbackJob
}

// RollbackExecutor is the global rollback executor set in Phase 1.
// It defaults to a no-op that returns ErrRollbackNotReady.
var RollbackExecutor RollbackHandler = noOpRollback{}

var ErrRollbackNotReady = &BackfillError{msg: "rollback: executor not yet initialized (Phase 1)"}

// SetRollbackExecutor sets the global rollback executor.
// It is only safe to call during server startup.
func SetRollbackExecutor(h RollbackHandler) {
	RollbackExecutor = h
}

type noOpRollback struct{}

func (noOpRollback) Run(ctx context.Context, target RollbackTarget, mailboxID MailboxId) error {
	return ErrRollbackNotReady
}

func (noOpRollback) Status() RollbackJob {
	return RollbackJob{Status: RollbackStatusFailed}
}

// ---------------------------------------------------------------------------
// Shared error type
// ---------------------------------------------------------------------------

// BackfillError is the canonical error type for backfill/rollback failures.
// It embeds the reason so that callers can inspect and surface it meaningfully.
type BackfillError struct {
	msg string
}

func (e *BackfillError) Error() string { return e.msg }
