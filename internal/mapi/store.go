package mapi

import (
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/semcore"
)

// Store is the read-only account/domain surface the MAPI/HTTP server (NSPI
// address book + OAB) needs from the database. *db.DB satisfies it today; a
// relational store satisfies it later, so the server carries no engine
// dependency.
type Store interface {
	GetAccount(domain, localPart string) (*db.AccountData, error)
	ListAccountsByDomain(domain string) ([]*db.AccountData, error)
	ListDomains() ([]*db.DomainData, error)
}

// Compile-time assertion that the bbolt-backed *db.DB satisfies Store.
var _ Store = (*db.DB)(nil)

// PolicyStore is the resource-policy surface the MAPI/HTTP server needs (used to
// resolve room/equipment resources for the NSPI address book).
// *semcore.BoltPolicyStore satisfies it.
type PolicyStore interface {
	GetResource(id semcore.ResourceId) (*semcore.ResourcePolicy, error)
}

var _ PolicyStore = (*semcore.BoltPolicyStore)(nil)
