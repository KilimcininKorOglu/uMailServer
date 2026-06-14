package emsmdb

import "github.com/umailserver/umailserver/internal/storage"

// Store is the canonical mailbox store the ROP layer reads. It surfaces the same
// folders and messages the IMAP and EWS servers serve, keyed by the user's email
// and an IMAP-canonical mailbox name. Message bodies live in Maildir and are not
// read through this interface.
type Store interface {
	ListMailboxes(user string) ([]string, error)
	GetMailboxCounts(user, mailbox string) (exists, recent, unseen int, err error)
	GetMessageUIDs(user, mailbox string) ([]uint32, error)
	GetMessageMetadata(user, mailbox string, uid uint32) (*storage.MessageMetadata, error)
	// GetHighestModSeq returns the mailbox change high-water (RFC 7162), which
	// advances for both message changes and expunges; an ICS contents download reports
	// it as the CnsetSeen high-water. ExpungedUIDsSince returns the uids expunged past
	// a change number (the QRESYNC tombstones), the deletion set the download reports.
	GetHighestModSeq(user, mailbox string) (uint64, error)
	ExpungedUIDsSince(user, mailbox string, sinceModSeq uint64) ([]uint32, error)
}

// The canonical store must satisfy the ROP layer's read interface.
var _ Store = (*storage.Database)(nil)
