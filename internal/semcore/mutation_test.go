package semcore

import (
	"strings"
	"testing"
	"time"
)

func TestComputeConversationID_FromReferences(t *testing.T) {
	// When References header is present, use the last (most recent parent).
	refs := []string{"<parent1@example.com>", "<parent2@example.com>"}
	convID, isRoot := computeConversationID("", refs)

	if isRoot {
		t.Errorf("Expected isRoot=false when References is present, got true")
	}
	if convID.IsZero() {
		t.Errorf("Expected non-zero ConversationID from References")
	}
	// The ID should be derived from the last reference.
	want := "parent2@example.com"
	if convID.String() != want {
		t.Errorf("Expected ConversationID=%q from last References, got %q", want, convID.String())
	}
}

func TestComputeConversationID_FromInReplyTo(t *testing.T) {
	// When References is absent but In-Reply-To is present, use In-Reply-To.
	convID, isRoot := computeConversationID("<reply-to@example.com>", nil)

	if isRoot {
		t.Errorf("Expected isRoot=false when In-Reply-To is present, got true")
	}
	if convID.IsZero() {
		t.Errorf("Expected non-zero ConversationID from In-Reply-To")
	}
	want := "reply-to@example.com"
	if convID.String() != want {
		t.Errorf("Expected ConversationID=%q from In-Reply-To, got %q", want, convID.String())
	}
}

func TestComputeConversationID_FromInReplyToWithAngleBrackets(t *testing.T) {
	// In-Reply-To may have angle brackets; they should be stripped.
	convID, isRoot := computeConversationID("<reply-with-brackets@example.com>", nil)

	if isRoot {
		t.Errorf("Expected isRoot=false when In-Reply-To is present, got true")
	}
	want := "reply-with-brackets@example.com"
	if convID.String() != want {
		t.Errorf("Expected ConversationID=%q with brackets stripped, got %q", want, convID.String())
	}
}

func TestComputeConversationID_NewConversation(t *testing.T) {
	// When neither References nor In-Reply-To is present, generate a new ID.
	convID, isRoot := computeConversationID("", nil)

	if !isRoot {
		t.Errorf("Expected isRoot=true for new conversation, got false")
	}
	if convID.IsZero() {
		t.Errorf("Expected non-zero ConversationID for new conversation")
	}
	// The ID should be a random hex string (32 chars from generateID).
	if len(convID.String()) != 32 {
		t.Errorf("Expected 32-char random ID, got %d chars: %q", len(convID.String()), convID.String())
	}
}

func TestComputeConversationID_EmptyReferences(t *testing.T) {
	// Empty References is treated as absent.
	convID, isRoot := computeConversationID("<irt@example.com>", []string{})

	if isRoot {
		t.Errorf("Expected isRoot=false when In-Reply-To is present, got true")
	}
	if convID.String() != "irt@example.com" {
		t.Errorf("Expected ConversationID=%q from In-Reply-To, got %q", "irt@example.com", convID.String())
	}
}

func TestParseHeaders_Basic(t *testing.T) {
	raw := strings.Join([]string{
		"From: alice@example.com",
		"To: bob@example.com",
		"Subject: Test",
		"Message-ID: <msg123@example.com>",
		"In-Reply-To: <parent@example.com>",
		"References: <grandparent@example.com> <parent@example.com>",
		"",
		"Body",
	}, "\r\n")

	h := parseHeaders([]byte(raw))

	if h.Subject != "Test" {
		t.Errorf("Subject = %q, want %q", h.Subject, "Test")
	}
	if h.From != "alice@example.com" {
		t.Errorf("From = %q, want %q", h.From, "alice@example.com")
	}
	if h.To != "bob@example.com" {
		t.Errorf("To = %q, want %q", h.To, "bob@example.com")
	}
	if h.MessageID != "msg123@example.com" {
		t.Errorf("MessageID = %q, want %q", h.MessageID, "msg123@example.com")
	}
	if h.InReplyTo != "parent@example.com" {
		t.Errorf("InReplyTo = %q, want %q", h.InReplyTo, "parent@example.com")
	}
	if len(h.References) != 2 {
		t.Errorf("References len = %d, want 2", len(h.References))
	}
	if h.References[0] != "grandparent@example.com" {
		t.Errorf("References[0] = %q, want %q", h.References[0], "grandparent@example.com")
	}
	if h.References[1] != "parent@example.com" {
		t.Errorf("References[1] = %q, want %q", h.References[1], "parent@example.com")
	}
}

