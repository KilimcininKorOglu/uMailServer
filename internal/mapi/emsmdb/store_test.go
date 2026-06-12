package emsmdb

import (
	"errors"
	"slices"

	"github.com/umailserver/umailserver/internal/storage"
)

// fakeStore is an in-memory canonical store for the ROP tests: a folder set plus,
// per folder, its message uids and per-uid metadata. A folder may exist with no
// messages (an empty folder still appears in the hierarchy).
type fakeStore struct {
	mailboxes []string
	uids      map[string][]uint32
	meta      map[string]map[uint32]*storage.MessageMetadata
	raw       map[string][]byte // storage message id -> raw RFC 822
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		uids: map[string][]uint32{},
		meta: map[string]map[uint32]*storage.MessageMetadata{},
		raw:  map[string][]byte{},
	}
}

// addMailbox registers a folder so it appears in ListMailboxes even with no
// messages.
func (f *fakeStore) addMailbox(name string) {
	if slices.Contains(f.mailboxes, name) {
		return
	}
	f.mailboxes = append(f.mailboxes, name)
}

// put adds a message to a mailbox in store order, registering the mailbox.
func (f *fakeStore) put(mailbox string, m *storage.MessageMetadata) {
	f.addMailbox(mailbox)
	f.uids[mailbox] = append(f.uids[mailbox], m.UID)
	if f.meta[mailbox] == nil {
		f.meta[mailbox] = map[uint32]*storage.MessageMetadata{}
	}
	f.meta[mailbox][m.UID] = m
}

func (f *fakeStore) ListMailboxes(_ string) ([]string, error) {
	return f.mailboxes, nil
}

func (f *fakeStore) GetMailboxCounts(_, mailbox string) (exists, recent, unseen int, err error) {
	uids := f.uids[mailbox]
	for _, uid := range uids {
		if m := f.meta[mailbox][uid]; m != nil && !slices.Contains(m.Flags, "\\Seen") {
			unseen++
		}
	}
	return len(uids), 0, unseen, nil
}

func (f *fakeStore) GetMessageUIDs(_, mailbox string) ([]uint32, error) {
	return f.uids[mailbox], nil
}

func (f *fakeStore) GetMessageMetadata(_, mailbox string, uid uint32) (*storage.MessageMetadata, error) {
	if m, ok := f.meta[mailbox][uid]; ok {
		return m, nil
	}
	return nil, errors.New("message not found")
}

// putRaw records the raw RFC 822 bytes for a message id, for the body store.
func (f *fakeStore) putRaw(messageID string, raw []byte) {
	f.raw[messageID] = raw
}

func (f *fakeStore) ReadMessage(_, messageID string) ([]byte, error) {
	if b, ok := f.raw[messageID]; ok {
		return b, nil
	}
	return nil, errors.New("raw message not found")
}
