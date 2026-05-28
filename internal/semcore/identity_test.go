package semcore

import "testing"

// ---------------------------------------------------------------------------
// MailboxId tests
// ---------------------------------------------------------------------------

func TestMailboxId_New(t *testing.T) {
	id, err := NewMailboxId("mbx-abc123")
	if err != nil {
		t.Fatalf("NewMailboxId returned error: %v", err)
	}
	if id.raw != "mbx-abc123" {
		t.Errorf("raw = %q, want %q", id.raw, "mbx-abc123")
	}
}

func TestMailboxId_NewEmpty(t *testing.T) {
	_, err := NewMailboxId("")
	if err == nil {
		t.Error("NewMailboxId('') did not return error")
	}
}

func TestMailboxId_IsZero(t *testing.T) {
	var zero MailboxId
	if !zero.IsZero() {
		t.Error("zero MailboxId should be IsZero")
	}
	id := MustMailboxId("x")
	if id.IsZero() {
		t.Error("non-zero MailboxId should not be IsZero")
	}
}

func TestMailboxId_Equal(t *testing.T) {
	a := MustMailboxId("mbx-abc")
	b := MustMailboxId("mbx-abc")
	c := MustMailboxId("mbx-xyz")
	if !a.Equal(b) {
		t.Error("a.Equal(b) with same raw value should be true")
	}
	if a.Equal(c) {
		t.Error("a.Equal(c) with different raw values should be false")
	}
}

func TestMailboxId_String(t *testing.T) {
	id := MustMailboxId("mbx-test")
	if s := id.String(); s != "mbx-test" {
		t.Errorf("String() = %q, want %q", s, "mbx-test")
	}
}

func TestMustMailboxId(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustMailboxId('') did not panic")
		}
	}()
	_ = MustMailboxId("")
}

// ---------------------------------------------------------------------------
// FolderId tests
// ---------------------------------------------------------------------------

func TestFolderId_New(t *testing.T) {
	id, err := NewFolderId("fld-xyz")
	if err != nil {
		t.Fatalf("NewFolderId returned error: %v", err)
	}
	if id.raw != "fld-xyz" {
		t.Errorf("raw = %q, want %q", id.raw, "fld-xyz")
	}
}

func TestFolderId_NewEmpty(t *testing.T) {
	_, err := NewFolderId("")
	if err == nil {
		t.Error("NewFolderId('') did not return error")
	}
}

func TestFolderId_IsZero(t *testing.T) {
	var zero FolderId
	if !zero.IsZero() {
		t.Error("zero FolderId should be IsZero")
	}
	id := MustFolderId("x")
	if id.IsZero() {
		t.Error("non-zero FolderId should not be IsZero")
	}
}

func TestFolderId_Equal(t *testing.T) {
	a := MustFolderId("fld-a")
	b := MustFolderId("fld-a")
	c := MustFolderId("fld-b")
	if !a.Equal(b) {
		t.Error("a.Equal(b) with same raw value should be true")
	}
	if a.Equal(c) {
		t.Error("a.Equal(c) with different raw values should be false")
	}
}

// ---------------------------------------------------------------------------
// ItemId tests
// ---------------------------------------------------------------------------

func TestItemId_New(t *testing.T) {
	id, err := NewItemId("item-789")
	if err != nil {
		t.Fatalf("NewItemId returned error: %v", err)
	}
	if id.raw != "item-789" {
		t.Errorf("raw = %q, want %q", id.raw, "item-789")
	}
}

func TestItemId_NewEmpty(t *testing.T) {
	_, err := NewItemId("")
	if err == nil {
		t.Error("NewItemId('') did not return error")
	}
}

func TestItemId_IsZero(t *testing.T) {
	var zero ItemId
	if !zero.IsZero() {
		t.Error("zero ItemId should be IsZero")
	}
	id := MustItemId("x")
	if id.IsZero() {
		t.Error("non-zero ItemId should not be IsZero")
	}
}

func TestItemId_Equal(t *testing.T) {
	a := MustItemId("item-a")
	b := MustItemId("item-a")
	c := MustItemId("item-b")
	if !a.Equal(b) {
		t.Error("a.Equal(b) with same raw value should be true")
	}
	if a.Equal(c) {
		t.Error("a.Equal(c) with different raw values should be false")
	}
}

// ---------------------------------------------------------------------------
// ChangeKey tests
// ---------------------------------------------------------------------------

func TestChangeKey_New(t *testing.T) {
	ck, err := NewChangeKey("CKabc123==")
	if err != nil {
		t.Fatalf("NewChangeKey returned error: %v", err)
	}
	if ck.raw != "CKabc123==" {
		t.Errorf("raw = %q, want %q", ck.raw, "CKabc123==")
	}
}

func TestChangeKey_NewEmpty(t *testing.T) {
	_, err := NewChangeKey("")
	if err == nil {
		t.Error("NewChangeKey('') did not return error")
	}
}

func TestChangeKey_IsZero(t *testing.T) {
	var zero ChangeKey
	if !zero.IsZero() {
		t.Error("zero ChangeKey should be IsZero")
	}
	ck := MustChangeKey("x")
	if ck.IsZero() {
		t.Error("non-zero ChangeKey should not be IsZero")
	}
}

