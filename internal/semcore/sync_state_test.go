package semcore

import (
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func tmpBoltDBForSync(t *testing.T) *bbolt.DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_sync_state.db")
	db, err := bbolt.Open(dbPath, 0o600, nil)
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	return db
}

// ---------------------------------------------------------------------------
// BoltSyncStateStore tests
// ---------------------------------------------------------------------------

func TestBoltSyncStateStore_NewAndClose(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}
	_ = store // suppress unused variable warning
}

func TestBoltSyncStateStore_PutSyncState_newRecord(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-1")
	folderID := MustFolderId("folder-1")
	clientID := "ews-client-1"
	watermark := "watermark-abc"

	err = store.PutSyncState(mboxID, folderID, clientID, watermark)
	if err != nil {
		t.Fatalf("PutSyncState: %v", err)
	}

	rec, err := store.GetSyncState(mboxID, folderID, clientID)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if rec.MailboxID != mboxID {
		t.Errorf("MailboxID = %v, want %v", rec.MailboxID, mboxID)
	}
	if rec.FolderID != folderID {
		t.Errorf("FolderID = %v, want %v", rec.FolderID, folderID)
	}
	if rec.ClientID != clientID {
		t.Errorf("ClientID = %v, want %v", rec.ClientID, clientID)
	}
	if rec.Watermark != watermark {
		t.Errorf("Watermark = %q, want %q", rec.Watermark, watermark)
	}
	if rec.Version != 1 {
		t.Errorf("Version = %d, want 1", rec.Version)
	}
	if rec.FolderGone {
		t.Error("FolderGone should be false for new record")
	}
}

func TestBoltSyncStateStore_PutSyncState_updatesExisting(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-2")
	folderID := MustFolderId("folder-2")
	clientID := "jmap-client"

	// First write.
	err = store.PutSyncState(mboxID, folderID, clientID, "v1")
	if err != nil {
		t.Fatalf("first PutSyncState: %v", err)
	}

	// Second write advances version.
	err = store.PutSyncState(mboxID, folderID, clientID, "v2")
	if err != nil {
		t.Fatalf("second PutSyncState: %v", err)
	}

	rec, err := store.GetSyncState(mboxID, folderID, clientID)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if rec.Watermark != "v2" {
		t.Errorf("Watermark = %q, want v2", rec.Watermark)
	}
	if rec.Version != 2 {
		t.Errorf("Version = %d, want 2", rec.Version)
	}
}

func TestBoltSyncStateStore_PutSyncState_mailboxLevel(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-3")
	clientID := "imap"

	// Zero folder ID = mailbox-level token.
	err = store.PutSyncState(mboxID, FolderId{}, clientID, "imap-watermark-xyz")
	if err != nil {
		t.Fatalf("PutSyncState: %v", err)
	}

	rec, err := store.GetSyncState(mboxID, FolderId{}, clientID)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if !rec.FolderID.IsZero() {
		t.Errorf("FolderID should be zero for mailbox-level token, got %v", rec.FolderID)
	}
	if rec.Watermark != "imap-watermark-xyz" {
		t.Errorf("Watermark = %q, want imap-watermark-xyz", rec.Watermark)
	}
}

func TestBoltSyncStateStore_GetSyncState_notFound(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	_, err = store.GetSyncState(MustMailboxId("nonexistent"), MustFolderId("folder"), "client")
	if err != ErrSyncStateNotFound {
		t.Errorf("error = %v, want ErrSyncStateNotFound", err)
	}
}

func TestBoltSyncStateStore_ListSyncStatesByMailbox(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-4")
	folderID1 := MustFolderId("folder-a")
	folderID2 := MustFolderId("folder-b")

	// Write tokens for two folders and a mailbox-level token.
	store.PutSyncState(mboxID, folderID1, "client1", "w1")  //nolint:errcheck
	store.PutSyncState(mboxID, folderID2, "client1", "w2")  //nolint:errcheck
	store.PutSyncState(mboxID, FolderId{}, "client2", "w3")  //nolint:errcheck

	// List all for mailbox.
	states, err := store.ListSyncStatesByMailbox(mboxID, FolderId{})
	if err != nil {
		t.Fatalf("ListSyncStatesByMailbox: %v", err)
	}
	if len(states) != 3 {
		t.Errorf("states count = %d, want 3", len(states))
	}

	// Filter to folder-a.
	folderAStates, err := store.ListSyncStatesByMailbox(mboxID, folderID1)
	if err != nil {
		t.Fatalf("ListSyncStatesByMailbox: %v", err)
	}
	if len(folderAStates) != 1 {
		t.Errorf("folder-a states count = %d, want 1", len(folderAStates))
	}
}

