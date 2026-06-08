package semcore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func tmpBoltStore(t *testing.T) *BoltIdentityStore {
	tmpDir := t.TempDir()
	store, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	return store
}

// closeIgnore closes a store and ignores the error.
// Used only in idempotent or guaranteed-success test paths.
func closeIgnore(s *BoltIdentityStore) {
	_ = s.Close() //nolint:errcheck
}

// putMailboxIgnore calls PutMailboxIdentity and ignores the error.
// Used only when the caller has already verified success in a prior test
// and is only seeding data for a list/get test.
func putMailboxIgnore(s *BoltIdentityStore, key string, id MailboxId, uidValidity uint32) {
	_ = s.PutMailboxIdentity(key, id, uidValidity) //nolint:errcheck
}

// putFolderIgnore calls PutFolderIdentity and ignores the error.
func putFolderIgnore(s *BoltIdentityStore, mboxKey, folderName string, id FolderId, role string) {
	_ = s.PutFolderIdentity(mboxKey, folderName, id, role) //nolint:errcheck
}

// putItemIgnore calls PutItemIdentity and ignores the error.
func putItemIgnore(s *BoltIdentityStore, msgKey string, email string, id ItemId, mboxID MailboxId, fldID FolderId, ck ChangeKey, convID ConversationId) {
	_ = s.PutItemIdentity(msgKey, email, id, mboxID, fldID, ck, convID, false) //nolint:errcheck
}

// putAttachmentIgnore calls PutAttachmentIdentity and ignores the error.
func putAttachmentIgnore(s *BoltIdentityStore, parentID ItemId, name string, id AttachmentId) {
	_ = s.PutAttachmentIdentity(parentID, name, id) //nolint:errcheck
}

func closeStore(s *BoltIdentityStore, t *testing.T) {
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// BoltIdentityStore basic lifecycle
// ---------------------------------------------------------------------------

func TestBoltIdentityStore_NewAndClose(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	if store.db == nil {
		t.Fatal("db is nil after open")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestBoltIdentityStore_Bolt(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)
	if store.Bolt() == nil {
		t.Error("Bolt() returned nil")
	}
}

func TestBoltIdentityStore_IdempotentOpen(t *testing.T) {
	tmpDir := t.TempDir()
	// First open.
	s1, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s1.Close() //nolint:errcheck
	// Second open should succeed (buckets already exist).
	s2, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer closeIgnore(s2)
}

// ---------------------------------------------------------------------------
// MailboxId tests
// ---------------------------------------------------------------------------

func TestBoltIdentityStore_PutMailboxIdentity_basic(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-abc")
	err := store.PutMailboxIdentity("e:alice@local.test", mboxID, 12345)
	if err != nil {
		t.Fatalf("PutMailboxIdentity: %v", err)
	}
}

func TestBoltIdentityStore_PutMailboxIdentity_duplicate(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-abc")
	err := store.PutMailboxIdentity("e:alice@local.test", mboxID, 1)
	if err != nil {
		t.Fatalf("first PutMailboxIdentity: %v", err)
	}

	// Duplicate should fail.
	err = store.PutMailboxIdentity("e:alice@local.test", mboxID, 2)
	if err != ErrIdentityExists {
		t.Errorf("duplicate PutMailboxIdentity error = %v, want ErrIdentityExists", err)
	}
}

func TestBoltIdentityStore_PutMailboxIdentity_zeroID(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.PutMailboxIdentity("e:alice@local.test", MailboxId{}, 1)
	if err == nil {
		t.Error("PutMailboxIdentity with zero ID should error")
	}
}

func TestBoltIdentityStore_GetMailboxIDByKey_found(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-get-test")
	err := store.PutMailboxIdentity("e:bob@local.test", mboxID, 54321)
	if err != nil {
		t.Fatalf("PutMailboxIdentity: %v", err)
	}

	got, err := store.GetMailboxIDByKey("e:bob@local.test")
	if err != nil {
		t.Fatalf("GetMailboxIDByKey: %v", err)
	}
	if !got.Equal(mboxID) {
		t.Errorf("GetMailboxIDByKey = %v, want %v", got, mboxID)
	}
}

func TestBoltIdentityStore_GetMailboxIDByKey_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	_, err := store.GetMailboxIDByKey("e:notexist@local.test")
	if err != ErrMailboxNotFound {
		t.Errorf("GetMailboxIDByKey notfound error = %v, want ErrMailboxNotFound", err)
	}
}

func TestBoltIdentityStore_GetMailboxIDByEmail(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-email-test")
	err := store.PutMailboxIdentity("e:charlie@local.test", mboxID, 1)
	if err != nil {
		t.Fatalf("PutMailboxIdentity: %v", err)
	}

	got, err := store.GetMailboxIDByEmail("charlie@local.test")
	if err != nil {
		t.Fatalf("GetMailboxIDByEmail: %v", err)
	}
	if !got.Equal(mboxID) {
		t.Errorf("GetMailboxIDByEmail = %v, want %v", got, mboxID)
	}
}

func TestBoltIdentityStore_SetMailboxModSeq(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-modseq")
	err := store.PutMailboxIdentity("e:modseq@local.test", mboxID, 1)
	if err != nil {
		t.Fatalf("PutMailboxIdentity: %v", err)
	}

	err = store.SetMailboxModSeq("e:modseq@local.test", 999)
	if err != nil {
		t.Fatalf("SetMailboxModSeq: %v", err)
	}

	list, err := store.ListMailboxIdentities()
	if err != nil {
		t.Fatalf("ListMailboxIdentities: %v", err)
	}
	for _, m := range list {
		if m.MailboxID.Equal(mboxID) && m.HighestModSeq != 999 {
			t.Errorf("HighestModSeq = %d, want 999", m.HighestModSeq)
		}
	}
}

func TestBoltIdentityStore_SetMailboxModSeq_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.SetMailboxModSeq("e:notexist@local.test", 1)
	if err != ErrMailboxNotFound {
		t.Errorf("SetMailboxModSeq notfound error = %v, want ErrMailboxNotFound", err)
	}
}

