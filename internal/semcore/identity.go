// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer. All protocol adapters (IMAP, JMAP, DAV, EWS, MAPI)
// and future Exchange-semantic surfaces must read and write through these
// definitions instead of inventing per-surface identity rules.
//
// # Identity Family
//
// The system converges on one durable identity family:
//
//   - MailboxId  — authoritative mailbox identity, stable across renames
//   - FolderId   — authoritative folder identity within a mailbox
//   - ItemId     — authoritative message/item identity within a mailbox
//   - ChangeKey  — opaque version token that advances on every semantically
//                 visible mutation (per RFC 4551 semantics)
//   - AttachmentId — identity for an attachment relative to its parent ItemId
//   - ConversationId — identity for a message thread/conversation lineage
//
// All IDs are opaque to clients; only equality comparisons are meaningful.
// IDs must not be derived from mailbox names, folder paths, filesystem mtimes,
// or folder:uid search keys.
//
// # Ownership Rules
//
//   - Raw MIME blobs live in storage.MessageStore and are referenced by blob key.
//   - Canonical semantic state lives in semcore types, not in protocol-local keys.
//   - SMTP local delivery and IMAP append must converge on the canonical mutation
//     pipeline; neither path may assign semantic identity independently.
//
// # Mutation Invariants
//
// Only the canonical mutation pipeline may:
//
//   - assign or mutate canonical identity
//   - advance change/version state (ChangeKey)
//   - update conversation/thread state
//   - record change-journal events
//   - update search index projections
//   - apply policy hooks (rules, OOF, delegation, audit)
//
// Protocol adapters own wire contracts, pagination, XML/HTTP details, and
// fault mapping. They do not own storage or canonical change semantics.
package semcore

import (
	"encoding/json"
	"errors"
)

// ---------------------------------------------------------------------------
// Identity types
// ---------------------------------------------------------------------------

// MailboxId is the authoritative identity for a mailbox.
// It is assigned at creation and must remain stable for the mailbox's lifetime.
// Two MailboxIds with the same raw value refer to the same logical mailbox.
type MailboxId struct {
	raw string
}

// NewMailboxId constructs a MailboxId from its raw string representation.
// The raw value must be non-empty; empty values are treated as a nil ID.
func NewMailboxId(raw string) (MailboxId, error) {
	if raw == "" {
		return MailboxId{}, errors.New("MailboxId: empty value")
	}
	return MailboxId{raw: raw}, nil
}

// MustMailboxId constructs a MailboxId and panics on invalid input.
// Use only in tests or trusted initialization code.
func MustMailboxId(raw string) MailboxId {
	id, err := NewMailboxId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value. Clients must treat this as opaque.
func (id MailboxId) String() string { return id.raw }

// IsZero returns true for a nil/empty MailboxId.
func (id MailboxId) IsZero() bool { return id.raw == "" }

// Equal reports whether two MailboxIds have the same raw value.
func (id MailboxId) Equal(other MailboxId) bool { return id.raw == other.raw }

// MarshalJSON serializes a MailboxId to its raw string value.
func (id MailboxId) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.raw)
}

// UnmarshalJSON deserializes a MailboxId from its raw string value.
func (id *MailboxId) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*id = MailboxId{}
		return nil
	}
	*id = MailboxId{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------

// FolderId is the authoritative identity for a folder within a mailbox.
// It is assigned at creation and must remain stable across renames and reparenting.
// FolderIds are scoped to a MailboxId; a FolderId alone is not globally unique.
type FolderId struct {
	raw string
}

// NewFolderId constructs a FolderId from its raw string representation.
func NewFolderId(raw string) (FolderId, error) {
	if raw == "" {
		return FolderId{}, errors.New("FolderId: empty value")
	}
	return FolderId{raw: raw}, nil
}

// MustFolderId constructs a FolderId and panics on invalid input.
func MustFolderId(raw string) FolderId {
	id, err := NewFolderId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value. Clients must treat this as opaque.
func (id FolderId) String() string { return id.raw }

// IsZero returns true for a nil/empty FolderId.
func (id FolderId) IsZero() bool { return id.raw == "" }

// Equal reports whether two FolderIds have the same raw value.
func (id FolderId) Equal(other FolderId) bool { return id.raw == other.raw }

// MarshalJSON serializes a FolderId to its raw string value.
func (id FolderId) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.raw)
}

