// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the backfill-seeding state store: durable tracking of
// which canonical structures have been seeded for each mailbox, enabling a
// resumable population path for existing mail data into the new semantic-core
// model. When a backfill job is interrupted, the seeding state determines
// exactly where to resume without re-processing already-completed phases.
//
// # Backfill Seeding State
//
// A mailbox enters the semantic-core world in phases:
//
//	Phase 0 — Raw legacy data only (IMAP UIDs, folder paths, file blobs)
//	Phase 1 — MailboxId assigned
//	Phase 2 — FolderId assigned for all folders (distinguished + user-created)
//	Phase 3 — ItemId + ChangeKey assigned for all messages
//	Phase 4 — ConversationId computed for all threads
//	Phase 5 — SyncState seeded for all known clients
//	Phase 6 — Lifecycle journal seeded
//	Complete — All canonical structures populated
//
// SeedingState records which phases are complete for each mailbox so that
// a resumed backfill can pick up exactly where it left off. Each phase is
// idempotent: re-running a completed phase is a no-op.
//
// # Invariants
//
//  1. SeedingState is written only by the backfill executor.
//  2. Completed phases are never reverted; only advanced.
//  3. A mailbox with a gap in phases must be backfilled from the first
//     incomplete phase, not from scratch.
//  4. SeedingState is initialized automatically when a new mailbox is
//     registered in the identity store — it starts at Phase 0.
package semcore

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// BackfillSeedingStore interface
// ---------------------------------------------------------------------------

// BackfillSeedingStore is the interface for persisting the backfill seeding
// state of each mailbox. This determines where a resumable backfill resumes.
type BackfillSeedingStore interface {
	// GetSeedingState returns the current seeding state for a mailbox.
	// Returns ErrMailboxNotFound if the mailbox is not registered.
	GetSeedingState(mboxID MailboxId) (*BackfillSeedingState, error)

	// PutSeedingState persists or updates the seeding state for a mailbox.
	PutSeedingState(state *BackfillSeedingState) error

	// ListSeedingStates returns all mailbox seeding states, optionally
	// filtered to those with an incomplete phase (e.g., for dashboard display).
	ListSeedingStates() ([]BackfillSeedingState, error)

	// InitSeedingState creates a Phase 0 seeding record for a newly
	// registered mailbox if one does not yet exist.
	InitSeedingState(mboxID MailboxId) error
}

// ---------------------------------------------------------------------------
// BackfillSeedingState
// ---------------------------------------------------------------------------

// BackfillPhase describes how far canonical seeding has progressed for a mailbox.
type BackfillPhase uint8

const (
	// SeedingPhaseNone — mailbox is new, no backfill attempted yet.
	SeedingPhaseNone BackfillPhase = iota

	// SeedingPhaseMailbox — MailboxId assigned and identity store populated.
	SeedingPhaseMailbox

	// SeedingPhaseFolder — FolderId assigned for all folders, distinguished
	// folder metadata is complete.
	SeedingPhaseFolder

	// SeedingPhaseItem — ItemId and ChangeKey assigned for all messages.
	SeedingPhaseItem

	// SeedingPhaseConversation — ConversationId computed for all threads.
	SeedingPhaseConversation

	// SeedingPhaseSyncState — SyncState seeded for all known clients.
	SeedingPhaseSyncState

	// SeedingPhaseLifecycle — Lifecycle journal entries populated.
	SeedingPhaseLifecycle

	// SeedingPhaseComplete — all canonical structures are populated.
	SeedingPhaseComplete BackfillPhase = 100
)

// String implements fmt.Stringer.
func (p BackfillPhase) String() string {
	switch p {
	case SeedingPhaseNone:
		return "none"
	case SeedingPhaseMailbox:
		return "mailbox"
	case SeedingPhaseFolder:
		return "folder"
	case SeedingPhaseItem:
		return "item"
	case SeedingPhaseConversation:
		return "conversation"
	case SeedingPhaseSyncState:
		return "sync_state"
	case SeedingPhaseLifecycle:
		return "lifecycle"
	case SeedingPhaseComplete:
		return "complete"
	default:
		return "unknown"
	}
}

// IsZero returns true for SeedingPhaseNone.
func (p BackfillPhase) IsZero() bool { return p == SeedingPhaseNone }

// AdvanceTo returns the higher of two phases.
func (p BackfillPhase) AdvanceTo(other BackfillPhase) BackfillPhase {
	if other > p {
		return other
	}
	return p
}

