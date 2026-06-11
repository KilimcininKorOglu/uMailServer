// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides Bolt-backed canonical persistence for stable identities
// and folder-lineage metadata. The identity store is the authoritative source
// for MailboxId, FolderId, ItemId, ChangeKey, AttachmentId, and ConversationId.
// All other stores (IMAP, JMAP, DAV, EWS projections) must read identity from
// here instead of deriving it from mailbox names, folder paths, or filenames.
//
// The store is safe for concurrent use; callers must hold the appropriate
// lock for the scope they are operating in. Separate identity families
// (mailbox vs folder vs item) are stored in independent Bolt buckets so that
// one family can be iterated without loading the others into memory.
package semcore

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// Identity store errors
// ---------------------------------------------------------------------------

var (
	ErrMailboxNotFound      = errors.New("mailbox identity not found")
	ErrFolderNotFound       = errors.New("folder identity not found")
	ErrItemNotFound         = errors.New("item identity not found")
	ErrIdentityExists       = errors.New("identity already assigned")
	ErrChangeKeyNotStorable = errors.New("ChangeKey cannot be replaced once set")
)

// ---------------------------------------------------------------------------
// Stored record types
// ---------------------------------------------------------------------------

// storedMailboxIdentity is what we persist for a canonical MailboxId.
type storedMailboxIdentity struct {
	MailboxID     MailboxId // stable canonical ID
	Email         string    // primary account email (used as human-readable key)
	UIDValidity   uint32    // mirrors current IMAP UIDVALIDITY
	HighestModSeq uint64    // mirrors current modseq for sync baseline
}

// StoredFolderIdentity is what we persist for a canonical FolderId.
type StoredFolderIdentity struct {
	FolderID      FolderId // stable canonical ID
	MailboxID     MailboxId
	ParentID      FolderId // zero for top-level folders
	Role          string   // e.g. "inbox", "drafts", "sent" — empty for user folders
	SortOrder     int
	HighestModSeq uint64
	IsSubscribed  bool
	// SearchDefinition, when non-nil, marks this folder as a MAPI/Outlook
	// search folder (a persistent saved-query virtual folder). It has no items
	// of its own; its contents are computed dynamically by matching the
	// definition against the messages in BaseFolders. Plain mail/collab folders
	// leave this nil. Stored inline so a search folder is one canonical record.
	SearchDefinition *SearchFolderDef `json:",omitempty"`
}

// SearchFolderDef is the canonical, surface-agnostic definition of a search
// folder: the saved criteria plus the folder set they are evaluated over. It is
// the single source consumed by both the EWS FindItem restriction path and the
// webmail saved-search scan; each surface extracts message fields in its own
// way but feeds them to the same Matches decision so behavior cannot drift.
//
// Every criterion is optional; an empty field imposes no constraint. Text
// criteria are case-insensitive substring (contains) matches, mirroring how
// Outlook search folders and the webmail full-text scan already behave.
type SearchFolderDef struct {
	From          string   `json:"from,omitempty"`
	Subject       string   `json:"subject,omitempty"`
	Body          string   `json:"body,omitempty"`
	DateFrom      string   `json:"date_from,omitempty"` // RFC3339 or YYYY-MM-DD; lower bound (inclusive)
	DateTo        string   `json:"date_to,omitempty"`   // RFC3339 or YYYY-MM-DD; upper bound (inclusive)
	HasAttachment *bool    `json:"has_attachment,omitempty"`
	BaseFolders   []string `json:"base_folders,omitempty"` // folder display names; empty = all mail folders
	Traversal     string   `json:"traversal,omitempty"`    // "Shallow" | "Deep"
}

// Matches reports whether a message with the given fields satisfies every set
// criterion in the definition. Callers normalize their message representation
// into these arguments: from and subject are the header values, body is the
// plain-text body (pass "" when a Body criterion need not be evaluated), date
// is the message date (zero time means unknown), and hasAttachment reflects the
// message's attachment state.
//
// An empty criterion is skipped. When a date bound is set but the message date
// is unknown (zero), the message is excluded, since membership in the bounded
// range cannot be confirmed. Unparseable date bounds are likewise skipped.
func (d *SearchFolderDef) Matches(from, subject, body string, date time.Time, hasAttachment bool) bool {
	if d == nil {
		return false
	}
	if d.From != "" && !strings.Contains(strings.ToLower(from), strings.ToLower(d.From)) {
		return false
	}
	if d.Subject != "" && !strings.Contains(strings.ToLower(subject), strings.ToLower(d.Subject)) {
		return false
	}
	if d.Body != "" && !strings.Contains(strings.ToLower(body), strings.ToLower(d.Body)) {
		return false
	}
	if from := d.DateFrom; from != "" {
		if lower, ok := parseSearchDate(from); ok {
			if date.IsZero() || date.Before(lower) {
				return false
			}
		}
	}
	if to := d.DateTo; to != "" {
		if upper, ok := parseSearchDate(to); ok {
			// A date-only upper bound is inclusive of the whole day.
			if len(to) == len("2006-01-02") {
				upper = upper.Add(24*time.Hour - time.Nanosecond)
			}
			if date.IsZero() || date.After(upper) {
				return false
			}
		}
	}
	if d.HasAttachment != nil && *d.HasAttachment != hasAttachment {
		return false
	}
	return true
}

// parseSearchDate parses a search-folder date bound, accepting RFC3339 and the
// date-only YYYY-MM-DD form. It reports false when the value is unparseable so
// callers can treat the bound as absent rather than excluding everything.
func parseSearchDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// StoredItemIdentity is what we persist for a canonical ItemId + ChangeKey.
type StoredItemIdentity struct {
	ItemID         ItemId
	MailboxID      MailboxId
	FolderID       FolderId
	ChangeKey      ChangeKey
	ConversationID ConversationId
	MsgKey         string // blob key used to look up raw message in msgStore
	Email          string // raw email (user key) for msgStore lookups
	IsRead         bool
	Categories     []string
}

// storedConversationIdentity is what we persist for a ConversationId.
type storedConversationIdentity struct {
	ConversationID ConversationId
	MailboxID      MailboxId
}