func TestChangeKey_Equal(t *testing.T) {
	a := MustChangeKey("v1")
	b := MustChangeKey("v1")
	c := MustChangeKey("v2")
	if !a.Equal(b) {
		t.Error("a.Equal(b) with same raw value should be true")
	}
	if a.Equal(c) {
		t.Error("a.Equal(c) with different raw values should be false")
	}
}

func TestMustChangeKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustChangeKey('') did not panic")
		}
	}()
	_ = MustChangeKey("")
}

// ---------------------------------------------------------------------------
// AttachmentId tests
// ---------------------------------------------------------------------------

func TestAttachmentId_New(t *testing.T) {
	id, err := NewAttachmentId("att-456")
	if err != nil {
		t.Fatalf("NewAttachmentId returned error: %v", err)
	}
	if id.raw != "att-456" {
		t.Errorf("raw = %q, want %q", id.raw, "att-456")
	}
}

func TestAttachmentId_NewEmpty(t *testing.T) {
	_, err := NewAttachmentId("")
	if err == nil {
		t.Error("NewAttachmentId('') did not return error")
	}
}

func TestAttachmentId_IsZero(t *testing.T) {
	var zero AttachmentId
	if !zero.IsZero() {
		t.Error("zero AttachmentId should be IsZero")
	}
	id := MustAttachmentId("x")
	if id.IsZero() {
		t.Error("non-zero AttachmentId should not be IsZero")
	}
}

func TestAttachmentId_Equal(t *testing.T) {
	a := MustAttachmentId("att-a")
	b := MustAttachmentId("att-a")
	c := MustAttachmentId("att-b")
	if !a.Equal(b) {
		t.Error("a.Equal(b) with same raw value should be true")
	}
	if a.Equal(c) {
		t.Error("a.Equal(c) with different raw values should be false")
	}
}

// ---------------------------------------------------------------------------
// ConversationId tests
// ---------------------------------------------------------------------------

func TestConversationId_New(t *testing.T) {
	id, err := NewConversationId("conv-111")
	if err != nil {
		t.Fatalf("NewConversationId returned error: %v", err)
	}
	if id.raw != "conv-111" {
		t.Errorf("raw = %q, want %q", id.raw, "conv-111")
	}
}

func TestConversationId_NewEmpty(t *testing.T) {
	_, err := NewConversationId("")
	if err == nil {
		t.Error("NewConversationId('') did not return error")
	}
}

func TestConversationId_IsZero(t *testing.T) {
	var zero ConversationId
	if !zero.IsZero() {
		t.Error("zero ConversationId should be IsZero")
	}
	id := MustConversationId("x")
	if id.IsZero() {
		t.Error("non-zero ConversationId should not be IsZero")
	}
}

func TestConversationId_Equal(t *testing.T) {
	a := MustConversationId("conv-a")
	b := MustConversationId("conv-a")
	c := MustConversationId("conv-b")
	if !a.Equal(b) {
		t.Error("a.Equal(b) with same raw value should be true")
	}
	if a.Equal(c) {
		t.Error("a.Equal(c) with different raw values should be false")
	}
}

// ---------------------------------------------------------------------------
// SyncToken tests
// ---------------------------------------------------------------------------

func TestSyncToken_New(t *testing.T) {
	tok := NewSyncToken("watermark-500")
	if tok.raw != "watermark-500" {
		t.Errorf("raw = %q, want %q", tok.raw, "watermark-500")
	}
}

func TestSyncToken_NewEmpty(t *testing.T) {
	// empty is valid for initial sync state
	tok := NewSyncToken("")
	if !tok.IsZero() {
		t.Error("empty SyncToken should be IsZero")
	}
}

func TestSyncToken_IsZero(t *testing.T) {
	var zero SyncToken
	if !zero.IsZero() {
		t.Error("zero SyncToken should be IsZero")
	}
	tok := NewSyncToken("state-1")
	if tok.IsZero() {
		t.Error("non-zero SyncToken should not be IsZero")
	}
}

func TestSyncToken_Equal(t *testing.T) {
	a := NewSyncToken("tok-a")
	b := NewSyncToken("tok-a")
	c := NewSyncToken("tok-b")
	if !a.Equal(b) {
		t.Error("a.Equal(b) with same raw value should be true")
	}
	if a.Equal(c) {
		t.Error("a.Equal(c) with different raw values should be false")
	}
}

func TestSyncToken_String(t *testing.T) {
	tok := NewSyncToken("sync-state-42")
	if s := tok.String(); s != "sync-state-42" {
		t.Errorf("String() = %q, want %q", s, "sync-state-42")
	}
}

// ---------------------------------------------------------------------------
// LifecycleKind tests
// ---------------------------------------------------------------------------

func TestLifecycleKind_String(t *testing.T) {
	tests := []struct {
		k    LifecycleKind
		want string
	}{
		{LifecycleKindCreated, "created"},
		{LifecycleKindUpdated, "updated"},
		{LifecycleKindMoved, "moved"},
		{LifecycleKindSoftDeleted, "soft_deleted"},
		{LifecycleKindHardDeleted, "hard_deleted"},
		{LifecycleKindRestored, "restored"},
		{LifecycleKind(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.k.String(); got != tt.want {
			t.Errorf("LifecycleKind(%d).String() = %q, want %q", tt.k, got, tt.want)
		}
	}
}
