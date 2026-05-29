// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the canonical subscription store: durable tracking of
// event subscriptions so that polling clients can resume from a watermark
// event subscriptions so that polling clients can resume from a watermark
// without missing events, and canceled subscriptions are honored.
package semcore

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// SubscriptionStore interface
// ---------------------------------------------------------------------------

// SubscriptionStore manages event subscriptions and their watermarks.
// It is the authoritative record for subscription identity and continuation
// state used by EWS GetEvents polling.
type SubscriptionStore interface {
	// CreateSubscription records a new subscription and returns its ID.
	CreateSubscription(sub Subscription) (SubscriptionId, error)

	// GetSubscription retrieves a subscription by ID.
	// Returns ErrSubscriptionDrained if the subscription was invalidated
	// by a server drain or restart action.
	GetSubscription(id SubscriptionId) (*Subscription, error)

	// RenewSubscription extends the subscription expiry window.
	RenewSubscription(id SubscriptionId) error

	// ListSubscriptionsByMailbox returns all active subscriptions for a mailbox.
	ListSubscriptionsByMailbox(mboxID MailboxId) ([]Subscription, error)

	// RemoveSubscription deletes a subscription (Unsubscribe).
	RemoveSubscription(id SubscriptionId) error

	// ExpireAllSubscriptions marks every active subscription as drained.
	// This is called during server drain or restart so that long-lived
	// sync clients receive an explicit termination signal instead of silently
	// continuing with stale watermarks.
	// Returns the count of subscriptions that were drained.
	ExpireAllSubscriptions() (int, error)
}

// ---------------------------------------------------------------------------
// Subscription
// ---------------------------------------------------------------------------

// SubscriptionKind identifies the subscription transport type.
type SubscriptionKind uint8

const (
	SubscriptionKindPull SubscriptionKind = iota
	SubscriptionKindPush
	SubscriptionKindStreaming
)

func (k SubscriptionKind) String() string {
	switch k {
	case SubscriptionKindPull:
		return "pull"
	case SubscriptionKindPush:
		return "push"
	case SubscriptionKindStreaming:
		return "streaming"
	default:
		return "unknown"
	}
}

// SubscriptionId is a globally unique subscription identifier.
type SubscriptionId struct {
	ID string `json:"id"`
}

// IsZero returns true when the subscription ID is empty.
func (s SubscriptionId) IsZero() bool {
	return s.ID == ""
}

// ErrSubscriptionDrained is returned by GetSubscription when the subscription
// was invalidated by a server drain or restart action.
var ErrSubscriptionDrained = errors.New("subscription was invalidated by server drain")

// Subscription records one event subscription for a mailbox.
type Subscription struct {
	ID        SubscriptionId
	MailboxID MailboxId
	Kind      SubscriptionKind
	FolderIDs []FolderId // subscribed folders; empty = all folders

	// LastSeq is the sequence number of the last event delivered.
	LastSeq uint64

	// PushURL for push notifications.
	PushURL string

	// ExpiresAt is the natural subscription expiry.
	ExpiresAt time.Time

	// DrainedAt is set when the subscription was invalidated by a server
	// drain or restart action. This is distinct from natural expiry so that
	// callers can distinguish a user-initiated Unsubscribe from a server-side
	// session termination. When DrainedAt is set, callers MUST treat the
	// subscription as permanently invalid and require explicit re-subscribe.
	DrainedAt time.Time

	CreatedAt time.Time
}

// IsExpired returns true when the subscription has passed its natural expiry time.
func (s *Subscription) IsExpired() bool {
	return !s.ExpiresAt.IsZero() && time.Now().After(s.ExpiresAt)
}

// IsDrained returns true when the subscription was invalidated by a server
// drain or restart action. A drained subscription MUST NOT be renewed
// or reused; the client must perform a fresh Subscribe call.
func (s *Subscription) IsDrained() bool {
	return !s.DrainedAt.IsZero()
}

// ---------------------------------------------------------------------------
// BoltSubscriptionStore
// ---------------------------------------------------------------------------

const bucketSubscriptions = "__semcore_subscriptions"

