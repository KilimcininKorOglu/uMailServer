package semcore

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// bucketDelegations is the bbolt bucket name for delegation records.
const bucketDelegations = "__semcore_delegations"

// ---------------------------------------------------------------------------
// DelegateStore interface
// ---------------------------------------------------------------------------

// DelegateStore is the interface for canonical delegation persistence.
// It manages Exchange-semantic delegate grants (per-folder permissions,
// meeting delivery mode, private-item visibility) separate from RFC 4314 ACLs.
type DelegateStore interface {
	// PutDelegate stores or updates a delegate grant. If a grant already exists
	// for the same owner+delegate, it is updated. Returns the assigned DelegateId.
	PutDelegate(delegate *DelegateUser) (DelegateId, error)

	// GetDelegate returns a delegate grant by DelegateId.
	GetDelegate(id DelegateId) (*DelegateUser, error)

	// ListDelegates returns all delegates for a mailbox owner.
	ListDelegates(ownerID MailboxId) ([]*DelegateUser, error)

	// GetDelegateForUser returns the delegate grant for a specific delegate on an owner mailbox.
	GetDelegateForUser(ownerID MailboxId, delegateEmail string) (*DelegateUser, error)

	// RemoveDelegate removes a delegate grant.
	RemoveDelegate(id DelegateId) error

	// ListMailboxesSharedViaDelegate returns all mailboxes accessible to a delegate
	// via a delegate grant (shared mailbox discovery via explicit grant).
	// This satisfies VAL-DIR-001: shared mailbox discovery requires an explicit grant.
	ListMailboxesSharedViaDelegate(delegateEmail string) ([]*DelegateUser, error)
}

// BoltDelegateStore persists delegation records in bbolt.
type BoltDelegateStore struct {
	db *bbolt.DB
	mu sync.RWMutex
}

// NewBoltDelegateStore creates a delegation store, creating the bucket if needed.
func NewBoltDelegateStore(db *bbolt.DB) (*BoltDelegateStore, error) {
	s := &BoltDelegateStore{db: db}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketDelegations))
		return err
	}); err != nil {
		return nil, fmt.Errorf("BoltDelegateStore: create bucket: %w", err)
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// Storage key strategy
// ---------------------------------------------------------------------------

// delegationKey formats the bucket key for a delegate grant.
// Key: "delegations:{owner_email}:{delegate_email}"
// This ensures one grant per owner+delegate pair.
func delegationKey(ownerEmail, delegateEmail string) string {
	return fmt.Sprintf("delegations:%s:%s", strings.ToLower(ownerEmail), strings.ToLower(delegateEmail))
}

// delegationByOwnerPrefix returns the prefix for listing all grants for an owner.
func delegationByOwnerPrefix(ownerEmail string) string {
	return fmt.Sprintf("delegations:%s:", strings.ToLower(ownerEmail))
}

// delegationByDelegatePrefix returns the prefix for listing all grants
// where the given email is the delegate.
func delegationByDelegatePrefix(delegateEmail string) string {
	return fmt.Sprintf("delegations:!%s:", strings.ToLower(delegateEmail))
}

// ---------------------------------------------------------------------------
// PutDelegate
// ---------------------------------------------------------------------------

// PutDelegate implements DelegateStore.
func (s *BoltDelegateStore) PutDelegate(delegate *DelegateUser) (DelegateId, error) {
	if delegate == nil {
		return DelegateId{}, fmt.Errorf("PutDelegate: nil delegate")
	}
	if delegate.OwnerID.IsZero() {
		return DelegateId{}, fmt.Errorf("PutDelegate: zero owner ID")
	}
	if delegate.DelegateEmail == "" {
		return DelegateId{}, fmt.Errorf("PutDelegate: empty delegate email")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := delegationKey(delegate.OwnerID.String(), delegate.DelegateEmail)

	var assignedID DelegateId
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketDelegations))

		// Check if this is an update (grant already exists).
		existing := b.Get([]byte(key))
		if existing != nil {
			var old DelegateUser
			if err := json.Unmarshal(existing, &old); err == nil {
				assignedID = old.ID
				delegate.ID = old.ID
				delegate.CreatedAt = old.CreatedAt
			}
		}

		// Assign new ID if creating.
		if assignedID.IsZero() {
			id, err := NewDelegateId("del-" + generateID())
			if err != nil {
				return fmt.Errorf("generate delegate ID: %w", err)
			}
			assignedID = id
			delegate.ID = id
			if delegate.CreatedAt.IsZero() {
				delegate.CreatedAt = timeNowUTC()
			}
		}

		delegate.UpdatedAt = timeNowUTC()

		data, err := json.Marshal(delegate)
		if err != nil {
			return fmt.Errorf("marshal delegate: %w", err)
		}

		// Store under owner→delegate key.
		if err := b.Put([]byte(key), data); err != nil {
			return fmt.Errorf("put delegate: %w", err)
		}

		// Also store under delegate reverse-lookup key so we can list
		// all mailboxes a delegate can access.
		// Key: "delegations:!{delegate}:{ownerID}"
		revKey := delegationByDelegatePrefix(delegate.DelegateEmail) + delegate.OwnerID.String()
		if err := b.Put([]byte(revKey), data); err != nil {
			return fmt.Errorf("put reverse delegate key: %w", err)
		}

		return nil
	})

	if err != nil {
		return DelegateId{}, err
	}
	return assignedID, nil
}

// ---------------------------------------------------------------------------
// GetDelegate
// ---------------------------------------------------------------------------

