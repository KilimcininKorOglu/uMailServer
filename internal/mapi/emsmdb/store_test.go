package emsmdb

import (
	"errors"

	"github.com/umailserver/umailserver/internal/storage"
)

// fakeStore is an in-memory canonical store for the ROP tests: it maps an
// IMAP-canonical mailbox name to its message uids and per-uid metadata.
type fakeStore struct {
	uids map[string][]uint32
	meta map[string]map[uint32]*storage.MessageMetadata
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		uids: map[string][]uint32{},
		meta: map[string]map[uint32]*storage.MessageMetadata{},
	}
}

// put adds a message to a mailbox in store order.
func (f *fakeStore) put(mailbox string, m *storage.MessageMetadata) {
	f.uids[mailbox] = append(f.uids[mailbox], m.UID)
	if f.meta[mailbox] == nil {
		f.meta[mailbox] = map[uint32]*storage.MessageMetadata{}
	}
	f.meta[mailbox][m.UID] = m
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
