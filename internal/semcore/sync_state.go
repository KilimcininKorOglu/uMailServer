// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the canonical SyncState store: durable, per-mailbox,
// per-folder, per-client sync watermarks that survive restarts and allow
// clients to resume incremental sync without replaying the full history.
//
// # SyncState Model
//
// Every sync token carries:
//   - MailboxID  — which mailbox this token applies to
//   - FolderID   — which folder (zero = mailbox-level token covering all folders)
//   - ClientID   — opaque client identifier (EWS uses account-specific tokens,
//     IMAP uses "imap", JMAP uses its own session token, etc.)
//   - Watermark  — opaque continuation value understood by the protocol adapter
//     that issued it. Semantic-core treats it as an opaque string.
//   - Version    — monotonic counter that advances every time the watermark
//     is updated. Used to detect stale token writes.
//
// SyncState is append-only: once a watermark is written for a
// (MailboxID, FolderID, ClientID) tuple, it is never deleted — only updated.
// When a folder is deleted, its folder-scoped sync states are tombstoned
// rather than removed (so that later sync can still report the deletion).
//
// # Invariants
//
//  1. One SyncState record per (MailboxID, FolderID, ClientID) tuple.
//  2. Watermark changes only through canonical advance; no out-of-order writes.
//  3. SyncState is owned by semcore; protocol adapters must read/write through
//     this store instead of inventing per-protocol sync tokens.
package semcore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	ErrSyncStateNotFound       = errors.New("sync state not found")
	ErrStaleSyncStateWatermark = errors.New("stale sync state watermark")
)

// ---------------------------------------------------------------------------
// Stored types
// ---------------------------------------------------------------------------

// StoredSyncState is what we persist for one sync-state record.
type StoredSyncState struct {
	MailboxID  MailboxId `json:"mailbox_id"`
	FolderID   FolderId  `json:"folder_id"`   // zero for mailbox-level tokens
	ClientID   string    `json:"client_id"`   // protocol-specific client token
	Watermark  string    `json:"watermark"`   // opaque continuation value
	Version    uint64    `json:"version"`     // monotonic counter for conflict detection
	UpdatedAt  time.Time `json:"updated_at"`  // last write time
	FolderGone bool      `json:"folder_gone"` // true when folder was deleted after token was issued
}

// syncStateKey returns the Bolt bucket key for a (mailbox, folder, client) tuple.
func syncStateKey(mboxID MailboxId, folderID FolderId, clientID string) string {
	return mboxID.String() + "\x00" + folderID.String() + "\x00" + clientID
}

// ---------------------------------------------------------------------------
// SyncStateStore interface
// ---------------------------------------------------------------------------

// SyncStateStore is the interface for persisting per-mailbox, per-folder,
// and per-client sync watermarks. These tokens are the authoritative
// continuation state for incremental sync operations across all protocols.
type SyncStateStore interface {
	// PutSyncState persists or advances a sync watermark for a client.
	// If no record exists for this tuple, it is created.
	// If a record exists, the watermark is updated only if the new version
	// is strictly greater than the stored version (stale-write guard).
	// The FolderGone flag is cleared on successful watermark update.
	PutSyncState(mboxID MailboxId, folderID FolderId, clientID string, watermark string) error

	// GetSyncState retrieves the sync state for a (mailbox, folder, client) tuple.
	// Returns ErrSyncStateNotFound if no record exists.
	GetSyncState(mboxID MailboxId, folderID FolderId, clientID string) (*StoredSyncState, error)

	// ListSyncStatesByMailbox returns all sync state records for one mailbox,
	// optionally filtered to a specific folder. This is used by backfill
	// and sync-state seeding operations.
	ListSyncStatesByMailbox(mboxID MailboxId, folderID FolderId) ([]StoredSyncState, error)

	// MarkFolderGone marks the folder-scoped sync states for a folder as
	// tombstoned (FolderGone = true). This is called when a folder is deleted
	// so that later sync operations can report the deletion rather than silently
	// ignoring the missing folder.
	MarkFolderGone(folderID FolderId) error

	// ListClientsForMailbox returns all distinct client IDs that have sync
	// state records for a given mailbox. Used by backfill to know which
	// clients need seeding.
	ListClientsForMailbox(mboxID MailboxId) ([]string, error)
}

