package server

import (
	"github.com/umailserver/umailserver/internal/api"
	"github.com/umailserver/umailserver/internal/db/postgres"
	"github.com/umailserver/umailserver/internal/ews"
	"github.com/umailserver/umailserver/internal/jmap"
	"github.com/umailserver/umailserver/internal/search"
	"github.com/umailserver/umailserver/internal/semcore"
)

// This file is the composition-root seam for the canonical semantic-core store.
// Every protocol consumer (EWS, CalDAV/CardDAV, JMAP, MAPI, search, the mutation
// pipeline, the API aggregate) already depends on a narrow consumer interface;
// semanticStores is the union the server holds so it can hand either the
// bbolt-backed *semcore.Store or the relational *postgres.DB to all of them.
//
// Each accessor returns the union of the consumer interfaces it is passed to, so
// the existing call sites compile unchanged. Both backends satisfy every member
// interface (proven by the assertions in the postgres and server store_assert
// files), so each adapter accessor simply returns its single backing value.

// semIdentity is the identity surface across EWS, search, and the mutation
// pipeline (the CalDAV/CardDAV identityBackend and JMAP notes surfaces are
// subsets of ews.IdentityStore).
type semIdentity interface {
	ews.IdentityStore
	search.IdentityStore
	semcore.PipelineIdentityStore
}

// semSubscriptions is the EWS + API subscription surface plus the drain used at
// shutdown and the enumerate/advance pair the push dispatcher needs. Adding the
// dispatcher methods here statically requires both backends to implement them.
type semSubscriptions interface {
	ews.SubscriptionStore
	api.SubscriptionStore
	ExpireAllSubscriptions() (int, error)
	ListPushSubscriptions() ([]semcore.Subscription, error)
	UpdateSubscriptionSeq(id semcore.SubscriptionId, seq uint64) error
}

// semLifecycle is the EWS lifecycle surface plus the pipeline's append.
type semLifecycle interface {
	ews.LifecycleStore
	semcore.PipelineLifecycleStore
}

// semCollab is the EWS collaboration surface plus the API/DAV bridge surface.
type semCollab interface {
	ews.CollabStore
	api.CollaborationStore
}

// semPolicy is the rule/OOF/resource/room-list surface across EWS, the API,
// JMAP (OOF), and MAPI (a subset).
type semPolicy interface {
	api.PolicyStore
	ews.PolicyStore
	jmap.OOFStore
}

// semDelegation is the EWS + API delegation surface.
type semDelegation interface {
	ews.DelegateStore
	api.DelegationStore
}

// semanticStores is the canonical semantic-core surface the server wires into
// every consumer. boltSemantic (bbolt) and pgSemantic (PostgreSQL) implement it.
type semanticStores interface {
	Identity() semIdentity
	SyncState() ews.SyncStore
	Tombstones() ews.TombstoneStore
	Subscriptions() semSubscriptions
	Lifecycle() semLifecycle
	Collaboration() semCollab
	Policy() semPolicy
	Delegation() semDelegation
	// APISemanticStore returns the aggregate the API server holds (its accessors
	// return the narrower api.* sub-store interfaces).
	APISemanticStore() api.SemanticStore
}

// boltSemantic adapts the bbolt-backed *semcore.Store. Each concrete *Bolt*Store
// satisfies the matching union interface.
type boltSemantic struct{ s *semcore.Store }

func (b boltSemantic) Identity() semIdentity               { return b.s.Identity() }
func (b boltSemantic) SyncState() ews.SyncStore            { return b.s.SyncState() }
func (b boltSemantic) Tombstones() ews.TombstoneStore      { return b.s.Tombstones() }
func (b boltSemantic) Subscriptions() semSubscriptions     { return b.s.Subscriptions() }
func (b boltSemantic) Lifecycle() semLifecycle             { return b.s.Lifecycle() }
func (b boltSemantic) Collaboration() semCollab            { return b.s.Collaboration() }
func (b boltSemantic) Policy() semPolicy                   { return b.s.Policy() }
func (b boltSemantic) Delegation() semDelegation           { return b.s.Delegation() }
func (b boltSemantic) APISemanticStore() api.SemanticStore { return api.BoltSemanticStore(b.s) }

// pgSemantic adapts the relational *postgres.DB, which alone satisfies every
// member interface, so every accessor returns the same handle.
type pgSemantic struct{ db *postgres.DB }

func (p pgSemantic) Identity() semIdentity           { return p.db }
func (p pgSemantic) SyncState() ews.SyncStore        { return p.db }
func (p pgSemantic) Tombstones() ews.TombstoneStore  { return p.db }
func (p pgSemantic) Subscriptions() semSubscriptions { return p.db }
func (p pgSemantic) Lifecycle() semLifecycle         { return p.db }
func (p pgSemantic) Collaboration() semCollab        { return p.db }
func (p pgSemantic) Policy() semPolicy               { return p.db }
func (p pgSemantic) Delegation() semDelegation       { return p.db }
func (p pgSemantic) APISemanticStore() api.SemanticStore {
	return pgAPISemanticStore(p)
}

// pgAPISemanticStore is the PostgreSQL api.SemanticStore aggregate: its accessors
// return *postgres.DB widened to the narrower api.* sub-store interfaces. The api
// package cannot import db/postgres, so the aggregate lives here.
type pgAPISemanticStore struct{ db *postgres.DB }

func (p pgAPISemanticStore) Identity() api.IdentityStore            { return p.db }
func (p pgAPISemanticStore) Policy() api.PolicyStore                { return p.db }
func (p pgAPISemanticStore) Delegation() api.DelegationStore        { return p.db }
func (p pgAPISemanticStore) Subscriptions() api.SubscriptionStore   { return p.db }
func (p pgAPISemanticStore) Collaboration() api.CollaborationStore  { return p.db }
func (p pgAPISemanticStore) NewJobStore() (semcore.JobStore, error) { return p.db.NewJobStore() }

var (
	_ semanticStores    = boltSemantic{}
	_ semanticStores    = pgSemantic{}
	_ api.SemanticStore = pgAPISemanticStore{}
)
