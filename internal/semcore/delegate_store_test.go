package semcore

import (
	"path/filepath"
	"testing"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func tmpBoltDBForDelegate(t *testing.T) *bbolt.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_delegate.db")
	db, err := bbolt.Open(dbPath, 0o600, nil)
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	return db
}

func newBoltDelegateStoreForTest(t *testing.T) (*BoltDelegateStore, func()) {
	t.Helper()
	db := tmpBoltDBForDelegate(t)

	// Create the delegations bucket.
	err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketDelegations))
		return err
	})
	if err != nil {
		_ = db.Close() //nolint:errcheck
		t.Fatalf("create delegations bucket: %v", err)
	}

	store, err := NewBoltDelegateStore(db)
	if err != nil {
		_ = db.Close() //nolint:errcheck
		t.Fatalf("NewBoltDelegateStore: %v", err)
	}
	return store, func() { _ = db.Close() } //nolint:errcheck
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestBoltDelegateStore_PutAndGet(t *testing.T) {
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	ownerID := MustMailboxId("mbox-owner-1")
	delegate := &DelegateUser{
		OwnerID:        ownerID,
		DelegateEmail:  "delegate@example.com",
		DelegateUserID: "delegate@example.com",
		Permissions: DelegateFolderPermissions{
			Calendar: DelegateFolderPermissionAuthor,
			Inbox:    DelegateFolderPermissionReviewer,
		},
		ViewPrivateItems: true,
		ReceiveCopies:    true,
		DeliverRequests:  DeliverDelegatesAndMe,
		GrantedBy:       "owner@example.com",
	}

	id, err := store.PutDelegate(delegate)
	if err != nil {
		t.Fatalf("PutDelegate: %v", err)
	}
	if id.IsZero() {
		t.Fatal("PutDelegate returned zero ID")
	}

	got, err := store.GetDelegate(id)
	if err != nil {
		t.Fatalf("GetDelegate: %v", err)
	}
	if got.DelegateEmail != "delegate@example.com" {
		t.Errorf("DelegateEmail = %q, want %q", got.DelegateEmail, "delegate@example.com")
	}
	if got.Permissions.Calendar != DelegateFolderPermissionAuthor {
		t.Errorf("Calendar permission = %q, want %q", got.Permissions.Calendar, DelegateFolderPermissionAuthor)
	}
	if got.Permissions.Inbox != DelegateFolderPermissionReviewer {
		t.Errorf("Inbox permission = %q, want %q", got.Permissions.Inbox, DelegateFolderPermissionReviewer)
	}
	if !got.ViewPrivateItems {
		t.Error("ViewPrivateItems = false, want true")
	}
}

func TestBoltDelegateStore_ListDelegates(t *testing.T) {
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	ownerID := MustMailboxId("mbox-owner-list")

	for i := 0; i < 2; i++ {
		delegate := &DelegateUser{
			OwnerID:       ownerID,
			DelegateEmail: string([]byte{byte('a' + i)}) + "@example.com",
			Permissions: DelegateFolderPermissions{
				Inbox: DelegateFolderPermissionAuthor,
			},
		}
		if _, err := store.PutDelegate(delegate); err != nil {
			t.Fatalf("PutDelegate[%d]: %v", i, err)
		}
	}

	delegates, err := store.ListDelegates(ownerID)
	if err != nil {
		t.Fatalf("ListDelegates: %v", err)
	}
	if len(delegates) != 2 {
		t.Errorf("len(delegates) = %d, want 2", len(delegates))
	}
}

func TestBoltDelegateStore_GetDelegateForUser(t *testing.T) {
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	ownerID := MustMailboxId("mbox-owner-getforuser")
	delegate := &DelegateUser{
		OwnerID:       ownerID,
		DelegateEmail: "specific@example.com",
		Permissions: DelegateFolderPermissions{
			Inbox: DelegateFolderPermissionAuthor,
		},
	}
	if _, err := store.PutDelegate(delegate); err != nil {
		t.Fatalf("PutDelegate: %v", err)
	}

	got, err := store.GetDelegateForUser(ownerID, "specific@example.com")
	if err != nil {
		t.Fatalf("GetDelegateForUser: %v", err)
	}
	if got.DelegateEmail != "specific@example.com" {
		t.Errorf("DelegateEmail = %q, want %q", got.DelegateEmail, "specific@example.com")
	}

	// Non-existent delegate should error.
	_, err = store.GetDelegateForUser(ownerID, "notadelegate@example.com")
	if err == nil {
		t.Error("GetDelegateForUser for non-existent delegate: expected error, got nil")
	}
}

