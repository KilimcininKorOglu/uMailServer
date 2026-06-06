package postgres

import (
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/jmap"
	"github.com/umailserver/umailserver/internal/ratelimit"
	"github.com/umailserver/umailserver/internal/search"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/spam"
)

// The relational backend must satisfy the same consumer interfaces as the bbolt
// stores, so the composition root can hand either one to every consumer.
//   - db.Store: the account/metadata surface.
//   - imap.MetadataStore: the full mailbox/message/thread/ACL surface (32
//     methods) the IMAP server holds.
//   - search.MetadataStore: the metadata surface the search service reads.
//   - jmap.MailStore: the mailbox/message/thread/changes surface JMAP reads.
//   - spam.Store / ratelimit.QuotaStore: the auxiliary volatile stores that on
//     bbolt share mail.db; here they are plain relational tables.
var (
	_ db.Store             = (*DB)(nil)
	_ imap.MetadataStore   = (*DB)(nil)
	_ search.MetadataStore = (*DB)(nil)
	_ jmap.MailStore       = (*DB)(nil)
	_ spam.Store           = (*DB)(nil)
	_ ratelimit.QuotaStore = (*DB)(nil)

	// Semantic-core sub-stores (the semcore->Postgres migration, in progress).
	_ semcore.PipelineLifecycleStore = (*DB)(nil)
	_ semcore.PipelineIdentityStore  = (*DB)(nil)
	_ semcore.TombstoneWriter        = (*DB)(nil)
	_ jmap.NotesIdentityStore        = (*DB)(nil)
)
