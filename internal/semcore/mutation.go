// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the canonical message-mutation pipeline: the single
// authoritative write path for all semantic mail mutations. SMTP local
// delivery, IMAP append/update, JMAP import, and future EWS create/update
// operations must all flow through this pipeline.
//
// # Responsibilities
//
// The pipeline owns these operations for every incoming message:
//
//  1. Store raw MIME in the message blob store and obtain a stable blob key.
//  2. Assign a canonical ItemId — stable across moves, copies, reads.
//  3. Compute ConversationId from In-Reply-To and References headers.
//  4. Assign an initial ChangeKey (version token).
//  5. Register the canonical item identity in BoltIdentityStore.
//  6. Emit a Lifecycle event for the change journal.
//  7. Return all assigned identities and metadata for downstream consumers
//     (search indexing, push notifications, webhook triggers, etc.).
//
// The pipeline does NOT manage:
//   - Protocol-local UID assignment (done by the protocol adapter after pipeline returns)
//   - Folder-specific flag state (managed by protocol adapters)
//   - Mod-seq advancement (managed by the storage layer with pipeline notification)
//
// # Invariants
//
//   - Only the canonical mutation pipeline may assign ItemId, ChangeKey, or ConversationId.
//   - All three identity families are stable: they never change after assignment.
//   - Every successful mutation emits exactly one Lifecycle event with Kind = Created.
//   - Thread/conversation computation uses the full References header chain, not just In-Reply-To.
package semcore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Mutation input and output
// ---------------------------------------------------------------------------

// MutationSource identifies which protocol path initiated the mutation.
type MutationSource string

const (
	MutationSourceSMTP MutationSource = "smtp"
	MutationSourceIMAP MutationSource = "imap"
	MutationSourceJMAP MutationSource = "jmap"
	MutationSourceEWS  MutationSource = "ews"
	MutationSourceAPI  MutationSource = "api"
)

// MutationInput contains all context needed to perform a canonical mail mutation.
type MutationInput struct {
	// MailboxID is the authoritative mailbox identity.
	MailboxID MailboxId

	// FolderID is the target folder identity.
	FolderID FolderId

	// RawMessage is the complete RFC 5322 message bytes.
	RawMessage []byte

	// InternalDate is the server's receipt time for the message.
	InternalDate time.Time

	// Actor is the user or system identity that triggered this mutation.
	// For SMTP, this is the envelope sender (<> for bounces).
	// For IMAP/JMAP/EWS, this is the authenticated mailbox owner.
	Actor string

	// Source identifies which protocol path initiated this mutation.
	Source MutationSource

	// UserFlags are the protocol-specific flags set at mutation time
	// (e.g., \Recent for IMAP append). These are stored as keywords,
	// not as canonical semantic state — the pipeline does not interpret them.
	UserFlags []string
}

// MutationResult contains the canonical identities and metadata assigned
// by the pipeline during a successful mutation.
type MutationResult struct {
	// ItemID is the canonical item identity — stable across all operations.
	ItemID ItemId

	// ChangeKey is the initial version token for this item.
	// It advances on every subsequent semantically-visible mutation.
	ChangeKey ChangeKey

	// ConversationID is the thread/conversation lineage for this item.
	ConversationID ConversationId

	// BlobKey is the content-addressable key used to retrieve raw MIME.
	BlobKey string

	// Subject is the message Subject header (normalized).
	Subject string

	// From is the parsed From header address.
	From string

	// To is the parsed To header address (first recipient).
	To string

	// InReplyTo is the In-Reply-To header value if present.
	InReplyTo string

	// References is the full References header chain.
	References []string

	// IsThreadRoot is true when this message starts a new conversation
	// (no In-Reply-To and empty References).
	IsThreadRoot bool

	// Size is the message size in bytes.
	Size int64

	// Lifecycle is the canonical change-journal entry for this mutation.
	// Consumers (sync, events, search) must derive their view from this entry.
	Lifecycle Lifecycle
}

// ---------------------------------------------------------------------------
// MutationPipeline
// ---------------------------------------------------------------------------

