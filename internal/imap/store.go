package imap

import "github.com/umailserver/umailserver/internal/storage"

// MetadataStore is the mailbox/message/ACL/thread metadata surface the IMAP
// mailstore needs from the storage database. *storage.Database satisfies it
// today; a relational store (with sequence-based UID allocation and a tsvector
// search path) satisfies it later, so the mailstore carries no engine
// dependency. This is the largest storage seam: every protocol that reads mail
// goes through the mailstore, so the relational backend slots in here.
type MetadataStore interface {
	AuthenticateUser(username, password string) (bool, error)
	Close() error

	CreateMailbox(user, mailbox string) error
	DeleteMailbox(user, mailbox string) error
	RenameMailbox(user, oldName, newName string) error
	GetMailbox(user, mailbox string) (*storage.Mailbox, error)
	ListMailboxes(user string) ([]string, error)
	EnsureDefaultMailboxes(user string) error
	GetMailboxCounts(user, mailbox string) (exists, recent, unseen int, err error)
	ClearRecent(user, mailbox string) error
	GetHighestModSeq(user, mailbox string) (uint64, error)

	GetNextUID(user, mailbox string) (uint32, error)
	ReconcileUIDNext(user, mailbox string) error
	GetMessageUIDs(user, mailbox string) ([]uint32, error)
	GetMessageMetadata(user, mailbox string, uid uint32) (*storage.MessageMetadata, error)
	StoreMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error
	UpdateMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error
	UpdateMessageMetadataFunc(user, mailbox string, uid uint32, fn func(*storage.MessageMetadata) error) error
	DeleteMessage(user, mailbox string, uid uint32) error

	GetSubscribed(user, mailbox string) (bool, error)
	SetSubscribed(user, mailbox string, subscribed bool) error
	ListSubscribed(user string) ([]string, error)

	GetACL(owner, mailbox, grantee string) (storage.ACLRights, error)
	SetACL(owner, mailbox, grantee string, rights storage.ACLRights, grantingUser string) error
	DeleteACL(owner, mailbox, grantee string) error
	ListACL(owner, mailbox string) ([]storage.ACLEntry, error)
	ListGranteesMailboxes(owner string) ([]string, error)
	ListMailboxesSharedWith(user string) ([]string, error)

	GetThread(user, threadID string) (*storage.Thread, error)
	GetThreadMessages(user, mailbox, threadID string) ([]*storage.ThreadMessage, error)
	GetOrCreateThreadID(user, mailbox, subject, ownMessageID, inReplyTo string, references []string) (string, error)
	UpdateThread(user string, thread *storage.Thread) error
}

// MessageStore is the raw message-body surface the IMAP mailstore needs. Bodies
// stay as Maildir files regardless of the metadata backend.
type MessageStore interface {
	ReadMessage(user, messageID string) ([]byte, error)
	StoreMessage(user string, data []byte) (string, error)
	DeleteMessage(user, messageID string) error
	Close() error
}

// Compile-time assertions that the bbolt-backed types satisfy the interfaces.
var (
	_ MetadataStore = (*storage.Database)(nil)
	_ MessageStore  = (*storage.MessageStore)(nil)
)