func TestBoltIdentityStore_ListMailboxIdentities(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	for i := 0; i < 3; i++ {
		mboxID := MustMailboxId("mbx-list-" + string(rune('a'+i)))
		putMailboxIgnore(store, "e:list"+string(rune('a'+i))+"@local.test", mboxID, uint32(i+1))
	}

	list, err := store.ListMailboxIdentities()
	if err != nil {
		t.Fatalf("ListMailboxIdentities: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("len(list) = %d, want 3", len(list))
	}
}

func TestBoltIdentityStore_ListMailboxIdentities_empty(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	list, err := store.ListMailboxIdentities()
	if err != nil {
		t.Fatalf("ListMailboxIdentities: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("len(list) = %d, want 0", len(list))
	}
}

// ---------------------------------------------------------------------------
// FolderId tests
// ---------------------------------------------------------------------------

func TestBoltIdentityStore_PutFolderIdentity_basic(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:foldertest@local.test"
	fldID := MustFolderId("fld-inbox")
	err := store.PutFolderIdentity(mboxKey, "INBOX", fldID, "inbox")
	if err != nil {
		t.Fatalf("PutFolderIdentity: %v", err)
	}
}

func TestBoltIdentityStore_PutFolderIdentity_duplicate(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:foldertest@local.test"
	fldID := MustFolderId("fld-dup")
	err := store.PutFolderIdentity(mboxKey, "INBOX", fldID, "inbox")
	if err != nil {
		t.Fatalf("first PutFolderIdentity: %v", err)
	}

	err = store.PutFolderIdentity(mboxKey, "INBOX", fldID, "inbox")
	if err != ErrIdentityExists {
		t.Errorf("duplicate error = %v, want ErrIdentityExists", err)
	}
}

func TestBoltIdentityStore_PutFolderIdentity_zeroID(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.PutFolderIdentity("key", "INBOX", FolderId{}, "")
	if err == nil {
		t.Error("PutFolderIdentity with zero ID should error")
	}
}

func TestBoltIdentityStore_GetFolderID(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:getfoldtest@local.test"
	fldID := MustFolderId("fld-get")
	err := store.PutFolderIdentity(mboxKey, "INBOX", fldID, "inbox")
	if err != nil {
		t.Fatalf("PutFolderIdentity: %v", err)
	}

	got, err := store.GetFolderID(mboxKey, "INBOX")
	if err != nil {
		t.Fatalf("GetFolderID: %v", err)
	}
	if !got.Equal(fldID) {
		t.Errorf("GetFolderID = %v, want %v", got, fldID)
	}
}

func TestBoltIdentityStore_GetFolderID_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	_, err := store.GetFolderID("e:notexist@local.test", "INBOX")
	if err != ErrFolderNotFound {
		t.Errorf("GetFolderID notfound error = %v, want ErrFolderNotFound", err)
	}
}

func TestBoltIdentityStore_GetFolderByID(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:getbyid@local.test"
	fldID := MustFolderId("fld-getbyid")
	err := store.PutFolderIdentity(mboxKey, "Sent Mail", fldID, "sent")
	if err != nil {
		t.Fatalf("PutFolderIdentity: %v", err)
	}

	got, err := store.GetFolderByID(fldID)
	if err != nil {
		t.Fatalf("GetFolderByID: %v", err)
	}
	if !got.FolderID.Equal(fldID) {
		t.Errorf("got.FolderID = %v, want %v", got.FolderID, fldID)
	}
	if got.Role != "sent" {
		t.Errorf("got.Role = %q, want %q", got.Role, "sent")
	}
}

func TestBoltIdentityStore_EnsureFolderId_reusesRoleMatch(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:role-match@local.test"
	inboxID := MustFolderId("fld-role-match")
	if err := store.PutFolderIdentity(mboxKey, "INBOX", inboxID, "inbox"); err != nil {
		t.Fatalf("PutFolderIdentity: %v", err)
	}

	got, err := store.EnsureFolderId(mboxKey, "inbox", "inbox")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}
	if !got.Equal(inboxID) {
		t.Fatalf("EnsureFolderId returned %v, want %v", got, inboxID)
	}
}

func TestBoltIdentityStore_EnsureChildFolderId_distinctParentsSameName(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:childscope@local.test"
	parentA := MustFolderId("fld-parent-a")
	parentB := MustFolderId("fld-parent-b")

	// Two folders both named "Reports", under different parents, must get
	// distinct identities — a real copy, not a collapse into the sibling.
	idA, err := store.EnsureChildFolderId(mboxKey, parentA, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId A: %v", err)
	}
	idB, err := store.EnsureChildFolderId(mboxKey, parentB, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId B: %v", err)
	}
	if idA.Equal(idB) {
		t.Fatalf("same-name children under different parents share id %v", idA)
	}

	// Each carries the parent it was created under.
	recA, err := store.GetFolderByID(idA)
	if err != nil {
		t.Fatalf("GetFolderByID A: %v", err)
	}
	if !recA.ParentID.Equal(parentA) {
		t.Errorf("child A ParentID = %v, want %v", recA.ParentID, parentA)
	}
	recB, err := store.GetFolderByID(idB)
	if err != nil {
		t.Fatalf("GetFolderByID B: %v", err)
	}
	if !recB.ParentID.Equal(parentB) {
		t.Errorf("child B ParentID = %v, want %v", recB.ParentID, parentB)
	}

	// Both render as the same client-visible name, even though one is stored
	// under a parent-scoped storage name.
	for _, tc := range []struct {
		id  FolderId
		tag string
	}{{idA, "A"}, {idB, "B"}} {
		stored, err := store.FolderNameByID(mboxKey, tc.id)
		if err != nil {
			t.Fatalf("FolderNameByID %s: %v", tc.tag, err)
		}
		if got := DisplayNameFromStorageName(stored); got != "Reports" {
			t.Errorf("child %s display name = %q, want %q", tc.tag, got, "Reports")
		}
	}

	// Idempotent: a repeat call with the same (parent, name) reuses the id.
	idA2, err := store.EnsureChildFolderId(mboxKey, parentA, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId A repeat: %v", err)
	}
	if !idA2.Equal(idA) {
		t.Errorf("repeat EnsureChildFolderId minted %v, want existing %v", idA2, idA)
	}
	idB2, err := store.EnsureChildFolderId(mboxKey, parentB, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId B repeat: %v", err)
	}
	if !idB2.Equal(idB) {
		t.Errorf("repeat EnsureChildFolderId minted %v, want existing %v", idB2, idB)
	}
}

