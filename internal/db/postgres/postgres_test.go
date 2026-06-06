package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/mapi"
	"github.com/umailserver/umailserver/internal/queue"
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
	t.Cleanup(d.Close)
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := d.pool.Exec(ctx,
		`TRUNCATE accounts, aliases, mail_groups, domains, tenants RESTART IDENTITY CASCADE`,
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
	t.Cleanup(db.Close)

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Idempotent: a second apply must succeed too.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (second apply): %v", err)
	}

	// Every net-surface table must exist after migration.
	want := []string{
		"tenants", "tenant_settings", "domains", "domain_settings",
		"accounts", "aliases", "mail_groups", "mail_group_members",
		"mail_queue", "mail_queue_recipients", "revoked_tokens", "client_sessions",
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

	// Claim flips status to sending atomically.
	claimed, err := d.ClaimPendingQueue(time.Now(), 10)
	if err != nil {
		t.Fatalf("ClaimPendingQueue: %v", err)
	}
	if len(claimed) != 1 || claimed[0].Status != "sending" {
		t.Errorf("ClaimPendingQueue mismatch: %+v", claimed)
	}
	// Already claimed: no longer pending.
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
