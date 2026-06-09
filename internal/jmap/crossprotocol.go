package jmap

import (
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// jmapFolderRole maps a mailbox name to its distinguished role, or "" for a
// user-created folder. EWS FindItem reads the semcore identity store keyed by
// folder identity, so a JMAP-filed message's semcore folder must resolve to the
// same distinguished folder EWS and IMAP use; that convergence is what keeps one
// message in one canonical folder across every surface. This mirrors the
// server-side distinguishedRole resolver and is deliberately NOT
// getMailboxIDFromName, which maps a folder to its JMAP id (returning the raw
// name for custom folders and omitting Notes/Scheduled) — wrong for a role.
func jmapFolderRole(name string) string {
	switch name {
	case "INBOX":
		return "inbox"
	case "Sent":
		return "sent"
	case "Drafts":
		return "drafts"
	case "Trash":
		return "trash"
	case "Junk":
		return "junk"
	case "Archive":
		return "archive"
	case "Notes":
		return "notes"
	case "Scheduled":
		return "scheduled"
	default:
		return ""
	}
}

// addSemcoreIdentity registers the semcore identity of a JMAP-filed message
// (already stored under blobKey) in folder, so the message is visible over EWS
// FindItem too — not only JMAP/IMAP/POP3/webmail, which read the mailstore
// index. Best-effort and idempotent: a no-op when semcore is unwired or the blob
// is missing, and the mutation pipeline collapses a repeat call onto the same
// content-addressed item. JMAP keeps its own mailstore write (it carries
// thread-id and keyword->flag logic the semcore path does not); this augments
// that write, it does not replace it.
func (s *Server) addSemcoreIdentity(user, folder, blobKey string, flags []string, when time.Time) {
	if s.notesPipe == nil || s.notesIdentity == nil || s.msgStore == nil || blobKey == "" {
		return
	}
	raw, err := s.msgStore.ReadMessage(user, blobKey)
	if err != nil {
		s.logger.Warn("jmap semcore add: read blob", "user", user, "blob", blobKey, "error", err)
		return
	}
	mboxID, err := s.notesIdentity.EnsureMailboxId(user)
	if err != nil {
		s.logger.Warn("jmap semcore add: ensure mailbox", "user", user, "error", err)
		return
	}
	folderID, err := s.notesIdentity.EnsureFolderId(user, folder, jmapFolderRole(folder))
	if err != nil {
		s.logger.Warn("jmap semcore add: ensure folder", "user", user, "folder", folder, "error", err)
		return
	}
	if _, err := s.notesPipe.MutateItem(&semcore.MutationInput{
		MailboxID:    mboxID,
		FolderID:     folderID,
		RawMessage:   raw,
		InternalDate: when,
		Actor:        user,
		Email:        user,
		Source:       semcore.MutationSourceJMAP,
		UserFlags:    flags,
		IsRead:       storage.HasFlag(flags, "\\Seen"),
	}); err != nil {
		s.logger.Warn("jmap semcore add: mutate", "user", user, "folder", folder, "error", err)
	}
}

// removeSemcoreIdentity drops the semcore identity of a message (matched by blob
// key) in folder, so a JMAP move-away or destroy does not leave the item
// ghosting in EWS FindItem. The mailstore delete path does not touch semcore, so
// the deletion must reach it here or the two stores diverge. Idempotent: a no-op
// when semcore is unwired or no identity matches.
func (s *Server) removeSemcoreIdentity(user, folder, blobKey string) {
	if s.notesIdentity == nil || blobKey == "" {
		return
	}
	folderID, err := s.notesIdentity.EnsureFolderId(user, folder, jmapFolderRole(folder))
	if err != nil {
		return
	}
	items, err := s.notesIdentity.ListItemIdentitiesByFolder(folderID)
	if err != nil {
		return
	}
	for _, it := range items {
		if it.MsgKey == blobKey {
			if err := s.notesIdentity.DeleteItemIdentity(it.ItemID); err != nil {
				s.logger.Warn("jmap semcore remove: delete identity", "user", user, "folder", folder, "error", err)
			}
		}
	}
}
