package emsmdb

import "github.com/umailserver/umailserver/internal/storage"

// Store is the canonical mailbox store the ROP layer reads. It surfaces the same
// folders and messages the IMAP and EWS servers serve, keyed by the user's email
// and an IMAP-canonical mailbox name. Message bodies live in Maildir and are not
// read through this interface.
type Store interface {
	GetMessageUIDs(user, mailbox string) ([]uint32, error)
	GetMessageMetadata(user, mailbox string, uid uint32) (*storage.MessageMetadata, error)
}

// The canonical store must satisfy the ROP layer's read interface.
var _ Store = (*storage.Database)(nil)