// storedAttachmentIdentity is what we persist for an AttachmentId.
type storedAttachmentIdentity struct {
	AttachmentID AttachmentId
	ParentID     ItemId
}

// ---------------------------------------------------------------------------
// IdentityStore interface
// ---------------------------------------------------------------------------

// IdentityStore is the interface for canonical identity persistence.
// All IDs are assigned exactly once; once written they cannot be moved or
// replaced without a full identity tombstone. This is intentional: Exchange
// clients depend on ID stability as the primary contract.
//
// The store is append-only for newly assigned IDs; updates are limited to
// non-identity-moving fields such as ChangeKey (version only), SortOrder,
// IsSubscribed, and HighestModSeq.
type IdentityStore interface {
	// --- MailboxId operations ---

	// PutMailboxIdentity stores a canonical MailboxId for a mailbox.
	// If an identity already exists for this Key, it returns ErrIdentityExists.
	// The UIDValidity is set from the current IMAP state.
	PutMailboxIdentity(key string, id MailboxId, uidValidity uint32) error

	// GetMailboxIDByKey retrieves the MailboxId for a mailbox key (email).
	// Returns ErrMailboxNotFound if not yet registered.
	GetMailboxIDByKey(key string) (MailboxId, error)

	// GetMailboxIDByEmail retrieves MailboxId by account email.
	GetMailboxIDByEmail(email string) (MailboxId, error)

	// SetMailboxModSeq updates HighestModSeq for a registered mailbox.
	SetMailboxModSeq(key string, modseq uint64) error

	// ListMailboxIdentities returns all registered mailbox identities.
	ListMailboxIdentities() ([]storedMailboxIdentity, error)

	// --- FolderId operations ---

	// PutFolderIdentity stores a canonical FolderId for a folder.
	// Role is the distinguished-role string ("inbox", "drafts", etc.) or
	// empty for user-created folders.
	PutFolderIdentity(mboxKey, folderName string, id FolderId, role string) error

	// GetFolderID retrieves the FolderId for a mailbox + folder name.
	// Returns ErrFolderNotFound if not registered.
	GetFolderID(mboxKey, folderName string) (FolderId, error)

	// GetFolderByID retrieves the full folder identity record by FolderId.
	GetFolderByID(id FolderId) (*StoredFolderIdentity, error)

	// SetFolderParent updates the ParentID of an existing folder.
	// This is the canonical rename/reparent operation; it does not rename
	// the folder itself (the Folder struct Name field handles that).
	SetFolderParent(id FolderId, parentID FolderId) error

	// SetFolderDistinguishedRole sets or clears the distinguished role.
	SetFolderDistinguishedRole(id FolderId, role string) error

	// SetFolderSortOrder updates the client sort order hint for a folder.
	SetFolderSortOrder(id FolderId, sortOrder int) error

	// SetFolderModSeq updates the HighestModSeq for a folder.
	SetFolderModSeq(id FolderId, modseq uint64) error

	// SetFolderSubscribed updates the subscription flag for a folder.
	SetFolderSubscribed(id FolderId, subscribed bool) error

	// DeleteFolder removes a folder identity by FolderId.
	DeleteFolder(id FolderId) error

	// ListFolderIdentities returns all registered folder identities.
	ListFolderIdentities() ([]StoredFolderIdentity, error)

	// ListFolderIdentitiesForMailbox returns all folder identities for one mailbox.
	ListFolderIdentitiesForMailbox(mboxKey string) ([]StoredFolderIdentity, error)

	// GetFolderByMailbox retrieves a folder identity by mailbox key and role.
	GetFolderByMailbox(mboxKey, role string) (*StoredFolderIdentity, error)

	// SetFolderSearchDefinition sets (or clears, when def is nil) the search
	// definition on a folder, marking it as a search folder. Returns
	// ErrFolderNotFound when no folder identity exists for the id.
	SetFolderSearchDefinition(id FolderId, def *SearchFolderDef) error

	// ListSearchFolders returns the search folders for one mailbox: folder
	// identities whose SearchDefinition is non-nil.
	ListSearchFolders(mboxKey string) ([]StoredFolderIdentity, error)

	// --- ItemId operations ---

	// PutItemIdentity stores a canonical ItemId + ChangeKey for a message.
	// If an item already exists for this message-key, it returns ErrIdentityExists.
	PutItemIdentity(msgKey string, email string, id ItemId, mailboxID MailboxId, folderID FolderId, ck ChangeKey, convID ConversationId, isRead bool) error

	// GetItemIDByKey retrieves ItemId for a message key.
	// Returns ErrItemNotFound if not registered.
	GetItemIDByKey(msgKey string) (ItemId, error)

	// GetItemIdentity retrieves the full item identity record by ItemId.
	GetItemIdentity(id ItemId) (*StoredItemIdentity, error)

	// SetItemFolder updates the FolderId for an existing item.
	SetItemFolder(id ItemId, folderID FolderId) error

	// PutChangeKey sets the ChangeKey for an existing ItemId.
	// Returns ErrItemNotFound if the item is not registered.
	// Returns ErrChangeKeyNotStorable if a non-empty ChangeKey already exists.
	// Callers must provide the current ChangeKey to guard against stale writes.
	PutChangeKey(id ItemId, currentCK ChangeKey, newCK ChangeKey) error

	// SetItemConversation updates the ConversationId for an existing item.
	SetItemConversation(id ItemId, convID ConversationId) error

	// SetItemMsgKey updates the raw-message blob key for an existing item.
	// Used when an item's MIME content is rewritten in place (e.g. after
	// DeleteAttachment), keeping the same ItemId while repointing the blob.
	SetItemMsgKey(id ItemId, msgKey string) error

	// ListItemIdentitiesByMailbox returns all item IDs for one mailbox.
	ListItemIdentitiesByMailbox(mboxID MailboxId) ([]StoredItemIdentity, error)

	// ListItemIdentitiesByFolder returns all item identities for one folder.
	// This is used by EWS FindItem and SyncFolderItems to enumerate folder contents.
	ListItemIdentitiesByFolder(folderID FolderId) ([]StoredItemIdentity, error)

	// DeleteItemIdentity removes an item identity from the store.
	// This is called after a soft-delete (MoveToDeletedItems) so the item is
	// no longer accessible via normal item operations.
	DeleteItemIdentity(id ItemId) error

	// --- AttachmentId operations ---

	// PutAttachmentIdentity stores a canonical AttachmentId relative to a parent ItemId.
	PutAttachmentIdentity(parentID ItemId, name string, id AttachmentId) error

	// GetAttachmentID retrieves the AttachmentId for a parent + name.
	GetAttachmentID(parentID ItemId, name string) (AttachmentId, error)

	// GetAttachmentIdentity retrieves the full attachment identity record.
	GetAttachmentIdentity(id AttachmentId) (*storedAttachmentIdentity, error)

	// ListAttachmentsByParent returns all attachment IDs for one item.
	ListAttachmentsByParent(parentID ItemId) ([]storedAttachmentIdentity, error)

	// --- ConversationId operations ---

	// PutConversationIdentity stores a canonical ConversationId.
	PutConversationIdentity(id ConversationId, mailboxID MailboxId) error

	// GetConversationIdentity retrieves the full conversation identity record.
	GetConversationIdentity(id ConversationId) (*storedConversationIdentity, error)
}

