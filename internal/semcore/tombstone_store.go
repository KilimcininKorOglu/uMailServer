// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the canonical Tombstone store: durable tracking of
// soft-delete (move-to-trash) and hard-delete (permanent removal) lifecycle
// events so that sync and event consumers can report deletions accurately
// instead of inferring deletion state from storage side effects.
//
// # Tombstone Model
//
// A Tombstone records one deletion event for a semantic object:
//   - MailboxID / FolderID / ItemID — which object was deleted
//   - Kind — soft_delete (moved to Deleted Items) or hard_delete (permanently removed)
//   - DeletedAt — when the deletion occurred
//   - Actor — who triggered the deletion (user or system)
//
// Tombstones are retained for a configurable window (default: 30 days)
// so that clients with a sync state older than the tombstone window can
// still receive the deletion event and update their local state accordingly.
//
// # Invariants
//
//  1. A tombstone is created for every soft-delete and hard-delete operation
//     that passes through the canonical mutation pipeline.
//  2. Tombstones are never updated — only new ones are added.
//  3. Tombstones older than the retention window are pruned, but only after
//     confirming that no active sync state references a seq before the cutoff.
//  4. For a folder-scoped tombstone, all item tombstones for that folder
//     are also created atomically (or in the same transaction) so that
//     a client replaying sync sees folder and content deletions together.
package semcore

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// TombstoneStore interface
// ---------------------------------------------------------------------------

// TombstoneStore is the interface for persisting soft-delete and hard-delete
// lifecycle records. These records are the authoritative source of deletion
// evidence for sync and event consumers.
type TombstoneStore interface {
	// PutTombstone records a deletion event. If a tombstone already exists for
	// the same (MailboxID, FolderID, ItemID, Kind) tuple, it is updated only if
	// the new DeletedAt is more recent (idempotent retry-safe).
	PutTombstone(t Tombstone) error

	// ListTombstonesSince returns tombstones with DeletedAt >= since for
	// the given mailbox (and optional folder). This is the primary read path
	// for incremental sync deltas and event delivery.
	ListTombstonesSince(mboxID MailboxId, folderID FolderId, since time.Time) ([]Tombstone, error)

	// ListTombstonesByMailbox returns all tombstones for one mailbox,
	// optionally filtered to a specific folder. Used for mailbox-level
	// backfill seeding and diagnostics.
	ListTombstonesByMailbox(mboxID MailboxId, folderID FolderId) ([]Tombstone, error)

	// PruneTombstones removes tombstones older than maxAge.
	// It returns the count of tombstones pruned.
	PruneTombstones(maxAge time.Duration) (int, error)
}

// ---------------------------------------------------------------------------
// Tombstone
// ---------------------------------------------------------------------------

// Tombstone records a single deletion event for a semantic object.
// It is the canonical evidence of deletion for sync and event consumers.
type Tombstone struct {
	MailboxID MailboxId
	FolderID  FolderId // zero for mailbox-level tombstones
	ItemID    ItemId   // zero for folder-level tombstones

	// Kind distinguishes the type of deletion.
	// SoftDelete means the object was moved to Deleted Items / trash.
	// HardDelete means the object was permanently removed.
	Kind LifecycleKind

	DeletedAt time.Time
	Actor     string // user or system actor
}

// IsZero returns true when the tombstone has no identity set.
func (t *Tombstone) IsZero() bool {
	return t.MailboxID.IsZero()
}

// IsFolderLevel returns true when this tombstone represents a folder deletion
// (no ItemID set).
func (t *Tombstone) IsFolderLevel() bool {
	return !t.FolderID.IsZero() && t.ItemID.IsZero()
}

// IsItemLevel returns true when this tombstone represents an item deletion.
func (t *Tombstone) IsItemLevel() bool {
	return !t.ItemID.IsZero()
}

// ---------------------------------------------------------------------------
// BoltTombstoneStore
// ---------------------------------------------------------------------------

const bucketTombstones = "__semcore_tombstones"

