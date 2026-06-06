package queue

import (
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// Store is the database surface the queue Manager depends on. *db.DB satisfies
// it today; a relational queue store (the report's FOR UPDATE SKIP LOCKED
// target) satisfies it later, so the Manager carries no engine dependency and
// never touches a raw bucket. Iteration goes through ForEachQueueEntry, which
// hides the on-disk encoding.
type Store interface {
	EnqueueWithLimit(entry *db.QueueEntry, maxSize int) error
	Dequeue(id string) error
	GetQueueEntry(id string) (*db.QueueEntry, error)
	GetPendingQueue(now time.Time) ([]*db.QueueEntry, error)
	UpdateQueueEntry(entry *db.QueueEntry) error
	ForEachQueueEntry(fn func(*db.QueueEntry) error) error
	GetDomain(name string) (*db.DomainData, error)
}

// Compile-time assertion that the bbolt-backed *db.DB satisfies Store.
var _ Store = (*db.DB)(nil)