// ---------------------------------------------------------------------------
// BoltIdentityStore
// ---------------------------------------------------------------------------

// bucket names in the identity DB
const (
	bucketMailbox      = "__semcore_mbox"
	bucketFolder       = "__semcore_folder"
	bucketItem         = "__semcore_item"
	bucketAttachment   = "__semcore_attachment"
	bucketConversation = "__semcore_conv"
)

// BoltIdentityStore persists canonical identity state in a dedicated bbolt DB.
// It is safe for concurrent use; callers must use the appropriate locks.
type BoltIdentityStore struct {
	db  *bbolt.DB
	mu  sync.RWMutex
	dir string // data directory for the DB file
}

// NewBoltIdentityStore opens a Bolt-backed identity store, creating the DB
// file and buckets if they do not exist yet.
func NewBoltIdentityStore(dataDir string) (*BoltIdentityStore, error) {
	dir := filepath.Join(dataDir, "semcore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("BoltIdentityStore: create dir: %w", err)
	}

	dbPath := filepath.Join(dir, "identity.db")
	db, err := bbolt.Open(dbPath, 0o600, &bbolt.Options{Timeout: 1})
	if err != nil {
		return nil, fmt.Errorf("BoltIdentityStore: open: %w", err)
	}

	// Create all buckets.
	fn := func(tx *bbolt.Tx) error {
		for _, b := range []string{bucketMailbox, bucketFolder, bucketItem, bucketAttachment, bucketConversation} {
			if _, err := tx.CreateBucketIfNotExists([]byte(b)); err != nil {
				return fmt.Errorf("create bucket %s: %w", b, err)
			}
		}
		return nil
	}
	if err := db.Update(fn); err != nil {
		_ = db.Close() //nolint:errcheck // best-effort cleanup; error already wrapped above
		return nil, fmt.Errorf("BoltIdentityStore: init buckets: %w", err)
	}

	return &BoltIdentityStore{db: db, dir: dir}, nil
}

// Close closes the underlying bbolt database.
func (s *BoltIdentityStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Bolt returns the underlying bbolt.DB so callers can perform transactions.
// The caller must hold the mutex for the duration of the transaction.
func (s *BoltIdentityStore) Bolt() *bbolt.DB {
	return s.db
}

// ---------------------------------------------------------------------------
// MailboxId operations
// ---------------------------------------------------------------------------

// mailboxKeyEmail returns the canonical storage key for a mailbox identity.
// We use the account email as the stable key because it does not change
// when folder names change.
func mailboxKeyEmail(email string) string {
	return "e:" + email
}

// PutMailboxIdentity implements IdentityStore.
func (s *BoltIdentityStore) PutMailboxIdentity(key string, id MailboxId, uidValidity uint32) error {
	if id.IsZero() {
		return errors.New("PutMailboxIdentity: zero MailboxId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketMailbox))
		// Check for existing key.
		if b.Get([]byte(key)) != nil {
			return ErrIdentityExists
		}
		rec := storedMailboxIdentity{
			MailboxID:     id,
			Email:         key,
			UIDValidity:   uidValidity,
			HighestModSeq: 0,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal mailbox identity: %w", err)
		}
		return b.Put([]byte(key), data)
	})
}

// GetMailboxIDByKey implements IdentityStore.
func (s *BoltIdentityStore) GetMailboxIDByKey(key string) (MailboxId, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var id MailboxId
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketMailbox))
		data := b.Get([]byte(key))
		if data == nil {
			return ErrMailboxNotFound
		}
		var rec storedMailboxIdentity
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("unmarshal mailbox identity: %w", err)
		}
		id = rec.MailboxID
		return nil
	})
	return id, err
}

// GetMailboxIDByEmail implements IdentityStore.
func (s *BoltIdentityStore) GetMailboxIDByEmail(email string) (MailboxId, error) {
	return s.GetMailboxIDByKey(mailboxKeyEmail(email))
}

// EnsureMailboxId returns the MailboxId for an email, creating and registering
// a new canonical identity if one does not yet exist. This is used by the
// canonical mutation pipeline to obtain a MailboxId for a mailbox that may not
// have been backfilled yet.
//
// If an identity already exists, the existing ID is returned (idempotent).
// A new identity is generated using a cryptographically random ID.
func (s *BoltIdentityStore) EnsureMailboxId(email string) (MailboxId, error) {
	// Fast path: existing identity.
	if id, err := s.GetMailboxIDByEmail(email); err == nil {
		return id, nil
	}

	// Slow path: create new identity.
	id, err := NewMailboxId(generateID())
	if err != nil {
		return MailboxId{}, fmt.Errorf("EnsureMailboxId: generate ID: %w", err)
	}

	// Use UIDValidity 1 as the initial value; the IMAP layer manages the real UIDValidity.
	if err := s.PutMailboxIdentity(mailboxKeyEmail(email), id, 1); err != nil {
		// Race: another goroutine may have created it. Check again.
		if err == ErrIdentityExists {
			return s.GetMailboxIDByEmail(email)
		}
		return MailboxId{}, fmt.Errorf("EnsureMailboxId: put identity: %w", err)
	}
	return id, nil
}

