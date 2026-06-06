package spam

import (
	"encoding/binary"
	"fmt"

	"go.etcd.io/bbolt"
)

// Store is the persistence surface the Bayesian classifier depends on. It hides
// the backing engine (today bbolt) so the classifier's logic stays
// backend-agnostic and a relational implementation can slot in later. A nil
// Store means "no persistence": the classifier degrades to neutral output.
type Store interface {
	// Initialize prepares the backing storage (buckets/tables).
	Initialize() error
	// IncrementToken adds delta to a token's count in the named class bucket
	// (SpamBucket or HamBucket).
	IncrementToken(bucketName, token string, delta uint32) error
	// GetTotalCounts returns the total ham and spam token counts.
	GetTotalCounts() (totalHam, totalSpam uint64, err error)
	// SetTotals persists the total ham and spam counts.
	SetTotals(totalHam, totalSpam uint64) error
	// GetTokenFrequency returns the ham and spam counts for a single token.
	GetTokenFrequency(token string) (hamCount, spamCount uint32, err error)
}

// tokenKey creates a bucket key for a token.
func tokenKey(token string) []byte {
	return []byte(token)
}

// countAllTokens sums every token count stored in a bucket.
func countAllTokens(bucket *bbolt.Bucket) uint64 {
	var count uint64
	cursor := bucket.Cursor()
	for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
		if len(v) >= 4 {
			count += uint64(binary.BigEndian.Uint32(v))
		}
	}
	return count
}

// boltStore implements Store over a bbolt database. The token-count buckets live
// alongside the rest of the storage data in the shared bbolt file.
type boltStore struct {
	bolt *bbolt.DB
}

// NewBoltStore returns a bbolt-backed spam token Store. A nil db yields a Store
// whose operations are no-ops returning neutral counts (matches the previous
// "no persistence" classifier behavior).
func NewBoltStore(db *bbolt.DB) Store {
	return &boltStore{bolt: db}
}

func (s *boltStore) Initialize() error {
	if s.bolt == nil {
		return nil
	}
	return s.bolt.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(SpamBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(HamBucket)); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists([]byte(StatsBucket))
		return err
	})
}

func (s *boltStore) IncrementToken(bucketName, token string, delta uint32) error {
	if s.bolt == nil {
		return nil
	}
	return s.bolt.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(bucketName))
		if bucket == nil {
			return fmt.Errorf("bucket %s not found", bucketName)
		}
		key := tokenKey(token)
		var count uint32
		if v := bucket.Get(key); len(v) >= 4 {
			count = binary.BigEndian.Uint32(v)
		}
		count += delta
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], count)
		return bucket.Put(key, buf[:])
	})
}

func (s *boltStore) GetTotalCounts() (totalHam, totalSpam uint64, err error) {
	if s.bolt == nil {
		return 1, 1, nil
	}
	err = s.bolt.View(func(tx *bbolt.Tx) error {
		spamBucket := tx.Bucket([]byte(SpamBucket))
		hamBucket := tx.Bucket([]byte(HamBucket))
		statsBucket := tx.Bucket([]byte(StatsBucket))

		if spamBucket != nil {
			totalSpam = countAllTokens(spamBucket)
		}
		if hamBucket != nil {
			totalHam = countAllTokens(hamBucket)
		}
		if statsBucket != nil {
			if v := statsBucket.Get([]byte("total_ham")); len(v) == 8 {
				totalHam = binary.BigEndian.Uint64(v)
			}
			if v := statsBucket.Get([]byte("total_spam")); len(v) == 8 {
				totalSpam = binary.BigEndian.Uint64(v)
			}
		}
		return nil
	})
	return
}

func (s *boltStore) SetTotals(totalHam, totalSpam uint64) error {
	if s.bolt == nil {
		return nil
	}
	return s.bolt.Update(func(tx *bbolt.Tx) error {
		statsBucket := tx.Bucket([]byte(StatsBucket))
		if statsBucket == nil {
			return nil
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], totalHam)
		if err := statsBucket.Put([]byte("total_ham"), buf[:]); err != nil {
			return err
		}
		binary.BigEndian.PutUint64(buf[:], totalSpam)
		return statsBucket.Put([]byte("total_spam"), buf[:])
	})
}

func (s *boltStore) GetTokenFrequency(token string) (hamCount, spamCount uint32, err error) {
	if s.bolt == nil {
		return 1, 1, nil
	}
	err = s.bolt.View(func(tx *bbolt.Tx) error {
		spamBucket := tx.Bucket([]byte(SpamBucket))
		hamBucket := tx.Bucket([]byte(HamBucket))

		if hamBucket != nil {
			if v := hamBucket.Get(tokenKey(token)); len(v) >= 4 {
				hamCount = binary.BigEndian.Uint32(v)
			}
		}
		if spamBucket != nil {
			if v := spamBucket.Get(tokenKey(token)); len(v) >= 4 {
				spamCount = binary.BigEndian.Uint32(v)
			}
		}
		return nil
	})
	return
}
