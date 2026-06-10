package db

import (
	"testing"
	"time"
)

func TestDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	t.Run("AccountOperations", func(t *testing.T) {
		account := &AccountData{
			Email:        "test@example.com",
			LocalPart:    "test",
			Domain:       "example.com",
			PasswordHash: "argon2:...",
			QuotaLimit:   5 * 1024 * 1024 * 1024, // 5GB
			IsActive:     true,
		}

		// Create
		if err := db.CreateAccount(account); err != nil {
			t.Fatalf("CreateAccount failed: %v", err)
		}

		// Get
		retrieved, err := db.GetAccount("example.com", "test")
		if err != nil {
			t.Fatalf("GetAccount failed: %v", err)
		}
		if retrieved.Email != account.Email {
			t.Errorf("expected email %s, got %s", account.Email, retrieved.Email)
		}

		// Update
		retrieved.QuotaUsed = 1024
		if err := db.UpdateAccount(retrieved); err != nil {
			t.Fatalf("UpdateAccount failed: %v", err)
		}

		// List
		accounts, err := db.ListAccountsByDomain("example.com")
		if err != nil {
			t.Fatalf("ListAccountsByDomain failed: %v", err)
		}
		if len(accounts) != 1 {
			t.Errorf("expected 1 account, got %d", len(accounts))
		}

		// Delete
		if err := db.DeleteAccount("example.com", "test"); err != nil {
			t.Fatalf("DeleteAccount failed: %v", err)
		}

		_, err = db.GetAccount("example.com", "test")
		if err == nil {
			t.Error("expected error after delete")
		}
	})

	t.Run("DomainOperations", func(t *testing.T) {
		domain := &DomainData{
			Name:           "example.com",
			MaxAccounts:    100,
			MaxMailboxSize: 5 * 1024 * 1024 * 1024,
			DKIMSelector:   "default",
			IsActive:       true,
		}

		// Create
		if err := db.CreateDomain(domain); err != nil {
			t.Fatalf("CreateDomain failed: %v", err)
		}

		// Get
		retrieved, err := db.GetDomain("example.com")
		if err != nil {
			t.Fatalf("GetDomain failed: %v", err)
		}
		if retrieved.Name != domain.Name {
			t.Errorf("expected name %s, got %s", domain.Name, retrieved.Name)
		}

		// List
		domains, err := db.ListDomains()
		if err != nil {
			t.Fatalf("ListDomains failed: %v", err)
		}
		if len(domains) != 1 {
			t.Errorf("expected 1 domain, got %d", len(domains))
		}

		// Delete
		if err := db.DeleteDomain("example.com"); err != nil {
			t.Fatalf("DeleteDomain failed: %v", err)
		}
	})

	t.Run("QueueOperations", func(t *testing.T) {
		entry := &QueueEntry{
			ID:          "msg-123",
			From:        "sender@example.com",
			To:          []string{"recipient@example.com"},
			MessagePath: "/tmp/msg-123",
			Status:      "pending",
			NextRetry:   time.Now(),
		}

		// Enqueue
		if err := db.Enqueue(entry); err != nil {
			t.Fatalf("Enqueue failed: %v", err)
		}

		// Get
		retrieved, err := db.GetQueueEntry("msg-123")
		if err != nil {
			t.Fatalf("GetQueueEntry failed: %v", err)
		}
		if retrieved.ID != entry.ID {
			t.Errorf("expected ID %s, got %s", entry.ID, retrieved.ID)
		}

		// GetPendingQueue
		pending, err := db.GetPendingQueue(time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("GetPendingQueue failed: %v", err)
		}
		if len(pending) != 1 {
			t.Errorf("expected 1 pending entry, got %d", len(pending))
		}

		// Dequeue
		if err := db.Dequeue("msg-123"); err != nil {
			t.Fatalf("Dequeue failed: %v", err)
		}

		_, err = db.GetQueueEntry("msg-123")
		if err == nil {
			t.Error("expected error after dequeue")
		}
	})
}

func TestAccountKey(t *testing.T) {
	key := AccountKey("example.com", "user")
	expected := "example.com/user"
	if key != expected {
		t.Errorf("expected %s, got %s", expected, key)
	}
}