// SetMailboxModSeq implements IdentityStore.
func (s *BoltIdentityStore) SetMailboxModSeq(key string, modseq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketMailbox))
		data := b.Get([]byte(key))
		if data == nil {
			return ErrMailboxNotFound
		}
		var rec storedMailboxIdentity
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("unmarshal mailbox identity: %w", err)
		}
		rec.HighestModSeq = modseq
		out, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal updated mailbox: %w", err)
		}
		return b.Put([]byte(key), out)
	})
}

// ListMailboxIdentities implements IdentityStore.
func (s *BoltIdentityStore) ListMailboxIdentities() ([]storedMailboxIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []storedMailboxIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketMailbox))
		return b.ForEach(func(_, v []byte) error {
			var rec storedMailboxIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil // skip corrupted entries
			}
			result = append(result, rec)
			return nil
		})
	})
	return result, err
}

// MailboxEmailsByID returns a map of MailboxId string -> account email for all
// registered mailbox identities. Used by admin surfaces that persist a
// MailboxId (e.g. delegation grants) but must display the human-readable email.
func (s *BoltIdentityStore) MailboxEmailsByID() (map[string]string, error) {
	ids, err := s.ListMailboxIdentities()
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(ids))
	for _, rec := range ids {
		m[rec.MailboxID.String()] = strings.TrimPrefix(rec.Email, "e:")
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// FolderId operations
// ---------------------------------------------------------------------------

// folderKey returns the storage key for a folder identity (mailboxKey + folderName).
func folderKey(mboxKey, folderName string) string {
	return mboxKey + "\x00" + folderName
}

// CanonicalFolderNameForRole returns the canonical IMAP folder name for a
// distinguished role (e.g. "inbox" -> "INBOX", "trash" -> "Trash"), or "" for a
// user-created folder with no role. Exported so cross-package callers (EWS) can
// map a folder's role to the mailbox name used in client change notifications.
func CanonicalFolderNameForRole(role string) string {
	return canonicalFolderNameForRole(role)
}

// RoleForCanonicalFolderName returns the distinguished role for a canonical IMAP
// folder name (e.g. "INBOX" -> "inbox", "Trash" -> "trash"), or "" for a folder
// name that maps to no distinguished role. It is the inverse of
// CanonicalFolderNameForRole; the two share the same role/name pairs so a
// standard storage folder reconciled into the identity store carries the role
// EWS distinguished-folder resolution expects.
func RoleForCanonicalFolderName(name string) string {
	switch name {
	case "INBOX":
		return "inbox"
	case "Drafts":
		return "drafts"
	case "Sent":
		return "sent"
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
	case "Recoverable Items":
		return "recoverableitems"
	default:
		return ""
	}
}

func canonicalFolderNameForRole(role string) string {
	switch role {
	case "inbox":
		return "INBOX"
	case "drafts":
		return "Drafts"
	case "sent":
		return "Sent"
	case "trash":
		return "Trash"
	case "junk":
		return "Junk"
	case "archive":
		return "Archive"
	case "notes":
		return "Notes"
	case "scheduled":
		return "Scheduled"
	case "recoverableitems":
		return "Recoverable Items"
	default:
		return ""
	}
}

func folderNameFromStorageKey(mboxKey string, key []byte) string {
	return strings.TrimPrefix(string(key), mboxKey+"\x00")
}

// ChildStorageName returns the parent-scoped storage name for a child folder
// whose plain display name collides with an existing sibling under a different
// parent. The form is "\x1f"+parentID+"\x1f"+displayName: the Unit Separator
// (\x1f) never appears in an IMAP path or an EWS display name, and differs from
// the "\x00" folder-key separator, so the stored name round-trips through the
// existing key and migration machinery unchanged. Exported so the relational
// backend builds byte-identical storage names for backend parity.
func ChildStorageName(parentID FolderId, displayName string) string {
	return "\x1f" + parentID.String() + "\x1f" + displayName
}

// DisplayNameFromStorageName recovers the client-visible folder name from a
// stored folder name. A parent-scoped name ("\x1f"+parentID+"\x1f"+display)
// yields its trailing display segment; a plain name is returned unchanged.
// Exported so EWS presentation and the IMAP-mirror bridge can strip the prefix
// that FolderNameByID otherwise returns verbatim.
func DisplayNameFromStorageName(stored string) string {
	if !strings.HasPrefix(stored, "\x1f") {
		return stored
	}
	if i := strings.LastIndexByte(stored, '\x1f'); i >= 0 {
		return stored[i+1:]
	}
	return stored
}

func itemStorageKey(msgKey, email string) string {
	if email == "" {
		return msgKey
	}
	return email + "\x00" + msgKey
}

func (s *BoltIdentityStore) getFolderByRoleLocked(mboxKey, role string) (*StoredFolderIdentity, error) {
	prefix := mboxKey + "\x00"
	var result *StoredFolderIdentity
	var resultName string
	canonicalName := canonicalFolderNameForRole(role)
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))
		return b.ForEach(func(k, v []byte) error {
			if !bytes.HasPrefix(k, []byte(prefix)) {
				return nil
			}
			var rec StoredFolderIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if rec.Role != role {
				return nil
			}

			name := folderNameFromStorageKey(mboxKey, k)
			if result == nil {
				recCopy := rec
				result = &recCopy
				resultName = name
				return nil
			}
			if canonicalName != "" && strings.EqualFold(name, canonicalName) && !strings.EqualFold(resultName, canonicalName) {
				recCopy := rec
				result = &recCopy
				resultName = name
			}
			return nil
		})
	})
	if result == nil {
		return nil, ErrFolderNotFound
	}
	return result, err
}

