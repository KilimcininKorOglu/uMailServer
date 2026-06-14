package server

import (
	"errors"
	"slices"

	"github.com/umailserver/umailserver/internal/activesync"
	"github.com/umailserver/umailserver/internal/ews"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// This file adapts the canonical stores onto the interfaces internal/activesync
// depends on, keeping the EAS package free of semcore/storage coupling. The
// adapters are backend-neutral: they take the same semIdentity / ews.SyncStore
// interfaces the EWS surface uses, so they work over both bbolt and PostgreSQL.

// easFolderType maps a canonical mailbox folder name to its EAS FolderHierarchy
// Type; unknown names fall back to the generic user mail-folder type.
var easFolderType = map[string]string{
	"INBOX":   activesync.FolderTypeInbox,
	"Sent":    activesync.FolderTypeSent,
	"Drafts":  activesync.FolderTypeDrafts,
	"Trash":   activesync.FolderTypeDeleted,
	"Junk":    activesync.FolderTypeUserMail,
	"Archive": activesync.FolderTypeUserMail,
	"Notes":   activesync.FolderTypeNotes,
}

// easFolderSource projects a mailbox's canonical folder list into EAS folders,
// using the folder name as the stable EAS ServerId.
type easFolderSource struct{ db storageBackend }

func (f easFolderSource) Folders(email string) ([]activesync.Folder, error) {
	names, err := f.db.ListMailboxes(email)
	if err != nil {
		return nil, err
	}
	folders := make([]activesync.Folder, 0, len(names))
	for _, name := range names {
		typ := easFolderType[name]
		if typ == "" {
			typ = activesync.FolderTypeUserMail
		}
		folders = append(folders, activesync.Folder{
			ServerID:    name,
			ParentID:    "0",
			DisplayName: name,
			Type:        typ,
		})
	}
	return folders, nil
}

// easSyncState adapts the EAS per-(email, collection, device) watermark onto
// semcore's canonical SyncStateStore: it resolves the mailbox id from the email
// and the collection name to a folder id (the empty collection is the mailbox-
// level hierarchy token). It carries the opaque EAS watermark verbatim.
type easSyncState struct {
	identity semIdentity
	sync     ews.SyncStore
}

func (a easSyncState) GetSyncState(email, collection, deviceID string) (string, error) {
	mboxID, err := a.identity.GetMailboxIDByEmail(email)
	if err != nil {
		return "", nil // no mailbox yet -> no state (the command primes from 0)
	}
	st, err := a.sync.GetSyncState(mboxID, easFolderID(collection), deviceID)
	if errors.Is(err, semcore.ErrSyncStateNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return st.Watermark, nil
}

func (a easSyncState) PutSyncState(email, collection, deviceID, watermark string) error {
	// EnsureMailboxId (not GetMailboxIDByEmail): the semcore identity is populated
	// asynchronously ("EWS visibility only"), so an IMAP-only account a mobile
	// client first reaches over EAS may have no identity yet. Ensuring it on the
	// establishing Sync (SyncKey 0) mirrors how EWS folder-hierarchy sync admits
	// new accounts, so the watermark write never fails for a fresh mailbox.
	mboxID, err := a.identity.EnsureMailboxId(email)
	if err != nil {
		return err
	}
	return a.sync.PutSyncState(mboxID, easFolderID(collection), deviceID, watermark)
}

// easFolderID maps an EAS collection id to a semcore FolderId; the empty
// collection resolves to the zero FolderId, semcore's mailbox-level token.
func easFolderID(collection string) semcore.FolderId {
	if collection == "" {
		return semcore.FolderId{}
	}
	fid, err := semcore.NewFolderId(collection)
	if err != nil {
		return semcore.FolderId{}
	}
	return fid
}

// easDateLayout is the EAS DateReceived format (MS-ASCMD): ISO 8601 UTC with
// millisecond precision and a trailing Z.
const easDateLayout = "2006-01-02T15:04:05.000Z"

// easChangeWindow bounds how many journal entries one incremental Sync drains.
const easChangeWindow = 512

// easMailSource projects a mailbox folder's mail for the EAS Sync command from
// the authoritative IMAP mailstore — the same store IMAP/POP3/JMAP/webmail read,
// updated synchronously on delivery. (The semcore identity store is a secondary,
// asynchronously-populated projection that "drives EWS visibility only", so it
// would lag freshly delivered mail.) The collection id is the folder name; a
// message's stable EAS ServerId is its storage blob key (storage.MessageMetadata.
// MessageID), which the change journal also records — so a snapshot Add and a
// later journal Delete name the same item.
type easMailSource struct {
	db  storageBackend
	msg *storage.MessageStore
}

// ListMessages returns the folder's current messages, oldest first. Headers,
// read state and received time come from the canonical metadata index; the body
// is read from the message blob. Bodies are read for the whole snapshot because
// the Sync command windows a fresh snapshot each request; a windowed body fetch
// is a later optimization, not a correctness concern (incremental syncs run off
// the cheap journal feed, not this path).
func (s easMailSource) ListMessages(email, collectionID string) ([]activesync.SyncMessage, error) {
	uids, err := s.db.GetMessageUIDs(email, collectionID)
	if err != nil {
		return nil, err
	}
	msgs := make([]activesync.SyncMessage, 0, len(uids))
	for _, uid := range uids {
		meta, merr := s.db.GetMessageMetadata(email, collectionID, uid)
		if merr != nil || meta == nil || meta.MessageID == "" {
			continue
		}
		msgs = append(msgs, s.snapshotMessage(email, meta))
	}
	return msgs, nil
}

// snapshotMessage builds a SyncMessage from the canonical metadata (authoritative
// for headers, read flag and received time) plus the body read from the blob.
func (s easMailSource) snapshotMessage(email string, meta *storage.MessageMetadata) activesync.SyncMessage {
	bodyType, body := "1", ""
	if raw, err := s.msg.ReadMessage(email, meta.MessageID); err == nil {
		bodyType, body = activesync.BodyForSync(raw)
	}
	dateRecv := ""
	if !meta.InternalDate.IsZero() {
		dateRecv = meta.InternalDate.UTC().Format(easDateLayout)
	}
	return activesync.SyncMessage{
		ServerID:     meta.MessageID,
		Subject:      meta.Subject,
		From:         meta.From,
		To:           meta.To,
		DateReceived: dateRecv,
		Read:         slices.Contains(meta.Flags, "\\Seen"),
		Importance:   "1",
		BodyType:     bodyType,
		Body:         body,
	}
}

// CurrentSeq returns the journal head — the baseline the snapshot enumeration
// advances to before incremental syncs read the change feed.
func (s easMailSource) CurrentSeq(email string) (uint64, error) {
	state, err := s.db.CurrentChangeState(email)
	if err != nil {
		return 0, err
	}
	return storage.ParseChangeState(state), nil
}

// ChangesSince reports the folder's adds and deletes since the journal sequence.
// A created entry's journaled id is the storage blob key, so the body and headers
// come straight from the blob via MessageFromRaw, matching the snapshot's stable
// ServerId. Flag-change fidelity (the updated kind) is deferred to the up-sync
// phase; it is skipped here rather than guessed, and the call still returns
// cleanly so the cursor advances.
func (s easMailSource) ChangesSince(email, collectionID string, since uint64) (adds, changes []activesync.SyncMessage, deletes []string, newSeq uint64, err error) {
	entries, _, lastSeq, err := s.db.GetChangesSince(email, storage.ChangeTypeEmail, since, easChangeWindow)
	if err != nil {
		return nil, nil, nil, since, err
	}
	for _, e := range entries {
		if e.Mailbox != collectionID {
			continue
		}
		switch e.Kind {
		case storage.ChangeKindCreated:
			if raw, rerr := s.msg.ReadMessage(email, e.ID); rerr == nil {
				adds = append(adds, activesync.MessageFromRaw(e.ID, raw))
			}
		case storage.ChangeKindDestroyed:
			deletes = append(deletes, e.ID)
		}
	}
	return adds, changes, deletes, lastSeq, nil
}

// easMutator applies a mobile client's EAS Sync up-sync changes to the canonical
// mailstore, converging them on the one store every surface reads — a read-flag
// set or a deletion authored on a phone is reflected over IMAP/POP3/JMAP/webmail
// too. It reuses the same canonical delete the MAPI surface uses and the IMAP
// flag-store (which raises the IMAP IDLE and webmail SSE notifications). The EAS
// ServerId is the storage blob key; the target message is present whenever a
// client mutates it, so the (folder, uid) is resolved by a folder scan, and a
// stale id (the item already gone) is treated as a no-op rather than an error.
// It embeds emsmdbMutator to reuse that surface's canonical delete and its
// uid->sequence-set mapping rather than reimplementing them.
type easMutator struct{ emsmdbMutator }

func (m easMutator) SetRead(email, collectionID, serverID string, read bool) error {
	uid, ok := m.uidByServerID(email, collectionID, serverID)
	if !ok {
		return nil
	}
	seqSet, n := m.uidsToSeqSet(email, collectionID, []uint32{uid})
	if n == 0 {
		return nil
	}
	op := imap.FlagRemove
	if read {
		op = imap.FlagAdd
	}
	return m.srv.mailstore.StoreFlags(email, collectionID, seqSet, []string{"\\Seen"}, op)
}

func (m easMutator) Delete(email, collectionID, serverID string) error {
	uid, ok := m.uidByServerID(email, collectionID, serverID)
	if !ok {
		return nil
	}
	_, err := m.DeleteMessages(email, collectionID, []uint32{uid})
	return err
}

// uidByServerID resolves an EAS ServerId (the storage blob key) to the message's
// IMAP uid within the folder by scanning its metadata.
func (m easMutator) uidByServerID(email, folder, serverID string) (uint32, bool) {
	uids, err := m.srv.storageDB.GetMessageUIDs(email, folder)
	if err != nil {
		return 0, false
	}
	for _, uid := range uids {
		meta, merr := m.srv.storageDB.GetMessageMetadata(email, folder, uid)
		if merr == nil && meta != nil && meta.MessageID == serverID {
			return uid, true
		}
	}
	return 0, false
}
