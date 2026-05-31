package semcore

import (
	"encoding/json"
	"fmt"
	"time"

	"go.etcd.io/bbolt"
)

// bucketRoomList is the bbolt bucket for room lists.
const bucketRoomList = "roomlists"

// RoomList is an administrator-managed grouping of room resources, mirroring the
// Exchange "room list" concept used by Outlook's Room Finder. Unlike ResourceId
// or MailboxId, a room list is not a sync-critical canonical identity, so it uses
// a plain string ID rather than a value-object wrapper.
type RoomList struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Rooms    []string  `json:"rooms"` // member room resource emails
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
}

// ListRoomLists returns all room lists.
func (s *BoltPolicyStore) ListRoomLists() ([]*RoomList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*RoomList
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketRoomList))
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, v []byte) error {
			var rl RoomList
			if err := json.Unmarshal(v, &rl); err != nil {
				return nil // skip corrupted entries
			}
			rlCopy := rl
			result = append(result, &rlCopy)
			return nil
		})
	})
	return result, err
}

// GetRoomList returns a room list by ID.
func (s *BoltPolicyStore) GetRoomList(id string) (*RoomList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rl *RoomList
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketRoomList))
		if b == nil {
			return fmt.Errorf("room list not found: %s", id)
		}
		data := b.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("room list not found: %s", id)
		}
		var r RoomList
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		rl = &r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rl, nil
}

// PutRoomList stores or updates a room list. A new ID and Created timestamp are
// assigned when the ID is empty.
func (s *BoltPolicyStore) PutRoomList(rl *RoomList) error {
	if rl == nil {
		return fmt.Errorf("PutRoomList: nil room list")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if rl.ID == "" {
		rl.ID = "rl-" + generateID()
		rl.Created = timeNowUTC()
	}
	if rl.Created.IsZero() {
		rl.Created = timeNowUTC()
	}
	rl.Modified = timeNowUTC()
	if rl.Rooms == nil {
		rl.Rooms = []string{}
	}

	data, err := json.Marshal(rl)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucketRoomList))
		if err != nil {
			return err
		}
		return b.Put([]byte(rl.ID), data)
	})
}

// DeleteRoomList removes a room list by ID.
func (s *BoltPolicyStore) DeleteRoomList(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketRoomList))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(id))
	})
}
