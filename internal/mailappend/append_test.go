package mailappend_test

import (
	"errors"
	"testing"

	"github.com/umailserver/umailserver/internal/mailappend"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// --- fakes -----------------------------------------------------------------

// fakeIdentity is a PipelineIdentityStore that returns real (non-zero) mailbox
// and folder ids so MutateItem's required-field validation passes, and records
// the role it was asked to resolve. Injected errors exercise the semcore step.
type fakeIdentity struct {
	mboxErr  error
	fldErr   error
	gotRole  string
	gotEmail string
}

func (f *fakeIdentity) EnsureMailboxId(email string) (semcore.MailboxId, error) {
	if f.mboxErr != nil {
		return semcore.MailboxId{}, f.mboxErr
	}
	f.gotEmail = email
	return semcore.NewMailboxId(email)
}

func (f *fakeIdentity) EnsureFolderId(mboxKey, folderName, role string) (semcore.FolderId, error) {
	if f.fldErr != nil {
		return semcore.FolderId{}, f.fldErr
	}
	f.gotRole = role
	return semcore.NewFolderId(mboxKey + ":" + folderName)
}

func (f *fakeIdentity) GetItemIdentity(semcore.ItemId) (*semcore.StoredItemIdentity, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeIdentity) PutItemIdentity(string, string, semcore.ItemId, semcore.MailboxId, semcore.FolderId, semcore.ChangeKey, semcore.ConversationId, bool) error {
	return nil
}

func (f *fakeIdentity) PutItemIdentityWithKey(string, string, string, semcore.ItemId, semcore.MailboxId, semcore.FolderId, semcore.ChangeKey, semcore.ConversationId, bool) error {
	return nil
}

func (f *fakeIdentity) PutChangeKey(semcore.ItemId, semcore.ChangeKey, semcore.ChangeKey) error {
	return nil
}

func (f *fakeIdentity) PutConversationIdentity(semcore.ConversationId, semcore.MailboxId) error {
	return nil
}

// fakePipe is an IdentityPipeline that hands out the fakeIdentity and records
// the MutationInput it received. mutErr injects a MutateItem failure.
type fakePipe struct {
	ident   *fakeIdentity
	mutErr  error
	gotIn   *semcore.MutationInput
	mutated bool
}

func (p *fakePipe) Identity() semcore.PipelineIdentityStore { return p.ident }

func (p *fakePipe) MutateItem(in *semcore.MutationInput) (*semcore.MutationResult, error) {
	p.gotIn = in
	if p.mutErr != nil {
		return nil, p.mutErr
	}
	p.mutated = true
	itemID, err := semcore.NewItemId("item-1234567890abcdef")
	if err != nil {
		return nil, err
	}
	convID, err := semcore.NewConversationId("conv-1234567890abcdef")
	if err != nil {
		return nil, err
	}
	ck, err := semcore.NewChangeKey("ck-1234567890abcdef")
	if err != nil {
		return nil, err
	}
	return &semcore.MutationResult{ItemID: itemID, ConversationID: convID, ChangeKey: ck}, nil
}

// fakeBlob records the stored bytes and returns a fixed key, or an injected error.
type fakeBlob struct {
	err    error
	stored []byte
	user   string
}

func (b *fakeBlob) StoreMessage(user string, data []byte) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	b.user = user
	b.stored = data
	return "blobkey-abcdef", nil
}

// fakeIndex records the stored metadata and lets each step's error be injected.
type fakeIndex struct {
	uidErr     error
	storeErr   error
	threadErr  error
	nextUID    uint32
	storedMeta *storage.MessageMetadata
	storedMbox string
}

func (i *fakeIndex) GetNextUID(string, string) (uint32, error) {
	if i.uidErr != nil {
		return 0, i.uidErr
	}
	if i.nextUID == 0 {
		i.nextUID = 7
	}
	return i.nextUID, nil
}

func (i *fakeIndex) GetOrCreateThreadID(_, _, _, _, _ string, _ []string) (string, error) {
	if i.threadErr != nil {
		return "", i.threadErr
	}
	return "thread-1", nil
}