func TestBoltSyncStateStore_MarkFolderGone(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-5")
	folderID := MustFolderId("folder-to-delete")

	store.PutSyncState(mboxID, folderID, "client", "w1") //nolint:errcheck

	// Mark folder gone.
	err = store.MarkFolderGone(folderID)
	if err != nil {
		t.Fatalf("MarkFolderGone: %v", err)
	}

	rec, err := store.GetSyncState(mboxID, folderID, "client")
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if !rec.FolderGone {
		t.Error("FolderGone should be true after MarkFolderGone")
	}
}

func TestBoltSyncStateStore_ListClientsForMailbox(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-6")

	store.PutSyncState(mboxID, FolderId{}, "client-a", "w1") //nolint:errcheck
	store.PutSyncState(mboxID, FolderId{}, "client-b", "w2") //nolint:errcheck
	store.PutSyncState(mboxID, FolderId{}, "client-a", "w3") //nolint:errcheck // update

	clients, err := store.ListClientsForMailbox(mboxID)
	if err != nil {
		t.Fatalf("ListClientsForMailbox: %v", err)
	}
	if len(clients) != 2 {
		t.Errorf("clients count = %d, want 2", len(clients))
	}
}

func TestBoltSyncStateStore_PutSyncState_clearsFolderGone(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-7")
	folderID := MustFolderId("folder-7")

	store.PutSyncState(mboxID, folderID, "client", "w1") //nolint:errcheck
	store.MarkFolderGone(folderID)                        //nolint:errcheck

	// New watermark write should clear FolderGone.
	store.PutSyncState(mboxID, folderID, "client", "w2") //nolint:errcheck

	rec, err := store.GetSyncState(mboxID, folderID, "client")
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if rec.FolderGone {
		t.Error("FolderGone should be cleared after new watermark write")
	}
}

// ---------------------------------------------------------------------------
// BackfillPhase tests
// ---------------------------------------------------------------------------

