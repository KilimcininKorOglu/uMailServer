package postgres

import (
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/jmap"
	"github.com/umailserver/umailserver/internal/search"
)

// The relational backend must satisfy the same consumer interfaces as the bbolt
// stores, so the composition root can hand either one to every consumer.
//   - db.Store: the account/metadata surface.
//   - imap.MetadataStore: the full mailbox/message/thread/ACL surface (32
//     methods) the IMAP server holds.
//   - search.MetadataStore: the metadata surface the search service reads.
//   - jmap.MailStore: the mailbox/message/thread/changes surface JMAP reads.
var (
	_ db.Store             = (*DB)(nil)
	_ imap.MetadataStore   = (*DB)(nil)
	_ search.MetadataStore = (*DB)(nil)
	_ jmap.MailStore       = (*DB)(nil)
)