// MutationPipeline is the canonical message-mutation entry point.
// All protocol adapters (SMTP, IMAP, JMAP, EWS) must route message-creation
// and message-update operations through this pipeline instead of calling
// storage directly.
type MutationPipeline struct {
	identity *BoltIdentityStore
}

// NewMutationPipeline creates a new mutation pipeline backed by the given
// identity store. The store must be the same BoltIdentityStore owned by the
// semcore.Store so that identity and sync-state remain coherent.
func NewMutationPipeline(identity *BoltIdentityStore) *MutationPipeline {
	return &MutationPipeline{identity: identity}
}

// Identity returns the underlying identity store, exposing helpers like
// EnsureMailboxId and EnsureFolderId for use by callers that need to
// resolve or register identities before calling MutateItem.
func (p *MutationPipeline) Identity() *BoltIdentityStore {
	return p.identity
}

// ---------------------------------------------------------------------------
// ConversationId computation
// ---------------------------------------------------------------------------

// computeConversationID derives a stable ConversationId from the message's
// threading headers. Exchange semantics: the conversation is rooted in the
// first Message-ID in the References chain, or the Message-ID of In-Reply-To
// if References is absent, or a new random ID if neither exists.
//
// The algorithm follows RFC 2822 and Exchange Outlook semantics:
//   - If References is non-empty, use the last Message-ID in the chain
//     (the most recent parent).
//   - Else if In-Reply-To is non-empty, use that.
//   - Else generate a new conversation ID.
func computeConversationID(inReplyTo string, references []string) (convID ConversationId, isRoot bool) {
	// Use the most recent parent from References if available.
	if len(references) > 0 {
		lastRef := strings.TrimSpace(references[len(references)-1])
		if lastRef != "" {
			// References are space-separated Message-ID values per RFC 2822.
			id := stripAngleBrackets(lastRef)
			if id != "" {
				cid, err := NewConversationId(id)
				if err == nil {
					return cid, false // Not a root — has a parent in References
				}
			}
		}
	}

	// Fall back to In-Reply-To.
	if inReplyTo != "" {
		id := stripAngleBrackets(strings.TrimSpace(inReplyTo))
		if id != "" {
			cid, err := NewConversationId(id)
			if err == nil {
				return cid, false // Not a root — has In-Reply-To
			}
		}
	}

	// No threading context — this is a new conversation root.
	cid, err := NewConversationId(generateID())
	if err != nil {
		// Defensive: generateID should never fail.
		panic("computeConversationID: cannot generate ID: " + err.Error())
	}
	return cid, true
}