func TestBackfillPhase_String(t *testing.T) {
	tests := []struct {
		p    BackfillPhase
		want string
	}{
		{SeedingPhaseNone, "none"},
		{SeedingPhaseMailbox, "mailbox"},
		{SeedingPhaseFolder, "folder"},
		{SeedingPhaseItem, "item"},
		{SeedingPhaseConversation, "conversation"},
		{SeedingPhaseSyncState, "sync_state"},
		{SeedingPhaseLifecycle, "lifecycle"},
		{SeedingPhaseComplete, "complete"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("BackfillPhase(%d).String() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestBackfillPhase_IsZero(t *testing.T) {
	if !SeedingPhaseNone.IsZero() {
		t.Error("SeedingPhaseNone.IsZero() should be true")
	}
	if SeedingPhaseMailbox.IsZero() {
		t.Error("SeedingPhaseMailbox.IsZero() should be false")
	}
}

func TestBackfillPhase_AdvanceTo(t *testing.T) {
	if SeedingPhaseNone.AdvanceTo(SeedingPhaseFolder) != SeedingPhaseFolder {
		t.Error("AdvanceTo should return the higher phase")
	}
	if SeedingPhaseItem.AdvanceTo(SeedingPhaseItem) != SeedingPhaseItem {
		t.Error("AdvanceTo should return the same phase when equal")
	}
}

func TestBackfillPhase_NextPhase(t *testing.T) {
	if SeedingPhaseNone.NextPhase() != SeedingPhaseMailbox {
		t.Errorf("None.NextPhase() = %v, want SeedingPhaseMailbox", SeedingPhaseNone.NextPhase())
	}
	if SeedingPhaseLifecycle.NextPhase() != SeedingPhaseComplete {
		t.Errorf("SeedingPhaseLifecycle.NextPhase() = %v, want SeedingPhaseComplete", SeedingPhaseLifecycle.NextPhase())
	}
	if SeedingPhaseComplete.NextPhase() != SeedingPhaseComplete {
		t.Error("SeedingPhaseComplete.NextPhase() should stay at complete")
	}
}

func TestBackfillPhase_IsComplete(t *testing.T) {
	if !SeedingPhaseComplete.IsComplete() {
		t.Error("SeedingPhaseComplete.IsComplete() should be true")
	}
	if SeedingPhaseMailbox.IsComplete() {
		t.Error("SeedingPhaseMailbox.IsComplete() should be false")
	}
}

// ---------------------------------------------------------------------------
// BoltBackfillSeedingStore tests
// ---------------------------------------------------------------------------

func TestBoltBackfillSeedingStore_NewAndClose(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltBackfillSeedingStore(db)
	if err != nil {
		t.Fatalf("NewBoltBackfillSeedingStore: %v", err)
	}
	_ = store // suppress unused variable warning
}

func TestBoltBackfillSeedingStore_InitSeedingState_new(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltBackfillSeedingStore(db)
	if err != nil {
		t.Fatalf("NewBoltBackfillSeedingStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-seeding-1")
	err = store.InitSeedingState(mboxID)
	if err != nil {
		t.Fatalf("InitSeedingState: %v", err)
	}

	state, err := store.GetSeedingState(mboxID)
	if err != nil {
		t.Fatalf("GetSeedingState: %v", err)
	}
	if !state.MailboxID.Equal(mboxID) {
		t.Errorf("MailboxID = %v, want %v", state.MailboxID, mboxID)
	}
	if state.CurrentPhase != SeedingPhaseNone {
		t.Errorf("CurrentPhase = %v, want SeedingPhaseNone", state.CurrentPhase)
	}
}

func TestBoltBackfillSeedingStore_InitSeedingState_idempotent(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltBackfillSeedingStore(db)
	if err != nil {
		t.Fatalf("NewBoltBackfillSeedingStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-seeding-2")
	err = store.InitSeedingState(mboxID)
	if err != nil {
		t.Fatalf("InitSeedingState: %v", err)
	}
	// Second init should be a no-op.
	err = store.InitSeedingState(mboxID)
	if err != nil {
		t.Fatalf("second InitSeedingState: %v", err)
	}
}

func TestBoltBackfillSeedingStore_PutSeedingState(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltBackfillSeedingStore(db)
	if err != nil {
		t.Fatalf("NewBoltBackfillSeedingStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-seeding-3")
	now := time.Now().UTC()

	state := &BackfillSeedingState{
		MailboxID:     mboxID,
		CurrentPhase:  SeedingPhaseMailbox,
		MailboxDone:   true,
		FolderDone:    false,
		ItemDone:      false,
		ConvDone:      false,
		SyncStateDone: false,
		LifecycleDone: false,
		LastPhaseAt:   now,
	}

	err = store.PutSeedingState(state)
	if err != nil {
		t.Fatalf("PutSeedingState: %v", err)
	}

	retrieved, err := store.GetSeedingState(mboxID)
	if err != nil {
		t.Fatalf("GetSeedingState: %v", err)
	}
	if retrieved.CurrentPhase != SeedingPhaseMailbox {
		t.Errorf("CurrentPhase = %v, want SeedingPhaseMailbox", retrieved.CurrentPhase)
	}
	if !retrieved.MailboxDone {
		t.Error("MailboxDone should be true")
	}
	if retrieved.FolderDone {
		t.Error("FolderDone should be false")
	}
}

func TestBoltBackfillSeedingStore_GetSeedingState_notFound(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltBackfillSeedingStore(db)
	if err != nil {
		t.Fatalf("NewBoltBackfillSeedingStore: %v", err)
	}

	_, err = store.GetSeedingState(MustMailboxId("nonexistent"))
	if err != ErrMailboxNotFound {
		t.Errorf("error = %v, want ErrMailboxNotFound", err)
	}
}

func TestBoltBackfillSeedingStore_ListSeedingStates(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltBackfillSeedingStore(db)
	if err != nil {
		t.Fatalf("NewBoltBackfillSeedingStore: %v", err)
	}

	mboxID1 := MustMailboxId("mbox-list-1")
	mboxID2 := MustMailboxId("mbox-list-2")

	store.InitSeedingState(mboxID1) //nolint:errcheck
	store.InitSeedingState(mboxID2) //nolint:errcheck

	states, err := store.ListSeedingStates()
	if err != nil {
		t.Fatalf("ListSeedingStates: %v", err)
	}
	if len(states) != 2 {
		t.Errorf("states count = %d, want 2", len(states))
	}
}

func TestBoltBackfillSeedingStore_PutSeedingState_advancesPhase(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltBackfillSeedingStore(db)
	if err != nil {
		t.Fatalf("NewBoltBackfillSeedingStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-advance")

	// Set mailbox phase.
	state := &BackfillSeedingState{
		MailboxID:    mboxID,
		CurrentPhase: SeedingPhaseMailbox,
		MailboxDone:  true,
	}
	store.PutSeedingState(state) //nolint:errcheck

	// Advance to folder phase.
	state.CurrentPhase = SeedingPhaseFolder
	state.FolderDone = true
	state.LastPhaseAt = time.Now().UTC()
	store.PutSeedingState(state) //nolint:errcheck

	retrieved, _ := store.GetSeedingState(mboxID) //nolint:errcheck
	if retrieved.CurrentPhase != SeedingPhaseFolder {
		t.Errorf("CurrentPhase = %v, want SeedingPhaseFolder", retrieved.CurrentPhase)
	}
}

// ---------------------------------------------------------------------------
// Tombstone tests
// ---------------------------------------------------------------------------

func TestTombstone_IsZero(t *testing.T) {
	empty := Tombstone{}
	if !empty.IsZero() {
		t.Error("empty Tombstone should be IsZero")
	}
	tomb := Tombstone{MailboxID: MustMailboxId("mbox")}
	if tomb.IsZero() {
		t.Error("Tombstone with MailboxID should not be IsZero")
	}
}

func TestTombstone_IsFolderLevel(t *testing.T) {
	folderTomb := Tombstone{
		MailboxID: MustMailboxId("mbox"),
		FolderID:  MustFolderId("folder"),
	}
	if !folderTomb.IsFolderLevel() {
		t.Error("Tombstone with FolderID but no ItemID should be folder-level")
	}
	itemTomb := Tombstone{
		MailboxID: MustMailboxId("mbox"),
		FolderID:  MustFolderId("folder"),
		ItemID:    MustItemId("item"),
	}
	if itemTomb.IsFolderLevel() {
		t.Error("Tombstone with ItemID should not be folder-level")
	}
}

func TestTombstone_IsItemLevel(t *testing.T) {
	itemTomb := Tombstone{
		MailboxID: MustMailboxId("mbox"),
		FolderID:  MustFolderId("folder"),
		ItemID:    MustItemId("item"),
		Kind:      LifecycleKindHardDeleted,
	}
	if !itemTomb.IsItemLevel() {
		t.Error("Tombstone with ItemID should be item-level")
	}
}

// ---------------------------------------------------------------------------
// BoltTombstoneStore tests
// ---------------------------------------------------------------------------

func TestBoltTombstoneStore_NewAndClose(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltTombstoneStore(db)
	if err != nil {
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}
	_ = store // suppress unused variable warning
}

func TestBoltTombstoneStore_PutTombstone_new(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltTombstoneStore(db)
	if err != nil {
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}

	tomb := Tombstone{
		MailboxID: MustMailboxId("mbox-tomb-1"),
		FolderID:  MustFolderId("folder-tomb-1"),
		ItemID:    MustItemId("item-tomb-1"),
		Kind:      LifecycleKindSoftDeleted,
		DeletedAt: time.Now().UTC(),
		Actor:     "test-user",
	}

	err = store.PutTombstone(tomb)
	if err != nil {
		t.Fatalf("PutTombstone: %v", err)
	}

	tombstones, err := store.ListTombstonesSince(tomb.MailboxID, FolderId{}, time.Time{})
	if err != nil {
		t.Fatalf("ListTombstonesSince: %v", err)
	}
	if len(tombstones) != 1 {
		t.Errorf("tombstones count = %d, want 1", len(tombstones))
	}
	if tombstones[0].Kind != LifecycleKindSoftDeleted {
		t.Errorf("Kind = %v, want LifecycleKindSoftDeleted", tombstones[0].Kind)
	}
}

func TestBoltTombstoneStore_PutTombstone_idempotent(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltTombstoneStore(db)
	if err != nil {
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-tomb-2")
	folderID := MustFolderId("folder-tomb-2")
	itemID := MustItemId("item-tomb-2")

	tomb1 := Tombstone{
		MailboxID: mboxID,
		FolderID:  folderID,
		ItemID:    itemID,
		Kind:      LifecycleKindHardDeleted,
		DeletedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	tomb2 := Tombstone{
		MailboxID: mboxID,
		FolderID:  folderID,
		ItemID:    itemID,
		Kind:      LifecycleKindHardDeleted,
		DeletedAt: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), // later
	}

	store.PutTombstone(tomb1) //nolint:errcheck
	store.PutTombstone(tomb2) //nolint:errcheck

	tombstones, _ := store.ListTombstonesByMailbox(mboxID, FolderId{}) //nolint:errcheck
	if len(tombstones) != 1 {
		t.Errorf("tombstones count = %d, want 1 (later write wins)", len(tombstones))
	}
	// Should have the later timestamp.
	if tombstones[0].DeletedAt != tomb2.DeletedAt {
		t.Errorf("DeletedAt = %v, want %v (later write)", tombstones[0].DeletedAt, tomb2.DeletedAt)
	}
}

func TestBoltTombstoneStore_ListTombstonesSince_filtersByTime(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltTombstoneStore(db)
	if err != nil {
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-tomb-3")

	now := time.Now().UTC()
	oldTomb := Tombstone{
		MailboxID: mboxID,
		FolderID:  MustFolderId("folder-old"),
		ItemID:    MustItemId("item-old"),
		Kind:      LifecycleKindHardDeleted,
		DeletedAt: now.Add(-48 * time.Hour),
	}
	newTomb := Tombstone{
		MailboxID: mboxID,
		FolderID:  MustFolderId("folder-new"),
		ItemID:    MustItemId("item-new"),
		Kind:      LifecycleKindSoftDeleted,
		DeletedAt: now.Add(-1 * time.Hour),
	}

	store.PutTombstone(oldTomb) //nolint:errcheck
	store.PutTombstone(newTomb) //nolint:errcheck

	// Query from 24 hours ago — should only get newTomb.
	since := now.Add(-24 * time.Hour)
	tombstones, err := store.ListTombstonesSince(mboxID, FolderId{}, since)
	if err != nil {
		t.Fatalf("ListTombstonesSince: %v", err)
	}
	if len(tombstones) != 1 {
		t.Errorf("tombstones count = %d, want 1", len(tombstones))
	}
	if !tombstones[0].ItemID.Equal(newTomb.ItemID) {
		t.Errorf("expected newer tombstone, got %v", tombstones[0].ItemID)
	}
}

func TestBoltTombstoneStore_ListTombstonesByMailbox_filtersFolder(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltTombstoneStore(db)
	if err != nil {
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-tomb-4")
	folderA := MustFolderId("folder-a")
	folderB := MustFolderId("folder-b")

	store.PutTombstone(Tombstone{MailboxID: mboxID, FolderID: folderA, ItemID: MustItemId("i1"), Kind: LifecycleKindHardDeleted, DeletedAt: time.Now()}) //nolint:errcheck
	store.PutTombstone(Tombstone{MailboxID: mboxID, FolderID: folderB, ItemID: MustItemId("i2"), Kind: LifecycleKindHardDeleted, DeletedAt: time.Now()}) //nolint:errcheck

	all, _ := store.ListTombstonesByMailbox(mboxID, FolderId{}) //nolint:errcheck
	if len(all) != 2 {
		t.Errorf("all tombstones = %d, want 2", len(all))
	}

	folderATombstones, _ := store.ListTombstonesByMailbox(mboxID, folderA) //nolint:errcheck
	if len(folderATombstones) != 1 {
		t.Errorf("folder-a tombstones = %d, want 1", len(folderATombstones))
	}
}

func TestBoltTombstoneStore_PruneTombstones(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltTombstoneStore(db)
	if err != nil {
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-tomb-prune")

	oldTomb := Tombstone{
		MailboxID: mboxID,
		FolderID:  MustFolderId("folder-prune-old"),
		ItemID:    MustItemId("item-prune-old"),
		Kind:      LifecycleKindHardDeleted,
		DeletedAt: time.Now().Add(-60 * 24 * time.Hour), // 60 days old
	}
	newTomb := Tombstone{
		MailboxID: mboxID,
		FolderID:  MustFolderId("folder-prune-new"),
		ItemID:    MustItemId("item-prune-new"),
		Kind:      LifecycleKindSoftDeleted,
		DeletedAt: time.Now().Add(-5 * 24 * time.Hour), // 5 days old
	}

	store.PutTombstone(oldTomb) //nolint:errcheck
	store.PutTombstone(newTomb)  //nolint:errcheck

	pruned, err := store.PruneTombstones(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned count = %d, want 1", pruned)
	}

	remaining, _ := store.ListTombstonesByMailbox(mboxID, FolderId{}) //nolint:errcheck
	if len(remaining) != 1 {
		t.Errorf("remaining tombstones = %d, want 1", len(remaining))
	}
	if !remaining[0].ItemID.Equal(newTomb.ItemID) {
		t.Errorf("expected newTomb to survive pruning, got %v", remaining[0].ItemID)
	}
}

func TestBoltTombstoneStore_PutTombstone_requiresMailboxID(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltTombstoneStore(db)
	if err != nil {
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}

	emptyTomb := Tombstone{}
	err = store.PutTombstone(emptyTomb)
	if err == nil {
		t.Error("PutTombstone with zero MailboxID should error")
	}
}

func TestBoltTombstoneStore_distinguishesSoftAndHardDelete(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltTombstoneStore(db)
	if err != nil {
		t.Fatalf("NewBoltTombstoneStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-tomb-kind")
	folderID := MustFolderId("folder-kind")
	itemID := MustItemId("item-kind")
	now := time.Now().UTC()

	softTomb := Tombstone{
		MailboxID: mboxID,
		FolderID:  folderID,
		ItemID:    itemID,
		Kind:      LifecycleKindSoftDeleted,
		DeletedAt: now,
	}
	hardTomb := Tombstone{
		MailboxID: mboxID,
		FolderID:  folderID,
		ItemID:    itemID,
		Kind:      LifecycleKindHardDeleted,
		DeletedAt: now,
	}

	store.PutTombstone(softTomb)  //nolint:errcheck
	store.PutTombstone(hardTomb)   //nolint:errcheck

	tombstones, _ := store.ListTombstonesByMailbox(mboxID, folderID) //nolint:errcheck
	if len(tombstones) != 2 {
		t.Errorf("tombstones count = %d, want 2 (separate soft and hard)", len(tombstones))
	}
}

func TestBoltSyncStateStore_PutSyncState_multipleClients(t *testing.T) {
	db := tmpBoltDBForSync(t)
	defer func() { _ = db.Close() }() //nolint:errcheck

	store, err := NewBoltSyncStateStore(db)
	if err != nil {
		t.Fatalf("NewBoltSyncStateStore: %v", err)
	}

	mboxID := MustMailboxId("mbox-multi-client")
	folderID := MustFolderId("folder-multi")

	clients := []string{"ews", "jmap", "imap"}
	for i, client := range clients {
		err := store.PutSyncState(mboxID, folderID, client, "watermark-"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("PutSyncState for %s: %v", client, err)
		}
	}

	for _, client := range clients {
		rec, err := store.GetSyncState(mboxID, folderID, client)
		if err != nil {
			t.Fatalf("GetSyncState for %s: %v", client, err)
		}
		if rec.Watermark == "" {
			t.Errorf("Watermark for %s should not be empty", client)
		}
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func TestSyncStateKey_unique(t *testing.T) {
	// Same IDs in different order produce different keys.
	mboxID := MustMailboxId("mbox")
	folderID := MustFolderId("folder")
	client := "client"

	// Verify key format is deterministic.
	k1 := syncStateKey(mboxID, folderID, client)
	k2 := syncStateKey(mboxID, folderID, client)
	if k1 != k2 {
		t.Error("syncStateKey should be deterministic")
	}

	// Different folder produces different key.
	k3 := syncStateKey(mboxID, MustFolderId("other-folder"), client)
	if k1 == k3 {
		t.Error("different folders should produce different keys")
	}
}