// UnmarshalJSON deserializes a FolderId from its raw string value.
func (id *FolderId) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*id = FolderId{}
		return nil
	}
	*id = FolderId{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------

// ItemId is the authoritative identity for a message or item within a mailbox.
// It is assigned at creation and must remain stable for the item's lifetime
// across reads, moves, copies, and non-destructive updates.
// ItemIds are scoped to a MailboxId; a bare ItemId is not globally unique.
type ItemId struct {
	raw string
}

// NewItemId constructs an ItemId from its raw string representation.
func NewItemId(raw string) (ItemId, error) {
	if raw == "" {
		return ItemId{}, errors.New("ItemId: empty value")
	}
	return ItemId{raw: raw}, nil
}

// MustItemId constructs an ItemId and panics on invalid input.
func MustItemId(raw string) ItemId {
	id, err := NewItemId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value. Clients must treat this as opaque.
func (id ItemId) String() string { return id.raw }

// IsZero returns true for a nil/empty ItemId.
func (id ItemId) IsZero() bool { return id.raw == "" }

// Equal reports whether two ItemIds have the same raw value.
func (id ItemId) Equal(other ItemId) bool { return id.raw == other.raw }

// MarshalJSON serializes an ItemId to its raw string value.
func (id ItemId) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.raw)
}

// UnmarshalJSON deserializes an ItemId from its raw string value.
func (id *ItemId) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*id = ItemId{}
		return nil
	}
	*id = ItemId{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------

// ChangeKey is an opaque version token that advances on every semantically
// visible mutation. Clients must treat it as opaque and compare for equality only.
// A stale ChangeKey on write must be rejected explicitly (version conflict).
type ChangeKey struct {
	raw string
}

// NewChangeKey constructs a ChangeKey from its raw string representation.
func NewChangeKey(raw string) (ChangeKey, error) {
	if raw == "" {
		return ChangeKey{}, errors.New("ChangeKey: empty value")
	}
	return ChangeKey{raw: raw}, nil
}

// MustChangeKey constructs a ChangeKey and panics on invalid input.
func MustChangeKey(raw string) ChangeKey {
	ck, err := NewChangeKey(raw)
	if err != nil {
		panic(err)
	}
	return ck
}

// String returns the raw string value. Clients must treat this as opaque.
func (ck ChangeKey) String() string { return ck.raw }

// IsZero returns true for a nil/empty ChangeKey.
func (ck ChangeKey) IsZero() bool { return ck.raw == "" }

// Equal reports whether two ChangeKeys have the same raw value.
func (ck ChangeKey) Equal(other ChangeKey) bool { return ck.raw == other.raw }

// MarshalJSON serializes a ChangeKey to its raw string value.
func (ck ChangeKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(ck.raw)
}

// UnmarshalJSON deserializes a ChangeKey from its raw string value.
func (ck *ChangeKey) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*ck = ChangeKey{}
		return nil
	}
	*ck = ChangeKey{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------

// AttachmentId is the authoritative identity for an attachment relative to
// its parent ItemId. AttachmentIds are stable across parent item edits.
// An AttachmentId alone is not globally unique; global uniqueness requires
// the parent ItemId context.
type AttachmentId struct {
	raw string
}

// NewAttachmentId constructs an AttachmentId from its raw string representation.
func NewAttachmentId(raw string) (AttachmentId, error) {
	if raw == "" {
		return AttachmentId{}, errors.New("AttachmentId: empty value")
	}
	return AttachmentId{raw: raw}, nil
}

// MustAttachmentId constructs an AttachmentId and panics on invalid input.
func MustAttachmentId(raw string) AttachmentId {
	id, err := NewAttachmentId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value. Clients must treat this as opaque.
func (id AttachmentId) String() string { return id.raw }

// IsZero returns true for a nil/empty AttachmentId.
func (id AttachmentId) IsZero() bool { return id.raw == "" }

// Equal reports whether two AttachmentIds have the same raw value.
func (id AttachmentId) Equal(other AttachmentId) bool { return id.raw == other.raw }

// MarshalJSON serializes an AttachmentId to its raw string value.
func (id AttachmentId) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.raw)
}