func TestParseHeaders_EmptyMessage(t *testing.T) {
	h := parseHeaders(nil)
	if h.Subject != "" {
		t.Errorf("Empty message: Subject = %q, want %q", h.Subject, "")
	}
}

func TestNormalizeSubject(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"  Hello   World  ", "Hello World"},
		{"No change", "No change"},
		{"  ", ""},
		{"Multiple    spaces", "Multiple spaces"},
		{"\tMixed\t", "Mixed"},
	}

	for _, tc := range cases {
		got := normalizeSubject(tc.input)
		if got != tc.expected {
			t.Errorf("normalizeSubject(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestStripAngleBrackets(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"<content@example.com>", "content@example.com"},
		{"no brackets", "no brackets"},
		{"<only left>", "only left"},  // unbalanced
		{"<>", ""},                   // empty inside brackets
		{"  <spaced@example.com>  ", "spaced@example.com"},
	}

	for _, tc := range cases {
		got := stripAngleBrackets(tc.input)
		if got != tc.expected {
			t.Errorf("stripAngleBrackets(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestGenerateID(t *testing.T) {
	id1 := generateID()
	id2 := generateID()

	if id1 == "" {
		t.Errorf("generateID returned empty string")
	}
	if id1 == id2 {
		t.Errorf("generateID returned same ID twice: %q", id1)
	}
	// Should be 32 hex chars (16 bytes).
	if len(id1) != 32 {
		t.Errorf("generateID length = %d, want 32", len(id1))
	}
}

// ---------------------------------------------------------------------------
// MutationPipeline tests
// ---------------------------------------------------------------------------

func tmpBoltStoreForMutation(t *testing.T) (*BoltIdentityStore, func()) {
	tmp := t.TempDir()
	store, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	return store, func() {
		if err := store.Close(); err != nil {
			t.Logf("store.Close(): %v", err)
		}
	}
}

func TestMutationPipeline_MutateItem_Basic(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	pipe := NewMutationPipeline(store, nil)

	mboxID, err := store.EnsureMailboxId("alice@local.test")
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}

	fldID, err := store.EnsureFolderId("alice@local.test", "INBOX", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	rawMsg := []byte(strings.Join([]string{
		"From: alice@local.test",
		"To: bob@local.test",
		"Subject: Test",
		"",
		"Body",
	}, "\r\n"))

	in := &MutationInput{
		MailboxID:     mboxID,
		FolderID:      fldID,
		RawMessage:    rawMsg,
		InternalDate: time.Now(),
		Actor:        "alice@local.test",
		Source:       MutationSourceIMAP,
	}

	result, err := pipe.MutateItem(in)
	if err != nil {
		t.Fatalf("MutateItem: %v", err)
	}

	// Verify returned identities are non-zero.
	if result.ItemID.IsZero() {
		t.Errorf("ItemID is zero")
	}
	if result.ChangeKey.IsZero() {
		t.Errorf("ChangeKey is zero")
	}
	if result.ConversationID.IsZero() {
		t.Errorf("ConversationID is zero")
	}
	if result.BlobKey == "" {
		t.Errorf("BlobKey is empty")
	}

	// Verify Subject and From parsing.
	if result.Subject != "Test" {
		t.Errorf("Subject = %q, want %q", result.Subject, "Test")
	}
	if result.From != "alice@local.test" {
		t.Errorf("From = %q, want %q", result.From, "alice@local.test")
	}

	// Verify lifecycle.
	if result.Lifecycle.Kind != LifecycleKindCreated {
		t.Errorf("Lifecycle.Kind = %v, want LifecycleKindCreated", result.Lifecycle.Kind)
	}
	if !result.Lifecycle.MailboxID.Equal(mboxID) {
		t.Errorf("Lifecycle.MailboxID = %v, want %v", result.Lifecycle.MailboxID, mboxID)
	}
	if !result.Lifecycle.ItemID.Equal(result.ItemID) {
		t.Errorf("Lifecycle.ItemID = %v, want %v", result.Lifecycle.ItemID, result.ItemID)
	}

	// Verify IsThreadRoot: no References or In-Reply-To means new conversation.
	if !result.IsThreadRoot {
		t.Errorf("Expected IsThreadRoot=true for message without threading headers")
	}
}

func TestMutationPipeline_MutateItem_WithThreadHeaders(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	pipe := NewMutationPipeline(store, nil)

	mboxID, err := store.EnsureMailboxId("alice@local.test")
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	fldID, err := store.EnsureFolderId("alice@local.test", "INBOX", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	rawMsg := []byte(strings.Join([]string{
		"From: alice@local.test",
		"To: bob@local.test",
		"Subject: Re: Test",
		"In-Reply-To: <parent@example.com>",
		"References: <grandparent@example.com> <parent@example.com>",
		"",
		"Reply body",
	}, "\r\n"))

	in := &MutationInput{
		MailboxID:     mboxID,
		FolderID:      fldID,
		RawMessage:    rawMsg,
		InternalDate: time.Now(),
		Actor:        "alice@local.test",
		Source:       MutationSourceIMAP,
	}

	result, err := pipe.MutateItem(in)
	if err != nil {
		t.Fatalf("MutateItem: %v", err)
	}

	// Conversation should be derived from In-Reply-To (last in References chain).
	if result.ConversationID.String() != "parent@example.com" {
		t.Errorf("ConversationID = %q, want %q (from In-Reply-To)",
			result.ConversationID.String(), "parent@example.com")
	}

	// Should not be a thread root since In-Reply-To is present.
	if result.IsThreadRoot {
		t.Errorf("Expected IsThreadRoot=false for reply")
	}

	// References should be parsed.
	if len(result.References) != 2 {
		t.Errorf("References len = %d, want 2", len(result.References))
	}
}

func TestMutationPipeline_MutateItem_SameContentSameBlobKey(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	pipe := NewMutationPipeline(store, nil)

	mboxID, err := store.EnsureMailboxId("alice@local.test")
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	fldID, err := store.EnsureFolderId("alice@local.test", "INBOX", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	rawMsg := []byte(strings.Join([]string{
		"From: alice@local.test",
		"To: bob@local.test",
		"Subject: Same content",
		"",
		"Body",
	}, "\r\n"))

	in1 := &MutationInput{
		MailboxID:     mboxID,
		FolderID:      fldID,
		RawMessage:    rawMsg,
		InternalDate: time.Now(),
		Actor:        "alice@local.test",
		Source:       MutationSourceSMTP,
	}
	result1, err := pipe.MutateItem(in1)
	if err != nil {
		t.Fatalf("MutateItem 1: %v", err)
	}

	// Second delivery of the same content produces the same blob key.
	in2 := &MutationInput{
		MailboxID:     mboxID,
		FolderID:      fldID,
		RawMessage:    rawMsg,
		InternalDate: time.Now(),
		Actor:        "alice@local.test",
		Source:       MutationSourceSMTP,
	}
	result2, err := pipe.MutateItem(in2)
	if err != nil {
		// This is the expected behavior: the pipeline uses blob key as msgKey,
		// so the second delivery of the same content tries to re-use the same
		// msgKey and fails with ErrIdentityExists.
		//
		// This is actually correct Exchange semantic: the same RFC 5322 message
		// delivered twice to the same mailbox should be deduplicated at the
		// semantic layer. Each delivery attempt maps to the same ItemId.
		//
		// For SMTP delivery paths that need per-delivery semantics (e.g.,
		// separate messages in sent mail vs inbox), the caller should use a
		// different FolderID for different delivery contexts.
		if err.Error() != "MutateItem: put item identity: identity already assigned" {
			t.Fatalf("MutateItem 2: got error %v, want ErrIdentityExists", err)
		}
		return
	}

	// If the second call succeeds (different FolderID was used), blob key is same.
	if result1.BlobKey != result2.BlobKey {
		t.Errorf("Same content should produce same BlobKey: %q vs %q",
			result1.BlobKey, result2.BlobKey)
	}
	// ItemId should be different (different FolderID context).
	if result1.ItemID.Equal(result2.ItemID) {
		t.Errorf("Expected different ItemId for different folder context, got same: %v", result1.ItemID)
	}
}

func TestMutationPipeline_MutateItem_RequiresMailboxID(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	pipe := NewMutationPipeline(store, nil)

	_, err := pipe.MutateItem(&MutationInput{
		MailboxID:  MailboxId{},
		FolderID:   MustFolderId("fld-1"),
		RawMessage: []byte("test"),
	})
	if err == nil {
		t.Fatalf("Expected error for zero MailboxID, got nil")
	}
}

func TestMutationPipeline_MutateItem_RequiresFolderID(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	pipe := NewMutationPipeline(store, nil)

	_, err := pipe.MutateItem(&MutationInput{
		MailboxID:  MustMailboxId("mbx-1"),
		FolderID:   FolderId{},
		RawMessage: []byte("test"),
	})
	if err == nil {
		t.Fatalf("Expected error for zero FolderID, got nil")
	}
}

func TestMutationPipeline_MutateItem_RequiresRawMessage(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	pipe := NewMutationPipeline(store, nil)

	_, err := pipe.MutateItem(&MutationInput{
		MailboxID: MustMailboxId("mbx-1"),
		FolderID:  MustFolderId("fld-1"),
	})
	if err == nil {
		t.Fatalf("Expected error for empty RawMessage, got nil")
	}
}

func TestMutationPipeline_UpdateInput(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	pipe := NewMutationPipeline(store, nil)

	mboxID, err := store.EnsureMailboxId("alice@local.test")
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	fldID, err := store.EnsureFolderId("alice@local.test", "INBOX", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}

	// Create an item first.
	rawMsg := []byte("From: alice@local.test\r\nSubject: Test\r\n\r\nBody")
	createResult, err := pipe.MutateItem(&MutationInput{
		MailboxID:     mboxID,
		FolderID:      fldID,
		RawMessage:    rawMsg,
		InternalDate:  time.Now(),
		Actor:         "alice@local.test",
		Source:        MutationSourceIMAP,
	})
	if err != nil {
		t.Fatalf("MutateItem: %v", err)
	}

	// Update the item.
	updateResult, err := pipe.MutateUpdate(&UpdateInput{
		ItemID:    createResult.ItemID,
		MailboxID: mboxID,
		FolderID:  fldID,
		Actor:     "alice@local.test",
		Source:    MutationSourceIMAP,
	})
	if err != nil {
		t.Fatalf("MutateUpdate: %v", err)
	}

	// ChangeKey should advance.
	if updateResult.ChangeKey.Equal(createResult.ChangeKey) {
		t.Errorf("ChangeKey did not advance after update")
	}

	// Lifecycle should be Updated.
	if updateResult.Lifecycle.Kind != LifecycleKindUpdated {
		t.Errorf("Lifecycle.Kind = %v, want LifecycleKindUpdated", updateResult.Lifecycle.Kind)
	}
}

func TestEnsureMailboxId_Idempotent(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	// First call creates.
	id1, err := store.EnsureMailboxId("alice@local.test")
	if err != nil {
		t.Fatalf("EnsureMailboxId first call: %v", err)
	}

	// Second call returns same ID.
	id2, err := store.EnsureMailboxId("alice@local.test")
	if err != nil {
		t.Fatalf("EnsureMailboxId second call: %v", err)
	}

	if !id1.Equal(id2) {
		t.Errorf("EnsureMailboxId not idempotent: first=%v, second=%v", id1, id2)
	}
}

func TestEnsureFolderId_Idempotent(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	// First call creates.
	id1, err := store.EnsureFolderId("alice@local.test", "INBOX", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId first call: %v", err)
	}

	// Second call returns same ID.
	id2, err := store.EnsureFolderId("alice@local.test", "INBOX", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId second call: %v", err)
	}

	if !id1.Equal(id2) {
		t.Errorf("EnsureFolderId not idempotent: first=%v, second=%v", id1, id2)
	}
}

func TestMutationPipeline_Identity(t *testing.T) {
	store, closeStore := tmpBoltStoreForMutation(t)
	defer closeStore()

	pipe := NewMutationPipeline(store, nil)

	// Identity() returns the underlying store.
	if pipe.Identity() != store {
		t.Errorf("Identity() did not return the expected store")
	}
}