func TestBoltDelegateStore_RemoveDelegate(t *testing.T) {
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	ownerID := MustMailboxId("mbox-owner-remove")
	delegate := &DelegateUser{
		OwnerID:       ownerID,
		DelegateEmail: "todelete@example.com",
		Permissions: DelegateFolderPermissions{
			Inbox: DelegateFolderPermissionAuthor,
		},
	}
	id, err := store.PutDelegate(delegate)
	if err != nil {
		t.Fatalf("PutDelegate: %v", err)
	}

	if err := store.RemoveDelegate(id); err != nil {
		t.Fatalf("RemoveDelegate: %v", err)
	}

	_, err = store.GetDelegate(id)
	if err == nil {
		t.Error("GetDelegate after removal: expected error, got nil")
	}
}

func TestBoltDelegateStore_ListMailboxesSharedViaDelegate(t *testing.T) {
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	alice1 := MustMailboxId("alice-mailbox-1")
	alice2 := MustMailboxId("alice-mailbox-2")

	for _, ownerID := range []MailboxId{alice1, alice2} {
		delegate := &DelegateUser{
			OwnerID:       ownerID,
			DelegateEmail: "bob@example.com",
			Permissions: DelegateFolderPermissions{
				Inbox: DelegateFolderPermissionAuthor,
			},
		}
		if _, err := store.PutDelegate(delegate); err != nil {
			t.Fatalf("PutDelegate for %s: %v", ownerID.String(), err)
		}
	}

	shared, err := store.ListMailboxesSharedViaDelegate("bob@example.com")
	if err != nil {
		t.Fatalf("ListMailboxesSharedViaDelegate: %v", err)
	}
	if len(shared) != 2 {
		t.Errorf("len(shared) = %d, want 2", len(shared))
	}
}

func TestBoltDelegateStore_DuplicateIsUpdate(t *testing.T) {
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	ownerID := MustMailboxId("mbox-owner-dup")
	delegate1 := &DelegateUser{
		OwnerID:       ownerID,
		DelegateEmail: "sam@example.com",
		Permissions: DelegateFolderPermissions{
			Inbox: DelegateFolderPermissionAuthor,
		},
	}
	id1, err := store.PutDelegate(delegate1)
	if err != nil {
		t.Fatalf("PutDelegate[1]: %v", err)
	}

	delegate2 := &DelegateUser{
		OwnerID:       ownerID,
		DelegateEmail: "sam@example.com",
		Permissions: DelegateFolderPermissions{
			Inbox: DelegateFolderPermissionReviewer,
		},
	}
	id2, err := store.PutDelegate(delegate2)
	if err != nil {
		t.Fatalf("PutDelegate[2] (update): %v", err)
	}

	// Same grant ID on update.
	if !id1.Equal(id2) {
		t.Errorf("updated ID = %v, want same as original %v", id2, id1)
	}

	// Only one delegate.
	delegates, err := store.ListDelegates(ownerID)
	if err != nil {
		t.Fatalf("ListDelegates: %v", err)
	}
	if len(delegates) != 1 {
		t.Errorf("len(delegates) = %d, want 1", len(delegates))
	}
	if delegates[0].Permissions.Inbox != DelegateFolderPermissionReviewer {
		t.Errorf("Inbox permission = %q, want %q (updated)", delegates[0].Permissions.Inbox, DelegateFolderPermissionReviewer)
	}
}

func TestBoltDelegateStore_EmptyList(t *testing.T) {
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	delegates, err := store.ListDelegates(MustMailboxId("empty-owner"))
	if err != nil {
		t.Fatalf("ListDelegates: %v", err)
	}
	if len(delegates) != 0 {
		t.Errorf("len(delegates) = %d, want 0", len(delegates))
	}
}

func TestBoltDelegateStore_CaseInsensitive(t *testing.T) {
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	ownerID := MustMailboxId("owner-case@test.com")
	delegate := &DelegateUser{
		OwnerID:       ownerID,
		DelegateEmail: "Delegate@Example.COM",
		Permissions: DelegateFolderPermissions{
			Inbox: DelegateFolderPermissionAuthor,
		},
	}
	id, err := store.PutDelegate(delegate)
	if err != nil {
		t.Fatalf("PutDelegate: %v", err)
	}

	// Retrieve with different case.
	got, err := store.GetDelegateForUser(ownerID, "DELEGATE@example.com")
	if err != nil {
		t.Fatalf("GetDelegateForUser with different case: %v", err)
	}
	if !got.ID.Equal(id) {
		t.Errorf("GetDelegateForUser returned wrong ID %v, want %v", got.ID, id)
	}
}