// PutFolderIdentity implements IdentityStore.
func (s *BoltIdentityStore) PutFolderIdentity(mboxKey, folderName string, id FolderId, role string) error {
	if id.IsZero() {
		return errors.New("PutFolderIdentity: zero FolderId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))
		k := folderKey(mboxKey, folderName)
		if b.Get([]byte(k)) != nil {
			return ErrIdentityExists
		}
		rec := StoredFolderIdentity{
			FolderID:      id,
			MailboxID:     MailboxId{raw: mboxKey},
			Role:          role,
			HighestModSeq: 0,
			IsSubscribed:  true,
		}
		if role != "" {
			rec.IsSubscribed = true
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal folder identity: %w", err)
		}
		return b.Put([]byte(k), data)
	})
}

// GetFolderID implements IdentityStore.
func (s *BoltIdentityStore) GetFolderID(mboxKey, folderName string) (FolderId, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var id FolderId
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))
		data := b.Get([]byte(folderKey(mboxKey, folderName)))
		if data == nil {
			return ErrFolderNotFound
		}
		var rec StoredFolderIdentity
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("unmarshal folder identity: %w", err)
		}
		id = rec.FolderID
		return nil
	})
	return id, err
}

// GetFolderByID implements IdentityStore.
func (s *BoltIdentityStore) GetFolderByID(id FolderId) (*StoredFolderIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rec *StoredFolderIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketFolder)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var f StoredFolderIdentity
			if err := json.Unmarshal(v, &f); err != nil {
				continue
			}
			if f.FolderID.Equal(id) {
				fcopy := f
				rec = &fcopy
				return nil
			}
		}
		return ErrFolderNotFound
	})
	return rec, err
}

// FolderNameByID returns the stored folder name for a folder id within the
// given mailbox. The folder identity is keyed by "mboxKey\x00folderName", so
// the name is recovered from the matching record's storage key. Exported so
// cross-package callers (EWS) can map a canonical FolderId back to the IMAP
// mailbox name used by the message-store index.
func (s *BoltIdentityStore) FolderNameByID(mboxKey string, id FolderId) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := mboxKey + "\x00"
	var name string
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))
		return b.ForEach(func(k, v []byte) error {
			if !bytes.HasPrefix(k, []byte(prefix)) {
				return nil
			}
			var rec StoredFolderIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if rec.FolderID.Equal(id) {
				name = folderNameFromStorageKey(mboxKey, k)
			}
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", ErrFolderNotFound
	}
	return name, nil
}

// EnsureFolderId returns the FolderId for a mailbox+folder combination,
// creating and registering a new canonical identity if one does not yet exist.
// This is used by the canonical mutation pipeline to obtain a FolderId for a
// folder that may not have been backfilled yet.
//
// Role is the distinguished-role string ("inbox", "drafts", "sent", etc.)
// or empty for user-created folders.
//
// If an identity already exists, the existing ID is returned (idempotent).
// A new identity is generated using a cryptographically random ID.
func (s *BoltIdentityStore) EnsureFolderId(mboxKey, folderName, role string) (FolderId, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Fast path: existing identity.
	if id, err := s.GetFolderID_Locked(mboxKey, folderName); err == nil {
		return id, nil
	}
	if role != "" {
		if existing, err := s.getFolderByRoleLocked(mboxKey, role); err == nil {
			return existing.FolderID, nil
		}
	}

	// Slow path: create new identity.
	id, err := NewFolderId(generateID())
	if err != nil {
		return FolderId{}, fmt.Errorf("EnsureFolderId: generate ID: %w", err)
	}

	if err := s.PutFolderIdentity_Locked(mboxKey, folderName, id, role); err != nil {
		// Race: another goroutine may have created it. Check again.
		if err == ErrIdentityExists {
			return s.GetFolderID_Locked(mboxKey, folderName)
		}
		return FolderId{}, fmt.Errorf("EnsureFolderId: put identity: %w", err)
	}

	return id, nil
}

// GetFolderID_Locked is like GetFolderID but requires caller to hold s.mu.
func (s *BoltIdentityStore) GetFolderID_Locked(mboxKey, folderName string) (FolderId, error) {
	var id FolderId
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))
		data := b.Get([]byte(folderKey(mboxKey, folderName)))
		if data == nil {
			return ErrFolderNotFound
		}
		var rec StoredFolderIdentity
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("unmarshal folder: %w", err)
		}
		id = rec.FolderID
		return nil
	})
	return id, err
}

// PutFolderIdentity_Locked is like PutFolderIdentity but requires caller to hold s.mu.
func (s *BoltIdentityStore) PutFolderIdentity_Locked(mboxKey, folderName string, id FolderId, role string) error {
	if id.IsZero() {
		return errors.New("PutFolderIdentity: zero FolderId")
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))
		k := folderKey(mboxKey, folderName)
		if b.Get([]byte(k)) != nil {
			return ErrIdentityExists
		}
		rec := StoredFolderIdentity{
			FolderID:      id,
			MailboxID:     MailboxId{raw: mboxKey},
			Role:          role,
			HighestModSeq: 0,
			IsSubscribed:  true,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal folder identity: %w", err)
		}
		return b.Put([]byte(k), data)
	})
}

// writeFolderRecord writes a new folder identity record within an existing
// bbolt transaction, setting ParentID at creation time. Unlike
// PutFolderIdentity_Locked it does not open its own transaction, so it can be
// composed inside a single atomic Update alongside collision checks.
func writeFolderRecord(b *bbolt.Bucket, key string, id FolderId, mboxKey string, parentID FolderId, role string) error {
	rec := StoredFolderIdentity{
		FolderID:     id,
		MailboxID:    MailboxId{raw: mboxKey},
		ParentID:     parentID,
		Role:         role,
		IsSubscribed: true,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal folder identity: %w", err)
	}
	return b.Put([]byte(key), data)
}

