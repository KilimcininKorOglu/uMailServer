// Package mailcheck is a read-only consistency checker (mbck) over the canonical
// store. uMailServer keeps a message in three places that must agree: the
// Maildir blob, the IMAP metadata index, and the semcore identity store. When
// they diverge a message can become a "ghost in EWS" (present in the IMAP index
// but with no semcore identity, so EWS/Outlook never sees it) or a dangling
// index/identity entry whose blob is gone. Check cross-references the three and
// reports every divergence; it never writes (repair is intentionally out of
// scope).
//
// The store dependencies are small interfaces so the checker is decoupled from
// the concrete storage/semcore types and unit-testable with fakes; the CLI
// adapts the real stores to them.
package mailcheck

import "fmt"

// IndexStore is the IMAP metadata index view the checker reads.
type IndexStore interface {
	ListMailboxes(user string) ([]string, error)
	GetMessageUIDs(user, mailbox string) ([]uint32, error)
	// MessageID returns the content-hash blob key recorded for a message.
	MessageID(user, mailbox string, uid uint32) (string, error)
}

// BlobStore reports whether a message blob is present on disk.
type BlobStore interface {
	MessageExists(user, messageID string) bool
}

// IdentityStore is the semcore identity view the checker reads.
type IdentityStore interface {
	// MailboxKey resolves a user's mailbox key; ok is false when no semcore
	// mailbox identity exists yet.
	MailboxKey(email string) (key string, ok bool, err error)
	// FolderIDs lists the semcore folder IDs under a mailbox key.
	FolderIDs(mboxKey string) ([]string, error)
	// ItemKeys lists the blob keys (MsgKey) of the items in a semcore folder.
	ItemKeys(folderID string) ([]string, error)
}

// Issue kinds.
const (
	// KindOrphanIndex: an IMAP index entry whose blob is missing.
	KindOrphanIndex = "orphan-index"
	// KindOrphanIdentity: a semcore identity whose blob is missing.
	KindOrphanIdentity = "orphan-identity"
	// KindGhostInEWS: a message in the IMAP index with no semcore identity, so
	// it is invisible over EWS/Outlook.
	KindGhostInEWS = "ghost-in-ews"
	// KindOrphanSemcore: a semcore identity with no IMAP index entry.
	KindOrphanSemcore = "orphan-semcore"
	// KindNoSemcoreMailbox: the user has IMAP messages but no semcore mailbox
	// identity at all.
	KindNoSemcoreMailbox = "no-semcore-mailbox"
)

// Issue is one detected divergence.
type Issue struct {
	Kind    string
	Mailbox string // IMAP mailbox, when the issue originates index-side
	MsgKey  string // content-hash blob key, when applicable
	Detail  string
}

func (i Issue) String() string {
	loc := i.Mailbox
	if loc == "" {
		loc = "-"
	}
	return fmt.Sprintf("[%s] mailbox=%s key=%s %s", i.Kind, loc, i.MsgKey, i.Detail)
}

// Report is the result of a consistency check.
type Report struct {
	Issues       []Issue
	IndexCount   int // IMAP index entries scanned
	SemcoreCount int // semcore item identities scanned
}

// Clean reports whether the check found no divergences.
func (r *Report) Clean() bool { return len(r.Issues) == 0 }

func (r *Report) add(kind, mailbox, msgKey, detail string) {
	r.Issues = append(r.Issues, Issue{Kind: kind, Mailbox: mailbox, MsgKey: msgKey, Detail: detail})
}

// Check cross-references the IMAP index, the blob store, and the semcore
// identity store for one user and returns every divergence. It is read-only.
func Check(email string, idx IndexStore, blob BlobStore, ident IdentityStore) (*Report, error) {
	rep := &Report{}

	// --- IMAP index side: collect message IDs, flag blobs that are gone. ---
	imapIDs := map[string]bool{}
	mailboxes, err := idx.ListMailboxes(email)
	if err != nil {
		return nil, fmt.Errorf("mailcheck: list mailboxes: %w", err)
	}
	for _, mailbox := range mailboxes {
		uids, uerr := idx.GetMessageUIDs(email, mailbox)
		if uerr != nil {
			return nil, fmt.Errorf("mailcheck: uids for %q: %w", mailbox, uerr)
		}
		for _, uid := range uids {
			id, merr := idx.MessageID(email, mailbox, uid)
			if merr != nil || id == "" {
				continue
			}
			rep.IndexCount++
			imapIDs[id] = true
			if !blob.MessageExists(email, id) {
				rep.add(KindOrphanIndex, mailbox, id, fmt.Sprintf("uid %d has no message blob", uid))
			}
		}
	}

	// --- semcore side: collect MsgKeys, flag blobs that are gone. ---
	semcoreKeys := map[string]bool{}
	mboxKey, ok, kerr := ident.MailboxKey(email)
	if kerr != nil {
		return nil, fmt.Errorf("mailcheck: resolve mailbox identity: %w", kerr)
	}
	if !ok {
		if len(imapIDs) > 0 {
			rep.add(KindNoSemcoreMailbox, "", "", "IMAP messages exist but the mailbox has no semcore identity")
		}
		return rep, nil
	}
	folderIDs, ferr := ident.FolderIDs(mboxKey)
	if ferr != nil {
		return nil, fmt.Errorf("mailcheck: list folders: %w", ferr)
	}
	for _, fid := range folderIDs {
		keys, ierr := ident.ItemKeys(fid)
		if ierr != nil {
			return nil, fmt.Errorf("mailcheck: items for folder %q: %w", fid, ierr)
		}
		for _, key := range keys {
			if key == "" {
				continue
			}
			rep.SemcoreCount++
			semcoreKeys[key] = true
			if !blob.MessageExists(email, key) {
				rep.add(KindOrphanIdentity, "", key, "semcore identity has no message blob")
			}
		}
	}

	// --- cross-check presence in both stores. ---
	for id := range imapIDs {
		if !semcoreKeys[id] {
			rep.add(KindGhostInEWS, "", id, "in IMAP index but no semcore identity (invisible over EWS)")
		}
	}
	for key := range semcoreKeys {
		if !imapIDs[key] {
			rep.add(KindOrphanSemcore, "", key, "semcore identity but no IMAP index entry")
		}
	}
	return rep, nil
}
