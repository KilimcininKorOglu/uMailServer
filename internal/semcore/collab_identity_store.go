// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides Bolt-backed persistence for collaboration identities:
// CalendarItemId, ContactId, TaskId, and their associated version tokens
// (CalendarChangeKey, ContactChangeKey, TaskChangeKey).
//
// The collaboration store is part of the same bbolt database as the mail
// identity store, but uses separate buckets so each can be iterated
// independently. The bucket naming follows the established __semcore_ prefix
// convention.
//
// # Canonical Collaboration Identity
//
// All CalDAV, CardDAV, and future EWS collaboration surfaces must use
// these identities instead of filesystem paths, UID values, or mtimes.
// The ETag returned to clients is derived from CalendarChangeKey / ContactChangeKey
// / TaskChangeKey, NOT from filesystem mtime.
//
// # Version Conflict Detection
//
// Before updating a collaboration object, callers must pass the current
// ChangeKey. If the stored ChangeKey differs, the update must be rejected
// with an explicit version-conflict error (HTTP 409 or EWS ErrorServerBusy).
// This mirrors the RFC 4791 / RFC 6352 ETags-over-mtime requirement.
package semcore

import (
	"encoding/json"
	"errors"
	"fmt"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// Collaboration store errors
// ---------------------------------------------------------------------------

var (
	ErrCalendarItemNotFound  = errors.New("calendar item identity not found")
	ErrContactNotFound       = errors.New("contact identity not found")
	ErrTaskNotFound          = errors.New("task identity not found")
	ErrCollabVersionConflict = errors.New("collaboration version conflict: stale change key")
)

// ---------------------------------------------------------------------------
// bucket names (additive — does not replace mail identity buckets)
// ---------------------------------------------------------------------------

const (
	bucketCalendarItem = "__semcore_calitem"
	bucketContact      = "__semcore_contact"
	bucketTask         = "__semcore_task"
)

// ---------------------------------------------------------------------------
// Stored collaboration records
// ---------------------------------------------------------------------------

// StoredCalendarItemIdentity is what we persist for a canonical CalendarItemId.
type StoredCalendarItemIdentity struct {
	ID        CalendarItemId
	MasterID  CalendarItemId // zero for master; set for exceptions
	FolderID  FolderId
	MailboxID MailboxId
	ChangeKey CalendarChangeKey
	Kind      CollabKind
	IcalUID   string
	RawHash   string // content hash of RawICal for content-change detection
	ETag      string // precomputed ETag (ChangeKey + RawHash)
}

// NewStoredCalendarItemIdentity constructs a StoredCalendarItemIdentity for use by
// protocol adapters (EWS, CalDAV, CardDAV) that need to register collaboration
// identities from wire-format data.
func NewStoredCalendarItemIdentity(id CalendarItemId, folderID FolderId, mailboxID MailboxId, ck CalendarChangeKey, kind CollabKind, icalUID, rawHash string) *StoredCalendarItemIdentity {
	return &StoredCalendarItemIdentity{
		ID: id, FolderID: folderID, MailboxID: mailboxID,
		ChangeKey: ck, Kind: kind, IcalUID: icalUID, RawHash: rawHash,
	}
}

// StoredContactIdentity is what we persist for a canonical ContactId.
type StoredContactIdentity struct {
	ID        ContactId
	FolderID  FolderId
	MailboxID MailboxId
	ChangeKey ContactChangeKey
	IcalUID   string
	RawHash   string
	ETag      string
}

// NewStoredContactIdentity constructs a StoredContactIdentity for use by protocol adapters.
func NewStoredContactIdentity(id ContactId, folderID FolderId, mailboxID MailboxId, ck ContactChangeKey, icalUID, rawHash string) *StoredContactIdentity {
	return &StoredContactIdentity{ID: id, FolderID: folderID, MailboxID: mailboxID, ChangeKey: ck, IcalUID: icalUID, RawHash: rawHash}
}

// StoredTaskIdentity is what we persist for a canonical TaskId.
type StoredTaskIdentity struct {
	ID        TaskId
	FolderID  FolderId
	MailboxID MailboxId
	ChangeKey TaskChangeKey
	IcalUID   string
	RawHash   string
	ETag      string
}

// NewStoredTaskIdentity constructs a StoredTaskIdentity for use by protocol adapters.
func NewStoredTaskIdentity(id TaskId, folderID FolderId, mailboxID MailboxId, ck TaskChangeKey, icalUID, rawHash string) *StoredTaskIdentity {
	return &StoredTaskIdentity{ID: id, FolderID: folderID, MailboxID: mailboxID, ChangeKey: ck, IcalUID: icalUID, RawHash: rawHash}
}

// ---------------------------------------------------------------------------
// BoltCollaborationStore
// ---------------------------------------------------------------------------

// BoltCollaborationStore provides Bolt-backed persistence for collaboration
// identities (calendar items, contacts, tasks) and their version tokens.
// It is safe for concurrent use.
type BoltCollaborationStore struct {
	db *bbolt.DB
}

// NewBoltCollaborationStore opens the collaboration store using the shared
// bbolt database. It creates the collaboration buckets if they do not exist yet.
func NewBoltCollaborationStore(db *bbolt.DB) (*BoltCollaborationStore, error) {
	fn := func(tx *bbolt.Tx) error {
		for _, b := range []string{
			bucketCalendarItem,
			bucketContact,
			bucketTask,
		} {
			if _, err := tx.CreateBucketIfNotExists([]byte(b)); err != nil {
				return fmt.Errorf("create bucket %s: %w", b, err)
			}
		}
		return nil
	}
	if err := db.Update(fn); err != nil {
		return nil, fmt.Errorf("BoltCollaborationStore: init buckets: %w", err)
	}
	return &BoltCollaborationStore{db: db}, nil
}

// ---------------------------------------------------------------------------
// Calendar item identity operations
// ---------------------------------------------------------------------------

// PutCalendarItemIdentity registers (or updates) a calendar item identity.
// If replacing an existing identity, the caller MUST provide currentChangeKey
// for optimistic locking. Pass zero CalendarChangeKey{} for new inserts.
func (s *BoltCollaborationStore) PutCalendarItemIdentity(msgKey string, rec *StoredCalendarItemIdentity, currentChangeKey CalendarChangeKey) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketCalendarItem))
		existing := bkt.Get([]byte(msgKey))

		if existing != nil {
			// Update path: must check version.
			if currentChangeKey.IsZero() {
				return fmt.Errorf("PutCalendarItemIdentity: update requires currentChangeKey")
			}
			var stored StoredCalendarItemIdentity
			if err := json.Unmarshal(existing, &stored); err != nil {
				return fmt.Errorf("PutCalendarItemIdentity: unmarshal existing: %w", err)
			}
			if !stored.ChangeKey.Equal(currentChangeKey) {
				return ErrCollabVersionConflict
			}
		}

		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("PutCalendarItemIdentity: marshal: %w", err)
		}
		return bkt.Put([]byte(msgKey), data)
	})
}

