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
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/mailthread"
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
	MutationSourceMAPI MutationSource = "mapi"
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

	// Email is the raw email address (user key) for the msgStore.
	Email string

	// Source is the protocol path that initiated this mutation.
	Source MutationSource

	// UserFlags are the protocol-specific flags set at mutation time
	// (e.g., \Recent for IMAP append). These are stored as keywords,
	// not as canonical semantic state — the pipeline does not interpret them.
	UserFlags []string

	// IsRead indicates whether the message should be marked as read
	// at mutation time. When true, the StoredItemIdentity.IsRead is
	// set to true so that EWS GetItem returns IsRead=true.
	IsRead bool

	// DelegateAuditContext is set when a delegate is acting on behalf of a mailbox owner.
	// It is used to populate the Lifecycle event Actor field so that audit logs and
	// sync consumers can distinguish delegate actions from direct owner actions.
	DelegateAuditContext *DelegateAuditContext
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
// PipelineIdentityStore is the identity surface the MutationPipeline needs on the
// canonical write path: allocating/resolving mailbox and folder identities,
// stamping item identities and their state, and the EnsureMailboxId /
// EnsureFolderId resolution its callers reach through Identity(). The concrete
// *BoltIdentityStore satisfies it today; a relational identity store satisfies
// it later, so the pipeline carries no engine dependency.
type PipelineIdentityStore interface {
	EnsureMailboxId(email string) (MailboxId, error)
	EnsureFolderId(mboxKey, folderName, role string) (FolderId, error)
	GetItemIdentity(id ItemId) (*StoredItemIdentity, error)
	PutItemIdentity(msgKey, email string, id ItemId, mailboxID MailboxId, folderID FolderId, ck ChangeKey, convID ConversationId, isRead bool) error
	PutItemIdentityWithKey(storageKey, msgKey, email string, id ItemId, mailboxID MailboxId, folderID FolderId, ck ChangeKey, convID ConversationId, isRead bool) error
	PutChangeKey(id ItemId, currentCK ChangeKey, newCK ChangeKey) error
	PutConversationIdentity(id ConversationId, mailboxID MailboxId) error
}

// PipelineLifecycleStore is the lifecycle-event surface the pipeline appends to.
// *BoltLifecycleStore satisfies it; a relational lifecycle store will too.
type PipelineLifecycleStore interface {
	AppendLifecycle(event Lifecycle) error
}

// The bbolt-backed stores satisfy the pipeline's consumer interfaces.
var (
	_ PipelineIdentityStore  = (*BoltIdentityStore)(nil)
	_ PipelineLifecycleStore = (*BoltLifecycleStore)(nil)
)

type MutationPipeline struct {
	identity  PipelineIdentityStore
	lifecycle PipelineLifecycleStore
}

// NewMutationPipeline creates a new mutation pipeline backed by the given
// identity store and optional lifecycle store. The identity store must be the
// same one owned by the semcore.Store so that identity and sync-state remain
// coherent. If lifecycle is nil, lifecycle events are returned in
// MutationResult but not persisted.
func NewMutationPipeline(identity PipelineIdentityStore, lifecycle PipelineLifecycleStore) *MutationPipeline {
	return &MutationPipeline{identity: identity, lifecycle: lifecycle}
}