func (i *fakeIndex) StoreMessageMetadata(_, mailbox string, _ uint32, meta *storage.MessageMetadata) error {
	if i.storeErr != nil {
		return i.storeErr
	}
	i.storedMbox = mailbox
	i.storedMeta = meta
	return nil
}

func roleResolver(name string) string {
	if name == "INBOX" {
		return "inbox"
	}
	return ""
}

const sampleMsg = "Subject: Hello\r\n" +
	"From: alice@local.test\r\n" +
	"To: bob@local.test\r\n" +
	"Message-ID: <m1@local.test>\r\n" +
	"\r\n" +
	"body\r\n"

func newAppender(p *fakePipe, b *fakeBlob, idx *fakeIndex) *mailappend.Appender {
	return mailappend.NewAppender(p, b, idx, roleResolver)
}

// --- tests -----------------------------------------------------------------

// TestAppend_HappyPath verifies the full canonical write: the blob is stored, a
// canonical mutation is produced, and the index row carries the parsed headers
// and caller flags. This is the contract every surface depends on for
// cross-protocol visibility, so the metadata fields are asserted, not just the
// no-error outcome.
func TestAppend_HappyPath(t *testing.T) {
	p := &fakePipe{ident: &fakeIdentity{}}
	b := &fakeBlob{}
	idx := &fakeIndex{}
	var notified bool
	var indexedItemID string
	a := newAppender(p, b, idx)
	a.SetNotifier(func(string, string, uint32) { notified = true })
	a.SetIndexer(func(_ string, _ uint32, itemID, _ string) { indexedItemID = itemID })

	res, err := a.Append(mailappend.Input{
		Email:      "alice@local.test",
		Folder:     "INBOX",
		Raw:        []byte(sampleMsg),
		Actor:      "alice@local.test",
		Source:     semcore.MutationSourceIMAP,
		ExtraFlags: []string{"\\Recent"},
	})
	if err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if res.SemcoreErr != nil {
		t.Fatalf("unexpected SemcoreErr: %v", res.SemcoreErr)
	}
	if res.MessageID != "blobkey-abcdef" {
		t.Errorf("MessageID = %q, want blobkey-abcdef", res.MessageID)
	}
	if res.UID != 7 {
		t.Errorf("UID = %d, want 7", res.UID)
	}
	if res.Mutation == nil {
		t.Fatal("Mutation is nil on success")
	}
	// The semcore step must receive the canonical folder role, not a raw name,
	// so the MAPI-created item lands under the same FolderId EWS/IMAP resolve.
	if p.ident.gotRole != "inbox" {
		t.Errorf("EnsureFolderId role = %q, want inbox", p.ident.gotRole)
	}
	// Index row must mirror exactly what the surfaces read back.
	if idx.storedMeta == nil {
		t.Fatal("no metadata stored")
	}
	if idx.storedMeta.MessageID != "blobkey-abcdef" {
		t.Errorf("meta MessageID = %q, want blobkey-abcdef", idx.storedMeta.MessageID)
	}
	if idx.storedMeta.Subject != "Hello" {
		t.Errorf("meta Subject = %q, want Hello", idx.storedMeta.Subject)
	}
	if idx.storedMeta.ThreadID != "thread-1" {
		t.Errorf("meta ThreadID = %q, want thread-1", idx.storedMeta.ThreadID)
	}
	if len(idx.storedMeta.Flags) != 1 || idx.storedMeta.Flags[0] != "\\Recent" {
		t.Errorf("meta Flags = %v, want [\\Recent]", idx.storedMeta.Flags)
	}
	if !notified {
		t.Error("notifier not called")
	}
	if indexedItemID != res.Mutation.ItemID.String() {
		t.Errorf("search indexer got itemID %q, want %q", indexedItemID, res.Mutation.ItemID.String())
	}
}

