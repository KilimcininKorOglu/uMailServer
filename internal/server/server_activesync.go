package server

import (
	"bytes"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/umailserver/umailserver/internal/activesync"
	"github.com/umailserver/umailserver/internal/caldav"
	"github.com/umailserver/umailserver/internal/carddav"
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/ews"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/mailappend"
	"github.com/umailserver/umailserver/internal/mapi"
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

// easFolderSource projects a mailbox's canonical folder list into EAS folders.
// A mail folder uses its name as the stable EAS ServerId; the Calendar collab
// folder is prefix-tagged (activesync.CalendarCollectionID) so the Sync router
// recognizes it and reads it from the collaboration store, not the mailstore.
type easFolderSource struct {
	db       storageBackend
	identity semIdentity
}

func (f easFolderSource) Folders(email string) ([]activesync.Folder, error) {
	names, err := f.db.ListMailboxes(email)
	if err != nil {
		return nil, err
	}
	folders := make([]activesync.Folder, 0, len(names)+1)
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
	// Expose the canonical Calendar folder so a mobile client always has a
	// calendar to sync. The collab folder set is populated lazily (on first
	// CalDAV/EWS access), so it is ensured here — mirroring how EWS reconciles
	// folder identities on its hierarchy sync — rather than only shown when a
	// prior surface happened to create it.
	if f.identity != nil {
		if fid, err := f.identity.EnsureFolderId(email, "calendar", "calendar"); err == nil && !fid.IsZero() {
			folders = append(folders, activesync.Folder{
				ServerID:    activesync.CalendarCollectionID(fid.String()),
				ParentID:    "0",
				DisplayName: "Calendar",
				Type:        activesync.FolderTypeCalendar,
			})
		}
		// Expose the canonical Contacts folder for the same reason — the collab
		// folder is created lazily, so it is ensured here rather than only shown
		// when CardDAV/EWS happened to create it first.
		if fid, err := f.identity.EnsureFolderId(email, "contacts", "contacts"); err == nil && !fid.IsZero() {
			folders = append(folders, activesync.Folder{
				ServerID:    activesync.ContactCollectionID(fid.String()),
				ParentID:    "0",
				DisplayName: "Contacts",
				Type:        activesync.FolderTypeContacts,
			})
		}
		// Expose the canonical Tasks folder for the same reason — the collab folder
		// is created lazily, so it is ensured here rather than only shown when
		// EWS/the webmail tasks API happened to create it first.
		if fid, err := f.identity.EnsureFolderId(email, "tasks", "tasks"); err == nil && !fid.IsZero() {
			folders = append(folders, activesync.Folder{
				ServerID:    activesync.TaskCollectionID(fid.String()),
				ParentID:    "0",
				DisplayName: "Tasks",
				Type:        activesync.FolderTypeTasks,
			})
		}
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

// Fetch returns the full, untruncated message for an ItemOperations Fetch by its
// ServerId (the storage blob key), or nil when the blob is gone.
func (s easMailSource) Fetch(email, collectionID, serverID string) (*activesync.SyncMessage, error) {
	raw, err := s.msg.ReadMessage(email, serverID)
	if err != nil {
		return nil, nil
	}
	m := activesync.MessageFromRaw(serverID, raw)
	return &m, nil
}

// easCalendarSource projects a mailbox's calendar events for the EAS Sync
// command from the shared collaboration store — the SAME store EWS, CalDAV and
// JMAP read — so a phone's calendar converges with every other surface on one
// source. The folder id arrives already stripped of the routing prefix. Each
// event's canonical iCalendar (RawData) is projected by the activesync package,
// keyed by the collab item id (its stable EAS ServerId) and the collab ETag
// (which drives the enumerate-and-diff cursor).
type easCalendarSource struct{ collab ews.CollabStore }

func (c easCalendarSource) ListItems(email, folderID string) ([]activesync.CalendarItem, error) {
	fid, err := semcore.NewFolderId(folderID)
	if err != nil {
		return nil, nil
	}
	recs, err := c.collab.ListCalendarItemsByFolder(fid)
	if err != nil {
		return nil, err
	}
	items := make([]activesync.CalendarItem, 0, len(recs))
	for i := range recs {
		r := &recs[i]
		// Skip recurrence-exception instances (fragments of a master event, not
		// standalone items) and any item without an iCal UID. The UID is the EAS
		// ServerId — the key the canonical store mutates on for up-sync — so an
		// event without one cannot round-trip.
		if !r.MasterID.IsZero() || r.IcalUID == "" {
			continue
		}
		items = append(items, activesync.CalendarItemFromICal(r.IcalUID, r.ETag, r.RawData))
	}
	return items, nil
}

// easCalendarID is the calendar id handed to the collaboration store's
// SaveEvent/DeleteEvent. The store resolves the mailbox's calendar folder by its
// canonical "calendar" role, so this value is a stable label, not a lookup key.
const easCalendarID = "calendar"

// easCalendarMutator applies a mobile client's calendar up-sync (Add/Change/
// Delete) through the canonical collaboration store — the same SaveEvent path
// CalDAV and the webmail calendar API use — so an event authored on a phone
// converges over EWS/CalDAV/JMAP/webmail. The EAS ServerId is the event's iCal
// UID, which the store keys on, so a Change/Delete maps to it directly and a
// new Add returns the UID as the client's permanent ServerId.
type easCalendarMutator struct{ store caldav.Store }

func (m easCalendarMutator) CreateItem(email, _ string, it activesync.CalendarItem) (string, error) {
	if it.UID == "" {
		it.UID = uuid.NewString()
	}
	ev := &caldav.CalendarEvent{UID: it.UID, Summary: it.Subject, Created: time.Now(), Modified: time.Now()}
	if err := m.store.SaveEvent(email, easCalendarID, ev, activesync.BuildICalEvent(it)); err != nil {
		return "", err
	}
	return it.UID, nil
}

func (m easCalendarMutator) UpdateItem(email, _, serverID string, it activesync.CalendarItem) error {
	it.UID = serverID // the EAS calendar ServerId is the iCal UID
	// Merge onto the canonical event's verbatim iCalendar so a phone edit cannot
	// erase properties the EAS projection does not model (recurrence, attendees,
	// alarms). A read failure aborts the write rather than truncating silently.
	existing, err := m.store.GetEvent(email, easCalendarID, serverID)
	if err != nil {
		return err
	}
	ev := &caldav.CalendarEvent{UID: serverID, Summary: it.Subject, Created: time.Now(), Modified: time.Now()}
	return m.store.SaveEvent(email, easCalendarID, ev, activesync.MergeICalEvent(existing, it))
}

func (m easCalendarMutator) DeleteItem(email, _, serverID string) error {
	return m.store.DeleteEvent(email, easCalendarID, serverID)
}

// easMeetingResponder applies an EAS MeetingResponse to the canonical calendar.
// The meeting request is an email in the mailbox (RequestId is its storage blob
// key), so the invite's event is read from the message blob and then written to
// (accept/tentative) or removed from (decline) the calendar through the same
// SaveEvent/DeleteEvent path the webmail RSVP and CalDAV use — converging the
// response across every surface. Matching that RSVP path, no iTIP reply email is
// sent (the calendar state is the canonical record of the response).
type easMeetingResponder struct {
	msg *storage.MessageStore
	cal easCalendarMutator
}

func (m easMeetingResponder) Respond(email, _, requestID, response string) (string, error) {
	raw, err := m.msg.ReadMessage(email, requestID)
	if err != nil {
		return "", err
	}
	inv, ok := activesync.InviteEventFromMIME(raw)
	if !ok {
		return "", fmt.Errorf("activesync meetingresponse: message %q is not a meeting invite", requestID)
	}
	if response == "decline" {
		// Decline removes the event if a prior accept added it; a missing event
		// is success. No CalendarId is returned (nothing is on the calendar).
		if err := m.cal.DeleteItem(email, "", inv.UID); err != nil {
			return "", err
		}
		return "", nil
	}
	if response == "tentative" {
		inv.Subject = "[Tentative] " + inv.Subject
		inv.BusyStatus = "1"
	}
	return m.cal.CreateItem(email, "", inv)
}

// easContactSource projects a mailbox's address book for the EAS Sync command
// from the shared collaboration store — the SAME store EWS and CardDAV read — so
// a phone's contacts converge with every other surface on one source. Each card's
// canonical vCard (RawData) is projected by the activesync package, keyed by the
// vCard UID (its stable EAS ServerId, the key the canonical store mutates on) and
// the collab ETag (which drives the enumerate-and-diff cursor).
type easContactSource struct{ collab ews.CollabStore }

func (c easContactSource) ListItems(email, folderID string) ([]activesync.ContactItem, error) {
	fid, err := semcore.NewFolderId(folderID)
	if err != nil {
		return nil, nil
	}
	recs, err := c.collab.ListContactsByFolder(fid)
	if err != nil {
		return nil, err
	}
	items := make([]activesync.ContactItem, 0, len(recs))
	for i := range recs {
		r := &recs[i]
		// The vCard UID is the EAS ServerId the canonical store keys on, so a card
		// without one cannot round-trip. The webmail and CardDAV write paths both
		// assign a UID, so this skips nothing in practice.
		if r.IcalUID == "" {
			continue
		}
		items = append(items, activesync.ContactItemFromVCard(r.IcalUID, r.ETag, r.RawData))
	}
	return items, nil
}

// easAddressbookID is the address-book id handed to the collaboration contact
// store. The store resolves the mailbox's contacts folder by its canonical
// "contacts" role, so this value is a stable label, not a lookup key.
const easAddressbookID = "default"

// easContactMutator applies a mobile client's contacts up-sync (Add/Change/
// Delete) through the canonical collaboration store — the same SaveContact path
// CardDAV and the webmail contacts API use — so a card authored on a phone
// converges over EWS/CardDAV/webmail. The EAS ServerId is the card's vCard UID,
// which the store keys on, so a Change/Delete maps to it directly and a new Add
// returns the UID as the client's permanent ServerId.
type easContactMutator struct{ store *carddav.CollabStore }

func (m easContactMutator) CreateItem(email, _ string, it activesync.ContactItem) (string, error) {
	if it.UID == "" {
		it.UID = uuid.NewString()
	}
	if err := m.store.SaveContact(email, easAddressbookID, &carddav.Contact{UID: it.UID}, activesync.BuildVCard(it)); err != nil {
		return "", err
	}
	return it.UID, nil
}

func (m easContactMutator) UpdateItem(email, _, serverID string, it activesync.ContactItem) error {
	it.UID = serverID // the EAS contact ServerId is the vCard UID
	// Merge onto the canonical card's verbatim vCard so a phone edit cannot erase
	// properties the EAS projection does not model (photo, categories, extended
	// fields). A read failure aborts the write rather than truncating silently.
	existing, err := m.store.GetContact(email, easAddressbookID, serverID)
	if err != nil {
		return err
	}
	return m.store.SaveContact(email, easAddressbookID, &carddav.Contact{UID: serverID}, activesync.MergeVCard(existing, it))
}

func (m easContactMutator) DeleteItem(email, _, serverID string) error {
	return m.store.DeleteContact(email, easAddressbookID, serverID)
}

// easTaskSource projects a mailbox's task list for the EAS Sync command from the
// shared collaboration store — the SAME store EWS and the webmail tasks API read
// — so a phone's to-dos converge with every other surface on one source. Each
// task's canonical VTODO (RawData) is projected by the activesync package, keyed
// by the VTODO UID (its stable EAS ServerId, the key the canonical store mutates
// on) and the collab ETag (which drives the enumerate-and-diff cursor).
type easTaskSource struct{ collab ews.CollabStore }

func (t easTaskSource) ListItems(email, folderID string) ([]activesync.TaskItem, error) {
	fid, err := semcore.NewFolderId(folderID)
	if err != nil {
		return nil, nil
	}
	recs, err := t.collab.ListTasksByFolder(fid)
	if err != nil {
		return nil, err
	}
	items := make([]activesync.TaskItem, 0, len(recs))
	for i := range recs {
		r := &recs[i]
		// The VTODO UID is the EAS ServerId the canonical store keys on, so a to-do
		// without one cannot round-trip. The webmail task write path assigns a UID,
		// so this skips nothing in practice.
		if r.IcalUID == "" {
			continue
		}
		items = append(items, activesync.TaskItemFromVTODO(r.IcalUID, r.ETag, r.RawData))
	}
	return items, nil
}

// easTaskListID is the list id handed to the collaboration task store. The store
// resolves the mailbox's tasks folder by its canonical "tasks" role, so this
// value is a stable label, not a lookup key.
const easTaskListID = "tasks"

// easTaskMutator applies a mobile client's tasks up-sync (Add/Change/Delete)
// through the canonical collaboration task store — the same SaveEvent path the
// webmail tasks API uses — so a to-do authored on a phone converges over
// EWS/webmail. The EAS ServerId is the to-do's VTODO UID, which the store keys
// on, so a Change/Delete maps to it directly and a new Add returns the UID as the
// client's permanent ServerId.
type easTaskMutator struct{ store caldav.Store }

func (m easTaskMutator) CreateItem(email, _ string, it activesync.TaskItem) (string, error) {
	if it.UID == "" {
		it.UID = uuid.NewString()
	}
	ev := &caldav.CalendarEvent{UID: it.UID, Summary: it.Subject, Created: time.Now(), Modified: time.Now()}
	if err := m.store.SaveEvent(email, easTaskListID, ev, activesync.BuildVTODO(it)); err != nil {
		return "", err
	}
	return it.UID, nil
}

func (m easTaskMutator) UpdateItem(email, _, serverID string, it activesync.TaskItem) error {
	it.UID = serverID // the EAS task ServerId is the VTODO UID
	// Merge onto the canonical to-do's verbatim VTODO so a phone edit cannot erase
	// properties the EAS projection does not model (recurrence, reminders,
	// categories). A read failure aborts the write rather than truncating silently.
	existing, err := m.store.GetEvent(email, easTaskListID, serverID)
	if err != nil {
		return err
	}
	ev := &caldav.CalendarEvent{UID: serverID, Summary: it.Subject, Created: time.Now(), Modified: time.Now()}
	return m.store.SaveEvent(email, easTaskListID, ev, activesync.MergeVTODO(existing, it))
}

func (m easTaskMutator) DeleteItem(email, _, serverID string) error {
	return m.store.DeleteEvent(email, easTaskListID, serverID)
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

func (m easMutator) Move(email, srcCollectionID, dstCollectionID, serverID string) (bool, error) {
	uid, ok := m.uidByServerID(email, srcCollectionID, serverID)
	if !ok {
		return false, nil
	}
	moved, err := m.MoveMessages(email, srcCollectionID, dstCollectionID, []uint32{uid})
	if err != nil {
		return false, err
	}
	return moved > 0, nil
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

// easSendMail submits a composed message for the EAS SendMail/SmartForward/
// SmartReply commands: it parses the recipients from the message headers, queues
// delivery through the shared Sieve-aware submit path (so the recipient's rules
// run), and — when the client asked to keep a copy — files the message in Sent
// through the canonical Appender, never a hand-rolled write.
func (s *Server) easSendMail(email string, mime []byte, saveToSent bool) error {
	to := recipientsFromMIME(mime)
	if len(to) == 0 {
		return fmt.Errorf("activesync sendmail: message has no recipients")
	}
	if err := s.submitMessageWithSieve(email, to, mime); err != nil {
		return err
	}
	if saveToSent && s.appender != nil {
		if _, err := s.appender.Append(mailappend.Input{
			Email:      email,
			Folder:     "Sent",
			Raw:        mime,
			Actor:      email,
			Source:     semcore.MutationSourceAPI,
			IsRead:     true,
			ExtraFlags: []string{"\\Seen"},
		}); err != nil {
			s.logger.Warn("activesync sendmail: save to Sent failed", "email", email, "error", err)
		}
	}
	return nil
}

// recipientsFromMIME collects the unique To/Cc/Bcc addresses of a composed
// message — the envelope recipients the submit path delivers to.
func recipientsFromMIME(raw []byte) []string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	var to []string
	seen := map[string]bool{}
	for _, hdr := range []string{"To", "Cc", "Bcc"} {
		addrs, aerr := msg.Header.AddressList(hdr)
		if aerr != nil {
			continue
		}
		for _, a := range addrs {
			if !seen[a.Address] {
				seen[a.Address] = true
				to = append(to, a.Address)
			}
		}
	}
	return to
}

// aliasLister is the slice of db.Store the EAS UserInformation adapter needs:
// the canonical alias table.
type aliasLister interface {
	ListAliases() ([]*db.AliasData, error)
}

// easAliasSource lists a mailbox's alias addresses for the EAS Settings
// UserInformation sub-command from the canonical alias table — the same
// db.ListAliases the admin alias API reads — so the address set converges with
// every other surface rather than maintaining a parallel list. Only active
// aliases targeting the mailbox are returned.
type easAliasSource struct{ db aliasLister }

func (a easAliasSource) AliasesFor(email string) ([]string, error) {
	all, err := a.db.ListAliases()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, al := range all {
		if al != nil && al.IsActive && strings.EqualFold(al.Target, email) {
			out = append(out, al.Alias)
		}
	}
	return out, nil
}

// easGALSource resolves Global Address List entries for the EAS Search command's
// GAL store from the same policy-filtered directory the binary NSPI and OAB
// address-book surfaces query, so every GAL surface agrees on one source. The
// Alias is the address local-part, which the canonical GAL entry does not carry
// separately.
type easGALSource struct{ gal *mapi.Server }

func (g easGALSource) ResolveGAL(query string) []activesync.GALResult {
	entries := g.gal.ResolveGAL(query)
	out := make([]activesync.GALResult, 0, len(entries))
	for _, e := range entries {
		out = append(out, activesync.GALResult{
			DisplayName: e.DisplayName,
			Email:       e.Email,
			Alias:       galAlias(e.Email),
		})
	}
	return out
}

// galAlias derives a GAL alias from an email address's local-part.
func galAlias(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return email
}

// easMailSearch runs the EAS Search command's Mailbox store over the canonical
// per-user mailstore — the SAME store the webmail search endpoint scans — applying
// the same case-insensitive substring match over sender, recipients, subject and
// the decoded body, so a phone's mailbox search returns the same hits as the
// browser. The matcher is mirrored from internal/api/server_search.go rather than
// shared, because that copy is entangled with the api package's email-DTO
// assembly; unifying the two into one matcher is an api-package refactor, deferred.
// Each hit resolves directly to its folder (the EAS CollectionId) and storage blob
// key (the EAS ServerId), both already in hand from the metadata scan.
type easMailSearch struct {
	db  storageBackend
	msg *storage.MessageStore
}

// SearchMail returns every message whose sender, recipients, subject or decoded
// body contains the query (case-insensitive), across all of the user's folders
// (or one folder when folder is non-empty). The full match set is returned
// unwindowed; the Search handler applies the requested Range. The decoded body
// comes from the same projection the EAS Sync/Fetch path shows, so a match is
// always over content the client can actually open.
func (s easMailSearch) SearchMail(email, query, folder string) ([]activesync.MailHit, error) {
	folders, err := s.db.ListMailboxes(email)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var hits []activesync.MailHit
	for _, mb := range folders {
		if folder != "" && mb != folder {
			continue
		}
		uids, uerr := s.db.GetMessageUIDs(email, mb)
		if uerr != nil {
			continue
		}
		for _, uid := range uids {
			meta, merr := s.db.GetMessageMetadata(email, mb, uid)
			if merr != nil || meta == nil || meta.MessageID == "" {
				continue
			}
			body := ""
			if raw, rerr := s.msg.ReadMessage(email, meta.MessageID); rerr == nil {
				_, body = activesync.BodyForSync(raw)
			}
			haystack := strings.ToLower(meta.From + " " + meta.Subject + " " + body + " " + meta.To)
			if q != "" && !strings.Contains(haystack, q) {
				continue
			}
			hits = append(hits, activesync.MailHit{
				CollectionID: mb,
				ServerID:     meta.MessageID,
				Class:        "Email",
			})
		}
	}
	return hits, nil
}

// easMailNotifier bridges the shared IMAP notification hub to the EAS Ping
// command, satisfying activesync.MailboxNotifier. A held Ping subscribes through
// it and receives the name of any mail folder that changed — the same string a
// mail collection uses as its EAS CollectionId — so a delivery, expunge, or flag
// change wakes the long-poll. It keeps the activesync package free of any imap
// import (the adapter does the translation), mirroring emsmdbNotifier; a
// non-blocking forward drops events when the consumer is behind, matching the
// hub's own back-pressure policy.
type easMailNotifier struct{}

func (easMailNotifier) Subscribe(email string) (<-chan string, func()) {
	hub := imap.GetNotificationHub()
	in := hub.Subscribe(email)
	out := make(chan string, 64)
	go func() {
		defer close(out)
		for n := range in {
			select {
			case out <- n.Mailbox:
			default:
			}
		}
	}()
	return out, func() { hub.Unsubscribe(email, in) }
}
