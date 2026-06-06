package server

import (
	"github.com/umailserver/umailserver/internal/api"
	"github.com/umailserver/umailserver/internal/db/postgres"
)

// Compile-time proof that the relational backend satisfies the API's storage
// surface. This assertion lives here, in the composition root, because the api
// package must not depend on db/postgres; the server imports both, so it is the
// natural place to pin that *postgres.DB can stand in for the bbolt
// *storage.Database behind api.MailStore.
var _ api.MailStore = (*postgres.DB)(nil)