// TestAppend_BlobFatal verifies the blob step is fatal: when StoreMessage fails,
// Append returns the error and never touches the semantic or index steps. This
// is why a delivery caller can release reserved quota on an Append error.
func TestAppend_BlobFatal(t *testing.T) {
	p := &fakePipe{ident: &fakeIdentity{}}
	b := &fakeBlob{err: errors.New("disk full")}
	idx := &fakeIndex{}
	a := newAppender(p, b, idx)

	res, err := a.Append(mailappend.Input{Email: "alice@local.test", Raw: []byte(sampleMsg)})
	if err == nil {
		t.Fatal("expected fatal error from blob failure, got nil")
	}
	if res != nil {
		t.Errorf("expected nil result on fatal blob error, got %+v", res)
	}
	if p.gotIn != nil {
		t.Error("semcore mutation ran despite fatal blob failure")
	}
	if idx.storedMeta != nil {
		t.Error("index write ran despite fatal blob failure")
	}
}

// TestAppend_SemcoreReported verifies the semantic step is reported, not fatal:
// when MutateItem fails, Append still succeeds, reports the error in SemcoreErr
// with a nil Mutation, and STILL writes the index row — preserving SMTP
// delivery's best-effort-semcore behavior so the message stays IMAP-visible.
func TestAppend_SemcoreReported(t *testing.T) {
	p := &fakePipe{ident: &fakeIdentity{}, mutErr: errors.New("identity store down")}
	b := &fakeBlob{}
	idx := &fakeIndex{}
	var searchCalled bool
	a := newAppender(p, b, idx)
	a.SetIndexer(func(string, uint32, string, string) { searchCalled = true })

	res, err := a.Append(mailappend.Input{Email: "alice@local.test", Folder: "INBOX", Raw: []byte(sampleMsg)})
	if err != nil {
		t.Fatalf("semcore failure must not be fatal, got error: %v", err)
	}
	if res.SemcoreErr == nil {
		t.Fatal("SemcoreErr not reported")
	}
	if res.Mutation != nil {
		t.Error("Mutation should be nil when semcore failed")
	}
	if idx.storedMeta == nil {
		t.Error("index row must still be written when semcore is best-effort")
	}
	if res.UID != 7 {
		t.Errorf("UID = %d, want 7 (index still ran)", res.UID)
	}
	// Search indexing requires canonical identity, so it must be skipped here.
	if searchCalled {
		t.Error("search indexer called despite nil Mutation")
	}
}

// TestAppend_IndexBestEffort verifies the index step is best-effort: when
// StoreMessageMetadata fails, Append still succeeds with the blob and identity
// intact, leaving UID==0 to signal the index entry was not written.
func TestAppend_IndexBestEffort(t *testing.T) {
	p := &fakePipe{ident: &fakeIdentity{}}
	b := &fakeBlob{}
	idx := &fakeIndex{storeErr: errors.New("index locked")}
	a := newAppender(p, b, idx)

	res, err := a.Append(mailappend.Input{Email: "alice@local.test", Folder: "INBOX", Raw: []byte(sampleMsg)})
	if err != nil {
		t.Fatalf("index failure must not be fatal, got error: %v", err)
	}
	if res.UID != 0 {
		t.Errorf("UID = %d, want 0 on index failure", res.UID)
	}
	if res.Mutation == nil {
		t.Error("identity must still be assigned when index is best-effort")
	}
	if len(b.stored) == 0 {
		t.Error("blob must still be stored when index is best-effort")
	}
}

// TestAppend_FolderDefaultsInbox verifies an empty folder defaults to INBOX so a
// caller that omits the folder still lands in the canonical inbox.
func TestAppend_FolderDefaultsInbox(t *testing.T) {
	p := &fakePipe{ident: &fakeIdentity{}}
	b := &fakeBlob{}
	idx := &fakeIndex{}
	a := newAppender(p, b, idx)

	if _, err := a.Append(mailappend.Input{Email: "alice@local.test", Raw: []byte(sampleMsg)}); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	if idx.storedMbox != "INBOX" {
		t.Errorf("stored mailbox = %q, want INBOX (empty folder default)", idx.storedMbox)
	}
	if p.ident.gotRole != "inbox" {
		t.Errorf("role = %q, want inbox", p.ident.gotRole)
	}
}