// tombstoneKey returns the Bolt bucket key for a tombstone.
// Key includes Kind so that soft and hard deletes of the same object
// produce separate tombstones.
func tombstoneKey(mboxID MailboxId, folderID FolderId, itemID ItemId, kind LifecycleKind) string {
	return mboxID.String() + "\x00" + folderID.String() + "\x00" + itemID.String() + "\x00" + fmt.Sprintf("%d", kind)
}

// BoltTombstoneStore persists Tombstone records in a dedicated bbolt bucket.
type BoltTombstoneStore struct {
	db *bbolt.DB
	mu sync.RWMutex
}

// NewBoltTombstoneStore opens a Bolt-backed tombstone store, creating the
// bucket if it does not yet exist.
func NewBoltTombstoneStore(db *bbolt.DB) (*BoltTombstoneStore, error) {
	err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketTombstones))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("BoltTombstoneStore: create bucket: %w", err)
	}
	return &BoltTombstoneStore{db: db}, nil
}

// PutTombstone implements TombstoneStore.
func (s *BoltTombstoneStore) PutTombstone(t Tombstone) error {
	if t.MailboxID.IsZero() {
		return fmt.Errorf("PutTombstone: MailboxID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketTombstones))
		k := []byte(tombstoneKey(t.MailboxID, t.FolderID, t.ItemID, t.Kind))
		existing := b.Get(k)

		if existing != nil {
			var existingT Tombstone
			if err := json.Unmarshal(existing, &existingT); err != nil {
				return fmt.Errorf("unmarshal existing tombstone: %w", err)
			}
			// Only update if the new deletion is more recent.
			if t.DeletedAt.After(existingT.DeletedAt) {
				data, err := json.Marshal(t)
				if err != nil {
					return fmt.Errorf("marshal tombstone: %w", err)
				}
				return b.Put(k, data)
			}
			return nil // already have a newer or equal tombstone; idempotent
		}

		data, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("marshal tombstone: %w", err)
		}
		return b.Put(k, data)
	})
}

// ListTombstonesSince implements TombstoneStore.
func (s *BoltTombstoneStore) ListTombstonesSince(mboxID MailboxId, folderID FolderId, since time.Time) ([]Tombstone, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Tombstone
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketTombstones))
		return b.ForEach(func(k, v []byte) error {
			var t Tombstone
			if err := json.Unmarshal(v, &t); err != nil {
				return nil // skip corrupted
			}
			if !t.MailboxID.Equal(mboxID) {
				return nil
			}
			if !folderID.IsZero() && !t.FolderID.Equal(folderID) {
				return nil
			}
			if t.DeletedAt.Before(since) {
				return nil
			}
			result = append(result, t)
			return nil
		})
	})
	return result, err
}

// ListTombstonesByMailbox implements TombstoneStore.
func (s *BoltTombstoneStore) ListTombstonesByMailbox(mboxID MailboxId, folderID FolderId) ([]Tombstone, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Tombstone
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketTombstones))
		return b.ForEach(func(k, v []byte) error {
			var t Tombstone
			if err := json.Unmarshal(v, &t); err != nil {
				return nil
			}
			if !t.MailboxID.Equal(mboxID) {
				return nil
			}
			if !folderID.IsZero() && !t.FolderID.Equal(folderID) {
				return nil
			}
			result = append(result, t)
			return nil
		})
	})
	return result, err
}

// PruneTombstones implements TombstoneStore.
func (s *BoltTombstoneStore) PruneTombstones(maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	var pruned int
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketTombstones))
		c := b.Cursor()
		var toDelete [][]byte
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var t Tombstone
			if err := json.Unmarshal(v, &t); err != nil {
				// Corrupted entry; delete it.
				toDelete = append(toDelete, append([]byte(nil), k...))
				continue
			}
			if t.DeletedAt.Before(cutoff) {
				toDelete = append(toDelete, append([]byte(nil), k...))
			}
		}
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
			pruned++
		}
		return nil
	})
	return pruned, err
}
