package storage

import (
	"bytes"
	"net/mail"
	"time"
)

// FileStore is the minimal mailbox-filing surface FileMessage needs. Both the
// bbolt *Database and the relational *postgres.DB satisfy it (and the api
// MailStore interface is a superset), so the shared "put a copy in a folder"
// primitive lives here in the storage package, reachable by every consumer.
type FileStore interface {
	CreateMailbox(user, name string) error
	GetNextUID(user, mailbox string) (uint32, error)
	StoreMessageMetadata(user, mailbox string, uid uint32, meta *MessageMetadata) error
}

// FileMessage stores raw in the content-addressed message store and files a
// metadata entry into the given folder, returning the assigned uid and the
// message-store blob key. It is the shared primitive behind webmail Sent
// filing and the scheduled-send Scheduled/Sent projection. The folder is
// created idempotently. Flags is applied verbatim (e.g. {"\\Seen"} for Sent,
// nil for a Scheduled placeholder).
func FileMessage(ms *MessageStore, fs FileStore, owner, folder string, raw []byte, flags []string) (uint32, string, error) {
	_ = fs.CreateMailbox(owner, folder) //nolint:errcheck // idempotent; absence is created, presence is fine

	blobKey, err := ms.StoreMessage(owner, raw)
	if err != nil {
		return 0, "", err
	}
	uid, err := fs.GetNextUID(owner, folder)
	if err != nil {
		return 0, "", err
	}
	meta := &MessageMetadata{
		MessageID:    blobKey,
		UID:          uid,
		Flags:        flags,
		InternalDate: time.Now(),
		Size:         int64(len(raw)),
		Subject:      rawMailHeader(raw, "Subject"),
		Date:         rawMailHeader(raw, "Date"),
		From:         rawMailHeader(raw, "From"),
		To:           rawMailHeader(raw, "To"),
	}
	if err := fs.StoreMessageMetadata(owner, folder, uid, meta); err != nil {
		return 0, "", err
	}
	return uid, blobKey, nil
}

// rawMailHeader returns a single header value from a raw RFC 5322 message, or ""
// when the message or header cannot be parsed.
func rawMailHeader(raw []byte, key string) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return msg.Header.Get(key)
}
