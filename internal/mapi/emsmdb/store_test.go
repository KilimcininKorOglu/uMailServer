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
	raw       map[string][]byte            // storage message id -> raw RFC 822
	expunged  map[string]map[uint32]uint64 // mailbox -> uid -> expunge change number
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		uids:     map[string][]uint32{},
		meta:     map[string]map[uint32]*storage.MessageMetadata{},
		raw:      map[string][]byte{},
		expunged: map[string]map[uint32]uint64{},
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

// expunge records an RFC 7162 tombstone (uid expunged at the given change number),
// mirroring the canonical store's expunge bucket so the ICS download can report it.
func (f *fakeStore) expunge(mailbox string, uid uint32, changeNumber uint64) {
	if f.expunged[mailbox] == nil {
		f.expunged[mailbox] = map[uint32]uint64{}
	}
	f.expunged[mailbox][uid] = changeNumber
}

// GetHighestModSeq returns the mailbox change high-water: the maximum over the live
// messages' ModSeqs and the expunge tombstones' change numbers, matching the canonical
// store whose modseq counter advances for both.
func (f *fakeStore) GetHighestModSeq(_, mailbox string) (uint64, error) {
	var high uint64
	for _, m := range f.meta[mailbox] {
		high = max(high, m.ModSeq)
	}
	for _, cn := range f.expunged[mailbox] {
		high = max(high, cn)
	}
	return high, nil
}

// ExpungedUIDsSince returns, in ascending order, the uids expunged at a change number
// greater than sinceModSeq.
func (f *fakeStore) ExpungedUIDsSince(_, mailbox string, sinceModSeq uint64) ([]uint32, error) {
	var uids []uint32
	for uid, cn := range f.expunged[mailbox] {
		if cn > sinceModSeq {
			uids = append(uids, uid)
		}
	}
	slices.Sort(uids)
	return uids, nil
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
