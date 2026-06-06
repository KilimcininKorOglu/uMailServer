package postgres

import "github.com/umailserver/umailserver/internal/db"

// The relational backend must satisfy the same runtime store surface as the
// bbolt store, so the composition root can hand either one to every consumer as
// a db.Store.
var _ db.Store = (*DB)(nil)