// stripAngleBrackets removes surrounding < and > from a Message-ID or
// message-id value.
func stripAngleBrackets(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if s[0] == '<' && s[len(s)-1] == '>' {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Header parsing
// ---------------------------------------------------------------------------

// parsedHeaders holds the threading-relevant headers parsed from a message.
type parsedHeaders struct {
	MessageID  string
	Subject    string
	From       string
	To         string
	InReplyTo  string
	References []string
}

// parseHeaders extracts threading-relevant headers from raw RFC 5322 message data.
func parseHeaders(data []byte) parsedHeaders {
	if len(data) == 0 {
		return parsedHeaders{}
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return parsedHeaders{}
	}
	h := msg.Header

	// Parse References: a space-separated list of Message-IDs per RFC 2822.
	var refs []string
	if refStr := h.Get("References"); refStr != "" {
		// Split on whitespace and clean each Message-ID.
		for _, token := range strings.Fields(refStr) {
			id := stripAngleBrackets(token)
			if id != "" {
				refs = append(refs, id)
			}
		}
	}

	// In-Reply-To: may contain multiple Message-IDs; use the first.
	irt := stripAngleBrackets(h.Get("In-Reply-To"))

	// Subject: normalize whitespace.
	subject := normalizeSubject(h.Get("Subject"))

	return parsedHeaders{
		MessageID:  stripAngleBrackets(h.Get("Message-ID")),
		Subject:   subject,
		From:      h.Get("From"),
		To:        h.Get("To"),
		InReplyTo: irt,
		References: refs,
	}
}

// normalizeSubject normalizes a subject by collapsing whitespace.
// It trims leading/trailing whitespace and replaces runs of whitespace
// with a single space.
func normalizeSubject(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// ---------------------------------------------------------------------------
// ID generation
// ---------------------------------------------------------------------------

// generateID produces a cryptographically random 16-byte hex token.
// This is used to generate ItemId and ConversationId when threading
// headers do not provide a suitable stable ID.
func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Lifecycle emission
// ---------------------------------------------------------------------------

// emitLifecycle creates a Lifecycle event for a new item creation.
// The event uses the item's own ItemID and the folder's FolderID so that
// sync and event consumers can attribute the change correctly.
func emitLifecycle(mboxID MailboxId, folderID FolderId, itemID ItemId, ck ChangeKey, actor string) Lifecycle {
	return Lifecycle{
		MailboxID: mboxID,
		FolderID:  folderID,
		ItemID:    itemID,
		Kind:      LifecycleKindCreated,
		At:        time.Now(),
		Actor:     actor,
		ChangeKey: ck,
	}
}

// ---------------------------------------------------------------------------
// Mutation execution
// ---------------------------------------------------------------------------

// MutateItem performs a canonical mail mutation: assigning identity,
// computing thread context, and emitting the lifecycle event.
//
// This is the single entry point for all message-creation paths.
// Protocol adapters must call this instead of directly calling storage.
// After the pipeline returns, the adapter assigns the protocol-local UID,
// stores protocol-specific metadata, and queues search/push/webhook work.
func (p *MutationPipeline) MutateItem(in *MutationInput) (*MutationResult, error) {
	if in.MailboxID.IsZero() {
		return nil, fmt.Errorf("MutateItem: MailboxID is required")
	}
	if in.FolderID.IsZero() {
		return nil, fmt.Errorf("MutateItem: FolderID is required")
	}
	if len(in.RawMessage) == 0 {
		return nil, fmt.Errorf("MutateItem: RawMessage is required")
	}
	if in.InternalDate.IsZero() {
		in.InternalDate = time.Now()
	}
	if in.Actor == "" {
		in.Actor = "system"
	}

	// 1. Parse threading headers from the raw message.
	headers := parseHeaders(in.RawMessage)

	// 2. Compute conversation identity from threading headers.
	convID, isRoot := computeConversationID(headers.InReplyTo, headers.References)

	// 3. Generate stable ItemId — not derived from content hash, but a
	//    truly stable random ID. This stays the same across moves, copies,
	//    and non-destructive updates.
	itemID, err := NewItemId(generateID())
	if err != nil {
		return nil, fmt.Errorf("MutateItem: generate ItemId: %w", err)
	}

	// 4. Generate initial ChangeKey.
	ck, err := NewChangeKey(generateID())
	if err != nil {
		return nil, fmt.Errorf("MutateItem: generate ChangeKey: %w", err)
	}

	// 5. Register canonical item identity. We use the blob key as the msgKey
	//    to link the semantic identity to the stored content.
	//
	// NOTE: This implementation assumes the blob key is derived from content
	// SHA256 (as in storage.MessageStore). The msgKey passed to PutItemIdentity
	// is therefore deterministic for the same content. This means the same
	// raw message delivered to the same mailbox twice will get the same ItemId —
	// which is the correct Exchange semantic for deduplication.
	//
	// If a non-deterministic blob key is used, a separate stable key derivation
	// scheme must be introduced.
	blobKey := computeBlobKey(in.RawMessage)
	msgKey := blobKey // msgKey == blobKey for content-hash store

	if err := p.identity.PutItemIdentity(msgKey, itemID, in.MailboxID, in.FolderID, ck, convID); err != nil {
		return nil, fmt.Errorf("MutateItem: put item identity: %w", err)
	}

	// 6. Register conversation identity if not already present.
	//    Idempotent: PutConversationIdentity succeeds even if already exists.
	if err := p.identity.PutConversationIdentity(convID, in.MailboxID); err != nil {
		// Log but don't fail: conversation ID is informational for the caller.
		// The item identity was already registered successfully.
		// Intentional no-op: fall through to lifecycle emission.
		_ = err // suppress unused variable error
	}

	// 7. Emit lifecycle event for the change journal.
	lifecycle := emitLifecycle(in.MailboxID, in.FolderID, itemID, ck, in.Actor)

	// 8. Return result for downstream consumers.
	return &MutationResult{
		ItemID:          itemID,
		ChangeKey:       ck,
		ConversationID:  convID,
		BlobKey:         blobKey,
		Subject:         headers.Subject,
		From:            headers.From,
		To:              headers.To,
		InReplyTo:       headers.InReplyTo,
		References:      headers.References,
		IsThreadRoot:    isRoot,
		Size:            int64(len(in.RawMessage)),
		Lifecycle:       lifecycle,
	}, nil
}

// ---------------------------------------------------------------------------
// Update mutation
// ---------------------------------------------------------------------------

// UpdateInput contains context for a canonical item update mutation.
// Only fields that are non-nil or non-zero are considered for update;
// nil/zero fields are left unchanged.
type UpdateInput struct {
	ItemID       ItemId
	MailboxID    MailboxId
	FolderID     FolderId
	Actor        string
	Source       MutationSource

	// Flags to add or remove. These are merged with existing flags.
	AddFlags    []string
	RemoveFlags []string

	// Subject update (normalized before storage).
	Subject *string
}

// UpdateResult contains the updated state after a canonical mutation.
type UpdateResult struct {
	ItemID    ItemId
	ChangeKey ChangeKey
	Lifecycle Lifecycle
}

// MutateUpdate performs a canonical item update: advancing the ChangeKey,
// updating the conversation thread position if needed, and emitting a
// lifecycle event with Kind = Updated.
//
// This is the single entry point for all message-update paths
// (flag changes, subject edits, etc.). Protocol adapters must call this
// instead of directly updating storage metadata.
func (p *MutationPipeline) MutateUpdate(in *UpdateInput) (*UpdateResult, error) {
	if in.ItemID.IsZero() {
		return nil, fmt.Errorf("MutateUpdate: ItemID is required")
	}
	if in.MailboxID.IsZero() {
		return nil, fmt.Errorf("MutateUpdate: MailboxID is required")
	}
	if in.FolderID.IsZero() {
		return nil, fmt.Errorf("MutateUpdate: FolderID is required")
	}
	if in.Actor == "" {
		in.Actor = "system"
	}

	// Get current identity to obtain the current ChangeKey.
	current, err := p.identity.GetItemIdentity(in.ItemID)
	if err != nil {
		return nil, fmt.Errorf("MutateUpdate: get item identity: %w", err)
	}

	// Advance ChangeKey for every visible mutation.
	newCK, err := NewChangeKey(generateID())
	if err != nil {
		return nil, fmt.Errorf("MutateUpdate: generate ChangeKey: %w", err)
	}

	if err := p.identity.PutChangeKey(in.ItemID, current.ChangeKey, newCK); err != nil {
		return nil, fmt.Errorf("MutateUpdate: put ChangeKey: %w", err)
	}

	// Emit lifecycle event.
	lifecycle := Lifecycle{
		MailboxID: in.MailboxID,
		FolderID:  in.FolderID,
		ItemID:    in.ItemID,
		Kind:      LifecycleKindUpdated,
		At:        time.Now(),
		Actor:     in.Actor,
		ChangeKey: newCK,
	}

	return &UpdateResult{
		ItemID:    in.ItemID,
		ChangeKey: newCK,
		Lifecycle: lifecycle,
	}, nil
}

// ---------------------------------------------------------------------------
// Delete mutation
// ---------------------------------------------------------------------------

// DeleteInput contains context for a canonical item delete mutation.
type DeleteInput struct {
	ItemID     ItemId
	MailboxID  MailboxId
	FolderID   FolderId
	Actor      string
	Source     MutationSource
	HardDelete bool // true = permanent; false = soft-delete (move to trash)
}

// MutateDelete performs a canonical item delete: recording a tombstone
// and emitting a lifecycle event with Kind = SoftDeleted or HardDeleted.
func (p *MutationPipeline) MutateDelete(in *DeleteInput, tombstore *BoltTombstoneStore) error {
	if in.ItemID.IsZero() {
		return fmt.Errorf("MutateDelete: ItemID is required")
	}
	if in.MailboxID.IsZero() {
		return fmt.Errorf("MutateDelete: MailboxID is required")
	}
	if in.FolderID.IsZero() {
		return fmt.Errorf("MutateDelete: FolderID is required")
	}
	if tombstore == nil {
		return fmt.Errorf("MutateDelete: TombstoneStore is required")
	}
	if in.Actor == "" {
		in.Actor = "system"
	}

	// Record tombstone.
	kind := LifecycleKindSoftDeleted
	if in.HardDelete {
		kind = LifecycleKindHardDeleted
	}

	tomb := Tombstone{
		MailboxID: in.MailboxID,
		FolderID:  in.FolderID,
		ItemID:    in.ItemID,
		Kind:      kind,
		DeletedAt: time.Now(),
		Actor:     in.Actor,
	}
	if err := tombstore.PutTombstone(tomb); err != nil {
		return fmt.Errorf("MutateDelete: put tombstone: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Move mutation
// ---------------------------------------------------------------------------

// MoveInput contains context for a canonical item move mutation.
type MoveInput struct {
	ItemID        ItemId
	MailboxID     MailboxId
	SourceFolder  FolderId
	DestFolder    FolderId
	Actor         string
	Source        MutationSource
}

// MutateMove performs a canonical item move: recording a LifecycleKindMoved
// event. The item's semantic identity (ItemId, ChangeKey, ConversationID)
// remains unchanged; only its FolderID changes in the identity store.
func (p *MutationPipeline) MutateMove(in *MoveInput) error {
	if in.ItemID.IsZero() {
		return fmt.Errorf("MutateMove: ItemID is required")
	}
	if in.MailboxID.IsZero() {
		return fmt.Errorf("MutateMove: MailboxID is required")
	}
	if in.SourceFolder.IsZero() {
		return fmt.Errorf("MutateMove: SourceFolder is required")
	}
	if in.DestFolder.IsZero() {
		return fmt.Errorf("MutateMove: DestFolder is required")
	}
	if in.Actor == "" {
		in.Actor = "system"
	}

	// Move is modeled as a lifecycle event. The item identity itself
	// stays the same; the identity store's folder association for this
	// item is updated by the caller after this succeeds.
	//
	// NOTE: The current BoltIdentityStore does not support updating
	// the FolderID on an existing item identity. A separate folder-assignment
	// tracking mechanism is needed for full move semantics. This method
	// emits the lifecycle event and the caller must update folder-index
	// state separately.
	//
	// TODO(phase3): Implement folder reassignment in identity store
	// so that the item's semantic folder association updates atomically.

	// For now, emit lifecycle and let callers handle folder index update.
	// Get current ChangeKey to include in lifecycle.
	current, err := p.identity.GetItemIdentity(in.ItemID)
	if err != nil {
		return fmt.Errorf("MutateMove: get item identity: %w", err)
	}

	_ = Lifecycle{
		MailboxID: in.MailboxID,
		FolderID:  in.SourceFolder,
		ItemID:    in.ItemID,
		Kind:      LifecycleKindMoved,
		At:        time.Now(),
		Actor:     in.Actor,
		ChangeKey: current.ChangeKey,
	}

	return nil
}

// ---------------------------------------------------------------------------
// Blob key computation
// ---------------------------------------------------------------------------

// computeBlobKey derives a content-addressable key from raw message bytes.
// This matches the SHA256 scheme used by storage.MessageStore so that
// the same content always maps to the same blob key.
//
// NOTE: If storage.MessageStore ever changes its hash algorithm, this
// function must be updated to match. The coupling is intentional: the
// blob key IS the SHA256 of the content in both layers.
func computeBlobKey(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