// GetCalendarItemIdentity retrieves a calendar item identity by its storage key.
// The msgKey is typically the blob key (SHA256 of the raw iCal content).
func (s *BoltCollaborationStore) GetCalendarItemIdentity(msgKey string) (*StoredCalendarItemIdentity, error) {
	var out *StoredCalendarItemIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketCalendarItem))
		data := bkt.Get([]byte(msgKey))
		if data == nil {
			return ErrCalendarItemNotFound
		}
		var rec StoredCalendarItemIdentity
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("GetCalendarItemIdentity: unmarshal: %w", err)
		}
		out = &rec
		return nil
	})
	return out, err
}

// GetCalendarItemByID retrieves a calendar item identity by its CalendarItemId.
// This requires iterating over the bucket; use msgKey lookups for hot paths.
func (s *BoltCollaborationStore) GetCalendarItemByID(id CalendarItemId) (*StoredCalendarItemIdentity, error) {
	var out *StoredCalendarItemIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketCalendarItem))
		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec StoredCalendarItemIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if rec.ID.Equal(id) {
				out = &rec
				return nil
			}
		}
		return ErrCalendarItemNotFound
	})
	return out, err
}

// ListCalendarItemsByFolder returns all calendar item identities in a folder.
func (s *BoltCollaborationStore) ListCalendarItemsByFolder(folderID FolderId) ([]StoredCalendarItemIdentity, error) {
	var out []StoredCalendarItemIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketCalendarItem))
		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec StoredCalendarItemIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if rec.FolderID.Equal(folderID) {
				out = append(out, rec)
			}
		}
		return nil
	})
	return out, err
}

