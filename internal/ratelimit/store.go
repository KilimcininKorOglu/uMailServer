package ratelimit

import (
	"encoding/binary"
	"math"

	"go.etcd.io/bbolt"
)

// QuotaStore persists per-user daily send counters so they survive a restart.
// It hides the backing engine (today bbolt) so the limiter logic carries no
// engine dependency and a relational store can slot in later. A nil QuotaStore
// disables persistence; in-memory rate limiting still works.
type QuotaStore interface {
	// Initialize prepares the backing storage.
	Initialize() error
	// GetUserSentToday returns the persisted daily-sent count for a user, or 0
	// when absent or on a read error (persistence is best-effort).
	GetUserSentToday(user string) int64
	// SetUserSentToday persists the daily-sent count for a user. Best-effort.
	SetUserSentToday(user string, count int64)
}

const ratelimitUsersBucket = "ratelimit_users"

func userSentTodayKey(user string) []byte {
	return []byte(user + ":sent_today")
}

// boltQuotaStore implements QuotaStore over a bbolt database, co-located with
// the rest of the storage data in the shared bbolt file.
type boltQuotaStore struct {
	bolt *bbolt.DB
}

// NewBoltStore returns a bbolt-backed QuotaStore. A nil db yields a store whose
// operations are no-ops (no persistence).
func NewBoltStore(db *bbolt.DB) QuotaStore {
	return &boltQuotaStore{bolt: db}
}

func (s *boltQuotaStore) Initialize() error {
	if s.bolt == nil {
		return nil
	}
	return s.bolt.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(ratelimitUsersBucket))
		return err
	})
}

func (s *boltQuotaStore) GetUserSentToday(user string) int64 {
	if s.bolt == nil {
		return 0
	}
	var count int64
	if err := s.bolt.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(ratelimitUsersBucket))
		if bucket == nil {
			return nil
		}
		if v := bucket.Get(userSentTodayKey(user)); len(v) == 8 {
			c := binary.BigEndian.Uint64(v)
			if c > uint64(math.MaxInt64) {
				c = uint64(math.MaxInt64)
			}
			count = int64(c)
		}
		return nil
	}); err != nil {
		return 0
	}
	return count
}

func (s *boltQuotaStore) SetUserSentToday(user string, count int64) {
	if s.bolt == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	if err := s.bolt.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(ratelimitUsersBucket))
		if bucket == nil {
			return nil
		}
		var buf [8]byte
		// #nosec G115 -- count is validated non-negative above
		binary.BigEndian.PutUint64(buf[:], uint64(count))
		return bucket.Put(userSentTodayKey(user), buf[:])
	}); err != nil {
		return
	}
}
