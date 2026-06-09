package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/mapi"
	"github.com/umailserver/umailserver/internal/queue"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
	"github.com/umailserver/umailserver/internal/vacation"
)

// The relational backend must satisfy the existing consumer interfaces so it can
// replace *db.DB behind them without touching the consuming packages.
var (
	_ mapi.Store  = (*DB)(nil)
	_ queue.Store = (*DB)(nil)
)

// openTestDB connects to UMAIL_TEST_POSTGRES_DSN, migrates, and truncates the
// net-surface tables so each test starts clean. It skips when no DSN is set.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("UMAIL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UMAIL_TEST_POSTGRES_DSN not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	d, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Migrate holds the boot-time advisory lock on a pinned connection; release it
	// so the test body runs against the full pool.
	d.ReleaseInitLock(ctx)
	if _, err := d.pool.Exec(ctx,
		`TRUNCATE accounts, aliases, mail_groups, mail_queue, scheduled_messages, domains, tenants,
			user_ui_prefs, user_signatures, user_vacation, ews_user_config,
			mailboxes, mailbox_subscriptions, messages, threads, mailbox_acl, changes,
			spam_tokens, spam_stats, ratelimit_quota, backup_jobs, backup_manifests,
			semcore_lifecycle, semcore_lifecycle_seq, semcore_mailbox_identity,
			semcore_folder_identity, semcore_item_identity, semcore_conversation_identity,
			semcore_sync_state, semcore_tombstone, semcore_subscription, semcore_delegate,
			semcore_rule, semcore_oof, semcore_resource, semcore_room_list,
			semcore_calendar_item, semcore_task, semcore_contact, semcore_job
			RESTART IDENTITY CASCADE`,
	); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return d
}

// TestOpenMigrate is an integration test: it connects to the PostgreSQL
// instance named by UMAIL_TEST_POSTGRES_DSN, applies the schema, and verifies
// the net-surface tables exist. It skips when the DSN is unset so the default
// `make test` run (which has no PostgreSQL) stays green; CI/local runs that
// provide a database exercise the real connection and schema apply — never a
// mock, because the point is to prove the embedded DDL is valid against a real
// server.
func TestOpenMigrate(t *testing.T) {
	dsn := os.Getenv("UMAIL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UMAIL_TEST_POSTGRES_DSN not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Idempotent: a second apply must succeed too.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (second apply): %v", err)
	}

	// Every table in the one relational schema must exist after migration —
	// both the account/metadata tables and the message-metadata/search tables
	// (they share this single schema and connection).
	want := []string{
		"tenants", "tenant_settings", "domains", "domain_settings",
		"accounts", "aliases", "mail_groups", "mail_group_members",
		"mail_queue", "mail_queue_recipients", "revoked_tokens", "client_sessions",
		"mailboxes", "mailbox_subscriptions", "messages", "threads", "mailbox_acl",
	}
	for _, table := range want {
		var exists bool
		err := db.Pool().QueryRow(ctx,
			"SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)",
			table,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if !exists {
			t.Errorf("table %q missing after Migrate", table)
		}
	}

	// The generated tsvector + GIN search index must exist.
	var hasGIN bool
	if err := db.Pool().QueryRow(ctx,
		"SELECT EXISTS (SELECT FROM pg_indexes WHERE indexname='idx_messages_search')",
	).Scan(&hasGIN); err != nil {
		t.Fatalf("check search index: %v", err)
	}
	if !hasGIN {
		t.Error("idx_messages_search (GIN) missing after Migrate")
	}
}