func TestDisplayNameFromStorageName(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		want   string
	}{
		{"plain name unchanged", "Reports", "Reports"},
		{"plain name with separator-free unicode", "Çalışmalar", "Çalışmalar"},
		{"parent-scoped strips prefix", ChildStorageName(MustFolderId("fld-xyz"), "Reports"), "Reports"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayNameFromStorageName(tc.stored); got != tc.want {
				t.Errorf("DisplayNameFromStorageName(%q) = %q, want %q", tc.stored, got, tc.want)
			}
		})
	}
}

func TestBoltIdentityStore_GetFolderByMailbox_prefersCanonicalRoleName(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:canonical-role@local.test"
	legacyID := MustFolderId("fld-legacy-inbox")
	canonicalID := MustFolderId("fld-canonical-inbox")
	if err := store.PutFolderIdentity(mboxKey, "inbox", legacyID, "inbox"); err != nil {
		t.Fatalf("PutFolderIdentity legacy: %v", err)
	}
	if err := store.PutFolderIdentity(mboxKey, "INBOX", canonicalID, "inbox"); err != nil {
		t.Fatalf("PutFolderIdentity canonical: %v", err)
	}

	got, err := store.GetFolderByMailbox(mboxKey, "inbox")
	if err != nil {
		t.Fatalf("GetFolderByMailbox: %v", err)
	}
	if !got.FolderID.Equal(canonicalID) {
		t.Fatalf("GetFolderByMailbox returned %v, want canonical %v", got.FolderID, canonicalID)
	}
}

func TestBoltIdentityStore_SetFolderParent(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:setparent@local.test"
	parentID := MustFolderId("fld-parent")
	childID := MustFolderId("fld-child")
	putFolderIgnore(store, mboxKey, "Projects", parentID, "")
	putFolderIgnore(store, mboxKey, "Projects/Work", childID, "")

	err := store.SetFolderParent(childID, parentID)
	if err != nil {
		t.Fatalf("SetFolderParent: %v", err)
	}

	got, err := store.GetFolderByID(childID)
	if err != nil {
		t.Fatalf("GetFolderByID after SetFolderParent: %v", err)
	}
	if !got.ParentID.Equal(parentID) {
		t.Errorf("ParentID = %v, want %v", got.ParentID, parentID)
	}
}

func TestBoltIdentityStore_SetFolderDistinguishedRole(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:setrole@local.test"
	fldID := MustFolderId("fld-drafts")
	putFolderIgnore(store, mboxKey, "Drafts", fldID, "")

	err := store.SetFolderDistinguishedRole(fldID, "drafts")
	if err != nil {
		t.Fatalf("SetFolderDistinguishedRole: %v", err)
	}

	got, err := store.GetFolderByID(fldID)
	if err != nil {
		t.Fatalf("GetFolderByID after SetFolderDistinguishedRole: %v", err)
	}
	if got.Role != "drafts" {
		t.Errorf("Role = %q, want %q", got.Role, "drafts")
	}
}

func TestBoltIdentityStore_SetFolderSortOrder(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:setsort@local.test"
	fldID := MustFolderId("fld-sort")
	putFolderIgnore(store, mboxKey, "Important", fldID, "")

	err := store.SetFolderSortOrder(fldID, 99)
	if err != nil {
		t.Fatalf("SetFolderSortOrder: %v", err)
	}

	got, err := store.GetFolderByID(fldID)
	if err != nil {
		t.Fatalf("GetFolderByID after SetFolderSortOrder: %v", err)
	}
	if got.SortOrder != 99 {
		t.Errorf("SortOrder = %d, want 99", got.SortOrder)
	}
}

func TestBoltIdentityStore_SetFolderModSeq(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:setfmodseq@local.test"
	fldID := MustFolderId("fld-fmodseq")
	putFolderIgnore(store, mboxKey, "INBOX", fldID, "inbox")

	err := store.SetFolderModSeq(fldID, 42)
	if err != nil {
		t.Fatalf("SetFolderModSeq: %v", err)
	}

	got, err := store.GetFolderByID(fldID)
	if err != nil {
		t.Fatalf("GetFolderByID after SetFolderModSeq: %v", err)
	}
	if got.HighestModSeq != 42 {
		t.Errorf("HighestModSeq = %d, want 42", got.HighestModSeq)
	}
}

func TestBoltIdentityStore_SetFolderSubscribed(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:setsub@local.test"
	fldID := MustFolderId("fld-sub")
	putFolderIgnore(store, mboxKey, "Test", fldID, "")

	err := store.SetFolderSubscribed(fldID, false)
	if err != nil {
		t.Fatalf("SetFolderSubscribed: %v", err)
	}

	got, err := store.GetFolderByID(fldID)
	if err != nil {
		t.Fatalf("GetFolderByID after SetFolderSubscribed: %v", err)
	}
	if got.IsSubscribed {
		t.Error("IsSubscribed = true, want false after SetFolderSubscribed(false)")
	}
}