// NextPhase returns the phase that follows this one.
// Returns SeedingPhaseComplete if already at the last phase.
func (p BackfillPhase) NextPhase() BackfillPhase {
	switch p {
	case SeedingPhaseNone:
		return SeedingPhaseMailbox
	case SeedingPhaseMailbox:
		return SeedingPhaseFolder
	case SeedingPhaseFolder:
		return SeedingPhaseItem
	case SeedingPhaseItem:
		return SeedingPhaseConversation
	case SeedingPhaseConversation:
		return SeedingPhaseSyncState
	case SeedingPhaseSyncState:
		return SeedingPhaseLifecycle
	case SeedingPhaseLifecycle:
		return SeedingPhaseComplete
	default:
		return SeedingPhaseComplete
	}
}

// IsComplete returns true when the phase is SeedingPhaseComplete.
func (p BackfillPhase) IsComplete() bool { return p == SeedingPhaseComplete }

// BackfillSeedingState records the seeding progress for one mailbox.
// Each field is updated only when the corresponding phase is confirmed complete.
type BackfillSeedingState struct {
	MailboxID      MailboxId     `json:"mailbox_id"`
	CurrentPhase   BackfillPhase `json:"current_phase"`
	MailboxDone    bool          `json:"mailbox_done"`     // MailboxId assigned
	FolderDone     bool          `json:"folder_done"`      // all FolderIds assigned
	ItemDone       bool          `json:"item_done"`        // all ItemId/ChangeKey assigned
	ConvDone       bool          `json:"conv_done"`        // all ConversationIds assigned
	SyncStateDone  bool          `json:"sync_state_done"`  // sync watermarks seeded
	LifecycleDone  bool          `json:"lifecycle_done"`   // lifecycle journal seeded
	LastPhaseAt    time.Time     `json:"last_phase_at"`    // when current phase was reached
	LastBackfillAt time.Time     `json:"last_backfill_at"` // last backfill activity
	Errors         int           `json:"errors"`           // non-fatal errors during backfill
	LastError      string        `json:"last_error"`       // most recent error
}

// seedingKey returns the Bolt bucket key for a mailbox's seeding state.
func seedingKey(mboxID MailboxId) string {
	return "seeding:" + mboxID.String()
}

// ---------------------------------------------------------------------------
// BoltBackfillSeedingStore
// ---------------------------------------------------------------------------

const bucketSeeding = "__semcore_seeding"

// BoltBackfillSeedingStore persists BackfillSeedingState in a dedicated bbolt bucket.
type BoltBackfillSeedingStore struct {
	db *bbolt.DB
	mu sync.RWMutex
}

// NewBoltBackfillSeedingStore opens a Bolt-backed backfill seeding store,
// creating the bucket if it does not yet exist.
func NewBoltBackfillSeedingStore(db *bbolt.DB) (*BoltBackfillSeedingStore, error) {
	err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketSeeding))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("BoltBackfillSeedingStore: create bucket: %w", err)
	}
	return &BoltBackfillSeedingStore{db: db}, nil
}

// GetSeedingState implements BackfillSeedingStore.
func (s *BoltBackfillSeedingStore) GetSeedingState(mboxID MailboxId) (*BackfillSeedingState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var state *BackfillSeedingState
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSeeding))
		data := b.Get([]byte(seedingKey(mboxID)))
		if data == nil {
			return ErrMailboxNotFound
		}
		var st BackfillSeedingState
		if err := json.Unmarshal(data, &st); err != nil {
			return fmt.Errorf("unmarshal seeding state: %w", err)
		}
		scopy := st
		state = &scopy
		return nil
	})
	return state, err
}

// PutSeedingState implements BackfillSeedingStore.
func (s *BoltBackfillSeedingStore) PutSeedingState(state *BackfillSeedingState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSeeding))
		data, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("marshal seeding state: %w", err)
		}
		return b.Put([]byte(seedingKey(state.MailboxID)), data)
	})
}

// ListSeedingStates implements BackfillSeedingStore.
func (s *BoltBackfillSeedingStore) ListSeedingStates() ([]BackfillSeedingState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []BackfillSeedingState
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSeeding))
		return b.ForEach(func(k, v []byte) error {
			var st BackfillSeedingState
			if err := json.Unmarshal(v, &st); err != nil {
				return nil
			}
			result = append(result, st)
			return nil
		})
	})
	return result, err
}

// InitSeedingState implements BackfillSeedingStore.
func (s *BoltBackfillSeedingStore) InitSeedingState(mboxID MailboxId) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSeeding))
		k := []byte(seedingKey(mboxID))
		if b.Get(k) != nil {
			return nil // already initialized; idempotent
		}
		state := &BackfillSeedingState{
			MailboxID:    mboxID,
			CurrentPhase: SeedingPhaseNone,
		}
		data, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("marshal seeding state: %w", err)
		}
		return b.Put(k, data)
	})
}
