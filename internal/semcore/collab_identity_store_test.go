package semcore

import (
	"testing"
)

// closeCollabStore closes an identity store (helper for tests).
// The error is intentionally ignored in cleanup paths.
func closeCollabStore(store *BoltIdentityStore, t *testing.T) {
	if err := store.Close(); err != nil { //nolint:errcheck
		t.Logf("store.Close(): %v", err)
	}
}

// ---------------------------------------------------------------------------
// CalendarItem identity CRUD
// ---------------------------------------------------------------------------

func TestBoltCollaborationStore_PutCalendarItemIdentity(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	//nolint:errcheck
	itemID, _ := NewCalendarItemId("cal-item-1")
	//nolint:errcheck
	folderID := MustFolderId("folder-cal")
	//nolint:errcheck
	mboxID := MustMailboxId("mbox-cal")
	//nolint:errcheck
	ck, _ := NewCalendarChangeKey("ck-cal-item-1")

	rec := &StoredCalendarItemIdentity{
		ID:        itemID,
		FolderID:  folderID,
		MailboxID: mboxID,
		ChangeKey: ck,
		Kind:      CollabKindEvent,
		IcalUID:   "uid-cal-item-1",
		RawHash:   "abc123",
		ETag:      "ck-cal-item-1",
	}

	// Insert.
	if err := collab.PutCalendarItemIdentity("msg-key-1", rec, CalendarChangeKey{}); err != nil {
		t.Fatalf("PutCalendarItemIdentity insert: %v", err)
	}

	// Retrieve.
	got, err := collab.GetCalendarItemIdentity("msg-key-1")
	if err != nil {
		t.Fatalf("GetCalendarItemIdentity: %v", err)
	}
	if !got.ID.Equal(itemID) {
		t.Errorf("CalendarItemIdentity.ID = %v, want %v", got.ID, itemID)
	}
	if !got.ChangeKey.Equal(ck) {
		t.Errorf("CalendarItemIdentity.ChangeKey = %v, want %v", got.ChangeKey, ck)
	}

	// Update with stale key should fail.
	//nolint:errcheck
	staleCK, _ := NewCalendarChangeKey("stale-ck")
	if err := collab.PutCalendarItemIdentity("msg-key-1", rec, staleCK); err != ErrCollabVersionConflict {
		t.Errorf("PutCalendarItemIdentity with stale key: got %v, want ErrCollabVersionConflict", err)
	}

	// Update with correct key should succeed.
	//nolint:errcheck
	newCK, _ := NewCalendarChangeKey("ck-cal-item-1-updated")
	rec.ChangeKey = newCK
	rec.ETag = newCK.String()
	if err := collab.PutCalendarItemIdentity("msg-key-1", rec, ck); err != nil {
		t.Fatalf("PutCalendarItemIdentity update with correct key: %v", err)
	}

	// Verify updated.
	got, err = collab.GetCalendarItemIdentity("msg-key-1")
	if err != nil {
		t.Fatalf("GetCalendarItemIdentity after update: %v", err)
	}
	if !got.ChangeKey.Equal(newCK) {
		t.Errorf("After update: ChangeKey = %v, want %v", got.ChangeKey, newCK)
	}
}

func TestBoltCollaborationStore_GetCalendarItemIdentity_notFound(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	_, err = collab.GetCalendarItemIdentity("nonexistent-key")
	if err != ErrCalendarItemNotFound {
		t.Errorf("GetCalendarItemIdentity nonexistent: got %v, want ErrCalendarItemNotFound", err)
	}
}