// EnsureChildFolderId returns a FolderId for a child folder identified by its
// parent and display name, creating a new canonical identity when none exists.
//
// It differs from EnsureFolderId in two ways. First, it records ParentID at
// creation time so the folder's lineage is known immediately. Second, it never
// collapses a real copy into an existing sibling: when displayName already
// belongs to a *different* parent, the new folder is stored under a
// parent-scoped storage name (childStorageName) so both folders keep distinct
// identities and distinct storage keys while rendering the same display name.
//
// The operation is idempotent: a repeat call with the same (parent, name)
// returns the existing FolderId rather than minting a second one.
func (s *BoltIdentityStore) EnsureChildFolderId(mboxKey string, parentID FolderId, displayName, role string) (FolderId, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result FolderId
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))

		// Plain-name slot first.
		plainKey := folderKey(mboxKey, displayName)
		if data := b.Get([]byte(plainKey)); data != nil {
			var rec StoredFolderIdentity
			if err := json.Unmarshal(data, &rec); err != nil {
				return fmt.Errorf("unmarshal folder identity: %w", err)
			}
			if rec.ParentID.Equal(parentID) {
				// Same parent: idempotent reuse.
				result = rec.FolderID
				return nil
			}
			// Different parent: fall through to the parent-scoped name.
		} else {
			id, err := NewFolderId(generateID())
			if err != nil {
				return fmt.Errorf("generate folder ID: %w", err)
			}
			if err := writeFolderRecord(b, plainKey, id, mboxKey, parentID, role); err != nil {
				return err
			}
			result = id
			return nil
		}

		// Parent-scoped name (the plain name belongs to a different parent).
		scopedKey := folderKey(mboxKey, ChildStorageName(parentID, displayName))
		if data := b.Get([]byte(scopedKey)); data != nil {
			var rec StoredFolderIdentity
			if err := json.Unmarshal(data, &rec); err != nil {
				return fmt.Errorf("unmarshal folder identity: %w", err)
			}
			result = rec.FolderID
			return nil
		}
		id, err := NewFolderId(generateID())
		if err != nil {
			return fmt.Errorf("generate folder ID: %w", err)
		}
		if err := writeFolderRecord(b, scopedKey, id, mboxKey, parentID, role); err != nil {
			return err
		}
		result = id
		return nil
	})
	if err != nil {
		return FolderId{}, fmt.Errorf("EnsureChildFolderId: %w", err)
	}
	return result, nil
}

// SetFolderParent implements IdentityStore.
func (s *BoltIdentityStore) SetFolderParent(id FolderId, parentID FolderId) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateFolder(id, func(f *StoredFolderIdentity) {
		f.ParentID = parentID
	})
}

// SetFolderDistinguishedRole implements IdentityStore.
func (s *BoltIdentityStore) SetFolderDistinguishedRole(id FolderId, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateFolder(id, func(f *StoredFolderIdentity) {
		f.Role = role
	})
}

// SetFolderSortOrder implements IdentityStore.
func (s *BoltIdentityStore) SetFolderSortOrder(id FolderId, sortOrder int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateFolder(id, func(f *StoredFolderIdentity) {
		f.SortOrder = sortOrder
	})
}

// SetFolderModSeq implements IdentityStore.
func (s *BoltIdentityStore) SetFolderModSeq(id FolderId, modseq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateFolder(id, func(f *StoredFolderIdentity) {
		f.HighestModSeq = modseq
	})
}

// SetFolderSubscribed implements IdentityStore.
func (s *BoltIdentityStore) SetFolderSubscribed(id FolderId, subscribed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateFolder(id, func(f *StoredFolderIdentity) {
		f.IsSubscribed = subscribed
	})
}

// DeleteFolder implements IdentityStore.
func (s *BoltIdentityStore) DeleteFolder(id FolderId) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var f StoredFolderIdentity
			if err := json.Unmarshal(v, &f); err != nil {
				continue
			}
			if f.FolderID.Equal(id) {
				return b.Delete(k)
			}
		}
		return ErrFolderNotFound
	})
}

// updateFolder is a helper that finds a folder by ID and applies an update.
func (s *BoltIdentityStore) updateFolder(id FolderId, fn func(*StoredFolderIdentity)) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketFolder)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var f StoredFolderIdentity
			if err := json.Unmarshal(v, &f); err != nil {
				continue
			}
			if f.FolderID.Equal(id) {
				fn(&f)
				out, err := json.Marshal(f)
				if err != nil {
					return fmt.Errorf("marshal updated folder: %w", err)
				}
				return tx.Bucket([]byte(bucketFolder)).Put(k, out)
			}
		}
		return ErrFolderNotFound
	})
}

// ListFolderIdentities implements IdentityStore.
func (s *BoltIdentityStore) ListFolderIdentities() ([]StoredFolderIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []StoredFolderIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))
		return b.ForEach(func(k, v []byte) error {
			var rec StoredFolderIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil // skip corrupted entries
			}
			result = append(result, rec)
			return nil
		})
	})
	return result, err
}

// ListFolderIdentitiesForMailbox implements IdentityStore.
func (s *BoltIdentityStore) ListFolderIdentitiesForMailbox(mboxKey string) ([]StoredFolderIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []StoredFolderIdentity
	prefix := mboxKey + "\x00"
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketFolder))
		return b.ForEach(func(k, v []byte) error {
			if !bytes.HasPrefix(k, []byte(prefix)) {
				return nil
			}
			var rec StoredFolderIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil // skip corrupted entries
			}
			result = append(result, rec)
			return nil
		})
	})
	return result, err
}

// GetFolderByMailbox retrieves a folder identity by mailbox key and role.
func (s *BoltIdentityStore) GetFolderByMailbox(mboxKey, role string) (*StoredFolderIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getFolderByRoleLocked(mboxKey, role)
}

// SetFolderSearchDefinition implements IdentityStore.
func (s *BoltIdentityStore) SetFolderSearchDefinition(id FolderId, def *SearchFolderDef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateFolder(id, func(f *StoredFolderIdentity) {
		f.SearchDefinition = def
	})
}

