// Package mailappend provides the single canonical message-append core shared by
// every mailbox-writing surface: SMTP local delivery, EWS CreateItem, and the
// MAPI/HTTP (emsmdb) write ROPs. Each of those surfaces must perform the same
// tail-end write — assign canonical semantic identity, store the raw RFC 5322
// blob, record IMAP-index metadata (with thread id), and signal real-time and
// search consumers — so that a message authored on any one surface is visible
// identically on all of them (cross-protocol integrity).
//
// Surface-specific concerns stay at the call site, not here: quota reservation,
// forwarding, and webhooks (SMTP delivery); the SOAP response and lifecycle
// persistence (EWS); the ROP response (emsmdb); and the resolution of a
// surface-native folder handle to an IMAP-canonical folder name.
//
// # Error contract
//
// The three storage steps keep the error policy every current call site already
// uses, so routing a call site through Append preserves its behavior exactly:
//
//   - Blob store (StoreMessage) is FATAL: a failure returns an error and nothing
//     else is attempted.
//   - Semantic identity (EnsureMailboxId/EnsureFolderId/MutateItem) is REPORTED:
//     a failure is recorded in Result.SemcoreErr and Append continues to the
//     index step. The caller decides whether that is fatal — SMTP delivery
//     ignores it (best-effort), EWS/emsmdb treat a non-nil SemcoreErr as fatal.
//   - IMAP index (GetNextUID/StoreMessageMetadata) is BEST-EFFORT: a failure is
//     logged and Append returns without error (the blob and identity remain the
//     canonical record).
package mailappend