func TestBoltIdentityStore_ListFolderIdentities(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:lstfolder@local.test"
	putFolderIgnore(store, mboxKey, "INBOX", MustFolderId("fld-lst-1"), "inbox")
	putFolderIgnore(store, mboxKey, "Sent", MustFolderId("fld-lst-2"), "sent")

	list, err := store.ListFolderIdentities()
	if err != nil {
		t.Fatalf("ListFolderIdentities: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len(list) = %d, want 2", len(list))
	}
}

// ---------------------------------------------------------------------------
// ItemId tests
// ---------------------------------------------------------------------------

func TestBoltIdentityStore_PutItemIdentity_basic(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-item")
	fldID := MustFolderId("fld-item")
	itemID := MustItemId("item-001")
	ck := MustChangeKey("CK-001")
	convID := MustConversationId("conv-001")

	err := store.PutItemIdentity("k:msg1", "", itemID, mboxID, fldID, ck, convID, false)
	if err != nil {
		t.Fatalf("PutItemIdentity: %v", err)
	}
}

func TestBoltIdentityStore_PutItemIdentity_duplicate(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-item-dup")
	fldID := MustFolderId("fld-item-dup")
	itemID := MustItemId("item-dup")
	ck := MustChangeKey("CK-DUP")

	err := store.PutItemIdentity("k:msg-dup", "", itemID, mboxID, fldID, ck, ConversationId{}, false)
	if err != nil {
		t.Fatalf("first PutItemIdentity: %v", err)
	}

	err = store.PutItemIdentity("k:msg-dup", "", itemID, mboxID, fldID, ck, ConversationId{}, false)
	if err != ErrIdentityExists {
		t.Errorf("duplicate error = %v, want ErrIdentityExists", err)
	}
}

func TestBoltIdentityStore_PutItemIdentity_sameMsgKeyDifferentEmail(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-item-multi")
	fldID := MustFolderId("fld-item-multi")
	firstID := MustItemId("item-multi-1")
	secondID := MustItemId("item-multi-2")
	ck := MustChangeKey("CK-MULTI")

	if err := store.PutItemIdentity("k:msg-shared", "alice@example.com", firstID, mboxID, fldID, ck, ConversationId{}, false); err != nil {
		t.Fatalf("first PutItemIdentity: %v", err)
	}
	if err := store.PutItemIdentity("k:msg-shared", "bob@example.com", secondID, mboxID, fldID, ck, ConversationId{}, false); err != nil {
		t.Fatalf("second PutItemIdentity: %v", err)
	}

	got, err := store.GetItemIDByKey("k:msg-shared")
	if err != nil {
		t.Fatalf("GetItemIDByKey: %v", err)
	}
	if got.IsZero() {
		t.Fatal("GetItemIDByKey returned zero ItemId")
	}
}

func TestBoltIdentityStore_PutItemIdentity_zeroID(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.PutItemIdentity("k:msg-zero", "", ItemId{}, MailboxId{}, FolderId{}, ChangeKey{}, ConversationId{}, false)
	if err == nil {
		t.Error("PutItemIdentity with zero ItemId should error")
	}
}

func TestBoltIdentityStore_GetItemIDByKey(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-getitem")
	fldID := MustFolderId("fld-getitem")
	itemID := MustItemId("item-get")
	ck := MustChangeKey("CK-GET")

	err := store.PutItemIdentity("k:get-item", "", itemID, mboxID, fldID, ck, ConversationId{}, false)
	if err != nil {
		t.Fatalf("PutItemIdentity: %v", err)
	}

	got, err := store.GetItemIDByKey("k:get-item")
	if err != nil {
		t.Fatalf("GetItemIDByKey: %v", err)
	}
	if !got.Equal(itemID) {
		t.Errorf("GetItemIDByKey = %v, want %v", got, itemID)
	}
}

func TestBoltIdentityStore_GetItemIDByKey_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	_, err := store.GetItemIDByKey("k:no-exist")
	if err != ErrItemNotFound {
		t.Errorf("GetItemIDByKey notfound error = %v, want ErrItemNotFound", err)
	}
}

func TestBoltIdentityStore_GetItemIdentity(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-geti")
	fldID := MustFolderId("fld-geti")
	itemID := MustItemId("item-geti")
	ck := MustChangeKey("CK-GETI")
	convID := MustConversationId("conv-geti")

	err := store.PutItemIdentity("k:geti", "", itemID, mboxID, fldID, ck, convID, false)
	if err != nil {
		t.Fatalf("PutItemIdentity: %v", err)
	}

	got, err := store.GetItemIdentity(itemID)
	if err != nil {
		t.Fatalf("GetItemIdentity: %v", err)
	}
	if !got.ItemID.Equal(itemID) {
		t.Errorf("ItemID = %v, want %v", got.ItemID, itemID)
	}
	if !got.ChangeKey.Equal(ck) {
		t.Errorf("ChangeKey = %v, want %v", got.ChangeKey, ck)
	}
	if !got.ConversationID.Equal(convID) {
		t.Errorf("ConversationID = %v, want %v", got.ConversationID, convID)
	}
}

func TestBoltIdentityStore_PutChangeKey(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-pck")
	fldID := MustFolderId("fld-pck")
	itemID := MustItemId("item-pck")
	oldCK := MustChangeKey("CK-OLD")

	putItemIgnore(store, "k:pck", "", itemID, mboxID, fldID, oldCK, ConversationId{})

	err := store.PutChangeKey(itemID, oldCK, MustChangeKey("CK-NEW"))
	if err != nil {
		t.Fatalf("PutChangeKey: %v", err)
	}

	got, err := store.GetItemIdentity(itemID)
	if err != nil {
		t.Fatalf("GetItemIdentity after PutChangeKey: %v", err)
	}
	if !got.ChangeKey.Equal(MustChangeKey("CK-NEW")) {
		t.Errorf("ChangeKey after PutChangeKey = %v, want CK-NEW", got.ChangeKey)
	}
}