// DeleteCalendarItemIdentity removes a calendar item identity.
// Caller MUST pass currentChangeKey for optimistic locking.
func (s *BoltCollaborationStore) DeleteCalendarItemIdentity(msgKey string, currentChangeKey CalendarChangeKey) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketCalendarItem))
		existing := bkt.Get([]byte(msgKey))
		if existing == nil {
			return ErrCalendarItemNotFound
		}
		var stored StoredCalendarItemIdentity
		if err := json.Unmarshal(existing, &stored); err != nil {
			return fmt.Errorf("DeleteCalendarItemIdentity: unmarshal: %w", err)
		}
		if !stored.ChangeKey.Equal(currentChangeKey) {
			return ErrCollabVersionConflict
		}
		return bkt.Delete([]byte(msgKey))
	})
}

// PutCalendarItemIdentityUnsafe is like PutCalendarItemIdentity but skips
// version checking. Use only for trusted initialization or migration code.
func (s *BoltCollaborationStore) PutCalendarItemIdentityUnsafe(msgKey string, rec *StoredCalendarItemIdentity) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketCalendarItem))
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("PutCalendarItemIdentityUnsafe: marshal: %w", err)
		}
		return bkt.Put([]byte(msgKey), data)
	})
}

// ---------------------------------------------------------------------------
// Contact identity operations
// ---------------------------------------------------------------------------

// PutContactIdentity registers or updates a contact identity.
// Caller MUST provide currentChangeKey for updates (zero for inserts).
func (s *BoltCollaborationStore) PutContactIdentity(msgKey string, rec *StoredContactIdentity, currentChangeKey ContactChangeKey) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketContact))
		existing := bkt.Get([]byte(msgKey))

		if existing != nil {
			if currentChangeKey.IsZero() {
				return fmt.Errorf("PutContactIdentity: update requires currentChangeKey")
			}
			var stored StoredContactIdentity
			if err := json.Unmarshal(existing, &stored); err != nil {
				return fmt.Errorf("PutContactIdentity: unmarshal existing: %w", err)
			}
			if !stored.ChangeKey.Equal(currentChangeKey) {
				return ErrCollabVersionConflict
			}
		}

		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("PutContactIdentity: marshal: %w", err)
		}
		return bkt.Put([]byte(msgKey), data)
	})
}

// GetContactIdentity retrieves a contact identity by its storage key.
func (s *BoltCollaborationStore) GetContactIdentity(msgKey string) (*StoredContactIdentity, error) {
	var out *StoredContactIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketContact))
		data := bkt.Get([]byte(msgKey))
		if data == nil {
			return ErrContactNotFound
		}
		var rec StoredContactIdentity
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("GetContactIdentity: unmarshal: %w", err)
		}
		out = &rec
		return nil
	})
	return out, err
}

// GetContactByID retrieves a contact identity by its ContactId.
func (s *BoltCollaborationStore) GetContactByID(id ContactId) (*StoredContactIdentity, error) {
	var out *StoredContactIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketContact))
		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec StoredContactIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if rec.ID.Equal(id) {
				out = &rec
				return nil
			}
		}
		return ErrContactNotFound
	})
	return out, err
}

// ListContactsByFolder returns all contact identities in a folder.
func (s *BoltCollaborationStore) ListContactsByFolder(folderID FolderId) ([]StoredContactIdentity, error) {
	var out []StoredContactIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketContact))
		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec StoredContactIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if rec.FolderID.Equal(folderID) {
				out = append(out, rec)
			}
		}
		return nil
	})
	return out, err
}

// DeleteContactIdentity removes a contact identity.
// Caller MUST pass currentChangeKey for optimistic locking.
func (s *BoltCollaborationStore) DeleteContactIdentity(msgKey string, currentChangeKey ContactChangeKey) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketContact))
		existing := bkt.Get([]byte(msgKey))
		if existing == nil {
			return ErrContactNotFound
		}
		var stored StoredContactIdentity
		if err := json.Unmarshal(existing, &stored); err != nil {
			return fmt.Errorf("DeleteContactIdentity: unmarshal: %w", err)
		}
		if !stored.ChangeKey.Equal(currentChangeKey) {
			return ErrCollabVersionConflict
		}
		return bkt.Delete([]byte(msgKey))
	})
}