// TestMigrateSerializesConcurrentBoot proves the boot-time advisory lock makes
// concurrent fresh-DB starts mutually exclusive: while one node holds the lock
// (Migrate done, bootstrap in progress) a second node's Migrate BLOCKS, and
// only proceeds once the first releases. This is what lets two nodes boot at
// once without a compose-level start ordering and without racing the schema or
// the bootstrap admin insert.
func TestMigrateSerializesConcurrentBoot(t *testing.T) {
	dsn := os.Getenv("UMAIL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UMAIL_TEST_POSTGRES_DSN not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()

	db1, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	defer db1.Close() //nolint:errcheck // test cleanup
	db2, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close() //nolint:errcheck // test cleanup

	// Node 1 migrates and HOLDS the init lock (simulating bootstrap in progress).
	if err := db1.Migrate(ctx); err != nil {
		t.Fatalf("db1 Migrate: %v", err)
	}

	// Node 2's Migrate must block on the lock.
	done := make(chan error, 1)
	go func() { done <- db2.Migrate(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("db2 Migrate returned (%v) while db1 still holds the init lock — not serialized", err)
	case <-time.After(500 * time.Millisecond):
		// Expected: still blocked.
	}

	// Releasing node 1's lock unblocks node 2.
	db1.ReleaseInitLock(ctx)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("db2 Migrate after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("db2 Migrate did not proceed after db1 released the init lock")
	}
	db2.ReleaseInitLock(ctx)
}

// TestDomainRoundTrip verifies a domain (with settings) and its accounts
// round-trip through the relational backend with the same shape the bbolt store
// returns, and that the auto-created self-tenant satisfies the foreign key.
func TestDomainRoundTrip(t *testing.T) {
	d := openTestDB(t)

	dom := &db.DomainData{
		Name:         "example.com",
		MaxAccounts:  50,
		DKIMSelector: "default",
		IsActive:     true,
		Settings:     map[string]string{"theme": "dark", "logo": "x.png"},
	}
	if err := d.CreateDomain(dom); err != nil {
		t.Fatalf("CreateDomain: %v", err)
	}
	if dom.TenantID != "example.com" {
		t.Errorf("self-tenant not set: TenantID=%q want example.com", dom.TenantID)
	}
	if dom.CreatedAt.IsZero() || dom.UpdatedAt.IsZero() {
		t.Error("CreateDomain did not stamp timestamps")
	}

	got, err := d.GetDomain("example.com")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if got.MaxAccounts != 50 || got.DKIMSelector != "default" || !got.IsActive {
		t.Errorf("domain fields mismatch: %+v", got)
	}
	if got.Settings["theme"] != "dark" || got.Settings["logo"] != "x.png" {
		t.Errorf("settings mismatch: %+v", got.Settings)
	}

	// A missing domain must error, matching the bbolt store.
	if _, err := d.GetDomain("nope.com"); err == nil {
		t.Error("GetDomain(nope.com) should error")
	}

	acc := &db.AccountData{
		Email:       "alice@example.com",
		LocalPart:   "alice",
		Domain:      "example.com",
		QuotaLimit:  1 << 30,
		IsActive:    true,
		DisplayName: "Alice",
		LastLoginAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := d.CreateAccount(acc); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	gotAcc, err := d.GetAccount("example.com", "alice")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if gotAcc.Email != "alice@example.com" || gotAcc.DisplayName != "Alice" || gotAcc.QuotaLimit != 1<<30 {
		t.Errorf("account fields mismatch: %+v", gotAcc)
	}
	if gotAcc.LastLoginAt.IsZero() {
		t.Error("LastLoginAt did not round-trip")
	}

	list, err := d.ListAccountsByDomain("example.com")
	if err != nil {
		t.Fatalf("ListAccountsByDomain: %v", err)
	}
	if len(list) != 1 || list[0].Email != "alice@example.com" {
		t.Errorf("ListAccountsByDomain mismatch: %+v", list)
	}

	domains, err := d.ListDomains()
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	if len(domains) != 1 || domains[0].Name != "example.com" || domains[0].Settings["theme"] != "dark" {
		t.Errorf("ListDomains mismatch: %+v", domains)
	}
}

// TestAliasRoundTrip covers alias create/get/list/delete with case-insensitive
// keying matching the bbolt store.
func TestAliasRoundTrip(t *testing.T) {
	d := openTestDB(t)
	if err := d.CreateDomain(&db.DomainData{Name: "example.com", IsActive: true}); err != nil {
		t.Fatalf("CreateDomain: %v", err)
	}
	if err := d.CreateAlias(&db.AliasData{Alias: "Info", Domain: "example.com", Target: "user@example.com", IsActive: true}); err != nil {
		t.Fatalf("CreateAlias: %v", err)
	}
	// Case-insensitive lookup matches the bbolt lower-cased key.
	got, err := d.GetAlias("example.com", "info")
	if err != nil {
		t.Fatalf("GetAlias: %v", err)
	}
	if got.Target != "user@example.com" {
		t.Errorf("alias target mismatch: %+v", got)
	}
	// Re-create with same case-insensitive key overwrites (bbolt Put semantics).
	if err := d.CreateAlias(&db.AliasData{Alias: "info", Domain: "example.com", Target: "two@example.com", IsActive: true}); err != nil {
		t.Fatalf("CreateAlias overwrite: %v", err)
	}
	list, err := d.ListAliases()
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	if len(list) != 1 || list[0].Target != "two@example.com" {
		t.Errorf("alias overwrite/list mismatch: %+v", list)
	}
	if err := d.DeleteAlias("example.com", "INFO"); err != nil {
		t.Fatalf("DeleteAlias: %v", err)
	}
	if _, err := d.GetAlias("example.com", "info"); err == nil {
		t.Error("GetAlias after delete should error")
	}
}

// TestTenantRoundTrip covers tenant create/get/list with settings.
func TestTenantRoundTrip(t *testing.T) {
	d := openTestDB(t)
	tn := &db.TenantData{ID: "acme", Name: "Acme", IsActive: true, Settings: map[string]string{"plan": "pro"}}
	if err := d.CreateTenant(tn); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	got, err := d.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Name != "Acme" || got.Settings["plan"] != "pro" {
		t.Errorf("tenant mismatch: %+v", got)
	}
	if err := d.CreateTenant(&db.TenantData{}); err == nil {
		t.Error("CreateTenant with empty id should error")
	}
}

// TestIncrementQuota covers the atomic quota update and the effective-ceiling
// rule (tighter of account limit and domain max_mailbox_size).
func TestIncrementQuota(t *testing.T) {
	d := openTestDB(t)
	if err := d.CreateDomain(&db.DomainData{Name: "example.com", MaxMailboxSize: 1000, IsActive: true}); err != nil {
		t.Fatalf("CreateDomain: %v", err)
	}
	if err := d.CreateAccount(&db.AccountData{Email: "a@example.com", LocalPart: "a", Domain: "example.com", QuotaLimit: 5000, IsActive: true}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	// 500 is within both limits.
	if err := d.IncrementQuota("example.com", "a", 500); err != nil {
		t.Fatalf("IncrementQuota(+500): %v", err)
	}
	acc, err := d.GetAccount("example.com", "a")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acc.QuotaUsed != 500 {
		t.Errorf("quota_used=%d want 500", acc.QuotaUsed)
	}
	// +600 would reach 1100 > domain ceiling 1000 (tighter than account 5000).
	if err := d.IncrementQuota("example.com", "a", 600); err == nil {
		t.Error("IncrementQuota past domain ceiling should error")
	}
	// Shrinking is always allowed and ignores the ceiling.
	if err := d.IncrementQuota("example.com", "a", -200); err != nil {
		t.Fatalf("IncrementQuota(-200): %v", err)
	}
	acc, err = d.GetAccount("example.com", "a")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acc.QuotaUsed != 300 {
		t.Errorf("quota_used=%d want 300", acc.QuotaUsed)
	}
	// Update/Delete round-trip.
	acc.DisplayName = "Renamed"
	if err := d.UpdateAccount(acc); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	reread, err := d.GetAccount("example.com", "a")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if reread.DisplayName != "Renamed" {
		t.Errorf("UpdateAccount not persisted: %+v", reread)
	}
	if err := d.DeleteAccount("example.com", "a"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, err := d.GetAccount("example.com", "a"); err == nil {
		t.Error("GetAccount after delete should error")
	}
}

// TestQueueRoundTrip covers enqueue (with recipients), limit, pending read,
// claim under SKIP LOCKED, update, and dequeue.
func TestQueueRoundTrip(t *testing.T) {
	d := openTestDB(t)
	past := time.Now().Add(-time.Minute)
	entry := &db.QueueEntry{
		ID:          "q1",
		From:        "s@example.com",
		To:          []string{"a@x.com", "b@x.com"},
		MessagePath: "/spool/q1",
		NextRetry:   past,
		Status:      "pending",
		Priority:    db.PriorityHigh,
	}
	if err := d.EnqueueWithLimit(entry, 10); err != nil {
		t.Fatalf("EnqueueWithLimit: %v", err)
	}
	if err := d.EnqueueWithLimit(&db.QueueEntry{ID: "q2", Status: "pending", NextRetry: past}, 1); err == nil {
		t.Error("EnqueueWithLimit past max should error")
	}

	got, err := d.GetQueueEntry("q1")
	if err != nil {
		t.Fatalf("GetQueueEntry: %v", err)
	}
	if len(got.To) != 2 || got.To[0] != "a@x.com" || got.To[1] != "b@x.com" || got.Priority != db.PriorityHigh {
		t.Errorf("queue entry mismatch: %+v", got)
	}

	pending, err := d.GetPendingQueue(time.Now())
	if err != nil {
		t.Fatalf("GetPendingQueue: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != "q1" {
		t.Errorf("GetPendingQueue mismatch: %+v", pending)
	}

	// Claim flips status to sending atomically and wins.
	won, err := d.ClaimQueueEntry("q1", time.Now())
	if err != nil {
		t.Fatalf("ClaimQueueEntry: %v", err)
	}
	if !won {
		t.Error("ClaimQueueEntry should win for a due pending entry")
	}
	// Already claimed (fresh lease): no longer due, so a re-claim loses.
	if again, err := d.ClaimQueueEntry("q1", time.Now()); err != nil {
		t.Fatalf("re-claim: %v", err)
	} else if again {
		t.Error("re-claim of a freshly leased entry should lose")
	}
	// And it no longer appears in the pending set.
	pending, err = d.GetPendingQueue(time.Now())
	if err != nil {
		t.Fatalf("GetPendingQueue after claim: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("claimed entry still pending: %+v", pending)
	}

	got.Status = "delivered"
	if err := d.UpdateQueueEntry(got); err != nil {
		t.Fatalf("UpdateQueueEntry: %v", err)
	}
	if err := d.Dequeue("q1"); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if _, err := d.GetQueueEntry("q1"); err == nil {
		t.Error("GetQueueEntry after dequeue should error")
	}
}

func TestScheduledRoundTrip(t *testing.T) {
	d := openTestDB(t)
	past := time.Now().Add(-time.Minute).UTC()
	m := &db.ScheduledMessage{
		ID:          "s1",
		Owner:       "u@example.com",
		From:        "u@example.com",
		To:          []string{"a@x.com", "b@x.com"},
		MessagePath: "/spool/s1",
		SendAt:      past,
		Status:      "pending",
		Source:      "webmail",
		FileSent:    true,
		FolderUID:   7,
		BlobKey:     "blob1",
	}
	if err := d.CreateScheduledMessageWithLimit(m, 10); err != nil {
		t.Fatalf("CreateScheduledMessageWithLimit: %v", err)
	}
	if err := d.CreateScheduledMessageWithLimit(&db.ScheduledMessage{ID: "s2", Owner: "u@example.com", SendAt: past, Status: "pending"}, 1); err == nil {
		t.Error("create past per-owner max should error")
	}

	got, err := d.GetScheduledMessage("s1")
	if err != nil {
		t.Fatalf("GetScheduledMessage: %v", err)
	}
	if len(got.To) != 2 || got.To[0] != "a@x.com" || !got.FileSent || got.FolderUID != 7 || got.BlobKey != "blob1" {
		t.Errorf("scheduled mismatch: %+v", got)
	}

	if byOwner, err := d.ListScheduledByOwner("u@example.com"); err != nil || len(byOwner) != 1 {
		t.Fatalf("ListScheduledByOwner: %v len=%d", err, len(byOwner))
	}
	due, err := d.ListDueScheduledMessages(time.Now())
	if err != nil || len(due) != 1 || due[0].ID != "s1" {
		t.Fatalf("ListDueScheduledMessages: %v %+v", err, due)
	}

	// Claim flips status to sending and wins; a re-claim loses.
	won, err := d.ClaimScheduledMessage("s1", time.Now())
	if err != nil || !won {
		t.Fatalf("ClaimScheduledMessage: won=%v err=%v", won, err)
	}
	if again, err := d.ClaimScheduledMessage("s1", time.Now()); err != nil || again {
		t.Errorf("re-claim should lose: again=%v err=%v", again, err)
	}
	if due, err := d.ListDueScheduledMessages(time.Now()); err != nil || len(due) != 0 {
		t.Errorf("claimed row still due: err=%v %+v", err, due)
	}

	// Stale-claim recovery resets it back to pending.
	if n, err := d.ResetStaleScheduledMessages(time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Errorf("ResetStaleScheduledMessages: n=%d err=%v", n, err)
	}
	if due, err := d.ListDueScheduledMessages(time.Now()); err != nil || len(due) != 1 {
		t.Errorf("reset row should be due again: err=%v %+v", err, due)
	}

	// Folder-ref cancel removes the row.
	if ok, err := d.CancelScheduledByFolderRef("u@example.com", 7); err != nil || !ok {
		t.Errorf("CancelScheduledByFolderRef: ok=%v err=%v", ok, err)
	}
	if _, err := d.GetScheduledMessage("s1"); err == nil {
		t.Error("GetScheduledMessage after cancel should error")
	}
}

// TestClaimQueueEntryConcurrent proves the HA claim contract: when two nodes try
// to claim the same due entries at the same time, each entry is won by exactly
// one caller, never both (no double-delivery). Both workers race over the whole
// backlog id-by-id; the per-entry atomic claim partitions it.
func TestClaimQueueEntryConcurrent(t *testing.T) {
	d := openTestDB(t)
	past := time.Now().Add(-time.Minute)
	const n = 40
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = fmt.Sprintf("c%02d", i)
		if err := d.EnqueueWithLimit(&db.QueueEntry{
			ID: ids[i], From: "s@x.com", To: []string{"r@x.com"},
			MessagePath: "/spool/c", NextRetry: past, Status: "pending",
		}, 1000); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	// Two concurrent claimers, each sweeping the whole backlog.
	type res struct {
		ids []string
		err error
	}
	ch := make(chan res, 2)
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won := []string{}
			for _, id := range ids {
				ok, err := d.ClaimQueueEntry(id, time.Now())
				if err != nil {
					ch <- res{nil, err}
					return
				}
				if ok {
					won = append(won, id)
				}
			}
			ch <- res{won, nil}
		}()
	}
	wg.Wait()
	close(ch)

	seen := map[string]int{}
	total := 0
	for r := range ch {
		if r.err != nil {
			t.Fatalf("concurrent claim: %v", r.err)
		}
		for _, id := range r.ids {
			seen[id]++
			total++
		}
	}
	// Every entry claimed exactly once across both nodes — no overlap, no loss.
	if total != n || len(seen) != n {
		t.Fatalf("claimed total=%d distinct=%d, want %d each (overlap or loss)", total, len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("entry %s claimed %d times (double-delivery)", id, c)
		}
	}
}

// TestClaimQueueEntryReclaimsStale proves a node that died mid-delivery does not
// strand its work: an entry left in 'sending' is reclaimable once its lease
// (next_retry) expires, but not while the lease is still in the future.
func TestClaimQueueEntryReclaimsStale(t *testing.T) {
	d := openTestDB(t)
	// An orphaned claim: status 'sending' with a lease that already expired.
	if err := d.EnqueueWithLimit(&db.QueueEntry{
		ID: "stale", From: "s@x.com", To: []string{"r@x.com"}, MessagePath: "/spool/s",
		NextRetry: time.Now().Add(-time.Minute), Status: "sending",
	}, 1000); err != nil {
		t.Fatalf("enqueue stale: %v", err)
	}
	won, err := d.ClaimQueueEntry("stale", time.Now())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !won {
		t.Fatal("expired-lease 'sending' entry should be reclaimable")
	}
	// It is now re-leased into the future, so an immediate re-claim loses.
	again, err := d.ClaimQueueEntry("stale", time.Now())
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if again {
		t.Fatal("freshly leased entry should not be re-claimed")
	}
}

// TestMailGroupRoundTrip covers static membership round-trip and dynamic
// expansion against accounts.
func TestMailGroupRoundTrip(t *testing.T) {
	d := openTestDB(t)
	if err := d.CreateDomain(&db.DomainData{Name: "example.com", IsActive: true}); err != nil {
		t.Fatalf("CreateDomain: %v", err)
	}
	static := &db.MailGroup{
		Email: "team@example.com", LocalPart: "team", Domain: "example.com",
		IsActive: true, Members: []string{"a@x.com", "b@x.com"},
	}
	if err := d.CreateMailGroup(static); err != nil {
		t.Fatalf("CreateMailGroup: %v", err)
	}
	got, err := d.GetMailGroup("example.com", "TEAM") // case-insensitive
	if err != nil {
		t.Fatalf("GetMailGroup: %v", err)
	}
	if len(got.Members) != 2 || got.Members[0] != "a@x.com" {
		t.Errorf("members mismatch: %+v", got.Members)
	}
	exp, err := d.ExpandMailGroup(got)
	if err != nil {
		t.Fatalf("ExpandMailGroup static: %v", err)
	}
	if len(exp) != 2 {
		t.Errorf("static expand mismatch: %+v", exp)
	}

	// Dynamic group: all active accounts in the domain.
	if err := d.CreateAccount(&db.AccountData{Email: "u1@example.com", LocalPart: "u1", Domain: "example.com", IsActive: true}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	dyn := &db.MailGroup{
		Email: "all@example.com", LocalPart: "all", Domain: "example.com",
		IsActive: true, Dynamic: true,
	}
	if err := d.CreateMailGroup(dyn); err != nil {
		t.Fatalf("CreateMailGroup dynamic: %v", err)
	}
	exp, err = d.ExpandMailGroup(dyn)
	if err != nil {
		t.Fatalf("ExpandMailGroup dynamic: %v", err)
	}
	if len(exp) != 1 || exp[0] != "u1@example.com" {
		t.Errorf("dynamic expand mismatch: %+v", exp)
	}

	if err := d.DeleteMailGroup("example.com", "team"); err != nil {
		t.Fatalf("DeleteMailGroup: %v", err)
	}
	if _, err := d.GetMailGroup("example.com", "team"); err == nil {
		t.Error("GetMailGroup after delete should error")
	}
}

// TestAuthRoundTrip covers the revoked-token blacklist and client sessions.
func TestAuthRoundTrip(t *testing.T) {
	d := openTestDB(t)

	// Active revocation.
	if err := d.StoreRevokedToken("hash1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("StoreRevokedToken: %v", err)
	}
	revoked, err := d.IsTokenRevoked("hash1")
	if err != nil || !revoked {
		t.Errorf("IsTokenRevoked(active)=%v,%v want true,nil", revoked, err)
	}
	// Expired revocation reports false and is pruned.
	if err := d.StoreRevokedToken("hash2", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("StoreRevokedToken expired: %v", err)
	}
	if revoked, rerr := d.IsTokenRevoked("hash2"); rerr != nil || revoked {
		t.Errorf("expired token: revoked=%v err=%v want false,nil", revoked, rerr)
	}
	// Unknown token is not revoked.
	if revoked, rerr := d.IsTokenRevoked("nope"); rerr != nil || revoked {
		t.Errorf("unknown token: revoked=%v err=%v want false,nil", revoked, rerr)
	}

	sess := &db.ClientSession{ID: "s1", Email: "u@example.com", TokenHash: "t", DeviceType: "mobile"}
	if err := d.CreateClientSession(sess); err != nil {
		t.Fatalf("CreateClientSession: %v", err)
	}
	list, err := d.ListClientSessionsByEmail("u@example.com")
	if err != nil {
		t.Fatalf("ListClientSessionsByEmail: %v", err)
	}
	if len(list) != 1 || list[0].ID != "s1" {
		t.Errorf("session list mismatch: %+v", list)
	}
	if err := d.RevokeClientSession("s1"); err != nil {
		t.Fatalf("RevokeClientSession: %v", err)
	}
	// Revoked sessions are excluded from the by-email listing.
	list, err = d.ListClientSessionsByEmail("u@example.com")
	if err != nil {
		t.Fatalf("ListClientSessionsByEmail after revoke: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("revoked session still listed: %+v", list)
	}
}

// TestTypedKVRoundTrip covers the typed replacements for the generic-KV buckets:
// UI prefs, signature, vacation (with excludes), and EWS user configuration.
func TestTypedKVRoundTrip(t *testing.T) {
	d := openTestDB(t)

	// UI prefs.
	if err := d.PutUIPrefs("u@x.com", map[string]bool{"dark": true, "compact": false}); err != nil {
		t.Fatalf("PutUIPrefs: %v", err)
	}
	prefs, err := d.GetUIPrefs("u@x.com")
	if err != nil {
		t.Fatalf("GetUIPrefs: %v", err)
	}
	if !prefs["dark"] || prefs["compact"] {
		t.Errorf("ui prefs mismatch: %+v", prefs)
	}
	// Unset user yields an empty (non-nil) map.
	empty, err := d.GetUIPrefs("none@x.com")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("empty prefs mismatch: %+v err=%v", empty, err)
	}

	// Signature.
	if err := d.PutSignature("u@x.com", "Best,\nA"); err != nil {
		t.Fatalf("PutSignature: %v", err)
	}
	sig, err := d.GetSignature("u@x.com")
	if err != nil || sig != "Best,\nA" {
		t.Errorf("signature mismatch: %q err=%v", sig, err)
	}
	if s, serr := d.GetSignature("none@x.com"); serr != nil || s != "" {
		t.Errorf("unset signature should be empty: %q err=%v", s, serr)
	}

	// Vacation with excludes and a duration.
	vac := &vacation.Config{
		Enabled:          true,
		Subject:          "OOO",
		Message:          "away",
		SendInterval:     7 * 24 * time.Hour,
		IgnoreLists:      true,
		ExcludeAddresses: []string{"boss@x.com", "ops@x.com"},
	}
	if err := d.PutVacation("u@x.com", vac); err != nil {
		t.Fatalf("PutVacation: %v", err)
	}
	gotVac, err := d.GetVacation("u@x.com")
	if err != nil {
		t.Fatalf("GetVacation: %v", err)
	}
	if !gotVac.Enabled || gotVac.Subject != "OOO" || gotVac.SendInterval != 7*24*time.Hour {
		t.Errorf("vacation mismatch: %+v", gotVac)
	}
	if len(gotVac.ExcludeAddresses) != 2 || gotVac.ExcludeAddresses[0] != "boss@x.com" {
		t.Errorf("vacation excludes mismatch: %+v", gotVac.ExcludeAddresses)
	}
	if err := d.DeleteVacation("u@x.com"); err != nil {
		t.Fatalf("DeleteVacation: %v", err)
	}
	if _, err := d.GetVacation("u@x.com"); err == nil {
		t.Error("GetVacation after delete should error")
	}

	// EWS user configuration (opaque blob).
	blob := &db.UserConfigBlob{Dictionary: "<d/>", XMLData: "<x/>", BinaryData: "Yg=="}
	if err := d.PutUserConfig("e:u@x.com", "Calendar:OWAUserConfig", blob); err != nil {
		t.Fatalf("PutUserConfig: %v", err)
	}
	gotBlob, err := d.GetUserConfig("e:u@x.com", "Calendar:OWAUserConfig")
	if err != nil {
		t.Fatalf("GetUserConfig: %v", err)
	}
	if gotBlob.Dictionary != "<d/>" || gotBlob.XMLData != "<x/>" || gotBlob.BinaryData != "Yg==" {
		t.Errorf("user config blob mismatch: %+v", gotBlob)
	}
	if err := d.DeleteUserConfig("e:u@x.com", "Calendar:OWAUserConfig"); err != nil {
		t.Fatalf("DeleteUserConfig: %v", err)
	}
	if _, err := d.GetUserConfig("e:u@x.com", "Calendar:OWAUserConfig"); err == nil {
		t.Error("GetUserConfig after delete should error")
	}
}

// TestMailboxState covers mailbox reads, the default fallbacks, and the
// subscription set (which is independent of mailbox existence).
func TestMailboxState(t *testing.T) {
	d := openTestDB(t)

	// No mailbox row yet: GetMailbox returns the default, ListMailboxes ["INBOX"].
	mb, err := d.GetMailbox("u@x.com", "INBOX")
	if err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if mb.UIDValidity != 1 || mb.UIDNext != 1 {
		t.Errorf("default mailbox mismatch: %+v", mb)
	}
	list, err := d.ListMailboxes("u@x.com")
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(list) != 1 || list[0] != "INBOX" {
		t.Errorf("default list mismatch: %+v", list)
	}

	// Subscriptions are independent of mailbox existence (RFC 3501).
	if err := d.SetSubscribed("u@x.com", "Archive", true); err != nil {
		t.Fatalf("SetSubscribed: %v", err)
	}
	if ok, err := d.GetSubscribed("u@x.com", "Archive"); err != nil || !ok {
		t.Errorf("Archive should be subscribed: ok=%v err=%v", ok, err)
	}
	subs, err := d.ListSubscribed("u@x.com")
	if err != nil {
		t.Fatalf("ListSubscribed: %v", err)
	}
	if len(subs) != 1 || subs[0] != "Archive" {
		t.Errorf("subscribed list mismatch: %+v", subs)
	}
	if err := d.SetSubscribed("u@x.com", "Archive", false); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if ok, err := d.GetSubscribed("u@x.com", "Archive"); err != nil || ok {
		t.Errorf("Archive should be unsubscribed: ok=%v err=%v", ok, err)
	}
}

// TestGetNextUIDConcurrent proves the atomic UID claim: many concurrent callers
// must each get a distinct UID with no gaps or duplicates — the relational
// replacement for the bbolt single-writer assumption.
func TestGetNextUIDConcurrent(t *testing.T) {
	d := openTestDB(t)

	const n = 50
	got := make([]uint32, n)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			uid, err := d.GetNextUID("u@x.com", "INBOX")
			got[idx] = uid
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	seen := map[uint32]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("GetNextUID[%d]: %v", i, errs[i])
		}
		if seen[got[i]] {
			t.Errorf("duplicate UID %d claimed by concurrent callers", got[i])
		}
		seen[got[i]] = true
	}
	// The n claims must be exactly the contiguous range 1..n.
	for uid := uint32(1); uid <= n; uid++ {
		if !seen[uid] {
			t.Errorf("UID %d missing from concurrent claims (gap)", uid)
		}
	}
	// uid_next must now be n+1.
	mb, err := d.GetMailbox("u@x.com", "INBOX")
	if err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if mb.UIDNext != n+1 {
		t.Errorf("uid_next=%d after %d claims, want %d", mb.UIDNext, n, n+1)
	}
}

// TestMailboxLifecycleAndChanges covers Create/Delete/Rename/EnsureDefault and
// the JMAP change journal they feed.
func TestMailboxLifecycleAndChanges(t *testing.T) {
	d := openTestDB(t)
	const user = "u@x.com"

	// Initial state token is "0".
	if st, err := d.CurrentChangeState(user); err != nil || st != "0" {
		t.Fatalf("initial state=%q err=%v want 0", st, err)
	}

	// Create records a "created" mailbox change; a no-op re-create records nothing.
	if err := d.CreateMailbox(user, "Work"); err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}
	if err := d.CreateMailbox(user, "Work"); err != nil {
		t.Fatalf("CreateMailbox (dup): %v", err)
	}
	changes, hasMore, last, err := d.GetChangesSince(user, "mailbox", 0, 100)
	if err != nil {
		t.Fatalf("GetChangesSince: %v", err)
	}
	if len(changes) != 1 || changes[0].Kind != "created" || changes[0].ID != "Work" {
		t.Errorf("expected one created change for Work, got %+v", changes)
	}
	if hasMore {
		t.Error("hasMore should be false")
	}

	// Rename records destroyed(old)+created(new); messages would cascade.
	if err := d.RenameMailbox(user, "Work", "Projects"); err != nil {
		t.Fatalf("RenameMailbox: %v", err)
	}
	list, err := d.ListMailboxes(user)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	foundProjects, foundWork := false, false
	for _, n := range list {
		if n == "Projects" {
			foundProjects = true
		}
		if n == "Work" {
			foundWork = true
		}
	}
	if !foundProjects || foundWork {
		t.Errorf("after rename, mailboxes=%v (want Projects, not Work)", list)
	}

	// Delete records a "destroyed" change.
	if err := d.DeleteMailbox(user, "Projects"); err != nil {
		t.Fatalf("DeleteMailbox: %v", err)
	}

	// The change feed since the start must include created+destroyed+rename pair.
	all, _, newLast, err := d.GetChangesSince(user, "mailbox", 0, 100)
	if err != nil {
		t.Fatalf("GetChangesSince (all): %v", err)
	}
	if len(all) < 4 { // created Work, destroyed Work, created Projects, destroyed Projects
		t.Errorf("expected >=4 mailbox changes, got %d: %+v", len(all), all)
	}
	if newLast <= last {
		t.Errorf("lastSeq did not advance: %d <= %d", newLast, last)
	}
	// State token advanced past the initial 0.
	if st, serr := d.CurrentChangeState(user); serr != nil || st == "0" {
		t.Errorf("state token should have advanced: st=%q err=%v", st, serr)
	}

	// EnsureDefaultMailboxes creates the standard set idempotently.
	if err := d.EnsureDefaultMailboxes(user); err != nil {
		t.Fatalf("EnsureDefaultMailboxes: %v", err)
	}
	if err := d.EnsureDefaultMailboxes(user); err != nil {
		t.Fatalf("EnsureDefaultMailboxes (idempotent): %v", err)
	}
	names, err := d.ListMailboxes(user)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(names) < 7 {
		t.Errorf("expected >=7 default mailboxes, got %d: %v", len(names), names)
	}
}

// TestMessageCRUD covers store/get/update/delete, counts, the atomic modseq
// bump in UpdateMessageMetadataFunc, ClearRecent, and the generated search
// vector — against real Postgres.
func TestMessageCRUD(t *testing.T) {
	d := openTestDB(t)
	const user, mbox = "u@x.com", "INBOX"

	meta := &storage.MessageMetadata{
		MessageID: "<m1@x>", UID: 1, Flags: []string{"\\Recent"},
		Size: 1234, Subject: "Quarterly report", From: "boss@x.com", To: "u@x.com",
		ThreadID: "t1", IsThreadRoot: true,
	}
	if err := d.StoreMessageMetadata(user, mbox, 1, meta); err != nil {
		t.Fatalf("StoreMessageMetadata: %v", err)
	}
	// Email "created" change recorded.
	ch, _, _, err := d.GetChangesSince(user, "email", 0, 10)
	if err != nil {
		t.Fatalf("GetChangesSince(email): %v", err)
	}
	if len(ch) != 1 || ch[0].Kind != "created" || ch[0].ID != "<m1@x>" {
		t.Errorf("expected created email change, got %+v", ch)
	}

	got, err := d.GetMessageMetadata(user, mbox, 1)
	if err != nil {
		t.Fatalf("GetMessageMetadata: %v", err)
	}
	if got.Subject != "Quarterly report" || got.Size != 1234 || got.ThreadID != "t1" || !got.IsThreadRoot {
		t.Errorf("message fields mismatch: %+v", got)
	}
	// Missing message returns empty (not error), matching bbolt.
	if empty, err := d.GetMessageMetadata(user, mbox, 999); err != nil || empty.MessageID != "" {
		t.Errorf("missing message should be empty: %+v err=%v", empty, err)
	}

	uids, err := d.GetMessageUIDs(user, mbox)
	if err != nil || len(uids) != 1 || uids[0] != 1 {
		t.Errorf("GetMessageUIDs mismatch: %v err=%v", uids, err)
	}

	// Counts: 1 exists, 1 recent (\Recent), 1 unseen (no \Seen).
	ex, rc, un, err := d.GetMailboxCounts(user, mbox)
	if err != nil || ex != 1 || rc != 1 || un != 1 {
		t.Errorf("counts exists=%d recent=%d unseen=%d err=%v want 1,1,1", ex, rc, un, err)
	}

	// UpdateMessageMetadataFunc sets \Seen and bumps modseq.
	if err := d.UpdateMessageMetadataFunc(user, mbox, 1, func(m *storage.MessageMetadata) error {
		m.Flags = []string{"\\Seen"}
		return nil
	}); err != nil {
		t.Fatalf("UpdateMessageMetadataFunc: %v", err)
	}
	after, err := d.GetMessageMetadata(user, mbox, 1)
	if err != nil {
		t.Fatalf("GetMessageMetadata: %v", err)
	}
	if after.ModSeq == 0 {
		t.Error("modseq should have advanced after flag update")
	}
	_, rc2, un2, cerr := d.GetMailboxCounts(user, mbox)
	if cerr != nil {
		t.Fatalf("GetMailboxCounts: %v", cerr)
	}
	if rc2 != 0 || un2 != 0 {
		t.Errorf("after \\Seen: recent=%d unseen=%d want 0,0", rc2, un2)
	}

	// ClearRecent is a no-op now (no \Recent left) but must not error.
	if err := d.ClearRecent(user, mbox); err != nil {
		t.Fatalf("ClearRecent: %v", err)
	}

	// The generated search vector indexes the subject.
	var matches int
	if err := d.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM messages WHERE user_email=$1 AND search @@ to_tsquery('simple','quarterly')`,
		user,
	).Scan(&matches); err != nil {
		t.Fatalf("search query: %v", err)
	}
	if matches != 1 {
		t.Errorf("tsvector search for 'quarterly' matched %d, want 1", matches)
	}

	// Delete records a destroyed email change and removes the row.
	if err := d.DeleteMessage(user, mbox, 1); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if uids, uerr := d.GetMessageUIDs(user, mbox); uerr != nil || len(uids) != 0 {
		t.Errorf("message still present after delete: %v err=%v", uids, uerr)
	}
}

// TestThreads covers deterministic and subject-based thread id assignment and
// the thread record round-trip / thread-message listing.
func TestThreads(t *testing.T) {
	d := openTestDB(t)
	const user, mbox = "u@x.com", "INBOX"

	// Header-based: a reply to the same root gets the same deterministic id.
	root, err := d.GetOrCreateThreadID(user, mbox, "Hello", "<a@x>", "", nil)
	if err != nil {
		t.Fatalf("GetOrCreateThreadID(root): %v", err)
	}
	reply, err := d.GetOrCreateThreadID(user, mbox, "Re: Hello", "<b@x>", "<a@x>", nil)
	if err != nil {
		t.Fatalf("GetOrCreateThreadID(reply): %v", err)
	}
	if root == "" || root != reply {
		t.Errorf("reply thread id %q != root %q (deterministic threading broken)", reply, root)
	}

	// Store two messages on the thread, list them.
	for i, mid := range []string{"<a@x>", "<b@x>"} {
		if err := d.StoreMessageMetadata(user, mbox, uint32(i+1), &storage.MessageMetadata{
			MessageID: mid, UID: uint32(i + 1), ThreadID: root, Subject: "Hello",
			From: "p@x", To: user, Flags: []string{}, InternalDate: time.Now(),
		}); err != nil {
			t.Fatalf("StoreMessageMetadata: %v", err)
		}
	}
	tmsgs, err := d.GetThreadMessages(user, mbox, root)
	if err != nil {
		t.Fatalf("GetThreadMessages: %v", err)
	}
	if len(tmsgs) != 2 {
		t.Errorf("GetThreadMessages returned %d, want 2", len(tmsgs))
	}

	// Subject-based fallback: a header-less message matches the stored subject.
	subjThread, err := d.GetOrCreateThreadID(user, mbox, "Hello", "", "", nil)
	if err != nil {
		t.Fatalf("GetOrCreateThreadID(subject): %v", err)
	}
	if subjThread != root {
		t.Errorf("subject fallback id %q != %q", subjThread, root)
	}

	// Thread record round-trip.
	th := &storage.Thread{
		ThreadID: root, Subject: "Hello", Participants: []string{"p@x", user},
		MessageCount: 2, UnreadCount: 1, CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := d.UpdateThread(user, th); err != nil {
		t.Fatalf("UpdateThread: %v", err)
	}
	got, err := d.GetThread(user, root)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.Subject != "Hello" || got.MessageCount != 2 || got.UnreadCount != 1 || len(got.Participants) != 2 {
		t.Errorf("thread mismatch: %+v", got)
	}
	if _, err := d.GetThread(user, "missing"); err == nil {
		t.Error("GetThread(missing) should error")
	}
}

// TestACL covers the RFC 4314 ACL surface: set/get/list, removal via rights=0,
// bulk delete, and the shared-with / grantees queries.
func TestACL(t *testing.T) {
	d := openTestDB(t)
	const owner, mbox = "owner@x.com", "Shared"

	if err := d.SetACL(owner, mbox, "bob@x.com", storage.ACLRead|storage.ACLLookup, owner); err != nil {
		t.Fatalf("SetACL: %v", err)
	}
	rights, err := d.GetACL(owner, mbox, "bob@x.com")
	if err != nil {
		t.Fatalf("GetACL: %v", err)
	}
	if rights != storage.ACLRead|storage.ACLLookup {
		t.Errorf("rights=%d want %d", rights, storage.ACLRead|storage.ACLLookup)
	}
	// Absent grant returns 0.
	if r, err := d.GetACL(owner, mbox, "nobody@x.com"); err != nil || r != 0 {
		t.Errorf("absent grant rights=%d err=%v want 0,nil", r, err)
	}

	entries, err := d.ListACL(owner, mbox)
	if err != nil || len(entries) != 1 || entries[0].Grantee != "bob@x.com" || entries[0].GrantedBy != owner {
		t.Errorf("ListACL mismatch: %+v err=%v", entries, err)
	}

	shared, err := d.ListMailboxesSharedWith("bob@x.com")
	if err != nil || len(shared) != 1 || shared[0] != owner+":"+mbox {
		t.Errorf("ListMailboxesSharedWith mismatch: %v err=%v", shared, err)
	}
	grantees, err := d.ListGranteesMailboxes(owner)
	if err != nil || len(grantees) != 1 || grantees[0] != mbox {
		t.Errorf("ListGranteesMailboxes mismatch: %v err=%v", grantees, err)
	}

	// rights=0 removes the grant.
	if err := d.SetACL(owner, mbox, "bob@x.com", 0, owner); err != nil {
		t.Fatalf("SetACL(0): %v", err)
	}
	if r, err := d.GetACL(owner, mbox, "bob@x.com"); err != nil || r != 0 {
		t.Errorf("after rights=0, rights=%d err=%v want 0,nil", r, err)
	}

	// Bulk delete: add two grants then delete all for the mailbox.
	for _, g := range []string{"a@x.com", "b@x.com"} {
		if err := d.SetACL(owner, mbox, g, storage.ACLRead, owner); err != nil {
			t.Fatalf("SetACL(%s): %v", g, err)
		}
	}
	if err := d.DeleteACL(owner, mbox, ""); err != nil {
		t.Fatalf("DeleteACL(all): %v", err)
	}
	if list, err := d.ListACL(owner, mbox); err != nil || len(list) != 0 {
		t.Errorf("after bulk delete, ListACL=%+v err=%v want empty", list, err)
	}
}

// TestSpamStore covers the Bayesian persistence surface (spam.Store): token
// increments, per-token frequency, live sums, and the persisted stats override.
func TestSpamStore(t *testing.T) {
	d := openTestDB(t)
	if err := d.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// Increment the same token in both classes.
	if err := d.IncrementToken("spam_tokens", "viagra", 3); err != nil {
		t.Fatalf("IncrementToken spam: %v", err)
	}
	if err := d.IncrementToken("spam_tokens", "viagra", 2); err != nil { // accumulates
		t.Fatalf("IncrementToken spam: %v", err)
	}
	if err := d.IncrementToken("ham_tokens", "viagra", 1); err != nil {
		t.Fatalf("IncrementToken ham: %v", err)
	}
	if err := d.IncrementToken("ham_tokens", "hello", 4); err != nil {
		t.Fatalf("IncrementToken ham: %v", err)
	}

	ham, spam, err := d.GetTokenFrequency("viagra")
	if err != nil || ham != 1 || spam != 5 {
		t.Errorf("GetTokenFrequency(viagra)=(%d,%d) err=%v want (1,5)", ham, spam, err)
	}

	// Live sums (no stats row yet): ham = 1+4 = 5, spam = 5.
	th, ts, err := d.GetTotalCounts()
	if err != nil || th != 5 || ts != 5 {
		t.Errorf("GetTotalCounts live=(%d,%d) err=%v want (5,5)", th, ts, err)
	}

	// Persisted stats override the live sums.
	if err := d.SetTotals(100, 200); err != nil {
		t.Fatalf("SetTotals: %v", err)
	}
	th, ts, err = d.GetTotalCounts()
	if err != nil || th != 100 || ts != 200 {
		t.Errorf("GetTotalCounts after SetTotals=(%d,%d) err=%v want (100,200)", th, ts, err)
	}

	// Unknown token reads as (0,0) with no error.
	if h, s, err := d.GetTokenFrequency("absent"); err != nil || h != 0 || s != 0 {
		t.Errorf("GetTokenFrequency(absent)=(%d,%d) err=%v want (0,0)", h, s, err)
	}
}

// TestQuotaStore covers the per-user daily-send quota surface
// (ratelimit.QuotaStore): persist, read back, overwrite, negative clamp, and
// the absent-user zero default.
func TestQuotaStore(t *testing.T) {
	d := openTestDB(t)
	if err := d.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := d.GetUserSentToday("nobody@x.com"); got != 0 {
		t.Errorf("absent user count=%d want 0", got)
	}
	d.SetUserSentToday("a@x.com", 7)
	if got := d.GetUserSentToday("a@x.com"); got != 7 {
		t.Errorf("count=%d want 7", got)
	}
	d.SetUserSentToday("a@x.com", 12) // overwrite
	if got := d.GetUserSentToday("a@x.com"); got != 12 {
		t.Errorf("count=%d want 12", got)
	}
	d.SetUserSentToday("a@x.com", -5) // clamps to 0
	if got := d.GetUserSentToday("a@x.com"); got != 0 {
		t.Errorf("count after negative=%d want 0", got)
	}
}

// TestBackupJobsAndManifests covers the admin backup persistence surface:
// job create/get/update/delete/list (with the enabledOnly filter) and manifest
// create/get/delete/list (with the target filter), including the not-found
// error parity with the bbolt store.
func TestBackupJobsAndManifests(t *testing.T) {
	d := openTestDB(t)

	// --- jobs ---
	lastRun := time.Now().UTC().Truncate(time.Second)
	job := &storage.BackupJob{
		ID: "job1", Name: "nightly", Type: "full", Target: "all",
		Schedule: "0 3 * * *", Retention: 7, Enabled: true, LastRun: &lastRun,
		Destinations: "s3", Options: "{}", Status: "idle",
	}
	if err := d.CreateBackupJob(job); err != nil {
		t.Fatalf("CreateBackupJob: %v", err)
	}
	got, err := d.GetBackupJob("job1")
	if err != nil || got.Name != "nightly" || got.Retention != 7 || !got.Enabled || got.LastRun == nil {
		t.Errorf("GetBackupJob mismatch: %+v err=%v", got, err)
	}
	job.Status = "running"
	job.Enabled = false
	if err := d.UpdateBackupJob(job); err != nil {
		t.Fatalf("UpdateBackupJob: %v", err)
	}
	if got, err := d.GetBackupJob("job1"); err != nil || got.Status != "running" || got.Enabled {
		t.Errorf("after update: %+v err=%v", got, err)
	}
	// A disabled job must be created and then excluded by the enabledOnly filter.
	if err := d.CreateBackupJob(&storage.BackupJob{ID: "job2", Name: "weekly", Enabled: true}); err != nil {
		t.Fatalf("CreateBackupJob job2: %v", err)
	}
	all, err := d.ListBackupJobs(false)
	if err != nil || len(all) != 2 {
		t.Errorf("ListBackupJobs(false)=%d err=%v want 2", len(all), err)
	}
	enabled, err := d.ListBackupJobs(true)
	if err != nil || len(enabled) != 1 || enabled[0].ID != "job2" {
		t.Errorf("ListBackupJobs(true)=%+v err=%v want [job2]", enabled, err)
	}
	if err := d.DeleteBackupJob("job1"); err != nil {
		t.Fatalf("DeleteBackupJob: %v", err)
	}
	if _, err := d.GetBackupJob("job1"); err == nil {
		t.Error("GetBackupJob(job1) should error after delete")
	}

	// --- manifests ---
	man := &storage.BackupManifest{
		ID: "m1", Filename: "m1.tar.gz", Size: 4096,
		CreatedAt: time.Now().UTC().Truncate(time.Second), Type: "full", Target: "alice",
		Checksum: "abc", Encrypted: true, RetentionUntil: time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second),
		Destination: "local", Path: "/backups/m1.tar.gz",
	}
	if err := d.CreateBackupManifest(man); err != nil {
		t.Fatalf("CreateBackupManifest: %v", err)
	}
	gotMan, err := d.GetBackupManifest("m1")
	if err != nil || gotMan.Size != 4096 || !gotMan.Encrypted || gotMan.RetentionUntil.IsZero() {
		t.Errorf("GetBackupManifest mismatch: %+v err=%v", gotMan, err)
	}
	if err := d.CreateBackupManifest(&storage.BackupManifest{ID: "m2", Target: "bob", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateBackupManifest m2: %v", err)
	}
	if list, err := d.ListBackupManifests(""); err != nil || len(list) != 2 {
		t.Errorf("ListBackupManifests()=%d err=%v want 2", len(list), err)
	}
	if list, err := d.ListBackupManifests("alice"); err != nil || len(list) != 1 || list[0].ID != "m1" {
		t.Errorf("ListBackupManifests(alice)=%+v err=%v want [m1]", list, err)
	}
	if err := d.DeleteBackupManifest("m1"); err != nil {
		t.Fatalf("DeleteBackupManifest: %v", err)
	}
	if _, err := d.GetBackupManifest("m1"); err == nil {
		t.Error("GetBackupManifest(m1) should error after delete")
	}
}

// TestSemcoreLifecycle covers the semantic-core lifecycle journal: per-mailbox
// monotonic seq allocation on append, seq-ordered polling with the sinceSeq
// filter and limit, HighestSequence, and that distinct mailboxes get
// independent seq streams.
func TestSemcoreLifecycle(t *testing.T) {
	d := openTestDB(t)
	mboxA := semcore.MustMailboxId("mbox:a@x.com")
	mboxB := semcore.MustMailboxId("mbox:b@x.com")

	// Empty mailbox: highest seq is 0, poll returns nothing.
	if h, err := d.HighestSequence(mboxA); err != nil || h != 0 {
		t.Errorf("HighestSequence(empty)=%d err=%v want 0", h, err)
	}

	// Append three events to A; seq must be 1,2,3.
	for i := 0; i < 3; i++ {
		ev := semcore.Lifecycle{
			MailboxID: mboxA,
			FolderID:  semcore.MustFolderId("folder:inbox"),
			Kind:      semcore.LifecycleKindCreated,
			At:        time.Now().UTC().Truncate(time.Second),
			Actor:     "alice@x.com",
		}
		if err := d.AppendLifecycle(ev); err != nil {
			t.Fatalf("AppendLifecycle #%d: %v", i, err)
		}
	}
	if h, err := d.HighestSequence(mboxA); err != nil || h != 3 {
		t.Errorf("HighestSequence(A)=%d err=%v want 3", h, err)
	}

	// B is independent: its first event is seq 1.
	if err := d.AppendLifecycle(semcore.Lifecycle{MailboxID: mboxB, Kind: semcore.LifecycleKindUpdated, At: time.Now().UTC()}); err != nil {
		t.Fatalf("AppendLifecycle B: %v", err)
	}
	if h, err := d.HighestSequence(mboxB); err != nil || h != 1 {
		t.Errorf("HighestSequence(B)=%d err=%v want 1", h, err)
	}

	// Poll A from the start returns all three, highest=3, fields round-trip.
	evs, highest, err := d.PollEvents(mboxA, 0, 100)
	if err != nil || len(evs) != 3 || highest != 3 {
		t.Fatalf("PollEvents(A,0)=%d highest=%d err=%v want 3,3", len(evs), highest, err)
	}
	if evs[0].FolderID.String() != "folder:inbox" || evs[0].Actor != "alice@x.com" ||
		evs[0].Kind != semcore.LifecycleKindCreated || evs[0].MailboxID.String() != "mbox:a@x.com" {
		t.Errorf("event round-trip mismatch: %+v", evs[0])
	}

	// sinceSeq filter: only events after seq 1.
	if evs, _, err := d.PollEvents(mboxA, 1, 100); err != nil || len(evs) != 2 {
		t.Errorf("PollEvents(A,since=1)=%d err=%v want 2", len(evs), err)
	}
	// limit caps the result.
	if evs, _, err := d.PollEvents(mboxA, 0, 2); err != nil || len(evs) != 2 {
		t.Errorf("PollEvents(A,limit=2)=%d err=%v want 2", len(evs), err)
	}
}

// TestSemcoreMailboxIdentity covers the semantic-core mailbox identity surface:
// EnsureMailboxId is idempotent (stable id per email), GetMailboxIDByEmail
// returns ErrMailboxNotFound when absent, and MailboxEmailsByID maps ids back to
// emails.
func TestSemcoreMailboxIdentity(t *testing.T) {
	d := openTestDB(t)

	// Absent mailbox.
	if _, err := d.GetMailboxIDByEmail("ghost@x.com"); !errors.Is(err, semcore.ErrMailboxNotFound) {
		t.Errorf("GetMailboxIDByEmail(absent) err=%v want ErrMailboxNotFound", err)
	}

	// EnsureMailboxId mints a stable id; a second call returns the same id.
	id1, err := d.EnsureMailboxId("alice@x.com")
	if err != nil || id1.IsZero() {
		t.Fatalf("EnsureMailboxId: id=%v err=%v", id1, err)
	}
	id2, err := d.EnsureMailboxId("alice@x.com")
	if err != nil || !id1.Equal(id2) {
		t.Errorf("EnsureMailboxId not idempotent: %v vs %v err=%v", id1, id2, err)
	}

	// GetMailboxIDByEmail returns the same id.
	got, err := d.GetMailboxIDByEmail("alice@x.com")
	if err != nil || !got.Equal(id1) {
		t.Errorf("GetMailboxIDByEmail=%v err=%v want %v", got, err, id1)
	}

	// A different email gets a distinct id.
	idBob, err := d.EnsureMailboxId("bob@x.com")
	if err != nil || idBob.Equal(id1) {
		t.Errorf("distinct email shares id: bob=%v alice=%v err=%v", idBob, id1, err)
	}

	// MailboxEmailsByID maps both ids back to their emails.
	m, err := d.MailboxEmailsByID()
	if err != nil {
		t.Fatalf("MailboxEmailsByID: %v", err)
	}
	if m[id1.String()] != "alice@x.com" || m[idBob.String()] != "bob@x.com" {
		t.Errorf("MailboxEmailsByID mismatch: %+v", m)
	}
}

// TestSemcoreFolderIdentity covers the folder-identity surface: EnsureFolderId
// idempotency and role-based dedup, lookups by name/id/role, listing,
// name-by-id, parent set, and delete.
func TestSemcoreFolderIdentity(t *testing.T) {
	d := openTestDB(t)
	const mbox = "alice@x.com"

	// EnsureFolderId mints a stable id; re-ensuring the same name returns it.
	inbox, err := d.EnsureFolderId(mbox, "INBOX", "inbox")
	if err != nil || inbox.IsZero() {
		t.Fatalf("EnsureFolderId(INBOX): id=%v err=%v", inbox, err)
	}
	if again, err := d.EnsureFolderId(mbox, "INBOX", "inbox"); err != nil || !again.Equal(inbox) {
		t.Errorf("EnsureFolderId not idempotent: %v vs %v err=%v", again, inbox, err)
	}

	// Role dedup: a different name with the same existing role returns the
	// existing folder's id (bbolt parity).
	if dup, err := d.EnsureFolderId(mbox, "Inbox-alias", "inbox"); err != nil || !dup.Equal(inbox) {
		t.Errorf("role dedup failed: %v vs %v err=%v", dup, inbox, err)
	}

	// GetFolderID resolves by name.
	if got, err := d.GetFolderID(mbox, "INBOX"); err != nil || !got.Equal(inbox) {
		t.Errorf("GetFolderID=%v err=%v want %v", got, err, inbox)
	}
	if _, err := d.GetFolderID(mbox, "Nope"); !errors.Is(err, semcore.ErrFolderNotFound) {
		t.Errorf("GetFolderID(absent) err=%v want ErrFolderNotFound", err)
	}

	// GetFolderByID returns the record with MailboxID == mbox key.
	rec, err := d.GetFolderByID(inbox)
	if err != nil || rec.Role != "inbox" || rec.MailboxID.String() != mbox || !rec.IsSubscribed {
		t.Errorf("GetFolderByID mismatch: %+v err=%v", rec, err)
	}

	// FolderNameByID recovers the stored name.
	if name, err := d.FolderNameByID(mbox, inbox); err != nil || name != "INBOX" {
		t.Errorf("FolderNameByID=%q err=%v want INBOX", name, err)
	}

	// A second role + a user folder, then list.
	sent, err := d.EnsureFolderId(mbox, "Sent", "sent")
	if err != nil {
		t.Fatalf("EnsureFolderId(Sent): %v", err)
	}
	if _, err := d.EnsureFolderId(mbox, "Project X", ""); err != nil {
		t.Fatalf("EnsureFolderId(user): %v", err)
	}
	list, err := d.ListFolderIdentitiesForMailbox(mbox)
	if err != nil || len(list) != 3 { // INBOX, Sent, Project X (Inbox-alias deduped to inbox, not stored)
		t.Errorf("ListFolderIdentitiesForMailbox=%d err=%v want 3", len(list), err)
	}

	// GetFolderByMailbox by role.
	if got, err := d.GetFolderByMailbox(mbox, "sent"); err != nil || !got.FolderID.Equal(sent) {
		t.Errorf("GetFolderByMailbox(sent)=%v err=%v want %v", got, err, sent)
	}

	// SetFolderParent then verify.
	if err := d.SetFolderParent(sent, inbox); err != nil {
		t.Fatalf("SetFolderParent: %v", err)
	}
	if rec, err := d.GetFolderByID(sent); err != nil || !rec.ParentID.Equal(inbox) {
		t.Errorf("SetFolderParent not applied: parent=%v err=%v want %v", rec.ParentID, err, inbox)
	}

	// DeleteFolder removes it.
	if err := d.DeleteFolder(sent); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	if _, err := d.GetFolderByID(sent); !errors.Is(err, semcore.ErrFolderNotFound) {
		t.Errorf("after delete, GetFolderByID err=%v want ErrFolderNotFound", err)
	}
	if err := d.DeleteFolder(sent); !errors.Is(err, semcore.ErrFolderNotFound) {
		t.Errorf("DeleteFolder(absent) err=%v want ErrFolderNotFound", err)
	}
}

// TestSemcoreChildFolderIdentity covers EnsureChildFolderId parity with the
// bbolt store: two children named the same under different parents get distinct
// identities (a real copy, not a collapse), each records its parent, both render
// the same client-visible name, and the operation is idempotent.
func TestSemcoreChildFolderIdentity(t *testing.T) {
	d := openTestDB(t)
	const mbox = "alice@x.com"

	parentA, err := d.EnsureFolderId(mbox, "Projects", "")
	if err != nil {
		t.Fatalf("EnsureFolderId(Projects): %v", err)
	}
	parentB, err := d.EnsureFolderId(mbox, "Archive", "")
	if err != nil {
		t.Fatalf("EnsureFolderId(Archive): %v", err)
	}

	idA, err := d.EnsureChildFolderId(mbox, parentA, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId A: %v", err)
	}
	idB, err := d.EnsureChildFolderId(mbox, parentB, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId B: %v", err)
	}
	if idA.Equal(idB) {
		t.Fatalf("same-name children under different parents share id %v", idA)
	}

	recA, err := d.GetFolderByID(idA)
	if err != nil || !recA.ParentID.Equal(parentA) {
		t.Errorf("child A parent=%v err=%v want %v", recA.ParentID, err, parentA)
	}
	recB, err := d.GetFolderByID(idB)
	if err != nil || !recB.ParentID.Equal(parentB) {
		t.Errorf("child B parent=%v err=%v want %v", recB.ParentID, err, parentB)
	}

	for _, tc := range []struct {
		id  semcore.FolderId
		tag string
	}{{idA, "A"}, {idB, "B"}} {
		stored, err := d.FolderNameByID(mbox, tc.id)
		if err != nil {
			t.Fatalf("FolderNameByID %s: %v", tc.tag, err)
		}
		if got := semcore.DisplayNameFromStorageName(stored); got != "Reports" {
			t.Errorf("child %s display name = %q, want Reports", tc.tag, got)
		}
	}

	// Idempotent: repeat calls reuse the existing ids.
	if again, err := d.EnsureChildFolderId(mbox, parentA, "Reports", ""); err != nil || !again.Equal(idA) {
		t.Errorf("EnsureChildFolderId A repeat: %v err=%v want %v", again, err, idA)
	}
	if again, err := d.EnsureChildFolderId(mbox, parentB, "Reports", ""); err != nil || !again.Equal(idB) {
		t.Errorf("EnsureChildFolderId B repeat: %v err=%v want %v", again, err, idB)
	}
}

// TestSemcoreItemIdentity covers the item-identity surface: put (default and
// explicit storage key), get-by-id, list-by-folder, folder/msgKey moves,
// read/category state updates, the optimistic ChangeKey advance (with stale
// rejection), delete, and conversation registration.
func TestSemcoreItemIdentity(t *testing.T) {
	d := openTestDB(t)
	mbox := semcore.MustMailboxId("alice@x.com")
	folder := semcore.MustFolderId("folder:inbox")
	item := semcore.MustItemId("item:1")
	conv := semcore.MustConversationId("conv:1")

	// Put under the default storage key (email + msgKey).
	if err := d.PutItemIdentity("msg-1", "alice@x.com", item, mbox, folder, semcore.ChangeKey{}, conv, false); err != nil {
		t.Fatalf("PutItemIdentity: %v", err)
	}
	// A duplicate storage key is rejected.
	if err := d.PutItemIdentity("msg-1", "alice@x.com", item, mbox, folder, semcore.ChangeKey{}, conv, false); !errors.Is(err, semcore.ErrIdentityExists) {
		t.Errorf("duplicate put err=%v want ErrIdentityExists", err)
	}

	// Get round-trips the fields.
	rec, err := d.GetItemIdentity(item)
	if err != nil || rec.MsgKey != "msg-1" || rec.Email != "alice@x.com" ||
		!rec.FolderID.Equal(folder) || rec.IsRead || !rec.ConversationID.Equal(conv) {
		t.Fatalf("GetItemIdentity mismatch: %+v err=%v", rec, err)
	}
	if _, err := d.GetItemIdentity(semcore.MustItemId("ghost")); !errors.Is(err, semcore.ErrItemNotFound) {
		t.Errorf("GetItemIdentity(absent) err=%v want ErrItemNotFound", err)
	}

	// List by folder.
	if list, err := d.ListItemIdentitiesByFolder(folder); err != nil || len(list) != 1 || !list[0].ItemID.Equal(item) {
		t.Errorf("ListItemIdentitiesByFolder=%+v err=%v want [item:1]", list, err)
	}

	// Move folder + msgKey.
	other := semcore.MustFolderId("folder:archive")
	if err := d.SetItemFolder(item, other); err != nil {
		t.Fatalf("SetItemFolder: %v", err)
	}
	if err := d.SetItemMsgKey(item, "msg-1-new"); err != nil {
		t.Fatalf("SetItemMsgKey: %v", err)
	}
	rec, err = d.GetItemIdentity(item)
	if err != nil || !rec.FolderID.Equal(other) || rec.MsgKey != "msg-1-new" {
		t.Errorf("after moves: folder=%v msgKey=%q err=%v", rec.FolderID, rec.MsgKey, err)
	}

	// UpdateItemState: read flag only, then categories only (each preserves the other).
	read := true
	if err := d.UpdateItemState(item, &read, nil); err != nil {
		t.Fatalf("UpdateItemState(read): %v", err)
	}
	if err := d.UpdateItemState(item, nil, []string{"Work", "Urgent"}); err != nil {
		t.Fatalf("UpdateItemState(cats): %v", err)
	}
	rec, err = d.GetItemIdentity(item)
	if err != nil || !rec.IsRead || len(rec.Categories) != 2 || rec.Categories[0] != "Work" {
		t.Errorf("UpdateItemState result: isRead=%v cats=%v err=%v", rec.IsRead, rec.Categories, err)
	}

	// ChangeKey optimistic advance: first write (zero current) succeeds.
	ck1 := semcore.MustChangeKey("ck-1")
	if err := d.PutChangeKey(item, semcore.ChangeKey{}, ck1); err != nil {
		t.Fatalf("PutChangeKey(first): %v", err)
	}
	// A stale current key is rejected.
	if err := d.PutChangeKey(item, semcore.ChangeKey{}, semcore.MustChangeKey("ck-x")); err == nil {
		t.Error("PutChangeKey with stale current should error")
	}
	// The matching current key advances it.
	if err := d.PutChangeKey(item, ck1, semcore.MustChangeKey("ck-2")); err != nil {
		t.Fatalf("PutChangeKey(advance): %v", err)
	}
	if rec, err := d.GetItemIdentity(item); err != nil || rec.ChangeKey.String() != "ck-2" {
		t.Errorf("ChangeKey=%q err=%v want ck-2", rec.ChangeKey.String(), err)
	}
	// Absent item.
	if err := d.PutChangeKey(semcore.MustItemId("ghost"), semcore.ChangeKey{}, ck1); !errors.Is(err, semcore.ErrItemNotFound) {
		t.Errorf("PutChangeKey(absent) err=%v want ErrItemNotFound", err)
	}

	// Explicit-storage-key put (same content, different folder identity).
	item2 := semcore.MustItemId("item:2")
	if err := d.PutItemIdentityWithKey("custom-key", "msg-1", "alice@x.com", item2, mbox, folder, semcore.ChangeKey{}, conv, true); err != nil {
		t.Fatalf("PutItemIdentityWithKey: %v", err)
	}
	if rec, err := d.GetItemIdentity(item2); err != nil || !rec.IsRead {
		t.Errorf("item2 get: %+v err=%v", rec, err)
	}
	// GetItemIDByKey resolves an item by its stored message key.
	if gotID, err := d.GetItemIDByKey("msg-1"); err != nil || !gotID.Equal(item2) {
		t.Errorf("GetItemIDByKey(msg-1)=%v err=%v want %v", gotID, err, item2)
	}
	if _, err := d.GetItemIDByKey("no-such-key"); !errors.Is(err, semcore.ErrItemNotFound) {
		t.Errorf("GetItemIDByKey(absent) err=%v want ErrItemNotFound", err)
	}

	// Conversation registration: idempotent-reject on duplicate.
	if err := d.PutConversationIdentity(conv, mbox); err != nil {
		t.Fatalf("PutConversationIdentity: %v", err)
	}
	if err := d.PutConversationIdentity(conv, mbox); !errors.Is(err, semcore.ErrIdentityExists) {
		t.Errorf("duplicate conversation err=%v want ErrIdentityExists", err)
	}

	// Delete.
	if err := d.DeleteItemIdentity(item); err != nil {
		t.Fatalf("DeleteItemIdentity: %v", err)
	}
	if _, err := d.GetItemIdentity(item); !errors.Is(err, semcore.ErrItemNotFound) {
		t.Errorf("after delete err=%v want ErrItemNotFound", err)
	}
}

// TestSemcoreSyncState covers per-client sync state: put creates version 1, a
// second put bumps the version and updates the watermark, GetSyncState reads it
// back, MarkFolderGone flags it, and absent lookups return ErrSyncStateNotFound.
func TestSemcoreSyncState(t *testing.T) {
	d := openTestDB(t)
	mbox := semcore.MustMailboxId("alice@x.com")
	folder := semcore.MustFolderId("folder:inbox")
	const client = "ews-1"

	if _, err := d.GetSyncState(mbox, folder, client); !errors.Is(err, semcore.ErrSyncStateNotFound) {
		t.Errorf("GetSyncState(absent) err=%v want ErrSyncStateNotFound", err)
	}

	if err := d.PutSyncState(mbox, folder, client, "wm-1"); err != nil {
		t.Fatalf("PutSyncState: %v", err)
	}
	rec, err := d.GetSyncState(mbox, folder, client)
	if err != nil || rec.Watermark != "wm-1" || rec.Version != 1 || rec.FolderGone {
		t.Fatalf("GetSyncState after first put: %+v err=%v", rec, err)
	}

	// Second put bumps version and updates watermark.
	if err := d.PutSyncState(mbox, folder, client, "wm-2"); err != nil {
		t.Fatalf("PutSyncState #2: %v", err)
	}
	rec, err = d.GetSyncState(mbox, folder, client)
	if err != nil || rec.Watermark != "wm-2" || rec.Version != 2 {
		t.Errorf("GetSyncState after second put: %+v err=%v", rec, err)
	}

	// MarkFolderGone flags the state.
	if err := d.MarkFolderGone(folder); err != nil {
		t.Fatalf("MarkFolderGone: %v", err)
	}
	if rec, err := d.GetSyncState(mbox, folder, client); err != nil || !rec.FolderGone {
		t.Errorf("after MarkFolderGone: folderGone=%v err=%v want true", rec.FolderGone, err)
	}
	// A subsequent put clears folder_gone.
	if err := d.PutSyncState(mbox, folder, client, "wm-3"); err != nil {
		t.Fatalf("PutSyncState #3: %v", err)
	}
	if rec, err := d.GetSyncState(mbox, folder, client); err != nil || rec.FolderGone || rec.Version != 3 {
		t.Errorf("after re-put: %+v err=%v want folderGone=false version=3", rec, err)
	}
}

// TestSemcoreTombstone covers tombstones: put, newest-wins upsert, the
// since/folder filters in ListTombstonesSince, and that an older write does not
// overwrite a newer record.
func TestSemcoreTombstone(t *testing.T) {
	d := openTestDB(t)
	mbox := semcore.MustMailboxId("alice@x.com")
	inbox := semcore.MustFolderId("folder:inbox")
	archive := semcore.MustFolderId("folder:archive")
	item := semcore.MustItemId("item:1")

	t0 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	t1 := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	t2 := time.Now().UTC().Truncate(time.Second)

	// Initial tombstone in the inbox.
	if err := d.PutTombstone(semcore.Tombstone{MailboxID: mbox, FolderID: inbox, ItemID: item, Kind: semcore.LifecycleKindSoftDeleted, DeletedAt: t1, Actor: "alice"}); err != nil {
		t.Fatalf("PutTombstone: %v", err)
	}
	// A newer write for the same key advances DeletedAt.
	if err := d.PutTombstone(semcore.Tombstone{MailboxID: mbox, FolderID: inbox, ItemID: item, Kind: semcore.LifecycleKindSoftDeleted, DeletedAt: t2, Actor: "alice"}); err != nil {
		t.Fatalf("PutTombstone newer: %v", err)
	}
	// An older write must NOT overwrite the newer record.
	if err := d.PutTombstone(semcore.Tombstone{MailboxID: mbox, FolderID: inbox, ItemID: item, Kind: semcore.LifecycleKindSoftDeleted, DeletedAt: t0, Actor: "stale"}); err != nil {
		t.Fatalf("PutTombstone older: %v", err)
	}
	// A tombstone in another folder.
	if err := d.PutTombstone(semcore.Tombstone{MailboxID: mbox, FolderID: archive, ItemID: semcore.MustItemId("item:2"), Kind: semcore.LifecycleKindHardDeleted, DeletedAt: t2}); err != nil {
		t.Fatalf("PutTombstone archive: %v", err)
	}

	// Since-filter from before t1 returns both, and the inbox record keeps t2.
	all, err := d.ListTombstonesSince(mbox, semcore.FolderId{}, t0)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListTombstonesSince(all)=%d err=%v want 2", len(all), err)
	}
	for _, ts := range all {
		if ts.FolderID.Equal(inbox) && (!ts.DeletedAt.Equal(t2) || ts.Actor != "alice") {
			t.Errorf("inbox tombstone not newest: %+v", ts)
		}
	}

	// Folder filter restricts to the archive folder.
	if list, err := d.ListTombstonesSince(mbox, archive, t0); err != nil || len(list) != 1 || !list[0].FolderID.Equal(archive) {
		t.Errorf("ListTombstonesSince(archive)=%+v err=%v want 1 archive", list, err)
	}

	// Since after t2 excludes everything.
	if list, err := d.ListTombstonesSince(mbox, semcore.FolderId{}, t2.Add(time.Second)); err != nil || len(list) != 0 {
		t.Errorf("ListTombstonesSince(future)=%d err=%v want 0", len(list), err)
	}
}

// TestSemcoreSubscription covers subscriptions: create assigns a sub- id and a
// default expiry, get/list round-trip the fields, renew extends expiry, a
// drained subscription surfaces ErrSubscriptionDrained, and remove deletes it.
func TestSemcoreSubscription(t *testing.T) {
	d := openTestDB(t)
	mbox := semcore.MustMailboxId("alice@x.com")
	f1 := semcore.MustFolderId("folder:inbox")

	id, err := d.CreateSubscription(semcore.Subscription{
		MailboxID: mbox,
		Kind:      semcore.SubscriptionKindPull,
		FolderIDs: []semcore.FolderId{f1},
		PushURL:   "https://example.com/push",
	})
	if err != nil || id.ID == "" || id.ID[:4] != "sub-" {
		t.Fatalf("CreateSubscription: id=%q err=%v", id.ID, err)
	}

	sub, err := d.GetSubscription(id)
	if err != nil || sub.MailboxID.String() != "alice@x.com" || sub.PushURL != "https://example.com/push" ||
		len(sub.FolderIDs) != 1 || !sub.FolderIDs[0].Equal(f1) || sub.ExpiresAt.IsZero() {
		t.Fatalf("GetSubscription mismatch: %+v err=%v", sub, err)
	}

	// List by mailbox.
	if list, err := d.ListSubscriptionsByMailbox(mbox); err != nil || len(list) != 1 || list[0].ID.ID != id.ID {
		t.Errorf("ListSubscriptionsByMailbox=%+v err=%v want 1", list, err)
	}

	// Renew extends the expiry.
	before := sub.ExpiresAt
	if err := d.RenewSubscription(id); err != nil {
		t.Fatalf("RenewSubscription: %v", err)
	}
	if renewed, err := d.GetSubscription(id); err != nil || (!renewed.ExpiresAt.After(before) && !renewed.ExpiresAt.Equal(before)) {
		t.Errorf("RenewSubscription did not extend expiry: %v vs %v err=%v", renewed.ExpiresAt, before, err)
	}

	// Renew/Get on an absent id errors.
	if err := d.RenewSubscription(semcore.SubscriptionId{ID: "sub-ghost"}); err == nil {
		t.Error("RenewSubscription(absent) should error")
	}
	if _, err := d.GetSubscription(semcore.SubscriptionId{ID: "sub-ghost"}); err == nil {
		t.Error("GetSubscription(absent) should error")
	}

	// Remove deletes it; a removed subscription is gone.
	if err := d.RemoveSubscription(id); err != nil {
		t.Fatalf("RemoveSubscription: %v", err)
	}
	if list, err := d.ListSubscriptionsByMailbox(mbox); err != nil || len(list) != 0 {
		t.Errorf("after remove, list=%d err=%v want 0", len(list), err)
	}
	// Removing an absent subscription is a no-op (no error).
	if err := d.RemoveSubscription(id); err != nil {
		t.Errorf("RemoveSubscription(absent) err=%v want nil", err)
	}
}

// TestSemcorePushDispatcherSurface covers the push-dispatcher store methods:
// ListPushSubscriptions returns only push subscriptions (not pull, not drained),
// and UpdateSubscriptionSeq advances last_seq while renewing expiry.
func TestSemcorePushDispatcherSurface(t *testing.T) {
	d := openTestDB(t)
	mbox := semcore.MustMailboxId("push@x.com")

	pushID, err := d.CreateSubscription(semcore.Subscription{
		MailboxID: mbox, Kind: semcore.SubscriptionKindPush, PushURL: "https://example.com/cb",
	})
	if err != nil {
		t.Fatalf("CreateSubscription(push): %v", err)
	}
	// A pull subscription must NOT appear in the push list.
	if _, err := d.CreateSubscription(semcore.Subscription{
		MailboxID: mbox, Kind: semcore.SubscriptionKindPull,
	}); err != nil {
		t.Fatalf("CreateSubscription(pull): %v", err)
	}

	list, err := d.ListPushSubscriptions()
	if err != nil || len(list) != 1 || list[0].ID.ID != pushID.ID || list[0].Kind != semcore.SubscriptionKindPush {
		t.Fatalf("ListPushSubscriptions=%+v err=%v want exactly the push sub", list, err)
	}

	// UpdateSubscriptionSeq advances last_seq.
	if err := d.UpdateSubscriptionSeq(pushID, 42); err != nil {
		t.Fatalf("UpdateSubscriptionSeq: %v", err)
	}
	got, err := d.GetSubscription(pushID)
	if err != nil || got.LastSeq != 42 {
		t.Fatalf("after UpdateSubscriptionSeq, last_seq=%d err=%v want 42", got.LastSeq, err)
	}
	if err := d.UpdateSubscriptionSeq(semcore.SubscriptionId{ID: "sub-ghost"}, 1); err == nil {
		t.Error("UpdateSubscriptionSeq(absent) should error")
	}

	// A drained push subscription is excluded from the push list.
	if _, err := d.ExpireAllSubscriptions(); err != nil {
		t.Fatalf("ExpireAllSubscriptions: %v", err)
	}
	if list, err := d.ListPushSubscriptions(); err != nil || len(list) != 0 {
		t.Errorf("after drain, ListPushSubscriptions=%d err=%v want 0", len(list), err)
	}
}

// TestSemcoreDelegate covers delegate grants: create assigns a del- id, upsert
// by (owner, email) preserves the id and created_at while updating fields and
// permissions, lookups by id and by (owner, email), listing, and remove.
func TestSemcoreDelegate(t *testing.T) {
	d := openTestDB(t)
	owner := semcore.MustMailboxId("owner@x.com")

	grant := &semcore.DelegateUser{
		OwnerID:         owner,
		DelegateEmail:   "bob@x.com",
		Permissions:     semcore.DelegateFolderPermissions{Calendar: "reviewer", Inbox: "editor"},
		CanSendAs:       true,
		CanSendOnBehalf: true,
		GrantedBy:       "owner@x.com",
	}
	id, err := d.PutDelegate(grant)
	if err != nil || id.String()[:4] != "del-" {
		t.Fatalf("PutDelegate: id=%q err=%v", id.String(), err)
	}
	if grant.CreatedAt.IsZero() {
		t.Error("PutDelegate did not stamp CreatedAt")
	}
	created := grant.CreatedAt

	// Get by id round-trips the permissions and flags.
	got, err := d.GetDelegate(id)
	if err != nil || got.Permissions.Calendar != "reviewer" || got.Permissions.Inbox != "editor" ||
		!got.CanSendAs || !got.CanSendOnBehalf || got.GrantedBy != "owner@x.com" {
		t.Fatalf("GetDelegate mismatch: %+v err=%v", got, err)
	}

	// Upsert (same owner+email) keeps the id and created_at, updates permissions.
	grant.Permissions.Calendar = "author"
	grant.CanSendAs = false
	id2, err := d.PutDelegate(grant)
	if err != nil || !id2.Equal(id) || !grant.CreatedAt.Equal(created) {
		t.Errorf("upsert changed id/created: id2=%v created=%v err=%v", id2, grant.CreatedAt, err)
	}
	if got, err := d.GetDelegate(id); err != nil || got.Permissions.Calendar != "author" || got.CanSendAs {
		t.Errorf("upsert did not update fields: %+v err=%v", got, err)
	}

	// GetDelegateForUser by (owner, email).
	if got, err := d.GetDelegateForUser(owner, "bob@x.com"); err != nil || !got.ID.Equal(id) {
		t.Errorf("GetDelegateForUser=%v err=%v want id %v", got, err, id)
	}
	if _, err := d.GetDelegateForUser(owner, "nobody@x.com"); err == nil {
		t.Error("GetDelegateForUser(absent) should error")
	}

	// A second grant from the same owner, then list.
	if _, err := d.PutDelegate(&semcore.DelegateUser{OwnerID: owner, DelegateEmail: "carol@x.com"}); err != nil {
		t.Fatalf("PutDelegate carol: %v", err)
	}
	if list, err := d.ListDelegates(owner); err != nil || len(list) != 2 {
		t.Errorf("ListDelegates=%d err=%v want 2", len(list), err)
	}
	if all, err := d.ListAllDelegates(); err != nil || len(all) != 2 {
		t.Errorf("ListAllDelegates=%d err=%v want 2", len(all), err)
	}

	// Remove by id; removing again errors.
	if err := d.RemoveDelegate(id); err != nil {
		t.Fatalf("RemoveDelegate: %v", err)
	}
	if _, err := d.GetDelegate(id); err == nil {
		t.Error("GetDelegate after remove should error")
	}
	if err := d.RemoveDelegate(id); err == nil {
		t.Error("RemoveDelegate(absent) should error")
	}
}

// TestSemcorePolicy covers the policy store: inbox rules (put assigns a
// ChangeKey, conditions/actions round-trip as JSONB, list sorts by priority),
// out-of-office (typed fields + excludes), resource policies, and room lists.
func TestSemcorePolicy(t *testing.T) {
	d := openTestDB(t)
	mbox := semcore.MustMailboxId("alice@x.com")

	// --- rules ---
	r1 := &semcore.Rule{
		ID: semcore.MustRuleId("rule:1"), MailboxID: mbox, Name: "Newsletter", Enabled: true, Priority: 5,
		MatchAll:   true,
		Conditions: []semcore.RuleCondition{{Kind: semcore.RuleConditionKindFrom, MatchType: semcore.RuleMatchTypeContains, Value: "news@x.com"}},
		Actions:    []semcore.RuleAction{{Kind: semcore.RuleActionKindMoveToFolder, Target: "News"}},
	}
	if err := d.PutRule(r1); err != nil {
		t.Fatalf("PutRule: %v", err)
	}
	if r1.ChangeKey.IsZero() {
		t.Error("PutRule did not assign a ChangeKey")
	}
	got, err := d.GetRule(r1.ID)
	if err != nil || got.Name != "Newsletter" || len(got.Conditions) != 1 || got.Conditions[0].Value != "news@x.com" ||
		len(got.Actions) != 1 || got.Actions[0].Target != "News" {
		t.Fatalf("GetRule mismatch: %+v err=%v", got, err)
	}
	// A higher-priority (lower number) rule, then list sorted.
	r2 := &semcore.Rule{ID: semcore.MustRuleId("rule:2"), MailboxID: mbox, Name: "Urgent", Priority: 1}
	if err := d.PutRule(r2); err != nil {
		t.Fatalf("PutRule r2: %v", err)
	}
	list, err := d.ListRules(mbox)
	if err != nil || len(list) != 2 || list[0].Priority != 1 {
		t.Errorf("ListRules sort: %+v err=%v want priority 1 first", list, err)
	}
	if all, err := d.ListAllRules(); err != nil || len(all) != 2 {
		t.Errorf("ListAllRules=%d err=%v want 2", len(all), err)
	}
	if err := d.DeleteRule(r1.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if _, err := d.GetRule(r1.ID); err == nil {
		t.Error("GetRule after delete should error")
	}

	// --- OOF (OOFId == MailboxId) ---
	oofID := semcore.MustOOFId("alice@x.com")
	oof := &semcore.OOFPolicy{
		ID: oofID, MailboxID: mbox, Enabled: true, State: "Enabled",
		Subject: "Away", TextBody: "Back Monday", ExcludeAddresses: []string{"boss@x.com"},
		SendIntervalSeconds: 3600,
	}
	if err := d.PutOOF(oof); err != nil {
		t.Fatalf("PutOOF: %v", err)
	}
	if oof.ChangeKey.IsZero() {
		t.Error("PutOOF did not assign a ChangeKey")
	}
	gotOOF, err := d.GetOOF(oofID)
	if err != nil || !gotOOF.Enabled || gotOOF.Subject != "Away" || len(gotOOF.ExcludeAddresses) != 1 ||
		gotOOF.ExcludeAddresses[0] != "boss@x.com" || gotOOF.SendIntervalSeconds != 3600 {
		t.Fatalf("GetOOF mismatch: %+v err=%v", gotOOF, err)
	}
	if _, err := d.GetOOF(semcore.MustOOFId("ghost@x.com")); err == nil {
		t.Error("GetOOF(absent) should error")
	}

	// --- resources ---
	resID := semcore.MustResourceId("room:1")
	res := &semcore.ResourcePolicy{
		ID: resID, MailboxID: mbox, Name: "Big Room", Email: "room1@x.com", Capacity: 12,
		AllowRecurring: true, MaxDurationMinutes: 120,
	}
	if err := d.PutResource(res); err != nil {
		t.Fatalf("PutResource: %v", err)
	}
	if res.ChangeKey.IsZero() {
		t.Error("PutResource did not assign a ChangeKey")
	}
	if gotRes, err := d.GetResource(resID); err != nil || gotRes.Capacity != 12 || !gotRes.AllowRecurring || gotRes.Email != "room1@x.com" {
		t.Fatalf("GetResource mismatch: %+v err=%v", gotRes, err)
	}
	if resList, err := d.ListResources(); err != nil || len(resList) != 1 {
		t.Errorf("ListResources=%d err=%v want 1", len(resList), err)
	}
	if err := d.DeleteResource(resID); err != nil {
		t.Fatalf("DeleteResource: %v", err)
	}
	if _, err := d.GetResource(resID); err == nil {
		t.Error("GetResource after delete should error")
	}

	// --- room lists ---
	rl := &semcore.RoomList{ID: "rl:1", Name: "Floor 3", Rooms: []string{"room1@x.com", "room2@x.com"}}
	if err := d.PutRoomList(rl); err != nil {
		t.Fatalf("PutRoomList: %v", err)
	}
	if gotRL, err := d.GetRoomList("rl:1"); err != nil || gotRL.Name != "Floor 3" || len(gotRL.Rooms) != 2 {
		t.Fatalf("GetRoomList mismatch: %+v err=%v", gotRL, err)
	}
	if rls, err := d.ListRoomLists(); err != nil || len(rls) != 1 {
		t.Errorf("ListRoomLists=%d err=%v want 1", len(rls), err)
	}
	if err := d.DeleteRoomList("rl:1"); err != nil {
		t.Fatalf("DeleteRoomList: %v", err)
	}
	if _, err := d.GetRoomList("rl:1"); err == nil {
		t.Error("GetRoomList after delete should error")
	}
}

// TestSemcoreCollab covers the collaboration identities (calendar/task/contact):
// PutUnsafe upsert, FindByUID (found + not-found), ListByFolder, and DeleteByUID.
func TestSemcoreCollab(t *testing.T) {
	d := openTestDB(t)
	mbox := semcore.MustMailboxId("alice@x.com")
	cal := semcore.MustFolderId("folder:calendar")
	tasks := semcore.MustFolderId("folder:tasks")
	contacts := semcore.MustFolderId("folder:contacts")

	// Calendar item.
	if err := d.PutCalendarItemIdentityUnsafe("cal-key-1", &semcore.StoredCalendarItemIdentity{
		ID: semcore.MustCalendarItemId("cal:1"), FolderID: cal, MailboxID: mbox,
		IcalUID: "uid-cal-1", RawData: "BEGIN:VEVENT", ETag: "etag1",
	}); err != nil {
		t.Fatalf("PutCalendarItemIdentityUnsafe: %v", err)
	}
	key, rec, found, err := d.FindCalendarItemByUID(cal, "uid-cal-1")
	if err != nil || !found || key != "cal-key-1" || rec.ID.String() != "cal:1" || rec.RawData != "BEGIN:VEVENT" {
		t.Fatalf("FindCalendarItemByUID: key=%q rec=%+v found=%v err=%v", key, rec, found, err)
	}
	if _, _, found, err := d.FindCalendarItemByUID(cal, "nope"); err != nil || found {
		t.Errorf("FindCalendarItemByUID(absent) found=%v err=%v want false,nil", found, err)
	}
	if list, err := d.ListCalendarItemsByFolder(cal); err != nil || len(list) != 1 {
		t.Errorf("ListCalendarItemsByFolder=%d err=%v want 1", len(list), err)
	}
	// Upsert (same key) updates in place.
	if err := d.PutCalendarItemIdentityUnsafe("cal-key-1", &semcore.StoredCalendarItemIdentity{
		ID: semcore.MustCalendarItemId("cal:1"), FolderID: cal, MailboxID: mbox, IcalUID: "uid-cal-1", RawData: "UPDATED",
	}); err != nil {
		t.Fatalf("PutCalendarItemIdentityUnsafe update: %v", err)
	}
	if _, rec, _, err := d.FindCalendarItemByUID(cal, "uid-cal-1"); err != nil || rec.RawData != "UPDATED" {
		t.Errorf("calendar upsert did not update RawData: %q err=%v", rec.RawData, err)
	}
	if err := d.DeleteCalendarItemByUID(cal, "uid-cal-1"); err != nil {
		t.Fatalf("DeleteCalendarItemByUID: %v", err)
	}
	if _, _, found, err := d.FindCalendarItemByUID(cal, "uid-cal-1"); err != nil || found {
		t.Errorf("calendar item still found after delete: found=%v err=%v", found, err)
	}
	// Delete-absent is a no-op.
	if err := d.DeleteCalendarItemByUID(cal, "uid-cal-1"); err != nil {
		t.Errorf("DeleteCalendarItemByUID(absent) err=%v want nil", err)
	}

	// Task.
	if err := d.PutTaskIdentityUnsafe("task-key-1", &semcore.StoredTaskIdentity{
		ID: semcore.MustTaskId("task:1"), FolderID: tasks, MailboxID: mbox, IcalUID: "uid-task-1", RawData: "BEGIN:VTODO",
	}); err != nil {
		t.Fatalf("PutTaskIdentityUnsafe: %v", err)
	}
	if key, rec, found, err := d.FindTaskByUID(tasks, "uid-task-1"); err != nil || !found || key != "task-key-1" || rec.ID.String() != "task:1" {
		t.Errorf("FindTaskByUID: key=%q rec=%+v found=%v err=%v", key, rec, found, err)
	}
	if list, err := d.ListTasksByFolder(tasks); err != nil || len(list) != 1 {
		t.Errorf("ListTasksByFolder=%d err=%v want 1", len(list), err)
	}
	if err := d.DeleteTaskByUID(tasks, "uid-task-1"); err != nil {
		t.Fatalf("DeleteTaskByUID: %v", err)
	}

	// Contact.
	if err := d.PutContactIdentityUnsafe("contact-key-1", &semcore.StoredContactIdentity{
		ID: semcore.MustContactId("contact:1"), FolderID: contacts, MailboxID: mbox, IcalUID: "uid-contact-1", RawData: "BEGIN:VCARD",
	}); err != nil {
		t.Fatalf("PutContactIdentityUnsafe: %v", err)
	}
	if key, rec, found, err := d.FindContactByUID(contacts, "uid-contact-1"); err != nil || !found || key != "contact-key-1" || rec.ID.String() != "contact:1" {
		t.Errorf("FindContactByUID: key=%q rec=%+v found=%v err=%v", key, rec, found, err)
	}
	if list, err := d.ListContactsByFolder(contacts); err != nil || len(list) != 1 {
		t.Errorf("ListContactsByFolder=%d err=%v want 1", len(list), err)
	}
	if err := d.DeleteContactByUID(contacts, "uid-contact-1"); err != nil {
		t.Fatalf("DeleteContactByUID: %v", err)
	}
	if _, _, found, err := d.FindContactByUID(contacts, "uid-contact-1"); err != nil || found {
		t.Errorf("contact still found after delete: found=%v err=%v", found, err)
	}
}

// TestSemcoreJobs covers the job store via the NewJobStore handle: put/get with
// steps round-trip, list with kind/state filters, update-in-place, and delete
// with the ErrJobNotFound parity.
func TestSemcoreJobs(t *testing.T) {
	d := openTestDB(t)
	js, err := d.NewJobStore()
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}

	// Absent job.
	if _, err := js.Get("ghost"); !errors.Is(err, semcore.ErrJobNotFound) {
		t.Errorf("Get(absent) err=%v want ErrJobNotFound", err)
	}

	job := semcore.Job{
		ID: "job-1", Kind: semcore.JobKindBackfill, State: semcore.JobStatePending,
		Target: "mailbox", Priority: 2, Actor: "admin",
		Steps: []semcore.JobStep{{Name: "scan", Description: "scan mailbox"}},
	}
	if err := js.Put(job); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := js.Get("job-1")
	if err != nil || got.Kind != semcore.JobKindBackfill || got.Priority != 2 ||
		len(got.Steps) != 1 || got.Steps[0].Name != "scan" {
		t.Fatalf("Get mismatch: %+v err=%v", got, err)
	}

	// Update in place (state transition).
	job.State = semcore.JobStateRunning
	if err := js.Put(job); err != nil {
		t.Fatalf("Put update: %v", err)
	}
	if got, err := js.Get("job-1"); err != nil || got.State != semcore.JobStateRunning {
		t.Errorf("after update state=%v err=%v want running", got.State, err)
	}

	// A second job of a different kind/state.
	if err := js.Put(semcore.Job{ID: "job-2", Kind: semcore.JobKindMigration, State: semcore.JobStateCompleted}); err != nil {
		t.Fatalf("Put job-2: %v", err)
	}

	// List with no filter returns both.
	if all, err := js.List("", ""); err != nil || len(all) != 2 {
		t.Errorf("List(all)=%d err=%v want 2", len(all), err)
	}
	// Filter by kind.
	if list, err := js.List(semcore.JobKindBackfill, ""); err != nil || len(list) != 1 || list[0].ID != "job-1" {
		t.Errorf("List(backfill)=%+v err=%v want [job-1]", list, err)
	}
	// Filter by state.
	if list, err := js.List("", semcore.JobStateCompleted); err != nil || len(list) != 1 || list[0].ID != "job-2" {
		t.Errorf("List(completed)=%+v err=%v want [job-2]", list, err)
	}

	// Delete; deleting again returns ErrJobNotFound.
	if err := js.Delete("job-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := js.Delete("job-1"); !errors.Is(err, semcore.ErrJobNotFound) {
		t.Errorf("Delete(absent) err=%v want ErrJobNotFound", err)
	}
}

// TestSemcoreCollabChecked covers the ews.CollabStore extras: by-id lookups,
// change-key-checked Put (conflict on stale key) and Delete, and the
// subscription drain (ExpireAllSubscriptions).
func TestSemcoreCollabChecked(t *testing.T) {
	d := openTestDB(t)
	cal := semcore.MustFolderId("folder:calendar")
	mbox := semcore.MustMailboxId("alice@x.com")
	ck1 := semcore.MustCalendarChangeKey("ck-1")

	rec := &semcore.StoredCalendarItemIdentity{
		ID: semcore.MustCalendarItemId("cal:1"), FolderID: cal, MailboxID: mbox,
		ChangeKey: ck1, IcalUID: "uid-1", RawData: "v1",
	}
	// First insert via checked Put (no existing row → zero currentChangeKey OK).
	if err := d.PutCalendarItemIdentity("k1", rec, semcore.CalendarChangeKey{}); err != nil {
		t.Fatalf("PutCalendarItemIdentity insert: %v", err)
	}
	// By-id lookup.
	if got, err := d.GetCalendarItemByID(semcore.MustCalendarItemId("cal:1")); err != nil || got.RawData != "v1" {
		t.Fatalf("GetCalendarItemByID: %+v err=%v", got, err)
	}
	if _, err := d.GetCalendarItemByID(semcore.MustCalendarItemId("ghost")); !errors.Is(err, semcore.ErrCalendarItemNotFound) {
		t.Errorf("GetCalendarItemByID(absent) err=%v want ErrCalendarItemNotFound", err)
	}
	// Stale currentChangeKey is rejected with a version conflict.
	stale := semcore.MustCalendarChangeKey("wrong")
	if err := d.PutCalendarItemIdentity("k1", rec, stale); !errors.Is(err, semcore.ErrCollabVersionConflict) {
		t.Errorf("PutCalendarItemIdentity(stale) err=%v want ErrCollabVersionConflict", err)
	}
	// Matching currentChangeKey updates.
	rec.RawData = "v2"
	if err := d.PutCalendarItemIdentity("k1", rec, ck1); err != nil {
		t.Fatalf("PutCalendarItemIdentity update: %v", err)
	}
	// Delete with wrong key conflicts; with right key succeeds.
	if err := d.DeleteCalendarItemIdentity("k1", stale); !errors.Is(err, semcore.ErrCollabVersionConflict) {
		t.Errorf("DeleteCalendarItemIdentity(stale) err=%v want ErrCollabVersionConflict", err)
	}
	if err := d.DeleteCalendarItemIdentity("k1", ck1); err != nil {
		t.Fatalf("DeleteCalendarItemIdentity: %v", err)
	}
	if err := d.DeleteCalendarItemIdentity("k1", ck1); !errors.Is(err, semcore.ErrCalendarItemNotFound) {
		t.Errorf("DeleteCalendarItemIdentity(absent) err=%v want ErrCalendarItemNotFound", err)
	}

	// ExpireAllSubscriptions drains active subscriptions.
	if _, err := d.CreateSubscription(semcore.Subscription{MailboxID: mbox, Kind: semcore.SubscriptionKindPull}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	n, err := d.ExpireAllSubscriptions()
	if err != nil || n != 1 {
		t.Errorf("ExpireAllSubscriptions=%d err=%v want 1", n, err)
	}
	// A second drain finds nothing new.
	if n, err := d.ExpireAllSubscriptions(); err != nil || n != 0 {
		t.Errorf("ExpireAllSubscriptions(2nd)=%d err=%v want 0", n, err)
	}
}