import (
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// IdentityPipeline is the canonical identity-and-mutation surface Append drives.
// *semcore.MutationPipeline satisfies it.
type IdentityPipeline interface {
	Identity() semcore.PipelineIdentityStore
	MutateItem(*semcore.MutationInput) (*semcore.MutationResult, error)
}

// BlobStore stores raw RFC 5322 MIME and returns its content-addressable key.
// *storage.MessageStore satisfies it.
type BlobStore interface {
	StoreMessage(user string, data []byte) (string, error)
}

// IndexStore is the IMAP mailstore metadata index (the per-mailbox UID index
// IMAP/POP3/JMAP/webmail and the emsmdb read path serve from). *storage.Database
// satisfies it.
type IndexStore interface {
	GetNextUID(user, mailbox string) (uint32, error)
	GetOrCreateThreadID(user, mailbox, subject, ownMessageID, inReplyTo string, references []string) (string, error)
	StoreMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error
}

// RoleResolver maps an IMAP-canonical folder name to its distinguished semantic
// role (e.g. "INBOX" -> "inbox"), or "" for a non-distinguished user folder. It
// MUST agree with the resolver every other surface uses so the same physical
// folder resolves to the same semcore FolderId across surfaces.
type RoleResolver func(folderName string) string

// Notifier signals a newly stored message to real-time consumers (IMAP IDLE
// untagged EXISTS, webmail SSE). It is optional.
type Notifier func(email, folder string, uid uint32)

// Indexer queues a search-index job for a stored message. It is optional and is
// invoked only when canonical identity assignment succeeded.
type Indexer func(email string, uid uint32, itemID, conversationID string)

// The canonical process stores satisfy the append core's consumer interfaces.
var (
	_ IdentityPipeline = (*semcore.MutationPipeline)(nil)
	_ BlobStore        = (*storage.MessageStore)(nil)
	_ IndexStore       = (*storage.Database)(nil)
)

// Appender performs the canonical message append. Construct it once with the
// process's canonical stores in the composition root and share the pointer
// across every writing surface.
type Appender struct {
	pipe   IdentityPipeline
	blob   BlobStore
	index  IndexStore
	role   RoleResolver
	logger *slog.Logger
	notify Notifier
	search Indexer
}

// NewAppender builds an Appender from the canonical stores. pipe, blob, index,
// and role are required. Attach the optional logger, notifier, and search
// indexer with the setters.
func NewAppender(pipe IdentityPipeline, blob BlobStore, index IndexStore, role RoleResolver) *Appender {
	return &Appender{pipe: pipe, blob: blob, index: index, role: role}
}

// SetLogger attaches the logger used for best-effort index-step failures.
func (a *Appender) SetLogger(l *slog.Logger) { a.logger = l }

// SetNotifier attaches the real-time new-message notifier.
func (a *Appender) SetNotifier(n Notifier) { a.notify = n }

// SetIndexer attaches the search-index job queue callback.
func (a *Appender) SetIndexer(i Indexer) { a.search = i }

// Input carries one canonical append. Folder is the IMAP-canonical mailbox name
// the surface has already resolved its native folder handle to; the caller owns
// that resolution because it differs per surface.
type Input struct {
	// Email is the mailbox owner's address (the user key for every store).
	Email string
	// MailboxID, when non-zero, is the canonical mailbox identity the caller has
	// already resolved (EWS holds it). When zero, the core resolves it from Email.
	MailboxID semcore.MailboxId
	// Folder is the IMAP-canonical mailbox name (e.g. "INBOX", "Sent"), used for
	// the IMAP index entry and — when FolderID is zero — to resolve the semantic
	// folder identity through the RoleResolver.
	Folder string
	// FolderID, when non-zero, is the canonical folder identity the caller has
	// already resolved (EWS holds it). When zero, the core resolves it from Folder
	// via the RoleResolver, the path SMTP delivery and the MAPI write ROPs take.
	FolderID semcore.FolderId
	// Raw is the complete RFC 5322 message.
	Raw []byte
	// InternalDate is the server receipt/authoring time; defaults to now.
	InternalDate time.Time
	// Actor is the identity that triggered the write (envelope sender for SMTP,
	// the mailbox owner for EWS/MAPI).
	Actor string
	// Source is the protocol path that initiated the write.
	Source semcore.MutationSource
	// IsRead marks the canonical item read at creation (carried into semcore).
	IsRead bool
	// ExtraFlags are the IMAP flags stored on the index entry (e.g. "\\Recent"
	// for delivery, "\\Draft"). The append core does not add flags of its own.
	ExtraFlags []string
	// DelegateAuditContext attributes the write to a delegate when set.
	DelegateAuditContext *semcore.DelegateAuditContext
}

// Result reports the outcome of a canonical append. A nil error from Append
// means the blob was stored; SemcoreErr and UID==0 distinguish the best-effort
// steps' outcomes for the caller's policy.
type Result struct {
	// UID is the assigned IMAP index UID, or 0 when the index step was skipped
	// (its failure is best-effort).
	UID uint32
	// MessageID is the content-addressable blob key (== SHA256 of Raw).
	MessageID string
	// Mutation is the canonical identity result, or nil when the semantic step
	// failed (see SemcoreErr).
	Mutation *semcore.MutationResult
	// SemcoreErr is the semantic-identity step error, reported for the caller to
	// classify as fatal (EWS/MAPI) or ignorable (SMTP delivery).
	SemcoreErr error
}

// Append performs the canonical message append for in. It returns an error only
// when the fatal blob step fails; the semantic and index steps follow the error
// contract documented on the package. See that contract before changing the
// ordering or fatality of any step.
func (a *Appender) Append(in Input) (*Result, error) {
	if in.Folder == "" {
		in.Folder = "INBOX"
	}
	if in.InternalDate.IsZero() {
		in.InternalDate = time.Now()
	}

	// Step 1 (FATAL): store the raw MIME blob. The blob key is the message-store
	// id every surface reads the body back by, and equals semcore's content hash.
	blobKey, err := a.blob.StoreMessage(in.Email, in.Raw)
	if err != nil {
		return nil, err
	}
	res := &Result{MessageID: blobKey}

	// Step 2 (REPORTED): assign canonical semantic identity. Any failure is
	// recorded in res.SemcoreErr; Append still records the index entry so the
	// caller's best-effort path (SMTP) keeps the message visible to IMAP.
	res.Mutation, res.SemcoreErr = a.mutate(in)

	// Step 3 (BEST-EFFORT): record the IMAP-index metadata entry and signal
	// real-time/search consumers. A failure leaves UID==0 and is logged.
	a.index1(in, res)
	return res, nil
}

// mutate runs the semantic-identity step: resolve the mailbox and folder
// identities, then perform the canonical mutation. It returns the mutation
// result or the first error encountered.
func (a *Appender) mutate(in Input) (*semcore.MutationResult, error) {
	ident := a.pipe.Identity()
	mboxID := in.MailboxID
	if mboxID.IsZero() {
		var err error
		if mboxID, err = ident.EnsureMailboxId(in.Email); err != nil {
			return nil, err
		}
	}
	fldID := in.FolderID
	if fldID.IsZero() {
		var err error
		if fldID, err = ident.EnsureFolderId(in.Email, in.Folder, a.role(in.Folder)); err != nil {
			return nil, err
		}
	}
	return a.pipe.MutateItem(&semcore.MutationInput{
		MailboxID:            mboxID,
		FolderID:             fldID,
		RawMessage:           in.Raw,
		InternalDate:         in.InternalDate,
		Actor:                in.Actor,
		Email:                in.Email,
		Source:               in.Source,
		IsRead:               in.IsRead,
		DelegateAuditContext: in.DelegateAuditContext,
	})
}

// index1 runs the best-effort IMAP-index step: allocate a UID, build the
// metadata row (subject/date/thread headers parsed from the raw message), store
// it, and fire the new-message and search signals. On res it sets UID on success
// and invokes the optional notifier/indexer; failures are logged and swallowed.
func (a *Appender) index1(in Input, res *Result) {
	if a.index == nil {
		return // no IMAP index store wired; the best-effort index step is a no-op
	}
	uid, err := a.index.GetNextUID(in.Email, in.Folder)
	if err != nil {
		a.logError("mailappend: GetNextUID failed", in, err)
		return
	}
	subject, fromAddr, toAddr, dateStr := ParseBasicHeaders(in.Raw)
	hdrMsgID, hdrInReplyTo, hdrRefs := ParseThreadingHeaders(in.Raw)
	threadID, terr := a.index.GetOrCreateThreadID(in.Email, in.Folder, subject, hdrMsgID, hdrInReplyTo, hdrRefs)
	if terr != nil {
		threadID = ""
	}
	meta := &storage.MessageMetadata{
		MessageID:    res.MessageID,
		UID:          uid,
		Flags:        in.ExtraFlags,
		InternalDate: in.InternalDate,
		Size:         int64(len(in.Raw)),
		Subject:      subject,
		Date:         dateStr,
		From:         fromAddr,
		To:           toAddr,
		ThreadID:     threadID,
		InReplyTo:    hdrInReplyTo,
		References:   hdrRefs,
	}
	if err := a.index.StoreMessageMetadata(in.Email, in.Folder, uid, meta); err != nil {
		a.logError("mailappend: StoreMessageMetadata failed", in, err)
		return
	}
	res.UID = uid
	if a.notify != nil {
		a.notify(in.Email, in.Folder, uid)
	}
	if a.search != nil && res.Mutation != nil {
		a.search(in.Email, uid, res.Mutation.ItemID.String(), res.Mutation.ConversationID.String())
	}
}

// logError logs a best-effort index-step failure when a logger is attached.
func (a *Appender) logError(msg string, in Input, err error) {
	if a.logger != nil {
		a.logger.Error(msg, "email", in.Email, "folder", in.Folder, "error", err)
	}
}

// ParseBasicHeaders returns the raw Subject, From, To, and Date header values of
// a raw RFC 5322 message (undecoded, as stored on the index row).
func ParseBasicHeaders(data []byte) (subject, from, to, date string) {
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return "", "", "", ""
	}
	return msg.Header.Get("Subject"), msg.Header.Get("From"), msg.Header.Get("To"), msg.Header.Get("Date")
}

// ParseThreadingHeaders extracts the RFC 2822 threading headers (Message-ID,
// In-Reply-To, References) from a raw message, stripped of angle brackets.
func ParseThreadingHeaders(data []byte) (messageID, inReplyTo string, references []string) {
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return "", "", nil
	}
	trim := func(s string) string { return strings.Trim(strings.TrimSpace(s), "<>") }
	messageID = trim(msg.Header.Get("Message-ID"))
	inReplyTo = trim(msg.Header.Get("In-Reply-To"))
	for _, ref := range strings.Fields(msg.Header.Get("References")) {
		if r := trim(ref); r != "" {
			references = append(references, r)
		}
	}
	return messageID, inReplyTo, references
}