func TestBoltDelegateStore_ZeroInputs(t *testing.T) {
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	// Zero owner ID.
	_, err := store.ListDelegates(MailboxId{})
	if err == nil {
		t.Error("ListDelegates with zero owner: expected error")
	}

	// Empty email for shared discovery.
	_, err = store.ListMailboxesSharedViaDelegate("")
	if err == nil {
		t.Error("ListMailboxesSharedViaDelegate with empty email: expected error")
	}

	// Get with zero ID.
	_, err = store.GetDelegate(DelegateId{})
	if err == nil {
		t.Error("GetDelegate with zero ID: expected error")
	}

	// Remove with zero ID.
	err = store.RemoveDelegate(DelegateId{})
	if err == nil {
		t.Error("RemoveDelegate with zero ID: expected error")
	}
}

func TestBoltDelegateStore_SharedMailboxRequiresGrant(t *testing.T) {
	// VAL-DIR-001: shared mailbox discovery requires an explicit grant.
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	// Without a grant, bob sees no mailboxes.
	shared, err := store.ListMailboxesSharedViaDelegate("bob@example.com")
	if err != nil {
		t.Fatalf("ListMailboxesSharedViaDelegate: %v", err)
	}
	if len(shared) != 0 {
		t.Errorf("bob with no grants: len(shared) = %d, want 0", len(shared))
	}

	// After grant, bob sees the mailbox.
	ownerID := MustMailboxId("alice-shared-mailbox")
	delegate := &DelegateUser{
		OwnerID:       ownerID,
		DelegateEmail: "bob@example.com",
		Permissions: DelegateFolderPermissions{
			Inbox: DelegateFolderPermissionAuthor,
		},
	}
	if _, err := store.PutDelegate(delegate); err != nil {
		t.Fatalf("PutDelegate: %v", err)
	}

	shared, err = store.ListMailboxesSharedViaDelegate("bob@example.com")
	if err != nil {
		t.Fatalf("ListMailboxesSharedViaDelegate after grant: %v", err)
	}
	if len(shared) != 1 {
		t.Errorf("bob with one grant: len(shared) = %d, want 1", len(shared))
	}
}

func TestDelegateFolderPermissions_HasAccess(t *testing.T) {
	tests := []struct {
		name      string
		perms     DelegateFolderPermissions
		hasAccess bool
	}{
		{"empty permissions", DelegateFolderPermissions{}, false},
		{"calendar reviewer", DelegateFolderPermissions{Calendar: DelegateFolderPermissionReviewer}, true},
		{"inbox author", DelegateFolderPermissions{Inbox: DelegateFolderPermissionAuthor}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.perms.HasAccess(); got != tt.hasAccess {
				t.Errorf("HasAccess() = %v, want %v", got, tt.hasAccess)
			}
		})
	}
}

func TestDelegateFolderPermissions_CanReadCalendar(t *testing.T) {
	tests := []struct {
		level   DelegateFolderPermissionLevel
		canRead bool
	}{
		{DelegateFolderPermissionNone, false},
		// Reviewer = read-only; Author and Delegate = can write, therefore can read.
		{DelegateFolderPermissionReviewer, true},
		{DelegateFolderPermissionAuthor, true},
		{DelegateFolderPermissionDelegate, true},
	}

	for _, tt := range tests {
		perms := DelegateFolderPermissions{Calendar: tt.level}
		if got := perms.CanReadCalendar(); got != tt.canRead {
			t.Errorf("CanReadCalendar with %q = %v, want %v", tt.level, got, tt.canRead)
		}
	}
}

func TestDelegateFolderPermissions_CanWriteCalendar(t *testing.T) {
	tests := []struct {
		level    DelegateFolderPermissionLevel
		canWrite bool
	}{
		{DelegateFolderPermissionNone, false},
		{DelegateFolderPermissionReviewer, false},
		{DelegateFolderPermissionAuthor, true},
		{DelegateFolderPermissionDelegate, true},
	}

	for _, tt := range tests {
		perms := DelegateFolderPermissions{Calendar: tt.level}
		if got := perms.CanWriteCalendar(); got != tt.canWrite {
			t.Errorf("CanWriteCalendar with %q = %v, want %v", tt.level, got, tt.canWrite)
		}
	}
}

// ---------------------------------------------------------------------------
// VAL-DIR-004 / VAL-DIR-005 send-as and send-on-behalf tests
// ---------------------------------------------------------------------------

