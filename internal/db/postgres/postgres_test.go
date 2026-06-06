package postgres

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/mapi"
	"github.com/umailserver/umailserver/internal/queue"
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
	if _, err := d.pool.Exec(ctx,
		`TRUNCATE accounts, aliases, mail_groups, mail_queue, domains, tenants,
			user_ui_prefs, user_signatures, user_vacation, ews_user_config,
			mailboxes, mailbox_subscriptions, messages, threads, mailbox_acl, changes,
			spam_tokens, spam_stats, ratelimit_quota, backup_jobs, backup_manifests
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
