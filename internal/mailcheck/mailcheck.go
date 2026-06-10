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

// ---------------------------------------------------------------------------
// Repair (Phase 2) — writes to the canonical store
// ---------------------------------------------------------------------------

// ItemRef pairs a semcore item's id with its blob key.
type ItemRef struct {
	ItemID string
	MsgKey string
}

// RepairBlob extends BlobStore with reading, needed to recreate an identity from
// an existing message.
type RepairBlob interface {
	BlobStore
	ReadMessage(user, messageID string) ([]byte, error)
}

// RepairIdentity exposes semcore items grouped by their IMAP folder name, so the
// cross-check is per-folder (a message must have an identity in the SAME folder
// it is indexed under).
type RepairIdentity interface {
	MailboxKey(email string) (key string, ok bool, err error)
	// FolderItems maps each folder's IMAP name to the items it holds.
	FolderItems(mboxKey string) (map[string][]ItemRef, error)
}

// Repairer applies fixes to the canonical store.
type Repairer interface {
	// RecreateIdentity files a semcore identity for an existing (mailbox, raw)
	// message so it becomes EWS-visible again.
	RecreateIdentity(email, mailbox string, raw []byte) error
	// DeleteIndexEntry removes a dangling IMAP index entry.
	DeleteIndexEntry(email, mailbox string, uid uint32) error
	// DeleteIdentity removes a dangling semcore identity.
	DeleteIdentity(itemID string) error
}

// RepairReport summarizes the fixes applied.
type RepairReport struct {
	Recreated       int // semcore identities recreated (ghost fixes)
	DeletedIndex    int // orphan IMAP index entries removed
	DeletedIdentity int // orphan semcore identities removed
	Actions         []string
}

// Clean reports whether nothing needed fixing.
func (r *RepairReport) Clean() bool {
	return r.Recreated+r.DeletedIndex+r.DeletedIdentity == 0
}

// Repair fixes the divergences Check detects: it recreates a missing semcore
// identity for a message present in the IMAP index + blob (the EWS-ghost), and
// deletes dangling IMAP index entries and semcore identities whose blob is gone.
// It does NOT touch orphan-semcore (a semcore identity with a live blob but no
// index entry) — that reverse case is left to a future phase. Run with the
// server stopped.
func Repair(email string, idx IndexStore, blob RepairBlob, ident RepairIdentity, w Repairer) (*RepairReport, error) {
	rep := &RepairReport{}

	// semcore side: per-folder live-key sets; delete identities whose blob is gone.
	semByFolder := map[string]map[string]bool{}
	mboxKey, ok, err := ident.MailboxKey(email)
	if err != nil {
		return nil, fmt.Errorf("mailcheck: resolve mailbox identity: %w", err)
	}
	if ok {
		folderItems, ferr := ident.FolderItems(mboxKey)
		if ferr != nil {
			return nil, fmt.Errorf("mailcheck: folder items: %w", ferr)
		}
		for folder, items := range folderItems {
			set := map[string]bool{}
			for _, it := range items {
				if it.MsgKey == "" {
					continue
				}
				if !blob.MessageExists(email, it.MsgKey) {
					if derr := w.DeleteIdentity(it.ItemID); derr != nil {
						return nil, fmt.Errorf("mailcheck: delete identity %s: %w", it.ItemID, derr)
					}
					rep.DeletedIdentity++
					rep.Actions = append(rep.Actions, fmt.Sprintf("deleted orphan semcore identity %s in %s (blob gone)", it.ItemID, folder))
					continue
				}
				set[it.MsgKey] = true
			}
			semByFolder[folder] = set
		}
	}

	// IMAP side: delete orphan index entries; recreate EWS-ghosts.
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
			if !blob.MessageExists(email, id) {
				if derr := w.DeleteIndexEntry(email, mailbox, uid); derr != nil {
					return nil, fmt.Errorf("mailcheck: delete index %s/%d: %w", mailbox, uid, derr)
				}
				rep.DeletedIndex++
				rep.Actions = append(rep.Actions, fmt.Sprintf("deleted orphan IMAP index entry %s uid %d (blob gone)", mailbox, uid))
				continue
			}
			if semByFolder[mailbox][id] {
				continue // already has a semcore identity in this folder
			}
			raw, rerr := blob.ReadMessage(email, id)
			if rerr != nil {
				return nil, fmt.Errorf("mailcheck: read blob %s: %w", id, rerr)
			}
			if rcerr := w.RecreateIdentity(email, mailbox, raw); rcerr != nil {
				return nil, fmt.Errorf("mailcheck: recreate identity %s in %s: %w", id, mailbox, rcerr)
			}
			rep.Recreated++
			rep.Actions = append(rep.Actions, fmt.Sprintf("recreated semcore identity for %s in %s (was EWS-ghost)", id, mailbox))
			if semByFolder[mailbox] == nil {
				semByFolder[mailbox] = map[string]bool{}
			}
			semByFolder[mailbox][id] = true
		}
	}
	return rep, nil
}