// TestUpdateDomain tests updating a domain
func TestUpdateDomain(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create a domain
	domain := &DomainData{
		Name:           "update-test.com",
		MaxAccounts:    100,
		MaxMailboxSize: 5 * 1024 * 1024 * 1024,
		DKIMSelector:   "default",
		IsActive:       true,
	}

	if err := db.CreateDomain(domain); err != nil {
		t.Fatalf("CreateDomain failed: %v", err)
	}

	// Update the domain
	domain.MaxAccounts = 200
	domain.DKIMSelector = "updated"

	if err := db.UpdateDomain(domain); err != nil {
		t.Fatalf("UpdateDomain failed: %v", err)
	}

	// Verify the update
	retrieved, err := db.GetDomain("update-test.com")
	if err != nil {
		t.Fatalf("GetDomain failed: %v", err)
	}
	if retrieved.MaxAccounts != 200 {
		t.Errorf("expected MaxAccounts 200, got %d", retrieved.MaxAccounts)
	}
	if retrieved.DKIMSelector != "updated" {
		t.Errorf("expected DKIMSelector 'updated', got %s", retrieved.DKIMSelector)
	}
	if retrieved.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
}

// TestUpdateQueueEntry tests updating a queue entry
func TestUpdateQueueEntry(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create a queue entry
	entry := &QueueEntry{
		ID:          "update-queue-test",
		From:        "sender@example.com",
		To:          []string{"recipient@example.com"},
		MessagePath: "/tmp/update-test",
		Status:      "pending",
		NextRetry:   time.Now(),
		RetryCount:  0,
	}

	if err := db.Enqueue(entry); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Update the entry
	entry.Status = "retrying"
	entry.RetryCount = 1
	entry.NextRetry = time.Now().Add(time.Minute)

	if err := db.UpdateQueueEntry(entry); err != nil {
		t.Fatalf("UpdateQueueEntry failed: %v", err)
	}

	// Verify the update
	retrieved, err := db.GetQueueEntry("update-queue-test")
	if err != nil {
		t.Fatalf("GetQueueEntry failed: %v", err)
	}
	if retrieved.Status != "retrying" {
		t.Errorf("expected Status 'retrying', got %s", retrieved.Status)
	}
	if retrieved.RetryCount != 1 {
		t.Errorf("expected RetryCount 1, got %d", retrieved.RetryCount)
	}
}

func TestAliasOperations(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	t.Run("GetAlias_NotFound", func(t *testing.T) {
		_, err := db.GetAlias("example.com", "nonexistent")
		if err == nil {
			t.Error("expected error for nonexistent alias")
		}
	})

	t.Run("GetAliasAndResolve", func(t *testing.T) {
		alias := &AliasData{
			Alias:     "alias@example.com",
			Target:    "user@example.com",
			Domain:    "example.com",
			IsActive:  true,
			CreatedAt: time.Now(),
		}

		key := "example.com:alias"
		if err := db.Put(BucketAliases, key, alias); err != nil {
			t.Fatalf("Put alias failed: %v", err)
		}

		// Test GetAlias
		retrieved, err := db.GetAlias("example.com", "alias")
		if err != nil {
			t.Fatalf("GetAlias failed: %v", err)
		}
		if retrieved.Alias != alias.Alias {
			t.Errorf("expected alias %s, got %s", alias.Alias, retrieved.Alias)
		}
		if retrieved.Target != alias.Target {
			t.Errorf("expected target %s, got %s", alias.Target, retrieved.Target)
		}

		// Test ResolveAlias
		target, err := db.ResolveAlias("example.com", "alias")
		if err != nil {
			t.Fatalf("ResolveAlias failed: %v", err)
		}
		if target != alias.Target {
			t.Errorf("expected target %s, got %s", alias.Target, target)
		}
	})

	t.Run("ResolveAlias_Inactive", func(t *testing.T) {
		alias := &AliasData{
			Alias:     "inactive@example.com",
			Target:    "user@example.com",
			Domain:    "example.com",
			IsActive:  false,
			CreatedAt: time.Now(),
		}

		key := "example.com:inactive"
		if err := db.Put(BucketAliases, key, alias); err != nil {
			t.Fatalf("Put alias failed: %v", err)
		}

		target, err := db.ResolveAlias("example.com", "inactive")
		if err != nil {
			t.Fatalf("ResolveAlias failed: %v", err)
		}
		if target != "" {
			t.Errorf("expected empty target for inactive alias, got %s", target)
		}
	})

	t.Run("ResolveAlias_NotFound", func(t *testing.T) {
		_, err := db.ResolveAlias("example.com", "nonexistent")
		if err == nil {
			t.Error("expected error for nonexistent alias")
		}
	})
}