// GetDelegate implements DelegateStore.
func (s *BoltDelegateStore) GetDelegate(id DelegateId) (*DelegateUser, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("GetDelegate: zero ID")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var delegate *DelegateUser
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketDelegations))
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Skip reverse-lookup entries (delegations:!{delegate}:...).
			if strings.HasPrefix(string(k), "delegations:!") {
				continue
			}
			var d DelegateUser
			if err := json.Unmarshal(v, &d); err != nil {
				continue
			}
			if d.ID.Equal(id) {
				dcopy := d
				delegate = &dcopy
				return nil
			}
		}
		return fmt.Errorf("delegate not found: %s", id.String())
	})

	if err != nil {
		return nil, err
	}
	return delegate, nil
}

// ---------------------------------------------------------------------------
// ListDelegates
// ---------------------------------------------------------------------------

// ListDelegates implements DelegateStore.
func (s *BoltDelegateStore) ListDelegates(ownerID MailboxId) ([]*DelegateUser, error) {
	if ownerID.IsZero() {
		return nil, fmt.Errorf("ListDelegates: zero owner ID")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*DelegateUser
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketDelegations))
		prefix := delegationByOwnerPrefix(ownerID.String())

		c := b.Cursor()
		for k, v := c.Seek([]byte(prefix)); k != nil && strings.HasPrefix(string(k), prefix); k, v = c.Next() {
			var d DelegateUser
			if err := json.Unmarshal(v, &d); err != nil {
				continue
			}
			// Only include entries where the owner matches (reverse-lookup entries
			// have the delegate as the key prefix).
			if strings.HasPrefix(string(k), "delegations:!") {
				continue
			}
			result = append(result, &d)
		}
		return nil
	})

	return result, err
}

// ---------------------------------------------------------------------------
// GetDelegateForUser
// ---------------------------------------------------------------------------

// GetDelegateForUser implements DelegateStore.
func (s *BoltDelegateStore) GetDelegateForUser(ownerID MailboxId, delegateEmail string) (*DelegateUser, error) {
	if ownerID.IsZero() || delegateEmail == "" {
		return nil, fmt.Errorf("GetDelegateForUser: invalid args")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key := delegationKey(ownerID.String(), delegateEmail)
	var delegate *DelegateUser

	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketDelegations))
		data := b.Get([]byte(key))
		if data == nil {
			return fmt.Errorf("no delegate grant for %s on %s", delegateEmail, ownerID.String())
		}
		var d DelegateUser
		if err := json.Unmarshal(data, &d); err != nil {
			return fmt.Errorf("unmarshal delegate: %w", err)
		}
		delegate = &d
		return nil
	})

	return delegate, err
}

// ---------------------------------------------------------------------------
// RemoveDelegate
// ---------------------------------------------------------------------------

// RemoveDelegate implements DelegateStore.
func (s *BoltDelegateStore) RemoveDelegate(id DelegateId) error {
	if id.IsZero() {
		return fmt.Errorf("RemoveDelegate: zero ID")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketDelegations))
		c := b.Cursor()

		// Find and delete the grant (skip reverse-lookup entries).
		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Skip reverse-lookup entries (delegations:!{delegate}:...).
			if strings.HasPrefix(string(k), "delegations:!") {
				continue
			}
			var d DelegateUser
			if err := json.Unmarshal(v, &d); err != nil {
				continue
			}
			if d.ID.Equal(id) {
				if err := b.Delete(k); err != nil {
					return fmt.Errorf("delete delegate: %w", err)
				}
				// Also delete the reverse-lookup entry.
				revKey := delegationByDelegatePrefix(d.DelegateEmail) + d.OwnerID.String()
				//nolint:errcheck // best-effort
				_ = b.Delete([]byte(revKey))
				return nil
			}
		}
		return fmt.Errorf("delegate not found: %s", id.String())
	})
}

// ---------------------------------------------------------------------------
// ListMailboxesSharedViaDelegate
// ---------------------------------------------------------------------------

// ListMailboxesSharedViaDelegate implements DelegateStore.
// Satisfies VAL-DIR-001: shared mailbox discovery requires an explicit grant.
func (s *BoltDelegateStore) ListMailboxesSharedViaDelegate(delegateEmail string) ([]*DelegateUser, error) {
	if delegateEmail == "" {
		return nil, fmt.Errorf("ListMailboxesSharedViaDelegate: empty email")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*DelegateUser
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketDelegations))
		prefix := delegationByDelegatePrefix(delegateEmail)

		c := b.Cursor()
		for k, v := c.Seek([]byte(prefix)); k != nil && strings.HasPrefix(string(k), prefix); k, v = c.Next() {
			var d DelegateUser
			if err := json.Unmarshal(v, &d); err != nil {
				continue
			}
			// d is the complete DelegateUser record stored at the reverse-lookup
			// key (delegations:!{delegate}:{ownerID}). No re-read needed.
			result = append(result, &d)
		}
		return nil
	})

	return result, err
}

// ---------------------------------------------------------------------------
// ListAllDelegates
// ---------------------------------------------------------------------------

// ListAllDelegates returns every delegate grant across all mailbox owners.
// It scans only the forward owner->delegate keys, skipping the reverse-lookup
// entries (delegations:!{delegate}:...). Intended for admin surfaces that need
// a global view of delegations.
func (s *BoltDelegateStore) ListAllDelegates() ([]*DelegateUser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*DelegateUser
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketDelegations))
		return b.ForEach(func(k, v []byte) error {
			// Skip reverse-lookup entries (delegations:!{delegate}:...).
			if strings.HasPrefix(string(k), "delegations:!") {
				return nil
			}
			var d DelegateUser
			if err := json.Unmarshal(v, &d); err != nil {
				return nil // skip corrupted entries
			}
			dcopy := d
			result = append(result, &dcopy)
			return nil
		})
	})
	return result, err
}

// timeNowUTC is a variable so it can be overridden in tests.
var timeNowUTC = func() time.Time { return time.Now().UTC() }
