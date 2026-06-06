package server

import (
	"github.com/umailserver/umailserver/internal/api"
	"github.com/umailserver/umailserver/internal/db/postgres"
	"github.com/umailserver/umailserver/internal/ews"
	"github.com/umailserver/umailserver/internal/mapi"
)

// Compile-time proof that the relational backend satisfies the storage surfaces
// that consumer packages must not couple to db/postgres for. These assertions
// live here, in the composition root, because the api and ews packages cannot
// import db/postgres; the server imports all of them, so it is the natural place
// to pin that *postgres.DB can stand in for the bbolt *storage.Database behind
// each consumer interface.
var (
	_ api.MailStore          = (*postgres.DB)(nil)
	_ api.IdentityStore      = (*postgres.DB)(nil)
	_ api.SubscriptionStore  = (*postgres.DB)(nil)
	_ api.DelegationStore    = (*postgres.DB)(nil)
	_ api.PolicyStore        = (*postgres.DB)(nil)
	_ api.CollaborationStore = (*postgres.DB)(nil)
	_ mapi.PolicyStore       = (*postgres.DB)(nil)
	_ ews.MailStore          = (*postgres.DB)(nil)
	_ ews.LifecycleStore     = (*postgres.DB)(nil)
	_ ews.IdentityStore      = (*postgres.DB)(nil)
	_ ews.SyncStore          = (*postgres.DB)(nil)
	_ ews.TombstoneStore     = (*postgres.DB)(nil)
	_ ews.SubscriptionStore  = (*postgres.DB)(nil)
	_ ews.DelegateStore      = (*postgres.DB)(nil)
	_ ews.PolicyStore        = (*postgres.DB)(nil)
	_ ews.CollabStore        = (*postgres.DB)(nil)
)