// TestScheduledMessageBbolt exercises the scheduled-message store on the bbolt
// backend (the postgres mirror is covered by TestScheduledRoundTrip).
func TestScheduledMessageBbolt(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	past := time.Now().Add(-time.Minute)
	m := &ScheduledMessage{
		ID: "s1", Owner: "u@example.com", From: "u@example.com",
		To: []string{"a@x.com", "b@x.com"}, MessagePath: "/spool/s1",
		SendAt: past, Status: "pending", Source: "webmail", FileSent: true, FolderUID: 7, BlobKey: "blob1",
	}
	if err := db.CreateScheduledMessageWithLimit(m, 10); err != nil {
		t.Fatalf("CreateScheduledMessageWithLimit: %v", err)
	}
	if err := db.CreateScheduledMessageWithLimit(&ScheduledMessage{ID: "s2", Owner: "u@example.com", SendAt: past, Status: "pending"}, 1); err == nil {
		t.Error("create past per-owner max should error")
	}

	got, err := db.GetScheduledMessage("s1")
	if err != nil {
		t.Fatalf("GetScheduledMessage: %v", err)
	}
	if len(got.To) != 2 || !got.FileSent || got.FolderUID != 7 || got.BlobKey != "blob1" {
		t.Errorf("scheduled mismatch: %+v", got)
	}

	if due, err := db.ListDueScheduledMessages(time.Now()); err != nil || len(due) != 1 || due[0].ID != "s1" {
		t.Fatalf("ListDueScheduledMessages: %v %+v", err, due)
	}
	if byOwner, err := db.ListScheduledByOwner("u@example.com"); err != nil || len(byOwner) != 1 {
		t.Fatalf("ListScheduledByOwner: %v len=%d", err, len(byOwner))
	}

	// A future message is not due.
	future := &ScheduledMessage{ID: "s3", Owner: "u@example.com", SendAt: time.Now().Add(time.Hour), Status: "pending"}
	if err := db.CreateScheduledMessage(future); err != nil {
		t.Fatalf("CreateScheduledMessage future: %v", err)
	}
	if due, err := db.ListDueScheduledMessages(time.Now()); err != nil || len(due) != 1 {
		t.Errorf("future message should not be due: err=%v %+v", err, due)
	}

	// Stale-claim recovery: a 'sending' row reset back to pending.
	got.Status = "sending"
	got.ClaimedAt = time.Now().Add(-2 * time.Hour)
	if err := db.UpdateScheduledMessage(got); err != nil {
		t.Fatalf("UpdateScheduledMessage: %v", err)
	}
	if n, err := db.ResetStaleScheduledMessages(time.Now().Add(-time.Hour)); err != nil || n != 1 {
		t.Errorf("ResetStaleScheduledMessages: n=%d err=%v", n, err)
	}

	// Folder-ref cancel removes the matching record.
	if ok, err := db.CancelScheduledByFolderRef("u@example.com", 7); err != nil || !ok {
		t.Errorf("CancelScheduledByFolderRef: ok=%v err=%v", ok, err)
	}
	if _, err := db.GetScheduledMessage("s1"); err == nil {
		t.Error("GetScheduledMessage after cancel should error")
	}
	if ok, err := db.CancelScheduledByFolderRef("u@example.com", 999); err != nil || ok {
		t.Errorf("cancel of a non-existent folder ref should report false: ok=%v err=%v", ok, err)
	}
}

