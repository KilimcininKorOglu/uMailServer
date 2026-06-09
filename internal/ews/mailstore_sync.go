package ews

import (
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// mailstore_sync mirrors EWS-originated item mutations into the IMAP mailstore
// index (storage.Database, the "m.db" UID index) so that items created, deleted,
// or moved over EWS become visible to IMAP, POP3, JMAP, and webmail — which all
// read messages from that index, not from the semcore identity store. Without
// this, EWS-authored items (drafts, sent copies, notes) only existed in the
// semcore identity store + the blob store and were invisible to every other
// surface. This is the cross-protocol-integrity bridge for the EWS write path,
// mirroring what deliverLocal does on the delivery path.

// mailboxNameForFolder resolves a semcore FolderId to the IMAP mailbox name the
// message-store index is keyed by. Distinguished folders map through their role
// (CanonicalFolderNameForRole); user folders resolve to their full IMAP
// hierarchy path (folderLineagePath). Collaboration folders (calendar/contacts/
// tasks) are NOT mail and return "" so callers skip them — their items live in
// the collaboration store, not m.db.
func (s *Server) mailboxNameForFolder(mailboxKey string, folderID semcore.FolderId) string {
	rec, err := s.identity.GetFolderByID(folderID)
	if err != nil || rec == nil {
		return ""
	}
	switch rec.Role {
	case "calendar", "contacts", "tasks":
		return ""
	case "":
		// User-created folder: resolve the full hierarchy path so two folders
		// that share a display name under different parents map to distinct
		// mailboxes (e.g. "Archive/Reports" vs a top-level "Reports"), and their
		// mirrored items never collide in the wrong IMAP mailbox.
		return s.folderLineagePath(mailboxKey, folderID)
	default:
		return semcore.CanonicalFolderNameForRole(rec.Role)
	}
}

// folderLineagePath builds the IMAP mailbox path for a user folder by walking
// ParentID to the root and joining each ancestor's client-visible display name
// with the IMAP hierarchy separator "/". The parent-scoped storage prefix is
// stripped per segment (DisplayNameFromStorageName), so a collided child
// resolves to a distinct path. A distinguished ancestor anchors the path at its
// canonical name and ends the walk. A top-level user folder yields exactly its
// flat name, matching the pre-existing mirror behavior. Depth is bounded and a
// visited set guards against a cyclic ParentID chain.
func (s *Server) folderLineagePath(mailboxKey string, id semcore.FolderId) string {
	const maxDepth = 64
	segments := make([]string, 0, 8)
	seen := make(map[string]bool, 8)
	cur := id
	for depth := 0; depth < maxDepth; depth++ {
		if cur.IsZero() || seen[cur.String()] {
			break
		}
		seen[cur.String()] = true
		name, err := s.identity.FolderNameByID(mailboxKey, cur)
		if err != nil || name == "" {
			break
		}
		segments = append(segments, semcore.DisplayNameFromStorageName(name))
		rec, err := s.identity.GetFolderByID(cur)
		if err != nil || rec == nil {
			break
		}
		if canon := semcore.CanonicalFolderNameForRole(rec.Role); canon != "" {
			segments[len(segments)-1] = canon
			break
		}
		cur = rec.ParentID
	}
	if len(segments) == 0 {
		return ""
	}
	// segments are leaf→root; reverse to root→leaf for the IMAP path.
	for i, j := 0, len(segments)-1; i < j; i, j = i+1, j-1 {
		segments[i], segments[j] = segments[j], segments[i]
	}
	return strings.Join(segments, "/")
}

// mirrorCreateToMailstore writes a metadata entry for an EWS-created item into
// the IMAP mailstore index and signals real-time consumers. blobKey is the
// message-store key returned by msgStore.StoreMessage (used as the metadata
// MessageID so IMAP FETCH/webmail can read the same blob). Best-effort: a
// failure is logged but never fails the EWS operation (the semcore identity
// write remains the canonical record).
func (s *Server) mirrorCreateToMailstore(mailboxKey string, folderID semcore.FolderId, rawMsg []byte, blobKey string) {
	if s.storageDB == nil {
		return
	}
	name := s.mailboxNameForFolder(mailboxKey, folderID)
	if name == "" {
		return // collaboration folder or unresolvable name: not a mail-index folder
	}
	uid, err := s.storageDB.GetNextUID(mailboxKey, name)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("ews: mailstore sync GetNextUID failed", "email", mailboxKey, "folder", name, "error", err)
		}
		return
	}
	meta := &storage.MessageMetadata{
		MessageID:    blobKey,
		UID:          uid,
		Flags:        []string{},
		InternalDate: time.Now(),
		Size:         int64(len(rawMsg)),
		Subject:      rawHeaderValue(rawMsg, "Subject"),
		Date:         rawHeaderValue(rawMsg, "Date"),
		From:         rawHeaderValue(rawMsg, "From"),
		To:           rawHeaderValue(rawMsg, "To"),
	}
	if err := s.storageDB.StoreMessageMetadata(mailboxKey, name, uid, meta); err != nil {
		if s.logger != nil {
			s.logger.Error("ews: mailstore sync StoreMessageMetadata failed", "email", mailboxKey, "folder", name, "uid", uid, "error", err)
		}
		return
	}
	if s.messageCreatedNotifier != nil {
		s.messageCreatedNotifier(mailboxKey, name, uid)
	}
}