// ListSearchFolders implements IdentityStore.
func (s *BoltIdentityStore) ListSearchFolders(mboxKey string) ([]StoredFolderIdentity, error) {
	all, err := s.ListFolderIdentitiesForMailbox(mboxKey)
	if err != nil {
		return nil, err
	}
	var result []StoredFolderIdentity
	for _, f := range all {
		if f.SearchDefinition != nil {
			result = append(result, f)
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// ItemId operations
// ---------------------------------------------------------------------------

// PutItemIdentityWithKey is like PutItemIdentity but uses the given storageKey
// as the Bolt key instead of deriving it from msgKey. This allows creating
// unique identity entries for the same content delivered to different folders
// while keeping the original msgKey for message-store lookups.
func (s *BoltIdentityStore) PutItemIdentityWithKey(storageKey, msgKey string, email string, id ItemId, mailboxID MailboxId, folderID FolderId, ck ChangeKey, convID ConversationId, isRead bool) error {
	if id.IsZero() {
		return errors.New("PutItemIdentityWithKey: zero ItemId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketItem))
		if b.Get([]byte(storageKey)) != nil {
			return ErrIdentityExists
		}
		rec := StoredItemIdentity{
			ItemID:         id,
			MailboxID:      mailboxID,
			FolderID:       folderID,
			ChangeKey:      ck,
			ConversationID: convID,
			MsgKey:         msgKey,
			Email:          email,
			IsRead:         isRead,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal item identity: %w", err)
		}
		return b.Put([]byte(storageKey), data)
	})
}

// PutItemIdentity implements IdentityStore.
func (s *BoltIdentityStore) PutItemIdentity(msgKey string, email string, id ItemId, mailboxID MailboxId, folderID FolderId, ck ChangeKey, convID ConversationId, isRead bool) error {
	if id.IsZero() {
		return errors.New("PutItemIdentity: zero ItemId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketItem))
		storageKey := itemStorageKey(msgKey, email)
		if b.Get([]byte(storageKey)) != nil {
			return ErrIdentityExists
		}
		rec := StoredItemIdentity{
			ItemID:         id,
			MailboxID:      mailboxID,
			FolderID:       folderID,
			ChangeKey:      ck,
			ConversationID: convID,
			MsgKey:         msgKey,
			Email:          email,
			IsRead:         isRead,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal item identity: %w", err)
		}
		return b.Put([]byte(storageKey), data)
	})
}

// GetItemIDByKey implements IdentityStore.
func (s *BoltIdentityStore) GetItemIDByKey(msgKey string) (ItemId, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var id ItemId
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketItem))
		data := b.Get([]byte(msgKey))
		if data == nil {
			c := b.Cursor()
			suffix := []byte("\x00" + msgKey)
			for k, v := c.First(); k != nil; k, v = c.Next() {
				if bytes.HasSuffix(k, suffix) {
					data = v
					break
				}
			}
			if data == nil {
				return ErrItemNotFound
			}
		}
		var rec StoredItemIdentity
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("unmarshal item identity: %w", err)
		}
		id = rec.ItemID
		return nil
	})
	return id, err
}

// GetItemIdentity implements IdentityStore.
func (s *BoltIdentityStore) GetItemIdentity(id ItemId) (*StoredItemIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rec *StoredItemIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketItem)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var it StoredItemIdentity
			if err := json.Unmarshal(v, &it); err != nil {
				continue
			}
			if it.ItemID.Equal(id) {
				itcopy := it
				rec = &itcopy
				return nil
			}
		}
		return ErrItemNotFound
	})
	return rec, err
}

// SetItemFolder implements IdentityStore.
func (s *BoltIdentityStore) SetItemFolder(id ItemId, folderID FolderId) error {
	if id.IsZero() {
		return errors.New("SetItemFolder: zero ItemId")
	}
	if folderID.IsZero() {
		return errors.New("SetItemFolder: zero FolderId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketItem)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var it StoredItemIdentity
			if err := json.Unmarshal(v, &it); err != nil {
				continue
			}
			if it.ItemID.Equal(id) {
				it.FolderID = folderID
				out, err := json.Marshal(it)
				if err != nil {
					return fmt.Errorf("marshal updated item: %w", err)
				}
				return tx.Bucket([]byte(bucketItem)).Put(k, out)
			}
		}
		return ErrItemNotFound
	})
}

// PutChangeKey implements IdentityStore.
func (s *BoltIdentityStore) PutChangeKey(id ItemId, currentCK ChangeKey, newCK ChangeKey) error {
	if id.IsZero() {
		return errors.New("PutChangeKey: zero ItemId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketItem)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var it StoredItemIdentity
			if err := json.Unmarshal(v, &it); err != nil {
				continue
			}
			if it.ItemID.Equal(id) {
				// Reject stale writes: only advance if currentCK matches.
				if currentCK.IsZero() && it.ChangeKey.IsZero() {
					// First write; OK.
				} else if !it.ChangeKey.Equal(currentCK) {
					return fmt.Errorf("stale ChangeKey: expected %v, found %v", currentCK, it.ChangeKey)
				}
				it.ChangeKey = newCK
				out, err := json.Marshal(it)
				if err != nil {
					return fmt.Errorf("marshal updated item: %w", err)
				}
				return tx.Bucket([]byte(bucketItem)).Put(k, out)
			}
		}
		return ErrItemNotFound
	})
}

// SetItemConversation implements IdentityStore.
func (s *BoltIdentityStore) SetItemConversation(id ItemId, convID ConversationId) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketItem)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var it StoredItemIdentity
			if err := json.Unmarshal(v, &it); err != nil {
				continue
			}
			if it.ItemID.Equal(id) {
				it.ConversationID = convID
				out, err := json.Marshal(it)
				if err != nil {
					return fmt.Errorf("marshal updated item: %w", err)
				}
				return tx.Bucket([]byte(bucketItem)).Put(k, out)
			}
		}
		return ErrItemNotFound
	})
}

// SetItemMsgKey implements IdentityStore.
func (s *BoltIdentityStore) SetItemMsgKey(id ItemId, msgKey string) error {
	if id.IsZero() {
		return errors.New("SetItemMsgKey: zero ItemId")
	}
	if msgKey == "" {
		return errors.New("SetItemMsgKey: empty msgKey")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketItem)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var it StoredItemIdentity
			if err := json.Unmarshal(v, &it); err != nil {
				continue
			}
			if it.ItemID.Equal(id) {
				it.MsgKey = msgKey
				out, err := json.Marshal(it)
				if err != nil {
					return fmt.Errorf("marshal updated item: %w", err)
				}
				return tx.Bucket([]byte(bucketItem)).Put(k, out)
			}
		}
		return ErrItemNotFound
	})
}

