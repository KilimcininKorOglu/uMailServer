package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/mapi"
)

// The relational backend must satisfy the existing consumer interface so it can
// replace *db.DB behind it without touching the MAPI/HTTP server.
var _ mapi.Store = (*DB)(nil)

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