// mailstoreLocate finds the IMAP mailstore entry for a blob within a mailbox,
// returning its UID, 1-based sequence number, and metadata. EWS items are keyed
// by blob key (which equals the metadata MessageID written on create), so a scan
// by MessageID reconciles the EWS item to its mailstore UID.
func (s *Server) mailstoreLocate(email, mailbox, blobKey string) (uid uint32, seqNum uint32, meta *storage.MessageMetadata, ok bool) {
	uids, err := s.storageDB.GetMessageUIDs(email, mailbox)
	if err != nil {
		return 0, 0, nil, false
	}
	for i, u := range uids {
		m, err := s.storageDB.GetMessageMetadata(email, mailbox, u)
		if err != nil || m == nil {
			continue
		}
		if m.MessageID == blobKey {
			return u, uint32(i + 1), m, true
		}
	}
	return 0, 0, nil, false
}

// mirrorDeleteFromMailstore removes an EWS-deleted item from the IMAP mailstore
// index and signals an EXPUNGE, so the item disappears from IMAP/POP3/JMAP/webmail.
func (s *Server) mirrorDeleteFromMailstore(mailboxKey string, folderID semcore.FolderId, blobKey string) {
	if s.storageDB == nil {
		return
	}
	name := s.mailboxNameForFolder(mailboxKey, folderID)
	if name == "" {
		return
	}
	uid, seqNum, _, ok := s.mailstoreLocate(mailboxKey, name, blobKey)
	if !ok {
		return
	}
	if err := s.storageDB.DeleteMessage(mailboxKey, name, uid); err != nil {
		if s.logger != nil {
			s.logger.Error("ews: mailstore sync DeleteMessage failed", "email", mailboxKey, "folder", name, "uid", uid, "error", err)
		}
		return
	}
	if s.messageExpungedNotifier != nil {
		s.messageExpungedNotifier(mailboxKey, name, uid, seqNum)
	}
	// Deleting an item out of the Scheduled folder cancels its pending send, so
	// EWS DeleteItem matches IMAP EXPUNGE (one surface cancels for all surfaces).
	if s.scheduledCancelNotifier != nil && strings.EqualFold(name, "Scheduled") {
		s.scheduledCancelNotifier(mailboxKey, uid)
	}
}

