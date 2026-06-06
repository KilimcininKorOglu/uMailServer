package search

import "github.com/umailserver/umailserver/internal/storage"

// MetadataStore is the message-metadata surface the search service reads while
// building and refreshing indexes. *storage.Database satisfies it today; a
// relational store satisfies it later, so the service carries no engine
// dependency.
type MetadataStore interface {
	ListMailboxes(user string) ([]string, error)
	GetMessageUIDs(user, mailbox string) ([]uint32, error)
	GetMessageMetadata(user, mailbox string, uid uint32) (*storage.MessageMetadata, error)
}

// MessageReader reads raw message bodies for indexing. *storage.MessageStore
// satisfies it; message bodies stay as Maildir files regardless of the metadata
// backend.
type MessageReader interface {
	ReadMessage(user, messageID string) ([]byte, error)
}

// Compile-time assertions that the bbolt-backed types satisfy the interfaces.
var (
	_ MetadataStore = (*storage.Database)(nil)
	_ MessageReader = (*storage.MessageStore)(nil)
)
