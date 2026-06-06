package jmap

import "github.com/umailserver/umailserver/internal/storage"

// MailStore is the mailbox/message-metadata surface the JMAP server needs from
// the storage database. *storage.Database satisfies it today; a relational
// store satisfies it later, so the server carries no engine dependency.
type MailStore interface {
	CreateMailbox(user, mailbox string) error
	DeleteMailbox(user, mailbox string) error
	RenameMailbox(user, oldName, newName string) error
	ListMailboxes(user string) ([]string, error)
	GetMailboxCounts(user, mailbox string) (exists, recent, unseen int, err error)
	GetNextUID(user, mailbox string) (uint32, error)
	GetMessageUIDs(user, mailbox string) ([]uint32, error)
	GetMessageMetadata(user, mailbox string, uid uint32) (*storage.MessageMetadata, error)
	StoreMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error
	UpdateMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error
	DeleteMessage(user, mailbox string, uid uint32) error
	GetChangesSince(user string, ct storage.ChangeType, sinceSeq uint64, max int) (entries []storage.ChangeEntry, hasMore bool, lastSeq uint64, err error)
	GetThreadMessages(user, mailbox, threadID string) ([]*storage.ThreadMessage, error)
	GetOrCreateThreadID(user, mailbox, subject, ownMessageID, inReplyTo string, references []string) (string, error)
}

// MessageStore is the raw message-body surface the JMAP server needs. Bodies
// stay as Maildir files regardless of the metadata backend.
type MessageStore interface {
	ReadMessage(user, messageID string) ([]byte, error)
	StoreMessage(user string, data []byte) (string, error)
	DeleteMessage(user, messageID string) error
}

// Compile-time assertions that the bbolt-backed types satisfy the interfaces.
var (
	_ MailStore    = (*storage.Database)(nil)
	_ MessageStore = (*storage.MessageStore)(nil)
)