func TestBoltCollaborationStore_ListCalendarItemsByFolder(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	//nolint:errcheck
	folderID := MustFolderId("folder-list-test")
	//nolint:errcheck
	mboxID := MustMailboxId("mbox-list")

	for i := 0; i < 3; i++ {
		//nolint:errcheck
		itemID, _ := NewCalendarItemId("cal-item-list-" + itoa(i))
		//nolint:errcheck
		ck, _ := NewCalendarChangeKey("ck-list-" + itoa(i))
		rec := &StoredCalendarItemIdentity{
			ID: itemID, FolderID: folderID, MailboxID: mboxID,
			ChangeKey: ck, Kind: CollabKindEvent,
		}
		if err := collab.PutCalendarItemIdentity("msg-key-list-"+itoa(i), rec, CalendarChangeKey{}); err != nil {
			t.Fatalf("PutCalendarItemIdentity: %v", err)
		}
	}

	items, err := collab.ListCalendarItemsByFolder(folderID)
	if err != nil {
		t.Fatalf("ListCalendarItemsByFolder: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("ListCalendarItemsByFolder count = %d, want 3", len(items))
	}
}

func TestBoltCollaborationStore_DeleteCalendarItemIdentity(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	//nolint:errcheck
	itemID, _ := NewCalendarItemId("cal-item-del")
	//nolint:errcheck
	folderID := MustFolderId("folder-del")
	//nolint:errcheck
	mboxID := MustMailboxId("mbox-del")
	//nolint:errcheck
	ck, _ := NewCalendarChangeKey("ck-del")

	rec := &StoredCalendarItemIdentity{
		ID: itemID, FolderID: folderID, MailboxID: mboxID,
		ChangeKey: ck, Kind: CollabKindEvent,
	}
	if err := collab.PutCalendarItemIdentity("msg-key-del", rec, CalendarChangeKey{}); err != nil {
		t.Fatalf("PutCalendarItemIdentity: %v", err)
	}

	// Delete with correct key.
	if err := collab.DeleteCalendarItemIdentity("msg-key-del", ck); err != nil {
		t.Fatalf("DeleteCalendarItemIdentity: %v", err)
	}

	// Verify gone.
	_, err = collab.GetCalendarItemIdentity("msg-key-del")
	if err != ErrCalendarItemNotFound {
		t.Errorf("After delete: got %v, want ErrCalendarItemNotFound", err)
	}

	// Delete with wrong key should fail.
	if err := collab.PutCalendarItemIdentity("msg-key-del-2", rec, CalendarChangeKey{}); err != nil {
		t.Fatalf("PutCalendarItemIdentity re-insert: %v", err)
	}
	//nolint:errcheck
	wrongCK, _ := NewCalendarChangeKey("wrong-ck")
	if err := collab.DeleteCalendarItemIdentity("msg-key-del-2", wrongCK); err != ErrCollabVersionConflict {
		t.Errorf("DeleteCalendarItemIdentity with wrong key: got %v, want ErrCollabVersionConflict", err)
	}
}

// ---------------------------------------------------------------------------
// Contact identity CRUD
// ---------------------------------------------------------------------------

func TestBoltCollaborationStore_PutContactIdentity(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	//nolint:errcheck
	contactID, _ := NewContactId("contact-1")
	//nolint:errcheck
	folderID := MustFolderId("folder-contact")
	//nolint:errcheck
	mboxID := MustMailboxId("mbox-contact")
	//nolint:errcheck
	ck, _ := NewContactChangeKey("ck-contact-1")

	rec := &StoredContactIdentity{
		ID: contactID, FolderID: folderID, MailboxID: mboxID,
		ChangeKey: ck, IcalUID: "uid-contact-1",
		ETag: "ck-contact-1",
	}

	if err := collab.PutContactIdentity("contact-key-1", rec, ContactChangeKey{}); err != nil {
		t.Fatalf("PutContactIdentity insert: %v", err)
	}

	got, err := collab.GetContactIdentity("contact-key-1")
	if err != nil {
		t.Fatalf("GetContactIdentity: %v", err)
	}
	if !got.ID.Equal(contactID) {
		t.Errorf("ContactIdentity.ID = %v, want %v", got.ID, contactID)
	}
}

func TestBoltCollaborationStore_GetContactIdentity_notFound(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	_, err = collab.GetContactIdentity("nonexistent-contact")
	if err != ErrContactNotFound {
		t.Errorf("GetContactIdentity nonexistent: got %v, want ErrContactNotFound", err)
	}
}

func TestBoltCollaborationStore_ListContactsByFolder(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	//nolint:errcheck
	folderID := MustFolderId("folder-contacts")
	//nolint:errcheck
	mboxID := MustMailboxId("mbox-contacts")

	for i := 0; i < 2; i++ {
		//nolint:errcheck
		contactID, _ := NewContactId("contact-list-" + itoa(i))
		//nolint:errcheck
		ck, _ := NewContactChangeKey("ck-contact-list-" + itoa(i))
		rec := &StoredContactIdentity{
			ID: contactID, FolderID: folderID, MailboxID: mboxID,
			ChangeKey: ck,
		}
		if err := collab.PutContactIdentity("contact-key-list-"+itoa(i), rec, ContactChangeKey{}); err != nil {
			t.Fatalf("PutContactIdentity: %v", err)
		}
	}

	contacts, err := collab.ListContactsByFolder(folderID)
	if err != nil {
		t.Fatalf("ListContactsByFolder: %v", err)
	}
	if len(contacts) != 2 {
		t.Errorf("ListContactsByFolder count = %d, want 2", len(contacts))
	}
}

// ---------------------------------------------------------------------------
// Task identity CRUD
// ---------------------------------------------------------------------------

func TestBoltCollaborationStore_PutTaskIdentity(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	//nolint:errcheck
	taskID, _ := NewTaskId("task-1")
	//nolint:errcheck
	folderID := MustFolderId("folder-task")
	//nolint:errcheck
	mboxID := MustMailboxId("mbox-task")
	//nolint:errcheck
	ck, _ := NewTaskChangeKey("ck-task-1")

	rec := &StoredTaskIdentity{
		ID: taskID, FolderID: folderID, MailboxID: mboxID,
		ChangeKey: ck, IcalUID: "uid-task-1",
		ETag: "ck-task-1",
	}

	if err := collab.PutTaskIdentity("task-key-1", rec, TaskChangeKey{}); err != nil {
		t.Fatalf("PutTaskIdentity insert: %v", err)
	}

	got, err := collab.GetTaskIdentity("task-key-1")
	if err != nil {
		t.Fatalf("GetTaskIdentity: %v", err)
	}
	if !got.ID.Equal(taskID) {
		t.Errorf("TaskIdentity.ID = %v, want %v", got.ID, taskID)
	}
}

func TestBoltCollaborationStore_GetTaskIdentity_notFound(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	_, err = collab.GetTaskIdentity("nonexistent-task")
	if err != ErrTaskNotFound {
		t.Errorf("GetTaskIdentity nonexistent: got %v, want ErrTaskNotFound", err)
	}
}

func TestBoltCollaborationStore_ListTasksByFolder(t *testing.T) {
	tmp := t.TempDir()
	identityStore, err := NewBoltIdentityStore(tmp)
	if err != nil {
		t.Fatalf("NewBoltIdentityStore: %v", err)
	}
	defer closeCollabStore(identityStore, t)

	collab, err := NewBoltCollaborationStore(identityStore.db)
	if err != nil {
		t.Fatalf("NewBoltCollaborationStore: %v", err)
	}

	//nolint:errcheck
	folderID := MustFolderId("folder-tasks")
	//nolint:errcheck
	mboxID := MustMailboxId("mbox-tasks")

	for i := 0; i < 2; i++ {
		//nolint:errcheck
		taskID, _ := NewTaskId("task-list-" + itoa(i))
		//nolint:errcheck
		ck, _ := NewTaskChangeKey("ck-task-list-" + itoa(i))
		rec := &StoredTaskIdentity{
			ID: taskID, FolderID: folderID, MailboxID: mboxID,
			ChangeKey: ck,
		}
		if err := collab.PutTaskIdentity("task-key-list-"+itoa(i), rec, TaskChangeKey{}); err != nil {
			t.Fatalf("PutTaskIdentity: %v", err)
		}
	}

	tasks, err := collab.ListTasksByFolder(folderID)
	if err != nil {
		t.Fatalf("ListTasksByFolder: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("ListTasksByFolder count = %d, want 2", len(tasks))
	}
}