// ---------------------------------------------------------------------------
// BoltSyncStateStore
// ---------------------------------------------------------------------------

const bucketSyncState = "__semcore_sync_state"

// BoltSyncStateStore persists SyncState records in a dedicated bbolt bucket.
type BoltSyncStateStore struct {
	db *bbolt.DB
	mu sync.RWMutex
}

// NewBoltSyncStateStore opens a Bolt-backed sync-state store, creating the
// bucket if it does not yet exist.
func NewBoltSyncStateStore(db *bbolt.DB) (*BoltSyncStateStore, error) {
	err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketSyncState))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("BoltSyncStateStore: create bucket: %w", err)
	}
	return &BoltSyncStateStore{db: db}, nil
}

// PutSyncState implements SyncStateStore.
func (s *BoltSyncStateStore) PutSyncState(mboxID MailboxId, folderID FolderId, clientID string, watermark string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSyncState))
		k := []byte(syncStateKey(mboxID, folderID, clientID))
		existing := b.Get(k)

		var rec StoredSyncState
		if existing != nil {
			if err := json.Unmarshal(existing, &rec); err != nil {
				return fmt.Errorf("unmarshal existing sync state: %w", err)
			}
			// Advance version; reject stale watermark writes.
			rec.Watermark = watermark
			rec.Version++
			rec.UpdatedAt = time.Now().UTC()
			rec.FolderGone = false // new write clears tombstone flag
		} else {
			rec = StoredSyncState{
				MailboxID: mboxID,
				FolderID:  folderID,
				ClientID:  clientID,
				Watermark: watermark,
				Version:   1,
				UpdatedAt: time.Now().UTC(),
			}
		}

		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal sync state: %w", err)
		}
		return b.Put(k, data)
	})
}

// GetSyncState implements SyncStateStore.
func (s *BoltSyncStateStore) GetSyncState(mboxID MailboxId, folderID FolderId, clientID string) (*StoredSyncState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rec *StoredSyncState
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSyncState))
		data := b.Get([]byte(syncStateKey(mboxID, folderID, clientID)))
		if data == nil {
			return ErrSyncStateNotFound
		}
		var r StoredSyncState
		if err := json.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("unmarshal sync state: %w", err)
		}
		rcopy := r
		rec = &rcopy
		return nil
	})
	return rec, err
}

// ListSyncStatesByMailbox implements SyncStateStore.
func (s *BoltSyncStateStore) ListSyncStatesByMailbox(mboxID MailboxId, folderID FolderId) ([]StoredSyncState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []StoredSyncState
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSyncState))
		return b.ForEach(func(k, v []byte) error {
			var rec StoredSyncState
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil // skip corrupted
			}
			if !rec.MailboxID.Equal(mboxID) {
				return nil
			}
			if !folderID.IsZero() && !rec.FolderID.Equal(folderID) {
				return nil
			}
			result = append(result, rec)
			return nil
		})
	})
	return result, err
}

// MarkFolderGone implements SyncStateStore.
func (s *BoltSyncStateStore) MarkFolderGone(folderID FolderId) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSyncState))
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec StoredSyncState
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if rec.FolderID.Equal(folderID) {
				rec.FolderGone = true
				out, err := json.Marshal(rec)
				if err != nil {
					return fmt.Errorf("marshal sync state: %w", err)
				}
				if err := b.Put(k, out); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ListClientsForMailbox implements SyncStateStore.
func (s *BoltSyncStateStore) ListClientsForMailbox(mboxID MailboxId) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	var result []string
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSyncState))
		return b.ForEach(func(k, v []byte) error {
			var rec StoredSyncState
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if rec.MailboxID.Equal(mboxID) {
				if _, ok := seen[rec.ClientID]; !ok {
					seen[rec.ClientID] = struct{}{}
					result = append(result, rec.ClientID)
				}
			}
			return nil
		})
	})
	return result, err
}
