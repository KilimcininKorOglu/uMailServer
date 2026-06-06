package ews

import "github.com/umailserver/umailserver/internal/semcore"

// This file collects the consumer-side interfaces the EWS server depends on for
// its semantic-core stores, decoupling the handlers from the concrete
// bbolt-backed implementations one store at a time so a relational backend can
// slot in later. The bbolt types satisfy these via compile-time assertions.

// DelegateStore is the delegation surface the EWS server needs.
type DelegateStore interface {
	PutDelegate(delegate *semcore.DelegateUser) (semcore.DelegateId, error)
	ListDelegates(ownerID semcore.MailboxId) ([]*semcore.DelegateUser, error)
	GetDelegateForUser(ownerID semcore.MailboxId, delegateEmail string) (*semcore.DelegateUser, error)
	RemoveDelegate(id semcore.DelegateId) error
}

var _ DelegateStore = (*semcore.BoltDelegateStore)(nil)
