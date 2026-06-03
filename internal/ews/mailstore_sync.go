package ews

import (
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
// (CanonicalFolderNameForRole); user folders are resolved by their stored name.
// Collaboration folders (calendar/contacts/tasks) are NOT mail and return "" so
// callers skip them — their items live in the collaboration store, not m.db.
func (s *Server) mailboxNameForFolder(mailboxKey string, folderID semcore.FolderId) string {
	rec, err := s.identity.GetFolderByID(folderID)
	if err != nil || rec == nil {
		return ""
	}
	switch rec.Role {
	case "calendar", "contacts", "tasks":
		return ""
	case "":
		// User-created folder: recover its name from the identity store.
		name, err := s.identity.FolderNameByID(mailboxKey, folderID)
		if err != nil {
			return ""
		}
		return name
	default:
		return semcore.CanonicalFolderNameForRole(rec.Role)
	}
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