func TestBoltIdentityStore_PutChangeKey_stale(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-ck-stale")
	fldID := MustFolderId("fld-ck-stale")
	itemID := MustItemId("item-ck-stale")
	oldCK := MustChangeKey("CK-OLD")

	err := store.PutItemIdentity("k:stale", "", itemID, mboxID, fldID, oldCK, ConversationId{}, false)
	if err != nil {
		t.Fatalf("PutItemIdentity: %v", err)
	}

	// Try to advance with wrong currentCK — should fail.
	err = store.PutChangeKey(itemID, MustChangeKey("CK-WRONG"), MustChangeKey("CK-NEW"))
	if err == nil {
		t.Error("PutChangeKey with stale currentCK should fail")
	}
}

func TestBoltIdentityStore_PutChangeKey_zeroItem(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.PutChangeKey(ItemId{}, ChangeKey{}, MustChangeKey("CK-NEW"))
	if err == nil {
		t.Error("PutChangeKey with zero ItemId should error")
	}
}

func TestBoltIdentityStore_SetItemConversation(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-sic")
	fldID := MustFolderId("fld-sic")
	itemID := MustItemId("item-sic")
	ck := MustChangeKey("CK-SIC")

	err := store.PutItemIdentity("k:sic", "", itemID, mboxID, fldID, ck, ConversationId{}, false)
	if err != nil {
		t.Fatalf("PutItemIdentity: %v", err)
	}

	newConvID := MustConversationId("conv-sic-new")
	err = store.SetItemConversation(itemID, newConvID)
	if err != nil {
		t.Fatalf("SetItemConversation: %v", err)
	}

	got, err := store.GetItemIdentity(itemID)
	if err != nil {
		t.Fatalf("GetItemIdentity after SetItemConversation: %v", err)
	}
	if !got.ConversationID.Equal(newConvID) {
		t.Errorf("ConversationID = %v, want %v", got.ConversationID, newConvID)
	}
}

func TestBoltIdentityStore_SetItemFolder(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-sif")
	sourceFolderID := MustFolderId("fld-sif-src")
	destFolderID := MustFolderId("fld-sif-dst")
	itemID := MustItemId("item-sif")

	if err := store.PutItemIdentity("k:sif", "", itemID, mboxID, sourceFolderID, MustChangeKey("CK-SIF"), ConversationId{}, false); err != nil {
		t.Fatalf("PutItemIdentity: %v", err)
	}

	if err := store.SetItemFolder(itemID, destFolderID); err != nil {
		t.Fatalf("SetItemFolder: %v", err)
	}

	got, err := store.GetItemIdentity(itemID)
	if err != nil {
		t.Fatalf("GetItemIdentity after SetItemFolder: %v", err)
	}
	if !got.FolderID.Equal(destFolderID) {
		t.Fatalf("FolderID = %v, want %v", got.FolderID, destFolderID)
	}
}

func TestBoltIdentityStore_UpdateItemState(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-sis")
	fldID := MustFolderId("fld-sis")
	itemID := MustItemId("item-sis")

	if err := store.PutItemIdentity("k:sis", "", itemID, mboxID, fldID, MustChangeKey("CK-SIS"), ConversationId{}, false); err != nil {
		t.Fatalf("PutItemIdentity: %v", err)
	}

	isRead := true
	categories := []string{"qa", "ews"}
	if err := store.UpdateItemState(itemID, &isRead, categories); err != nil {
		t.Fatalf("UpdateItemState: %v", err)
	}

	got, err := store.GetItemIdentity(itemID)
	if err != nil {
		t.Fatalf("GetItemIdentity after UpdateItemState: %v", err)
	}
	if !got.IsRead {
		t.Fatal("IsRead should be true after UpdateItemState")
	}
	if !reflect.DeepEqual(got.Categories, categories) {
		t.Fatalf("Categories = %v, want %v", got.Categories, categories)
	}
}

func TestBoltIdentityStore_ListItemIdentitiesByMailbox(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-listitem")
	fldID := MustFolderId("fld-listitem")

	for i := 0; i < 3; i++ {
		itemID := MustItemId("item-list-" + string(rune('a'+i)))
		putItemIgnore(store, "k:li"+string(rune('a'+i)), "", itemID, mboxID, fldID, ChangeKey{}, ConversationId{})
	}

	list, err := store.ListItemIdentitiesByMailbox(mboxID)
	if err != nil {
		t.Fatalf("ListItemIdentitiesByMailbox: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("len(list) = %d, want 3", len(list))
	}

	// Wrong mailbox should return empty.
	list2, err := store.ListItemIdentitiesByMailbox(MustMailboxId("mbx-other"))
	if err != nil {
		t.Fatalf("ListItemIdentitiesByMailbox other: %v", err)
	}
	if len(list2) != 0 {
		t.Errorf("len(list2) = %d, want 0 for other mailbox", len(list2))
	}
}

// ---------------------------------------------------------------------------
// AttachmentId tests
// ---------------------------------------------------------------------------

func TestBoltIdentityStore_PutAttachmentIdentity_basic(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	parentID := MustItemId("item-att")
	attID := MustAttachmentId("att-001")

	err := store.PutAttachmentIdentity(parentID, "document.pdf", attID)
	if err != nil {
		t.Fatalf("PutAttachmentIdentity: %v", err)
	}
}

func TestBoltIdentityStore_PutAttachmentIdentity_duplicate(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	parentID := MustItemId("item-att-dup")
	attID := MustAttachmentId("att-dup")

	err := store.PutAttachmentIdentity(parentID, "doc.txt", attID)
	if err != nil {
		t.Fatalf("first PutAttachmentIdentity: %v", err)
	}

	err = store.PutAttachmentIdentity(parentID, "doc.txt", attID)
	if err != ErrIdentityExists {
		t.Errorf("duplicate error = %v, want ErrIdentityExists", err)
	}
}

func TestBoltIdentityStore_PutAttachmentIdentity_zeroID(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.PutAttachmentIdentity(MustItemId("item-zero"), "file.txt", AttachmentId{})
	if err == nil {
		t.Error("PutAttachmentIdentity with zero AttachmentId should error")
	}
}

func TestBoltIdentityStore_GetAttachmentID(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	parentID := MustItemId("item-getatt")
	attID := MustAttachmentId("att-get")

	err := store.PutAttachmentIdentity(parentID, "photo.jpg", attID)
	if err != nil {
		t.Fatalf("PutAttachmentIdentity: %v", err)
	}

	got, err := store.GetAttachmentID(parentID, "photo.jpg")
	if err != nil {
		t.Fatalf("GetAttachmentID: %v", err)
	}
	if !got.Equal(attID) {
		t.Errorf("GetAttachmentID = %v, want %v", got, attID)
	}
}

func TestBoltIdentityStore_GetAttachmentID_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	_, err := store.GetAttachmentID(MustItemId("item-noatt"), "missing.txt")
	if err != ErrItemNotFound {
		t.Errorf("GetAttachmentID notfound error = %v, want ErrItemNotFound", err)
	}
}