func TestEffectiveQuotaThresholds(t *testing.T) {
	const gb = int64(1) << 30
	cases := []struct {
		name                           string
		acct                           *AccountData
		dom                            *DomainData
		wantWarn, wantProhib, wantHard int64
	}{
		{
			name:     "account values used directly",
			acct:     &AccountData{QuotaLimit: 10 * gb, QuotaWarn: 8 * gb, QuotaProhibitSend: 9 * gb},
			dom:      &DomainData{},
			wantWarn: 8 * gb, wantProhib: 9 * gb, wantHard: 10 * gb,
		},
		{
			name:     "domain defaults fill unset account thresholds",
			acct:     &AccountData{QuotaLimit: 10 * gb},
			dom:      &DomainData{QuotaWarn: 7 * gb, QuotaProhibitSend: 9 * gb},
			wantWarn: 7 * gb, wantProhib: 9 * gb, wantHard: 10 * gb,
		},
		{
			name:     "account overrides domain default",
			acct:     &AccountData{QuotaLimit: 10 * gb, QuotaWarn: 6 * gb},
			dom:      &DomainData{QuotaWarn: 7 * gb, QuotaProhibitSend: 9 * gb},
			wantWarn: 6 * gb, wantProhib: 9 * gb, wantHard: 10 * gb,
		},
		{
			name:     "hardCap is the tighter of account limit and domain max",
			acct:     &AccountData{QuotaLimit: 10 * gb},
			dom:      &DomainData{MaxMailboxSize: 4 * gb},
			wantWarn: 0, wantProhib: 0, wantHard: 4 * gb,
		},
		{
			name:     "thresholds clamp to the hard cap",
			acct:     &AccountData{QuotaLimit: 5 * gb, QuotaWarn: 8 * gb, QuotaProhibitSend: 9 * gb},
			dom:      &DomainData{},
			wantWarn: 5 * gb, wantProhib: 5 * gb, wantHard: 5 * gb,
		},
		{
			name:     "all disabled when nothing set",
			acct:     &AccountData{},
			dom:      nil,
			wantWarn: 0, wantProhib: 0, wantHard: 0,
		},
		{
			name:     "no clamp when hard cap is unlimited",
			acct:     &AccountData{QuotaWarn: 8 * gb, QuotaProhibitSend: 9 * gb},
			dom:      nil,
			wantWarn: 8 * gb, wantProhib: 9 * gb, wantHard: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warn, prohib, hard := EffectiveQuotaThresholds(tc.acct, tc.dom)
			if warn != tc.wantWarn || prohib != tc.wantProhib || hard != tc.wantHard {
				t.Errorf("got (warn=%d prohib=%d hard=%d), want (warn=%d prohib=%d hard=%d)",
					warn, prohib, hard, tc.wantWarn, tc.wantProhib, tc.wantHard)
			}
		})
	}
}

func TestRecoverableItemBbolt(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now()
	expired := &RecoverableItem{
		ID: "r1", Owner: "u@example.com", OriginalFolder: "INBOX",
		BlobKey: "blob1", FolderUID: 5, DeletedAt: old, Size: 1024, Subject: "old mail",
	}
	kept := &RecoverableItem{
		ID: "r2", Owner: "u@example.com", OriginalFolder: "Archive",
		BlobKey: "blob2", FolderUID: 6, DeletedAt: fresh, Subject: "recent mail",
	}
	if err := db.CreateRecoverableItem(expired); err != nil {
		t.Fatalf("CreateRecoverableItem expired: %v", err)
	}
	if err := db.CreateRecoverableItem(kept); err != nil {
		t.Fatalf("CreateRecoverableItem kept: %v", err)
	}

	got, err := db.GetRecoverableItem("r1")
	if err != nil {
		t.Fatalf("GetRecoverableItem: %v", err)
	}
	if got.OriginalFolder != "INBOX" || got.FolderUID != 5 || got.BlobKey != "blob1" || got.Size != 1024 {
		t.Errorf("recoverable mismatch: %+v", got)
	}

	if byOwner, err := db.ListRecoverableByOwner("u@example.com"); err != nil || len(byOwner) != 2 {
		t.Fatalf("ListRecoverableByOwner: %v len=%d", err, len(byOwner))
	}

	// Only the 48h-old item is expired against a 24h-ago cutoff; the fresh one is kept.
	cutoff := time.Now().Add(-24 * time.Hour)
	expiredList, err := db.ListExpiredRecoverableItems(cutoff)
	if err != nil || len(expiredList) != 1 || expiredList[0].ID != "r1" {
		t.Fatalf("ListExpiredRecoverableItems: err=%v %+v", err, expiredList)
	}

	// FindRecoverableByFolderRef resolves restore/cleanup by the folder projection.
	if found, err := db.FindRecoverableByFolderRef("u@example.com", 6); err != nil || found == nil || found.ID != "r2" {
		t.Fatalf("FindRecoverableByFolderRef hit: err=%v %+v", err, found)
	}
	if found, err := db.FindRecoverableByFolderRef("u@example.com", 999); err != nil || found != nil {
		t.Errorf("FindRecoverableByFolderRef miss should be nil: err=%v %+v", err, found)
	}

	if err := db.DeleteRecoverableItem("r1"); err != nil {
		t.Fatalf("DeleteRecoverableItem: %v", err)
	}
	if _, err := db.GetRecoverableItem("r1"); err == nil {
		t.Error("GetRecoverableItem after delete should error")
	}
}