func TestDelegateUser_CanSendAs_NotImpliedByFolderPermissions(t *testing.T) {
	// VAL-DIR-004: send-as is NOT implied by general mailbox access.
	// A delegate with folder permissions but no explicit CanSendAs flag
	// must have CanSendAs == false.
	ownerID := MustMailboxId("owner-val-dir-004")
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	delegate := &DelegateUser{
		OwnerID:        ownerID,
		DelegateEmail:  "bob@example.com",
		DelegateUserID: "bob@example.com",
		Permissions: DelegateFolderPermissions{
			Calendar: DelegateFolderPermissionAuthor,
			Inbox:    DelegateFolderPermissionAuthor,
		},
		CanSendAs:       false, // explicit; no send-as grant
		CanSendOnBehalf: false,
	}
	if _, err := store.PutDelegate(delegate); err != nil {
		t.Fatalf("PutDelegate: %v", err)
	}

	got, err := store.GetDelegateForUser(ownerID, "bob@example.com")
	if err != nil {
		t.Fatalf("GetDelegateForUser: %v", err)
	}
	if got.CanSendAs {
		t.Errorf("delegate with folder permissions but CanSendAs=false: got.CanSendAs = true, want false")
	}
	if got.CanSendOnBehalf {
		t.Errorf("delegate with folder permissions but CanSendOnBehalf=false: got.CanSendOnBehalf = true, want false")
	}
}

func TestDelegateUser_CanSendAs_RequiresExplicitGrant(t *testing.T) {
	// VAL-DIR-004: send-as requires an explicit CanSendAs grant.
	ownerID := MustMailboxId("owner-sendas")
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	delegate := &DelegateUser{
		OwnerID:        ownerID,
		DelegateEmail:  "alice@example.com",
		DelegateUserID: "alice@example.com",
		Permissions: DelegateFolderPermissions{
			Inbox: DelegateFolderPermissionAuthor,
		},
		CanSendAs:       true, // explicit send-as grant
		CanSendOnBehalf: false,
	}
	if _, err := store.PutDelegate(delegate); err != nil {
		t.Fatalf("PutDelegate: %v", err)
	}

	got, err := store.GetDelegateForUser(ownerID, "alice@example.com")
	if err != nil {
		t.Fatalf("GetDelegateForUser: %v", err)
	}
	if !got.CanSendAs {
		t.Errorf("delegate with explicit CanSendAs=true: got.CanSendAs = false, want true")
	}
	if got.CanSendOnBehalf {
		t.Errorf("delegate with CanSendOnBehalf=false: got.CanSendOnBehalf = true, want false")
	}
}

func TestDelegateUser_CanSendOnBehalf_PreservedDistinctly(t *testing.T) {
	// VAL-DIR-005: send-on-behalf preserves represented identity distinctly from send-as.
	ownerID := MustMailboxId("owner-sob")
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	// Grant only send-on-behalf, not send-as.
	delegate := &DelegateUser{
		OwnerID:        ownerID,
		DelegateEmail:  "carol@example.com",
		DelegateUserID: "carol@example.com",
		Permissions: DelegateFolderPermissions{
			Inbox: DelegateFolderPermissionAuthor,
		},
		CanSendAs:       false,
		CanSendOnBehalf: true, // explicit send-on-behalf grant
	}
	if _, err := store.PutDelegate(delegate); err != nil {
		t.Fatalf("PutDelegate: %v", err)
	}

	got, err := store.GetDelegateForUser(ownerID, "carol@example.com")
	if err != nil {
		t.Fatalf("GetDelegateForUser: %v", err)
	}
	if got.CanSendAs {
		t.Errorf("delegate with only CanSendOnBehalf=true: got.CanSendAs = true, want false")
	}
	if !got.CanSendOnBehalf {
		t.Errorf("delegate with CanSendOnBehalf=true: got.CanSendOnBehalf = false, want true")
	}
}

func TestDelegateUser_CanSendAs_UpdatePersists(t *testing.T) {
	// Test that updating CanSendAs through PutDelegate persists correctly.
	ownerID := MustMailboxId("owner-sendas-update")
	store, cleanup := newBoltDelegateStoreForTest(t)
	defer cleanup()

	// Start without send-as.
	delegate := &DelegateUser{
		OwnerID:        ownerID,
		DelegateEmail:  "dave@example.com",
		DelegateUserID: "dave@example.com",
		Permissions: DelegateFolderPermissions{
			Inbox: DelegateFolderPermissionAuthor,
		},
		CanSendAs: false,
	}
	id, err := store.PutDelegate(delegate)
	if err != nil {
		t.Fatalf("PutDelegate: %v", err)
	}

	// Add send-as in update.
	delegate.CanSendAs = true
	_, err = store.PutDelegate(delegate)
	if err != nil {
		t.Fatalf("PutDelegate update: %v", err)
	}

	got, err := store.GetDelegate(id)
	if err != nil {
		t.Fatalf("GetDelegate: %v", err)
	}
	if !got.CanSendAs {
		t.Errorf("after update: got.CanSendAs = false, want true")
	}
}
