// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides a unified canonical Store that owns one bbolt database
// and exposes all semantic-core sub-stores: identity, sync-state, tombstones,
// and backfill-seeding. Using a single DB file keeps the data model coherent
// and simplifies startup. All sub-stores are safe for concurrent use.
package semcore

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.etcd.io/bbolt"
)

// Store is the unified canonical semantic-core store. It owns one bbolt
// database file and exposes all semantic-core sub-stores.
// All stores are safe for concurrent use; callers must hold the appropriate
// lock for the scope they are operating in.
type Store struct {
	db     *bbolt.DB
	mu     sync.RWMutex
	dir    string

	identity      *BoltIdentityStore
	syncState     *BoltSyncStateStore
	tombstones    *BoltTombstoneStore
	seeding       *BoltBackfillSeedingStore
	collab        *BoltCollaborationStore
	lifecycle     *BoltLifecycleStore
	subscriptions *BoltSubscriptionStore
	policy        *BoltPolicyStore
	delegation    *BoltDelegateStore
}

// NewStore opens a canonical semantic-core store, creating the database
// file and all buckets if they do not yet exist. The dataDir is the
// directory where the semcore DB file (identity.db) will be stored.
func NewStore(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "semcore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("semcore.NewStore: create dir: %w", err)
	}

	dbPath := filepath.Join(dir, "identity.db")
	db, err := bbolt.Open(dbPath, 0o600, &bbolt.Options{Timeout: 1})
	if err != nil {
		return nil, fmt.Errorf("semcore.NewStore: open: %w", err)
	}

	// Create all sub-store buckets in one transaction.
	fn := func(tx *bbolt.Tx) error {
		buckets := []string{
			bucketMailbox,
			bucketFolder,
			bucketItem,
			bucketAttachment,
			bucketConversation,
			bucketSyncState,
			bucketTombstones,
			bucketSeeding,
			bucketCalendarItem,
			bucketContact,
			bucketTask,
			bucketLifecycle,
			bucketSubscriptions,
			bucketRule,
			bucketOOF,
			bucketResource,
			bucketNotification,
			bucketDelegations,
		}
		for _, b := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(b)); err != nil {
				return fmt.Errorf("create bucket %s: %w", b, err)
			}
		}
		return nil
	}
	if err := db.Update(fn); err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("semcore.NewStore: init buckets: %w", err)
	}

	s := &Store{db: db, dir: dir}

	// Initialize sub-stores.
	s.identity = &BoltIdentityStore{db: db}
	s.syncState = &BoltSyncStateStore{db: db}
	s.tombstones = &BoltTombstoneStore{db: db}
	s.seeding = &BoltBackfillSeedingStore{db: db}

	collab, err := NewBoltCollaborationStore(db)
	if err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("semcore.NewStore: collab store: %w", err)
	}
	s.collab = collab

	lifecycle, err := NewBoltLifecycleStore(db)
	if err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("semcore.NewStore: lifecycle store: %w", err)
	}
	s.lifecycle = lifecycle

	subscriptions, err := NewBoltSubscriptionStore(db)
	if err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("semcore.NewStore: subscription store: %w", err)
	}
	s.subscriptions = subscriptions

	// Policy store (rules, OOF, resources, notifications)
	s.policy, err = NewBoltPolicyStore(db)
	if err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("semcore.NewStore: policy store: %w", err)
	}

	// Delegation store (delegate grants per mailbox)
	delegation, err := NewBoltDelegateStore(db)
	if err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("semcore.NewStore: delegation store: %w", err)
	}
	s.delegation = delegation

	return s, nil
}

// Close closes the underlying bbolt database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Bolt returns the underlying bbolt.DB.
func (s *Store) Bolt() *bbolt.DB { return s.db }

// Identity returns the canonical identity store (MailboxId, FolderId, ItemId,
// ChangeKey, AttachmentId, ConversationId).
func (s *Store) Identity() *BoltIdentityStore { return s.identity }

// SyncState returns the sync-state store (per-mailbox, per-folder, per-client
// sync watermarks).
func (s *Store) SyncState() *BoltSyncStateStore { return s.syncState }

// Tombstones returns the tombstone store (soft-delete and hard-delete
// lifecycle records).
func (s *Store) Tombstones() *BoltTombstoneStore { return s.tombstones }

// Seeding returns the backfill-seeding store (mailbox population progress).
func (s *Store) Seeding() *BoltBackfillSeedingStore { return s.seeding }

// Collaboration returns the canonical collaboration store (CalendarItem,
// Contact, and Task identities with version tokens).
func (s *Store) Collaboration() *BoltCollaborationStore { return s.collab }

// Lifecycle returns the lifecycle event store (append-only canonical
// mailbox mutation events for sync and event consumers).
func (s *Store) Lifecycle() *BoltLifecycleStore { return s.lifecycle }

// Subscriptions returns the event subscription store (pull/push/streaming
// subscription watermarks for EWS event polling).
func (s *Store) Subscriptions() *BoltSubscriptionStore { return s.subscriptions }

// Policy returns the canonical policy store (inbox rules, OOF, resources,
// and notification policies).
func (s *Store) Policy() *BoltPolicyStore { return s.policy }

// Delegation returns the canonical delegation store (delegate grants,
// shared mailbox discovery, and meeting delivery settings).
// Satisfies VAL-DIR-001, VAL-DIR-002, VAL-DIR-003, VAL-DIR-013, VAL-DIR-014.
func (s *Store) Delegation() *BoltDelegateStore { return s.delegation }