// UnmarshalJSON deserializes an AttachmentId from its raw string value.
func (id *AttachmentId) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*id = AttachmentId{}
		return nil
	}
	*id = AttachmentId{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------

// ConversationId is the authoritative identity for a message thread or
// conversation lineage. It groups related messages and remains stable across
// folder moves and non-destructive edits. ConversationId is scoped to a MailboxId.
type ConversationId struct {
	raw string
}

// NewConversationId constructs a ConversationId from its raw string representation.
func NewConversationId(raw string) (ConversationId, error) {
	if raw == "" {
		return ConversationId{}, errors.New("ConversationId: empty value")
	}
	return ConversationId{raw: raw}, nil
}

// MustConversationId constructs a ConversationId and panics on invalid input.
func MustConversationId(raw string) ConversationId {
	id, err := NewConversationId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value. Clients must treat this as opaque.
func (id ConversationId) String() string { return id.raw }

// IsZero returns true for a nil/empty ConversationId.
func (id ConversationId) IsZero() bool { return id.raw == "" }

// Equal reports whether two ConversationIds have the same raw value.
func (id ConversationId) Equal(other ConversationId) bool { return id.raw == other.raw }

// MarshalJSON serializes a ConversationId to its raw string value.
func (id ConversationId) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.raw)
}

// UnmarshalJSON deserializes a ConversationId from its raw string value.
func (id *ConversationId) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*id = ConversationId{}
		return nil
	}
	*id = ConversationId{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------
// Sync token
// ---------------------------------------------------------------------------

// SyncToken is an opaque continuation token for incremental sync operations.
// It encodes the sync state (watermark, version, page) required to resume
// a folder or mailbox sync without replaying the full history.
// Unknown/malformed tokens parse as zero-equivalent; callers must handle
// the resync-from-empty case explicitly.
type SyncToken struct {
	raw string
}

// NewSyncToken constructs a SyncToken from its raw string representation.
// An empty raw string is valid and represents the initial/empty sync state.
func NewSyncToken(raw string) SyncToken {
	return SyncToken{raw: raw}
}

// String returns the raw token value. Clients must treat this as opaque.
func (t SyncToken) String() string { return t.raw }

// IsZero returns true for an empty token (initial sync state).
func (t SyncToken) IsZero() bool { return t.raw == "" }

// Equal reports whether two SyncTokens have the same raw value.
func (t SyncToken) Equal(other SyncToken) bool { return t.raw == other.raw }

// ---------------------------------------------------------------------------
// Lifecycle kind
// ---------------------------------------------------------------------------

// LifecycleKind identifies the kind of state transition for an object.
type LifecycleKind uint8

const (
	LifecycleKindCreated   LifecycleKind = iota // object was created
	LifecycleKindUpdated                         // object was mutated
	LifecycleKindMoved                          // object changed parent/folder
	LifecycleKindSoftDeleted                    // object moved to trash/tombstoned
	LifecycleKindHardDeleted                    // object permanently removed
	LifecycleKindRestored                       // object restored from trash
)

// String returns a human-readable label for the kind.
func (k LifecycleKind) String() string {
	switch k {
	case LifecycleKindCreated:
		return "created"
	case LifecycleKindUpdated:
		return "updated"
	case LifecycleKindMoved:
		return "moved"
	case LifecycleKindSoftDeleted:
		return "soft_deleted"
	case LifecycleKindHardDeleted:
		return "hard_deleted"
	case LifecycleKindRestored:
		return "restored"
	default:
		return "unknown"
	}
}
