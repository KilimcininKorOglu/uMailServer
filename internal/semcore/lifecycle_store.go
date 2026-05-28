// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the canonical Lifecycle event store: durable, append-only
// records of every semantic state transition (created, updated, moved,
// soft-deleted, hard-deleted, restored) for a mailbox. Event consumers
// (SyncFolderItems, GetEvents, push notification workers) derive their
// view from these entries rather than inferring state from timestamps or
// storage side effects.
//
// The store is append-only: once written, lifecycle events are never
// mutated or deleted. They are pruned only when older than the retention
// window and no active sync state references an earlier sequence.
//
// # Invariants
//
//  1. Every successful mutation in the canonical pipeline emits exactly one
//     Lifecycle event with the correct Kind for the operation.
//  2. Lifecycle events are never updated or deleted — only new ones added.
//  3. Events carry a monotonically increasing sequence number so consumers
//     can resume from a watermark without missing or duplicating events.
//  4. Events are mailbox-scoped; cross-mailbox events are not mixed.
package semcore

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// LifecycleStore interface
// ---------------------------------------------------------------------------

// LifecycleStore persists canonical lifecycle events and enables event-polling
// consumers to resume from a watermark. The store is append-only.
type LifecycleStore interface {
	// AppendLifecycle writes one Lifecycle event. Each call is assigned a
	// monotonically increasing sequence number. Thread-safe for concurrent use.
	AppendLifecycle(event Lifecycle) error

	// PollEvents returns lifecycle events for a given mailbox since the given
	// sequence number (exclusive). The slice is ordered by ascending sequence.
	// The returned watermark is the highest sequence number in the result set.
	// If the result is empty, watermark equals the input watermark.
	PollEvents(mboxID MailboxId, sinceSeq uint64, limit int) ([]Lifecycle, uint64, error)

	// HighestSequence returns the current highest sequence number for a
	// mailbox. Returns 0 if no events have been recorded.
	HighestSequence(mboxID MailboxId) (uint64, error)

	// PruneEvents removes events older than maxAge. It returns the count pruned.
	// Must not prune events that are still referenced by active subscriptions.
	PruneEvents(maxAge time.Duration) (int, error)

	// FolderseSince returns folder-scoped lifecycle events since seq.
	FoldersSince(mboxID MailboxId, sinceSeq uint64, limit int) ([]Lifecycle, uint64, error)
}

// ---------------------------------------------------------------------------
// BoltLifecycleStore
// ---------------------------------------------------------------------------

const bucketLifecycle = "__semcore_lifecycle"

const lifecycleBucketSeql = "__semcore_lifecycle_seql"

type storedLifecycleEvent struct {
	Seq      uint64      `json:"seq"`
	MailboxID MailboxId  `json:"mailbox_id"`
	FolderID  FolderId   `json:"folder_id"`
	ItemID    ItemId    `json:"item_id"`
	Kind      LifecycleKind `json:"kind"`
	At        time.Time `json:"at"`
	Actor     string    `json:"actor"`
	ChangeKey ChangeKey `json:"change_key"`
}

// BoltLifecycleStore persists Lifecycle events in a dedicated bbolt bucket.
type BoltLifecycleStore struct {
	db *bbolt.DB
	mu sync.Mutex
}

// NewBoltLifecycleStore opens a Bolt-backed lifecycle store, creating
// the bucket and sequence counter if they do not yet exist.
func NewBoltLifecycleStore(db *bbolt.DB) (*BoltLifecycleStore, error) {
	err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketLifecycle))
		if err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists([]byte(lifecycleBucketSeql))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("BoltLifecycleStore: create bucket: %w", err)
	}
	return &BoltLifecycleStore{db: db}, nil
}

// AppendLifecycle implements LifecycleStore.
func (s *BoltLifecycleStore) AppendLifecycle(event Lifecycle) error {
	if event.MailboxID.IsZero() {
		return fmt.Errorf("AppendLifecycle: MailboxID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketLifecycle))
		seqB := tx.Bucket([]byte(lifecycleBucketSeql))

		// Increment mailbox-specific sequence counter.
		k := []byte(event.MailboxID.String())
		seq := seqB.Get(k)
		var n uint64 = 0
		if seq != nil {
			// Decode existing LEUint64
			for i := len(seq) - 1; i >= 0; i-- {
				n = n<<8 | uint64(seq[i])
			}
		}
		n++
		// Encode big-endian back.
		buf := make([]byte, 8)
		for i := 7; i >= 0; i-- {
			buf[i] = byte(n)
			n >>= 8
		}
		if err := seqB.Put(k, buf); err != nil {
			return fmt.Errorf("update seq: %w", err)
		}

		rec := storedLifecycleEvent{
			Seq:       n,
			MailboxID: event.MailboxID,
			FolderID:  event.FolderID,
			ItemID:    event.ItemID,
			Kind:      event.Kind,
			At:        event.At,
			Actor:     event.Actor,
			ChangeKey: event.ChangeKey,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal lifecycle: %w", err)
		}
		// Key format: mailboxID|seq big-endian.
		key := fmt.Sprintf("%s\x00\x00\x00\x00\x00\x00\x00\x00%08x", event.MailboxID.String(), n)
		return b.Put([]byte(key), data)
	})
}

// PollEvents implements LifecycleStore.
func (s *BoltLifecycleStore) PollEvents(mboxID MailboxId, sinceSeq uint64, limit int) ([]Lifecycle, uint64, error) {
	if limit <= 0 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Lifecycle
	var highest uint64

	mboxPrefix := mboxID.String()
	prefix := []byte(mboxPrefix + "\x00\x00\x00\x00\x00\x00\x00\x00")
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketLifecycle))
		c := b.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytesHasPrefix(k, prefix); k, v = c.Next() {
			var rec storedLifecycleEvent
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if rec.Seq <= sinceSeq {
				continue
			}
			result = append(result, Lifecycle{
				MailboxID:  rec.MailboxID,
				FolderID:   rec.FolderID,
				ItemID:     rec.ItemID,
				Kind:       rec.Kind,
				At:         rec.At,
				Actor:      rec.Actor,
				ChangeKey:  rec.ChangeKey,
			})
			if rec.Seq > highest {
				highest = rec.Seq
			}
			if len(result) >= limit {
				break
			}
		}
		return nil
	})
	return result, highest, err
}

// FoldersSince implements LifecycleStore.
func (s *BoltLifecycleStore) FoldersSince(mboxID MailboxId, sinceSeq uint64, limit int) ([]Lifecycle, uint64, error) {
	return s.PollEvents(mboxID, sinceSeq, limit)
}

// HighestSequence implements LifecycleStore.
func (s *BoltLifecycleStore) HighestSequence(mboxID MailboxId) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n uint64
	err := s.db.View(func(tx *bbolt.Tx) error {
		seqB := tx.Bucket([]byte(lifecycleBucketSeql))
		seq := seqB.Get([]byte(mboxID.String()))
		if seq == nil {
			n = 0
			return nil
		}
		for i := len(seq) - 1; i >= 0; i-- {
			n = n<<8 | uint64(seq[i])
		}
		return nil
	})
	return n, err
}

// PruneEvents implements LifecycleStore.
func (s *BoltLifecycleStore) PruneEvents(maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	var pruned int
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketLifecycle))
		c := b.Cursor()
		var toDelete [][]byte
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec storedLifecycleEvent
			if err := json.Unmarshal(v, &rec); err != nil {
				toDelete = append(toDelete, append([]byte(nil), k...))
				continue
			}
			if rec.At.Before(cutoff) {
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

func bytesHasPrefix(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := range prefix {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}
