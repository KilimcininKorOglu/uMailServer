package imap

import (
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// addSemcoreIdentity registers a message's semcore identity in folder so a COPY
// destination is visible over EWS FindItem too (which reads the semcore identity
// store), not only the IMAP/POP3/JMAP/webmail mailstore index. It mirrors the
// semcore block in AppendMessage; the message bytes are passed directly because
// the pipeline derives the content-addressed identity key from them. Best-effort
// and idempotent: a no-op when the pipeline is unwired, and a repeat call on the
// same content registers a folder-scoped identity rather than duplicating.
func (m *BboltMailstore) addSemcoreIdentity(user, folder string, flags []string, date time.Time, raw []byte) {
	if m.mutationPipe == nil {
		return
	}
	mboxID, err := m.mutationPipe.Identity().EnsureMailboxId(user)
	if err != nil {
		return
	}
	fldID, err := m.mutationPipe.Identity().EnsureFolderId(user, folder, distinguishedRole(folder))
	if err != nil {
		return
	}
	in := &semcore.MutationInput{
		MailboxID:    mboxID,
		FolderID:     fldID,
		RawMessage:   raw,
		InternalDate: date,
		Actor:        user,
		Email:        user,
		Source:       semcore.MutationSourceIMAP,
		UserFlags:    flags,
		IsRead:       storage.HasFlag(flags, "\\Seen"),
	}
	//nolint:errcheck // best-effort: identity registration must not fail the COPY
	_, _ = m.mutationPipe.MutateItem(in)
}

// removeSemcoreIdentity drops a message's semcore identity (matched by blob key)
// in folder so an EXPUNGE does not leave the item ghosting in EWS FindItem. The
// IMAP expunge path removes the mailstore index entry and the blob but not the
// semantic identity, so the deletion must reach it here. Idempotent: a no-op
// when the identity store is unwired or no identity matches.
func (m *BboltMailstore) removeSemcoreIdentity(user, folder, blobKey string) {
	if m.identity == nil || blobKey == "" {
		return
	}
	fldID, err := m.identity.EnsureFolderId(user, folder, distinguishedRole(folder))
	if err != nil {
		return
	}
	items, err := m.identity.ListItemIdentitiesByFolder(fldID)
	if err != nil {
		return
	}
	for _, it := range items {
		if it.MsgKey == blobKey {
			//nolint:errcheck // best-effort: a failed cleanup only risks an EWS ghost
			_ = m.identity.DeleteItemIdentity(it.ItemID)
		}
	}
}
