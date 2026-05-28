// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides a Bolt-backed JobStore for persisting migration and
// backfill job records. Every step checkpoint is written to Bolt so that
// an interrupted job can resume from the last successful boundary without
// repeating completed work.
package semcore

import (
	"encoding/json"
	"fmt"

	"go.etcd.io/bbolt"
)

// jobBucket is the Bolt bucket name used for job records.
const jobBucket = "__semcore_jobs"

// bucketKey is the per-job key within the jobs bucket.
func bucketKey(id string) []byte {
	return []byte(id)
}

// BoltJobStore persists Job records in a bbolt database.
type BoltJobStore struct {
	db *bbolt.DB
}

// NewBoltJobStore opens a Bolt-backed job store, creating the jobs bucket
// if it does not yet exist.
func NewBoltJobStore(db *bbolt.DB) (*BoltJobStore, error) {
	err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(jobBucket))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("BoltJobStore: failed to create bucket: %w", err)
	}
	return &BoltJobStore{db: db}, nil
}

// Put persists or updates a job record.
func (s *BoltJobStore) Put(job Job) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		data, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("BoltJobStore.Put: marshal error: %w", err)
		}
		return tx.Bucket([]byte(jobBucket)).Put(bucketKey(job.ID), data)
	})
}

// Get retrieves a job by ID. Returns ErrJobNotFound if absent.
func (s *BoltJobStore) Get(id string) (Job, error) {
	var job Job
	err := s.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket([]byte(jobBucket)).Get(bucketKey(id))
		if data == nil {
			return ErrJobNotFound
		}
		var err2 error
		job, err2 = UnmarshalJob(data)
		if err2 != nil {
			return fmt.Errorf("BoltJobStore.Get: unmarshal error: %w", err2)
		}
		return nil
	})
	return job, err
}

// List returns all jobs matching the given kind and state filters.
// Empty filters match everything.
func (s *BoltJobStore) List(kind JobKind, state JobState) ([]Job, error) {
	var jobs []Job
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(jobBucket)).ForEach(func(k, v []byte) error {
			job, err := UnmarshalJob(v)
			if err != nil {
				return fmt.Errorf("BoltJobStore.List: unmarshal error: %w", err)
			}
			if kind != "" && job.Kind != kind {
				return nil
			}
			if state != "" && job.State != state {
				return nil
			}
			jobs = append(jobs, job)
			return nil
		})
	})
	return jobs, err
}

// Delete removes a job. Returns ErrJobNotFound if not found.
func (s *BoltJobStore) Delete(id string) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(jobBucket))
		if bucket.Get(bucketKey(id)) == nil {
			return ErrJobNotFound
		}
		return bucket.Delete(bucketKey(id))
	})
}