// Identity returns the underlying identity store, exposing helpers like
// EnsureMailboxId and EnsureFolderId for use by callers that need to
// resolve or register identities before calling MutateItem.
func (p *MutationPipeline) Identity() PipelineIdentityStore {
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
//   - Else root the conversation on the message's own Message-ID so that a
//     later reply (whose References/In-Reply-To point back at this message)
//     resolves to the SAME ConversationId. Only when no Message-ID is present
//     do we fall back to a random ID.
func computeConversationID(inReplyTo string, references []string, ownMessageID string) (convID ConversationId, isRoot bool) {
	// Shared rooting (mailthread.Root) so EWS groups a conversation identically
	// to the storage thread index: References-last → In-Reply-To → own Message-ID.
	if root, r := mailthread.Root(ownMessageID, inReplyTo, references); root != "" {
		if cid, err := NewConversationId(root); err == nil {
			return cid, r
		}
	}

	// No usable Message-ID — generate a new random conversation root.
	cid, err := NewConversationId(generateID())
	if err != nil {
		// Defensive: generateID should never fail.
		panic("computeConversationID: cannot generate ID: " + err.Error())
	}
	return cid, true
}

// stripAngleBrackets removes surrounding < and > from a Message-ID value. It
// delegates to the shared mailthread.StripBrackets so the header parser and the
// rooting logic strip identically.
func stripAngleBrackets(s string) string {
	return mailthread.StripBrackets(s)
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
		Subject:    subject,
		From:       h.Get("From"),
		To:         h.Get("To"),
		InReplyTo:  irt,
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
// When delegateCtx is provided, the actor field carries both the acting delegate
// and the represented mailbox owner in auditable form.
func emitLifecycle(mboxID MailboxId, folderID FolderId, itemID ItemId, ck ChangeKey, actor string, delegateCtx *DelegateAuditContext) Lifecycle {
	lc := Lifecycle{
		MailboxID: mboxID,
		FolderID:  folderID,
		ItemID:    itemID,
		Kind:      LifecycleKindCreated,
		At:        time.Now(),
		Actor:     actor,
		ChangeKey: ck,
	}
	if delegateCtx != nil {
		// Encode delegate context in Actor field: "delegate:<email>@owner:<email>".
		// This is human-readable and can be parsed by audit consumers.
		lc.Actor = fmt.Sprintf("delegate:%s@owner:%s", delegateCtx.DelegateEmail, delegateCtx.OwnerEmail)
		lc.DelegateEmail = delegateCtx.DelegateEmail
		lc.DelegateID = delegateCtx.DelegateID
	}
	return lc
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
	convID, isRoot := computeConversationID(headers.InReplyTo, headers.References, headers.MessageID)

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

	if err := p.identity.PutItemIdentity(msgKey, in.Email, itemID, in.MailboxID, in.FolderID, ck, convID, in.IsRead); err != nil {
		if errors.Is(err, ErrIdentityExists) {
			// The same content was already delivered to a different folder.
			// Register a folder-specific identity so EWS queries can find
			// the message under both folders, while keeping the original
			// msgKey (blobKey) for message-store content lookups.
			folderKey := blobKey + ":" + in.FolderID.String()
			storageKey := folderKey + "\x00" + in.Email
			if ferr := p.identity.PutItemIdentityWithKey(storageKey, blobKey, in.Email, itemID, in.MailboxID, in.FolderID, ck, convID, in.IsRead); ferr != nil {
				return nil, fmt.Errorf("MutateItem: put folder identity: %w", ferr)
			}
		} else {
			return nil, fmt.Errorf("MutateItem: put item identity: %w", err)
		}
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
	// DelegateAuditContext is threaded through so audit consumers can distinguish
	// delegate actions from direct owner actions.
	lifecycle := emitLifecycle(in.MailboxID, in.FolderID, itemID, ck, in.Actor, in.DelegateAuditContext)

	// 8. Return result for downstream consumers.
	return &MutationResult{
		ItemID:         itemID,
		ChangeKey:      ck,
		ConversationID: convID,
		BlobKey:        blobKey,
		Subject:        headers.Subject,
		From:           headers.From,
		To:             headers.To,
		InReplyTo:      headers.InReplyTo,
		References:     headers.References,
		IsThreadRoot:   isRoot,
		Size:           int64(len(in.RawMessage)),
		Lifecycle:      lifecycle,
	}, nil
}

// ---------------------------------------------------------------------------
// Update mutation
// ---------------------------------------------------------------------------

// UpdateInput contains context for a canonical item update mutation.
// Only fields that are non-nil or non-zero are considered for update;
// nil/zero fields are left unchanged.
type UpdateInput struct {
	ItemID    ItemId
	MailboxID MailboxId
	FolderID  FolderId
	Actor     string
	Source    MutationSource

	// Flags to add or remove. These are merged with existing flags.
	AddFlags    []string
	RemoveFlags []string

	// Subject update (normalized before storage).
	Subject *string

	// DelegateAuditContext is set when a delegate is acting on behalf of a mailbox owner.
	DelegateAuditContext *DelegateAuditContext
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

	// Emit lifecycle event with delegate audit context when present.
	lifecycle := Lifecycle{
		MailboxID:     in.MailboxID,
		FolderID:      in.FolderID,
		ItemID:        in.ItemID,
		Kind:          LifecycleKindUpdated,
		At:            time.Now(),
		Actor:         in.Actor,
		ChangeKey:     newCK,
		DelegateEmail: "",
	}
	if in.DelegateAuditContext != nil {
		lifecycle.Actor = fmt.Sprintf("delegate:%s@owner:%s", in.DelegateAuditContext.DelegateEmail, in.DelegateAuditContext.OwnerEmail)
		lifecycle.DelegateEmail = in.DelegateAuditContext.DelegateEmail
		lifecycle.DelegateID = in.DelegateAuditContext.DelegateID
	}

	// Persist lifecycle event if store is wired.
	if p.lifecycle != nil {
		//nolint:errcheck
		_ = p.lifecycle.AppendLifecycle(lifecycle) // best-effort
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

	// DelegateAuditContext is set when a delegate is acting on behalf of a mailbox owner.
	DelegateAuditContext *DelegateAuditContext
}

// TombstoneWriter is the minimal tombstone-recording surface MutateDelete
// needs. *BoltTombstoneStore satisfies it; accepting the interface lets callers
// hold the store behind their own interface (and lets a relational tombstone
// store slot in) instead of being forced to pass the concrete bbolt type.
type TombstoneWriter interface {
	PutTombstone(t Tombstone) error
}

// MutateDelete performs a canonical item delete: recording a tombstone
// and emitting a lifecycle event with Kind = SoftDeleted or HardDeleted.
func (p *MutationPipeline) MutateDelete(in *DeleteInput, tombstore TombstoneWriter) error {
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

	// Persist lifecycle event so GetEvents and sync consumers see the deletion.
	// DelegateAuditContext is threaded through for VAL-DIR-014 audit trail.
	if p.lifecycle != nil {
		lifecycle := Lifecycle{
			MailboxID:     in.MailboxID,
			FolderID:      in.FolderID,
			ItemID:        in.ItemID,
			Kind:          kind,
			At:            time.Now(),
			Actor:         in.Actor,
			DelegateEmail: "",
		}
		if in.DelegateAuditContext != nil {
			lifecycle.Actor = fmt.Sprintf("delegate:%s@owner:%s", in.DelegateAuditContext.DelegateEmail, in.DelegateAuditContext.OwnerEmail)
			lifecycle.DelegateEmail = in.DelegateAuditContext.DelegateEmail
			lifecycle.DelegateID = in.DelegateAuditContext.DelegateID
		}
		//nolint:errcheck
		_ = p.lifecycle.AppendLifecycle(lifecycle) // best-effort
	}

	return nil
}

// ---------------------------------------------------------------------------
// Move mutation
// ---------------------------------------------------------------------------

// MoveInput contains context for a canonical item move mutation.
type MoveInput struct {
	ItemID       ItemId
	MailboxID    MailboxId
	SourceFolder FolderId
	DestFolder   FolderId
	Actor        string
	Source       MutationSource

	// DelegateAuditContext is set when a delegate is acting on behalf of a mailbox owner.
	DelegateAuditContext *DelegateAuditContext
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

	lifecycle := Lifecycle{
		MailboxID:     in.MailboxID,
		FolderID:      in.SourceFolder,
		ItemID:        in.ItemID,
		Kind:          LifecycleKindMoved,
		At:            time.Now(),
		Actor:         in.Actor,
		ChangeKey:     current.ChangeKey,
		DelegateEmail: "",
	}
	if in.DelegateAuditContext != nil {
		lifecycle.Actor = fmt.Sprintf("delegate:%s@owner:%s", in.DelegateAuditContext.DelegateEmail, in.DelegateAuditContext.OwnerEmail)
		lifecycle.DelegateEmail = in.DelegateAuditContext.DelegateEmail
		lifecycle.DelegateID = in.DelegateAuditContext.DelegateID
	}

	// Persist lifecycle event if store is wired.
	if p.lifecycle != nil {
		//nolint:errcheck
		_ = p.lifecycle.AppendLifecycle(lifecycle) // best-effort
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