// UpdateItemState stores read/category state for an existing item identity.
func (s *BoltIdentityStore) UpdateItemState(id ItemId, isRead *bool, categories []string) error {
	if id.IsZero() {
		return errors.New("UpdateItemState: zero ItemId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketItem)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var it StoredItemIdentity
			if err := json.Unmarshal(v, &it); err != nil {
				continue
			}
			if it.ItemID.Equal(id) {
				if isRead != nil {
					it.IsRead = *isRead
				}
				if categories != nil {
					it.Categories = append([]string(nil), categories...)
				}
				out, err := json.Marshal(it)
				if err != nil {
					return fmt.Errorf("marshal updated item: %w", err)
				}
				return tx.Bucket([]byte(bucketItem)).Put(k, out)
			}
		}
		return ErrItemNotFound
	})
}

// DeleteItemIdentity implements IdentityStore.
func (s *BoltIdentityStore) DeleteItemIdentity(id ItemId) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketItem)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var it StoredItemIdentity
			if err := json.Unmarshal(v, &it); err != nil {
				continue
			}
			if it.ItemID.Equal(id) {
				return tx.Bucket([]byte(bucketItem)).Delete(k)
			}
		}
		return ErrItemNotFound
	})
}

// ListItemIdentitiesByMailbox implements IdentityStore.
func (s *BoltIdentityStore) ListItemIdentitiesByMailbox(mboxID MailboxId) ([]StoredItemIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []StoredItemIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketItem))
		return b.ForEach(func(_, v []byte) error {
			var rec StoredItemIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if rec.MailboxID.Equal(mboxID) {
				result = append(result, rec)
			}
			return nil
		})
	})
	return result, err
}

// ListItemIdentitiesByFolder implements IdentityStore.
func (s *BoltIdentityStore) ListItemIdentitiesByFolder(folderID FolderId) ([]StoredItemIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []StoredItemIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketItem))
		return b.ForEach(func(_, v []byte) error {
			var rec StoredItemIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if rec.FolderID.Equal(folderID) {
				result = append(result, rec)
			}
			return nil
		})
	})
	return result, err
}

// ---------------------------------------------------------------------------
// AttachmentId operations
// ---------------------------------------------------------------------------

// attachmentKey returns the storage key for attachment (parentItemID + name).
func attachmentKey(parentID ItemId, name string) string {
	return parentID.String() + "\x00" + name
}

// PutAttachmentIdentity implements IdentityStore.
func (s *BoltIdentityStore) PutAttachmentIdentity(parentID ItemId, name string, id AttachmentId) error {
	if id.IsZero() {
		return errors.New("PutAttachmentIdentity: zero AttachmentId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketAttachment))
		k := attachmentKey(parentID, name)
		if b.Get([]byte(k)) != nil {
			return ErrIdentityExists
		}
		rec := storedAttachmentIdentity{
			AttachmentID: id,
			ParentID:     parentID,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal attachment identity: %w", err)
		}
		return b.Put([]byte(k), data)
	})
}

// GetAttachmentID implements IdentityStore.
func (s *BoltIdentityStore) GetAttachmentID(parentID ItemId, name string) (AttachmentId, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var id AttachmentId
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketAttachment))
		data := b.Get([]byte(attachmentKey(parentID, name)))
		if data == nil {
			return ErrItemNotFound
		}
		var rec storedAttachmentIdentity
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("unmarshal attachment identity: %w", err)
		}
		id = rec.AttachmentID
		return nil
	})
	return id, err
}

// GetAttachmentIdentity implements IdentityStore.
func (s *BoltIdentityStore) GetAttachmentIdentity(id AttachmentId) (*storedAttachmentIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rec *storedAttachmentIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketAttachment)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var a storedAttachmentIdentity
			if err := json.Unmarshal(v, &a); err != nil {
				continue
			}
			if a.AttachmentID.Equal(id) {
				acopy := a
				rec = &acopy
				return nil
			}
		}
		return ErrItemNotFound
	})
	return rec, err
}

// ListAttachmentsByParent implements IdentityStore.
func (s *BoltIdentityStore) ListAttachmentsByParent(parentID ItemId) ([]storedAttachmentIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []storedAttachmentIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketAttachment))
		return b.ForEach(func(_, v []byte) error {
			var rec storedAttachmentIdentity
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if rec.ParentID.Equal(parentID) {
				result = append(result, rec)
			}
			return nil
		})
	})
	return result, err
}

// ---------------------------------------------------------------------------
// ConversationId operations
// ---------------------------------------------------------------------------

// PutConversationIdentity implements IdentityStore.
func (s *BoltIdentityStore) PutConversationIdentity(id ConversationId, mailboxID MailboxId) error {
	if id.IsZero() {
		return errors.New("PutConversationIdentity: zero ConversationId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketConversation))
		if b.Get([]byte(id.String())) != nil {
			return ErrIdentityExists
		}
		rec := storedConversationIdentity{
			ConversationID: id,
			MailboxID:      mailboxID,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal conversation identity: %w", err)
		}
		return b.Put([]byte(id.String()), data)
	})
}

// GetConversationIdentity implements IdentityStore.
func (s *BoltIdentityStore) GetConversationIdentity(id ConversationId) (*storedConversationIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rec *storedConversationIdentity
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketConversation))
		data := b.Get([]byte(id.String()))
		if data == nil {
			return ErrItemNotFound
		}
		var c storedConversationIdentity
		if err := json.Unmarshal(data, &c); err != nil {
			return fmt.Errorf("unmarshal conversation identity: %w", err)
		}
		ccopy := c
		rec = &ccopy
		return nil
	})
	return rec, err
}

// ---------------------------------------------------------------------------
// ChangeKey generation
// ---------------------------------------------------------------------------

// generateChangeKey produces a cryptographically random version token.
// This is used to generate RuleChangeKey, OOFChangeKey, ResourceChangeKey,
// and NotificationChangeKey when a new policy version is needed.
func generateChangeKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