// mirrorReadFlagToMailstore syncs an EWS IsRead change onto the mailstore entry's
// \Seen flag so the read state matches across IMAP/JMAP/webmail.
func (s *Server) mirrorReadFlagToMailstore(mailboxKey string, folderID semcore.FolderId, blobKey string, isRead bool) {
	if s.storageDB == nil {
		return
	}
	name := s.mailboxNameForFolder(mailboxKey, folderID)
	if name == "" {
		return
	}
	uid, _, meta, ok := s.mailstoreLocate(mailboxKey, name, blobKey)
	if !ok || meta == nil {
		return
	}
	meta.Flags = setSeenFlag(meta.Flags, isRead)
	if err := s.storageDB.StoreMessageMetadata(mailboxKey, name, uid, meta); err != nil {
		if s.logger != nil {
			s.logger.Error("ews: mailstore sync flag update failed", "email", mailboxKey, "folder", name, "uid", uid, "error", err)
		}
		return
	}
	// Coarse refresh: a folder-update notification makes IMAP IDLE re-sync and
	// the webmail SSE refetch, surfacing the new read state.
	if s.folderChangeNotifier != nil {
		s.folderChangeNotifier(mailboxKey, name)
	}
}

// mirrorMoveInMailstore moves an EWS item between mailstore folders: it removes
// the source entry (EXPUNGE) and re-stores the same blob under a fresh UID in the
// destination (EXISTS), keeping IMAP/JMAP/webmail consistent with the EWS move.
func (s *Server) mirrorMoveInMailstore(mailboxKey string, sourceFolder, destFolder semcore.FolderId, blobKey string) {
	if s.storageDB == nil {
		return
	}
	srcName := s.mailboxNameForFolder(mailboxKey, sourceFolder)
	dstName := s.mailboxNameForFolder(mailboxKey, destFolder)
	if srcName == "" || dstName == "" || srcName == dstName {
		return
	}
	uid, seqNum, meta, ok := s.mailstoreLocate(mailboxKey, srcName, blobKey)
	if !ok {
		return
	}
	if err := s.storageDB.DeleteMessage(mailboxKey, srcName, uid); err != nil {
		if s.logger != nil {
			s.logger.Error("ews: mailstore sync move DeleteMessage failed", "email", mailboxKey, "folder", srcName, "uid", uid, "error", err)
		}
		return
	}
	if s.messageExpungedNotifier != nil {
		s.messageExpungedNotifier(mailboxKey, srcName, uid, seqNum)
	}
	// Moving an item out of the Scheduled folder cancels its pending send, just
	// like deleting it (IMAP move-out cancels via the source EXPUNGE hook too).
	if s.scheduledCancelNotifier != nil && strings.EqualFold(srcName, "Scheduled") {
		s.scheduledCancelNotifier(mailboxKey, uid)
	}
	newUID, err := s.storageDB.GetNextUID(mailboxKey, dstName)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("ews: mailstore sync move GetNextUID failed", "email", mailboxKey, "folder", dstName, "error", err)
		}
		return
	}
	if meta == nil {
		meta = &storage.MessageMetadata{MessageID: blobKey}
	}
	meta.UID = newUID
	if err := s.storageDB.StoreMessageMetadata(mailboxKey, dstName, newUID, meta); err != nil {
		if s.logger != nil {
			s.logger.Error("ews: mailstore sync move StoreMessageMetadata failed", "email", mailboxKey, "folder", dstName, "uid", newUID, "error", err)
		}
		return
	}
	if s.messageCreatedNotifier != nil {
		s.messageCreatedNotifier(mailboxKey, dstName, newUID)
	}
}

// setSeenFlag returns flags with the \Seen IMAP flag added or removed.
func setSeenFlag(flags []string, seen bool) []string {
	out := make([]string, 0, len(flags)+1)
	has := false
	for _, f := range flags {
		if strings.EqualFold(f, "\\Seen") {
			has = true
			if !seen {
				continue
			}
		}
		out = append(out, f)
	}
	if seen && !has {
		out = append(out, "\\Seen")
	}
	return out
}