// PutContactIdentityUnsafe skips version checking. Use only for migration/trusted init.
func (s *BoltCollaborationStore) PutContactIdentityUnsafe(msgKey string, rec *StoredContactIdentity) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketContact))
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("PutContactIdentityUnsafe: marshal: %w", err)
		}
		return bkt.Put([]byte(msgKey), data)
	})
}

// ---------------------------------------------------------------------------
// Task identity operations
// ---------------------------------------------------------------------------

// PutTaskIdentity registers or updates a task identity.
// Caller MUST provide currentChangeKey for updates (zero for inserts).
func (s *BoltCollaborationStore) PutTaskIdentity(msgKey string, rec *StoredTaskIdentity, currentChangeKey TaskChangeKey) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketTask))
		existing := bkt.Get([]byte(msgKey))

		if existing != nil {
			if currentChangeKey.IsZero() {
				return fmt.Errorf("PutTaskIdentity: update requires currentChangeKey")
			}
			var stored StoredTaskIdentity
			if err := json.Unmarshal(existing, &stored); err != nil {
				return fmt.Errorf("PutTaskIdentity: unmarshal existing: %w", err)
			}
			if !stored.ChangeKey.Equal(currentChangeKey) {
				return ErrCollabVersionConflict
			}
		}

		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("PutTaskIdentity: marshal: %w", err)
		}
		return bkt.Put([]byte(msgKey), data)
	})
}

// GetTaskIdentity retrieves a task identity by its storage key.
func (s *BoltCollaborationStore) GetTaskIdentity(msgKey string) (*StoredTaskIdentity, error) {
	var out *StoredTaskIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketTask))
		data := bkt.Get([]byte(msgKey))
		if data == nil {
			return ErrTaskNotFound
		}
		var rec StoredTaskIdentity
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("GetTaskIdentity: unmarshal: %w", err)
		}
		out = &rec
		return nil
	})
	return out, err
}

// GetTaskByID retrieves a task identity by its TaskId.
func (s *BoltCollaborationStore) GetTaskByID(id TaskId) (*StoredTaskIdentity, error) {
	var out *StoredTaskIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketTask))
		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec StoredTaskIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if rec.ID.Equal(id) {
				out = &rec
				return nil
			}
		}
		return ErrTaskNotFound
	})
	return out, err
}

// ListTasksByFolder returns all task identities in a folder.
func (s *BoltCollaborationStore) ListTasksByFolder(folderID FolderId) ([]StoredTaskIdentity, error) {
	var out []StoredTaskIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketTask))
		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec StoredTaskIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if rec.FolderID.Equal(folderID) {
				out = append(out, rec)
			}
		}
		return nil
	})
	return out, err
}

// DeleteTaskIdentity removes a task identity.
// Caller MUST pass currentChangeKey for optimistic locking.
func (s *BoltCollaborationStore) DeleteTaskIdentity(msgKey string, currentChangeKey TaskChangeKey) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketTask))
		existing := bkt.Get([]byte(msgKey))
		if existing == nil {
			return ErrTaskNotFound
		}
		var stored StoredTaskIdentity
		if err := json.Unmarshal(existing, &stored); err != nil {
			return fmt.Errorf("DeleteTaskIdentity: unmarshal: %w", err)
		}
		if !stored.ChangeKey.Equal(currentChangeKey) {
			return ErrCollabVersionConflict
		}
		return bkt.Delete([]byte(msgKey))
	})
}

// PutTaskIdentityUnsafe skips version checking. Use only for migration/trusted init.
func (s *BoltCollaborationStore) PutTaskIdentityUnsafe(msgKey string, rec *StoredTaskIdentity) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bkt := tx.Bucket([]byte(bucketTask))
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("PutTaskIdentityUnsafe: marshal: %w", err)
		}
		return bkt.Put([]byte(msgKey), data)
	})
}