type storedSubscription struct {
	ID        string           `json:"id"`
	MailboxID string           `json:"mailbox_id"`
	Kind      SubscriptionKind `json:"kind"`
	FolderIDs []string         `json:"folder_ids"`
	LastSeq   uint64           `json:"last_seq"`
	PushURL   string           `json:"push_url"`
	ExpiresAt time.Time        `json:"expires_at"`
	DrainedAt time.Time        `json:"drained_at,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
}

func (s *storedSubscription) toSubscription() *Subscription {
	sub := &Subscription{
		ID:        SubscriptionId{ID: s.ID},
		Kind:      s.Kind,
		PushURL:   s.PushURL,
		ExpiresAt: s.ExpiresAt,
		DrainedAt: s.DrainedAt,
		CreatedAt: s.CreatedAt,
		LastSeq:   s.LastSeq,
	}
	if s.MailboxID != "" {
		sub.MailboxID = MustMailboxId(s.MailboxID)
	}
	for _, fid := range s.FolderIDs {
		if fid != "" {
			sub.FolderIDs = append(sub.FolderIDs, MustFolderId(fid))
		}
	}
	return sub
}

func storedSubscriptionFrom(sub *Subscription) storedSubscription {
	st := storedSubscription{
		ID:        sub.ID.ID,
		Kind:      sub.Kind,
		LastSeq:   sub.LastSeq,
		PushURL:   sub.PushURL,
		ExpiresAt: sub.ExpiresAt,
		DrainedAt: sub.DrainedAt,
		CreatedAt: sub.CreatedAt,
	}
	if !sub.MailboxID.IsZero() {
		st.MailboxID = sub.MailboxID.String()
	}
	for _, fid := range sub.FolderIDs {
		if !fid.IsZero() {
			st.FolderIDs = append(st.FolderIDs, fid.String())
		}
	}
	return st
}

// BoltSubscriptionStore persists subscription state in bbolt.
type BoltSubscriptionStore struct {
	db *bbolt.DB
	mu sync.Mutex
}

// NewBoltSubscriptionStore opens a Bolt-backed subscription store, creating
// the bucket if it does not yet exist.
func NewBoltSubscriptionStore(db *bbolt.DB) (*BoltSubscriptionStore, error) {
	err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketSubscriptions))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("BoltSubscriptionStore: create bucket: %w", err)
	}
	return &BoltSubscriptionStore{db: db}, nil
}

// CreateSubscription implements SubscriptionStore.
func (s *BoltSubscriptionStore) CreateSubscription(sub Subscription) (SubscriptionId, error) {
	if sub.MailboxID.IsZero() {
		return SubscriptionId{}, fmt.Errorf("CreateSubscription: MailboxID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate subscription ID.
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return SubscriptionId{}, fmt.Errorf("generate subscription id: %w", err)
	}
	sid := fmt.Sprintf("sub-%x", idBytes)

	sub.ID = SubscriptionId{ID: sid}
	if sub.ExpiresAt.IsZero() {
		sub.ExpiresAt = time.Now().Add(30 * time.Minute)
	}
	sub.CreatedAt = time.Now()

	st := storedSubscriptionFrom(&sub)
	data, err := json.Marshal(st)
	if err != nil {
		return SubscriptionId{}, fmt.Errorf("marshal subscription: %w", err)
	}

	err = s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSubscriptions))
		return b.Put([]byte(sid), data)
	})
	if err != nil {
		return SubscriptionId{}, err
	}
	return sub.ID, nil
}

// GetSubscription implements SubscriptionStore.
func (s *BoltSubscriptionStore) GetSubscription(id SubscriptionId) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rec *storedSubscription
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSubscriptions))
		data := b.Get([]byte(id.ID))
		if data == nil {
			return fmt.Errorf("subscription %q not found", id.ID)
		}
		var r storedSubscription
		if err := json.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("unmarshal subscription: %w", err)
		}
		rcopy := r
		rec = &rcopy
		return nil
	})
	if err != nil {
		return nil, err
	}
	sub := rec.toSubscription()
	// A drained subscription must not be renewed or reused.
	if sub.IsDrained() {
		return sub, ErrSubscriptionDrained
	}
	return sub, nil
}

// RenewSubscription implements SubscriptionStore.
func (s *BoltSubscriptionStore) RenewSubscription(id SubscriptionId) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSubscriptions))
		data := b.Get([]byte(id.ID))
		if data == nil {
			return fmt.Errorf("subscription %q not found", id.ID)
		}
		var r storedSubscription
		if err := json.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("unmarshal: %w", err)
		}
		r.ExpiresAt = time.Now().Add(30 * time.Minute) // renouvel subscription
		out, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return b.Put([]byte(id.ID), out)
	})
}

// ListSubscriptionsByMailbox implements SubscriptionStore.
func (s *BoltSubscriptionStore) ListSubscriptionsByMailbox(mboxID MailboxId) ([]Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Subscription
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSubscriptions))
		return b.ForEach(func(k, v []byte) error {
			var r storedSubscription
			if err := json.Unmarshal(v, &r); err != nil {
				return nil
			}
			if r.MailboxID != mboxID.String() {
				return nil
			}
			sub := r.toSubscription()
			result = append(result, *sub)
			return nil
		})
	})
	return result, err
}

// RemoveSubscription implements SubscriptionStore.
func (s *BoltSubscriptionStore) RemoveSubscription(id SubscriptionId) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSubscriptions))
		return b.Delete([]byte(id.ID))
	})
}

// ExpireAllSubscriptions implements SubscriptionStore.
// It marks every active subscription as drained so that long-lived sync
// clients receive an explicit termination signal during server drain or restart.
func (s *BoltSubscriptionStore) ExpireAllSubscriptions() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	drainedAt := time.Now().UTC()
	var count int

	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketSubscriptions))
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var r storedSubscription
			if err := json.Unmarshal(v, &r); err != nil {
				continue
			}
			// Skip already drained subscriptions.
			if !r.DrainedAt.IsZero() {
				continue
			}
			r.DrainedAt = drainedAt
			out, err := json.Marshal(r)
			if err != nil {
				continue
			}
			if err := b.Put(k, out); err != nil {
				continue
			}
			count++
		}
		return nil
	})

	return count, err
}


