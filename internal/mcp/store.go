package mcp

import "github.com/umailserver/umailserver/internal/db"

// Store is the account/domain surface the MCP server needs from the database.
// *db.DB satisfies it today; a relational store satisfies it later, so the
// server carries no engine dependency.
type Store interface {
	GetAccount(domain, localPart string) (*db.AccountData, error)
	ListAccountsByDomain(domain string) ([]*db.AccountData, error)
	ListDomains() ([]*db.DomainData, error)
	CreateAccount(account *db.AccountData) error
	DeleteAccount(domain, localPart string) error
	CreateDomain(domain *db.DomainData) error
	DeleteDomain(name string) error
}

// Compile-time assertion that the bbolt-backed *db.DB satisfies Store.
var _ Store = (*db.DB)(nil)