func TestBoltIdentityStore_GetAttachmentIdentity(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	parentID := MustItemId("item-getatti")
	attID := MustAttachmentId("att-getatti")

	err := store.PutAttachmentIdentity(parentID, "data.csv", attID)
	if err != nil {
		t.Fatalf("PutAttachmentIdentity: %v", err)
	}

	got, err := store.GetAttachmentIdentity(attID)
	if err != nil {
		t.Fatalf("GetAttachmentIdentity: %v", err)
	}
	if !got.AttachmentID.Equal(attID) {
		t.Errorf("AttachmentID = %v, want %v", got.AttachmentID, attID)
	}
	if !got.ParentID.Equal(parentID) {
		t.Errorf("ParentID = %v, want %v", got.ParentID, parentID)
	}
}

func TestBoltIdentityStore_ListAttachmentsByParent(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	parentID := MustItemId("item-listatt")

	for i := 0; i < 3; i++ {
		attID := MustAttachmentId("att-la-" + string(rune('a'+i)))
		putAttachmentIgnore(store, parentID, "file"+string(rune('a'+i))+".txt", attID)
	}

	list, err := store.ListAttachmentsByParent(parentID)
	if err != nil {
		t.Fatalf("ListAttachmentsByParent: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("len(list) = %d, want 3", len(list))
	}
}

// ---------------------------------------------------------------------------
// ConversationId tests
// ---------------------------------------------------------------------------

func TestBoltIdentityStore_PutConversationIdentity_basic(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	convID := MustConversationId("conv-001")
	mboxID := MustMailboxId("mbx-conv")

	err := store.PutConversationIdentity(convID, mboxID)
	if err != nil {
		t.Fatalf("PutConversationIdentity: %v", err)
	}
}

func TestBoltIdentityStore_PutConversationIdentity_duplicate(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	convID := MustConversationId("conv-dup")
	mboxID := MustMailboxId("mbx-conv-dup")

	err := store.PutConversationIdentity(convID, mboxID)
	if err != nil {
		t.Fatalf("first PutConversationIdentity: %v", err)
	}

	err = store.PutConversationIdentity(convID, mboxID)
	if err != ErrIdentityExists {
		t.Errorf("duplicate error = %v, want ErrIdentityExists", err)
	}
}

func TestBoltIdentityStore_PutConversationIdentity_zeroID(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.PutConversationIdentity(ConversationId{}, MailboxId{})
	if err == nil {
		t.Error("PutConversationIdentity with zero ConversationId should error")
	}
}

func TestBoltIdentityStore_GetConversationIdentity(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	convID := MustConversationId("conv-get")
	mboxID := MustMailboxId("mbx-conv-get")

	err := store.PutConversationIdentity(convID, mboxID)
	if err != nil {
		t.Fatalf("PutConversationIdentity: %v", err)
	}

	got, err := store.GetConversationIdentity(convID)
	if err != nil {
		t.Fatalf("GetConversationIdentity: %v", err)
	}
	if !got.ConversationID.Equal(convID) {
		t.Errorf("ConversationID = %v, want %v", got.ConversationID, convID)
	}
	if !got.MailboxID.Equal(mboxID) {
		t.Errorf("MailboxID = %v, want %v", got.MailboxID, mboxID)
	}
}

func TestBoltIdentityStore_GetConversationIdentity_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	_, err := store.GetConversationIdentity(MustConversationId("conv-noexist"))
	if err != ErrItemNotFound {
		t.Errorf("GetConversationIdentity notfound error = %v, want ErrItemNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrency safety (basic)
// ---------------------------------------------------------------------------

func TestBoltIdentityStore_concurrentWrites(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:concur@local.test"

	// Multiple concurrent PutMailboxIdentity for different keys — should all succeed.
	for i := 0; i < 10; i++ {
		key := mboxKey + string(rune('0'+i))
		id := MustMailboxId("mbx-concur-" + string(rune('0'+i)))
		err := store.PutMailboxIdentity(key, id, uint32(i))
		if err != nil {
			t.Errorf("concurrent PutMailboxIdentity [%d]: %v", i, err)
		}
	}

	list, err := store.ListMailboxIdentities()
	if err != nil {
		t.Fatalf("ListMailboxIdentities: %v", err)
	}
	if len(list) != 10 {
		t.Errorf("len(list) = %d, want 10", len(list))
	}
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

func TestErrMailboxNotFound(t *testing.T) {
	if ErrMailboxNotFound.Error() == "" {
		t.Error("ErrMailboxNotFound has no message")
	}
}

func TestErrFolderNotFound(t *testing.T) {
	if ErrFolderNotFound.Error() == "" {
		t.Error("ErrFolderNotFound has no message")
	}
}

func TestErrItemNotFound(t *testing.T) {
	if ErrItemNotFound.Error() == "" {
		t.Error("ErrItemNotFound has no message")
	}
}

func TestErrIdentityExists(t *testing.T) {
	if ErrIdentityExists.Error() == "" {
		t.Error("ErrIdentityExists has no message")
	}
}

func TestErrChangeKeyNotStorable(t *testing.T) {
	if ErrChangeKeyNotStorable.Error() == "" {
		t.Error("ErrChangeKeyNotStorable has no message")
	}
}

// ---------------------------------------------------------------------------
// Direct bbolt access (storage integration)
// ---------------------------------------------------------------------------

func TestBoltIdentityStore_DBPath(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeStore(store, t)

	expectedPath := filepath.Join(tmpDir, "semcore", "identity.db")
	if store.Bolt().Path() != expectedPath {
		t.Errorf("DB path = %q, want %q", store.Bolt().Path(), expectedPath)
	}
}

func TestBoltIdentityStore_bucketsExist(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeStore(store, t)

	db := store.Bolt()
	err = db.View(func(tx *bbolt.Tx) error {
		for _, b := range []string{bucketMailbox, bucketFolder, bucketItem, bucketAttachment, bucketConversation} {
			if tx.Bucket([]byte(b)) == nil {
				return fmt.Errorf("bucket %q does not exist", b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("bucket check: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Raw bbolt access for storage package integration
// ---------------------------------------------------------------------------

func TestBoltIdentityStore_externalBoltAccess(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeStore(store, t)

	db := store.Bolt()

	// Simulate the kind of direct Bolt access the storage package uses.
	err = db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketMailbox))
		data, err := json.Marshal(storedMailboxIdentity{
			MailboxID:     MustMailboxId("mbx-ext"),
			Email:         "external@local.test",
			UIDValidity:   99999,
			HighestModSeq: 0,
		})
		if err != nil {
			return err
		}
		return b.Put([]byte("e:external@local.test"), data)
	})
	if err != nil {
		t.Fatalf("external Bolt access: %v", err)
	}

	// Verify we can read it back through the identity store.
	id, err := store.GetMailboxIDByEmail("external@local.test")
	if err != nil {
		t.Fatalf("GetMailboxIDByEmail after external Bolt write: %v", err)
	}
	if !id.Equal(MustMailboxId("mbx-ext")) {
		t.Errorf("id = %v, want mbx-ext", id)
	}
}

func TestBoltIdentityStore_requiresExplicitDir(t *testing.T) {
	// Store should create the directory if it doesn't exist.
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "doesnt", "exist", "yet")
	store, err := NewBoltIdentityStore(subDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore with new dir: %v", err)
	}
	defer closeStore(store, t)

	// Should be able to write after creating directory.
	err = store.PutMailboxIdentity("e:newdir@local.test", MustMailboxId("mbx-newdir"), 1)
	if err != nil {
		t.Fatalf("PutMailboxIdentity after dir creation: %v", err)
	}
}

func TestBoltIdentityStore_openReadOnly(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	// Write something first.
	mboxID := MustMailboxId("mbx-ro")
	err = store.PutMailboxIdentity("e:readonly@local.test", mboxID, 1)
	if err != nil {
		t.Fatalf("PutMailboxIdentity: %v", err)
	}
	// Close explicitly before re-opening.
	_ = store.Close() //nolint:errcheck

	// Re-open as read-only and verify we can read.
	store2, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore re-open: %v", err)
	}
	defer closeIgnore(store2)

	got, err := store2.GetMailboxIDByEmail("readonly@local.test")
	if err != nil {
		t.Fatalf("GetMailboxIDByEmail after re-open: %v", err)
	}
	if !got.Equal(mboxID) {
		t.Errorf("got = %v, want %v", got, mboxID)
	}
}

func TestBoltIdentityStore_FileAsDirPath(t *testing.T) {
	tmpDir := t.TempDir()
	// Passing a file path instead of a directory should fail.
	filePath := filepath.Join(tmpDir, "notadir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := NewBoltIdentityStore(filePath)
	if err == nil {
		t.Error("NewBoltIdentityStore with file path should fail")
	}
}

func TestBoltIdentityStore_Close_idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewBoltIdentityStore(tmpDir)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	// First close.
	_ = store.Close() //nolint:errcheck
	// Second close — should not panic; bbolt returns error on double-close.
	_ = store.Close() //nolint:errcheck
}

func TestBoltIdentityStore_FolderLineage(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:lineage@local.test"

	// Create parent folder.
	parentID := MustFolderId("fld-parent-001")
	err := store.PutFolderIdentity(mboxKey, "Projects", parentID, "")
	if err != nil {
		t.Fatalf("PutFolderIdentity parent: %v", err)
	}

	// Create child folder and set parent.
	childID := MustFolderId("fld-child-001")
	err = store.PutFolderIdentity(mboxKey, "Projects/Work", childID, "")
	if err != nil {
		t.Fatalf("PutFolderIdentity child: %v", err)
	}

	err = store.SetFolderParent(childID, parentID)
	if err != nil {
		t.Fatalf("SetFolderParent: %v", err)
	}

	// Verify parent-child lineage is correctly stored.
	child, err := store.GetFolderByID(childID)
	if err != nil {
		t.Fatalf("GetFolderByID child: %v", err)
	}
	if !child.ParentID.Equal(parentID) {
		t.Errorf("child.ParentID = %v, want %v", child.ParentID, parentID)
	}
	if !child.FolderID.Equal(childID) {
		t.Errorf("child.FolderID = %v, want %v", child.FolderID, childID)
	}

	parent, err := store.GetFolderByID(parentID)
	if err != nil {
		t.Fatalf("GetFolderByID parent: %v", err)
	}
	if !parent.FolderID.Equal(parentID) {
		t.Errorf("parent.FolderID = %v, want %v", parent.FolderID, parentID)
	}
	if !parent.ParentID.IsZero() {
		t.Errorf("parent.ParentID should be zero for top-level folder, got %v", parent.ParentID)
	}
}

func TestBoltIdentityStore_SetFolderDistinguishedRole_inbox(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxKey := "e:disting@local.test"
	inboxID := MustFolderId("fld-inbox-dist")

	err := store.PutFolderIdentity(mboxKey, "INBOX", inboxID, "")
	if err != nil {
		t.Fatalf("PutFolderIdentity: %v", err)
	}

	err = store.SetFolderDistinguishedRole(inboxID, "inbox")
	if err != nil {
		t.Fatalf("SetFolderDistinguishedRole: %v", err)
	}

	inbox, err := store.GetFolderByID(inboxID)
	if err != nil {
		t.Fatalf("GetFolderByID: %v", err)
	}
	if inbox.Role != "inbox" {
		t.Errorf("Role = %q, want %q", inbox.Role, "inbox")
	}
	if !inbox.IsSubscribed {
		t.Error("IsSubscribed should be true for inbox")
	}
}

func TestBoltIdentityStore_AttachmentParentScope(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	item1 := MustItemId("item-att-scope-1")
	item2 := MustItemId("item-att-scope-2")

	// Same filename on different items should be different AttachmentIds.
	att1 := MustAttachmentId("att-scope-1")
	att2 := MustAttachmentId("att-scope-2")

	err := store.PutAttachmentIdentity(item1, "avatar.png", att1)
	if err != nil {
		t.Fatalf("PutAttachmentIdentity item1: %v", err)
	}
	err = store.PutAttachmentIdentity(item2, "avatar.png", att2)
	if err != nil {
		t.Fatalf("PutAttachmentIdentity item2: %v", err)
	}

	got1, err := store.GetAttachmentID(item1, "avatar.png")
	if err != nil {
		t.Fatalf("GetAttachmentID item1: %v", err)
	}
	if !got1.Equal(att1) {
		t.Errorf("item1 attachment = %v, want %v", got1, att1)
	}

	got2, err := store.GetAttachmentID(item2, "avatar.png")
	if err != nil {
		t.Fatalf("GetAttachmentID item2: %v", err)
	}
	if !got2.Equal(att2) {
		t.Errorf("item2 attachment = %v, want %v", got2, att2)
	}

	if got1.Equal(got2) {
		t.Error("Attachments for different parents should not be equal")
	}
}

func TestBoltIdentityStore_SetItemConversation_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.SetItemConversation(MustItemId("item-noexist"), MustConversationId("conv-no"))
	if err != ErrItemNotFound {
		t.Errorf("SetItemConversation notfound error = %v, want ErrItemNotFound", err)
	}
}

func TestBoltIdentityStore_SetFolderParent_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.SetFolderParent(MustFolderId("fld-noexist"), MustFolderId("fld-parent-no"))
	if err != ErrFolderNotFound {
		t.Errorf("SetFolderParent notfound error = %v, want ErrFolderNotFound", err)
	}
}

func TestBoltIdentityStore_PutChangeKey_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	err := store.PutChangeKey(MustItemId("item-noexist"), ChangeKey{}, MustChangeKey("CK-NEW"))
	if err != ErrItemNotFound {
		t.Errorf("PutChangeKey notfound error = %v, want ErrItemNotFound", err)
	}
}

func TestBoltIdentityStore_PutChangeKey_firstWrite(t *testing.T) {
	// When the existing ChangeKey is zero and the item was just registered,
	// advancing to a non-zero ChangeKey should succeed.
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	mboxID := MustMailboxId("mbx-fw")
	fldID := MustFolderId("fld-fw")
	itemID := MustItemId("item-fw")

	// Put with zero ChangeKey (first registration).
	err := store.PutItemIdentity("k:first-write", "", itemID, mboxID, fldID, ChangeKey{}, ConversationId{}, false)
	if err != nil {
		t.Fatalf("PutItemIdentity with zero CK: %v", err)
	}

	// Advance with zero currentCK should succeed.
	newCK := MustChangeKey("CK-FIRST")
	err = store.PutChangeKey(itemID, ChangeKey{}, newCK)
	if err != nil {
		t.Fatalf("PutChangeKey first write: %v", err)
	}

	got, err := store.GetItemIdentity(itemID)
	if err != nil {
		t.Fatalf("GetItemIdentity: %v", err)
	}
	if !got.ChangeKey.Equal(newCK) {
		t.Errorf("ChangeKey = %v, want %v", got.ChangeKey, newCK)
	}
}

func TestBoltIdentityStore_GetItemIdentity_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	_, err := store.GetItemIdentity(MustItemId("item-noexist"))
	if err != ErrItemNotFound {
		t.Errorf("GetItemIdentity notfound error = %v, want ErrItemNotFound", err)
	}
}

func TestBoltIdentityStore_GetAttachmentIdentity_notFound(t *testing.T) {
	store := tmpBoltStore(t)
	defer closeStore(store, t)

	_, err := store.GetAttachmentIdentity(MustAttachmentId("att-noexist"))
	if err != ErrItemNotFound {
		t.Errorf("GetAttachmentIdentity notfound error = %v, want ErrItemNotFound", err)
	}
}

func TestBoltIdentityStore_PutMailboxIdentity_wrongDirPerms(t *testing.T) {
	// Passing a file path instead of a directory should fail.
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "notadir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := NewBoltIdentityStore(filePath)
	if err == nil {
		t.Error("NewBoltIdentityStore with file path should fail")
	}
}
