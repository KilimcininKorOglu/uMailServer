// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// # Canonical Object Model
//
// semcore defines the authoritative shape of core Exchange-semantic objects.
// These types are the only permissible source of truth for identity, version,
// and lifecycle state. Protocol adapters (IMAP, JMAP, DAV, EWS, MAPI) must
// translate wire formats through these types; they may not redefine storage.
//
// # Identity
//
// The identity family is defined in identity.go. All IDs are opaque; only
// equality comparisons are meaningful. IDs must not be derived from mailbox
// names, folder paths, filesystem mtimes, or folder:uid search keys.
package semcore

import "time"

// ---------------------------------------------------------------------------
// Mailbox
// ---------------------------------------------------------------------------

// Mailbox represents the canonical mailbox object.
// MailboxId is assigned at creation and remains stable for the mailbox's lifetime.
type Mailbox struct {
	ID            MailboxId
	Name          string // display name, not used for identity
	UIDValidity   uint32 // RFC 3501:UIDVALIDITY
	UIDNext       uint32 // next-assigned UID for this mailbox
	HighestModSeq uint64 // RFC 7162: highest modification sequence number
	IsSubscribed  bool
}

// ---------------------------------------------------------------------------
// Folder
// ---------------------------------------------------------------------------

// Folder represents the canonical folder object within a Mailbox.
// FolderId is assigned at creation and remains stable across renames and
// reparenting. FolderId is scoped to a MailboxId.
type Folder struct {
	ID            FolderId
	MailboxID     MailboxId
	Name          string // display name (may change without identity change)
	ParentID      FolderId // zero if this is a top-level folder
	Role          string // e.g., "inbox", "drafts", "sent", "trash", "archive" — empty for user folders
	SortOrder     int    // client sort hint
	TotalItems    int
	UnreadItems   int
	HighestModSeq uint64 // RFC 7162
	IsSubscribed  bool
}

// IsDistinguished returns true for system folders with a fixed Role.
func (f *Folder) IsDistinguished() bool { return f.Role != "" }

// ---------------------------------------------------------------------------
// Item
// ---------------------------------------------------------------------------

// Item represents the canonical message/item object within a Folder.
// ItemId is assigned at creation and remains stable across reads, moves,
// copies, and non-destructive updates. ChangeKey advances on every semantically
// visible mutation. ItemId is scoped to a MailboxId.
type Item struct {
	ID            ItemId
	MailboxID     MailboxId
	FolderID      FolderId
	ChangeKey     ChangeKey
	ConversationID ConversationId

	// Content
	Subject    string
	Sender     string
	To         string
	CC         string
	Date       time.Time // message date (authored or received)
	ReceivedAt time.Time // server receipt time

	// Flags (keyword-style, RFC 7161)
	Keywords map[string]bool

	// Size in bytes
	Size int64

	// Thread position
	InReplyTo  string   // References header base
	References []string // full References header chain

	// Attachments
	HasAttachments bool
	Preview        string // first ~200 chars of body
}

// HasKeyword reports whether the item carries the given keyword/flag.
func (i *Item) HasKeyword(flag string) bool {
	return i.Keywords[flag]
}

// ---------------------------------------------------------------------------
// Attachment
// ---------------------------------------------------------------------------

// Attachment represents a file attachment on an Item.
// AttachmentId is scoped to its parent ItemId; the combination is globally unique.
type Attachment struct {
	ID       AttachmentId
	ParentID ItemId
	Name     string
	ContentType string
	Size     int64 // bytes
	IsInline bool  // inline disposition (CID-backed body reference)
}

// ---------------------------------------------------------------------------
// Conversation
// ---------------------------------------------------------------------------

// Conversation represents a message thread lineage. It groups related Items
// across folder boundaries. ConversationId is scoped to a MailboxId.
type Conversation struct {
	ID          ConversationId
	MailboxID   MailboxId
	ItemIDs     []ItemId // ordered list of Items in this conversation
	Subject     string   // root message subject
	HasAttachments bool
	UnreadCount int
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Lifecycle records a single canonical state transition for an object.
// All mutations in the canonical pipeline must emit one Lifecycle entry
// per object changed. Downstream consumers (sync, events, search) derive
// their view from these entries rather than inferring state from timestamps
// or filesystem artifacts.
type Lifecycle struct {
	MailboxID  MailboxId
	FolderID   FolderId // zero for mailbox-scoped events
	ItemID     ItemId   // zero for folder-scoped events
	Kind       LifecycleKind
	At         time.Time
	Actor      string // user or system actor that triggered the change
	ChangeKey  ChangeKey
}

// IsZero returns true when the entry has no identity set.
func (l *Lifecycle) IsZero() bool {
	return l.MailboxID.IsZero() && l.FolderID.IsZero() && l.ItemID.IsZero()
}
