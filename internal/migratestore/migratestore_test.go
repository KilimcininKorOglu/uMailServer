package migratestore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/db/postgres"
	"github.com/umailserver/umailserver/internal/vacation"
)

// openTestPostgres connects to UMAIL_TEST_POSTGRES_DSN, applies the schema, and
// truncates the account-layer tables so the migration runs against an empty
// target. It skips when no DSN is set so the default `make test` stays green.
func openTestPostgres(t *testing.T) *postgres.DB {
	t.Helper()
	dsn := os.Getenv("UMAIL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UMAIL_TEST_POSTGRES_DSN not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	pg, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := pg.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := pg.Pool().Exec(ctx,
		`TRUNCATE accounts, aliases, mail_groups, domains, tenants,
			user_ui_prefs, user_signatures, user_categories, user_vacation, ews_user_config
			RESTART IDENTITY CASCADE`,
	); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pg
}

// seedBolt creates a bbolt source store and fills it with one record of each
// account-layer type the migrator copies.
func seedBolt(t *testing.T) *db.DB {
	t.Helper()
	src, err := db.Open(filepath.Join(t.TempDir(), "umailserver.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := src.Close(); err != nil {
			t.Errorf("close source: %v", err)
		}
	})
	now := time.Now().Truncate(time.Second)

	if err := src.CreateTenant(&db.TenantData{ID: "t1", Name: "Tenant One", IsActive: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := src.CreateDomain(&db.DomainData{Name: "ex.test", TenantID: "t1", MaxAccounts: 50, IsActive: true, DKIMSelector: "default", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	if err := src.CreateAccount(&db.AccountData{Email: "a@ex.test", LocalPart: "a", Domain: "ex.test", PasswordHash: "hash", IsActive: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := src.CreateAlias(&db.AliasData{Alias: "al@ex.test", Target: "a@ex.test", Domain: "ex.test", IsActive: true, CreatedAt: now}); err != nil {
		t.Fatalf("seed alias: %v", err)
	}
	if err := src.CreateMailGroup(&db.MailGroup{Email: "grp@ex.test", LocalPart: "grp", Domain: "ex.test", IsActive: true, Members: []string{"a@ex.test"}}); err != nil {
		t.Fatalf("seed mail group: %v", err)
	}

	if err := src.PutUIPrefs("a@ex.test", map[string]bool{"darkMode": true}); err != nil {
		t.Fatalf("seed ui prefs: %v", err)
	}
	if err := src.PutSignature("a@ex.test", "Best regards"); err != nil {
		t.Fatalf("seed signature: %v", err)
	}
	if err := src.PutCategories("a@ex.test", []db.Category{{Name: "Work", Color: "#ff0000"}}); err != nil {
		t.Fatalf("seed categories: %v", err)
	}
	if err := src.PutVacation("a@ex.test", &vacation.Config{Enabled: true, Subject: "Away", Message: "OOO", SendInterval: 7 * 24 * time.Hour}); err != nil {
		t.Fatalf("seed vacation: %v", err)
	}
	if err := src.PutUserConfig("a@ex.test", "OWA.UserOptions", &db.UserConfigBlob{Dictionary: `{"theme":"dark"}`}); err != nil {
		t.Fatalf("seed user config: %v", err)
	}
	return src
}

func TestCopyDB(t *testing.T) {
	src := seedBolt(t)
	dst := openTestPostgres(t)

	var r Report
	if err := CopyDB(src, dst, &r); err != nil {
		t.Fatalf("CopyDB: %v", err)
	}

	want := Report{Tenants: 1, Domains: 1, Accounts: 1, Aliases: 1, MailGroups: 1, UIPrefs: 1, Signatures: 1, Categories: 1, Vacations: 1, UserConfigs: 1}
	if r != want {
		t.Fatalf("report = %+v, want %+v", r, want)
	}

	// Verify the records landed in Postgres, read back through the same Store
	// surface the server uses.
	if tn, err := dst.GetTenant("t1"); err != nil || tn.Name != "Tenant One" {
		t.Fatalf("GetTenant: %+v err=%v", tn, err)
	}
	dm, err := dst.GetDomain("ex.test")
	if err != nil || dm.TenantID != "t1" || dm.MaxAccounts != 50 {
		t.Fatalf("GetDomain: %+v err=%v", dm, err)
	}
	ac, err := dst.GetAccount("ex.test", "a")
	if err != nil || ac.Email != "a@ex.test" || ac.PasswordHash != "hash" {
		t.Fatalf("GetAccount: %+v err=%v", ac, err)
	}
	if aliases, err := dst.ListAliases(); err != nil || len(aliases) != 1 || aliases[0].Alias != "al@ex.test" {
		t.Fatalf("ListAliases: %+v err=%v", aliases, err)
	}
	if g, err := dst.GetMailGroup("ex.test", "grp"); err != nil || len(g.Members) != 1 || g.Members[0] != "a@ex.test" {
		t.Fatalf("GetMailGroup: %+v err=%v", g, err)
	}

	if prefs, err := dst.GetUIPrefs("a@ex.test"); err != nil || !prefs["darkMode"] {
		t.Fatalf("GetUIPrefs: %+v err=%v", prefs, err)
	}
	if sig, err := dst.GetSignature("a@ex.test"); err != nil || sig != "Best regards" {
		t.Fatalf("GetSignature: %q err=%v", sig, err)
	}
	if cats, err := dst.GetCategories("a@ex.test"); err != nil || len(cats) != 1 || cats[0].Name != "Work" {
		t.Fatalf("GetCategories: %+v err=%v", cats, err)
	}
	if vac, err := dst.GetVacation("a@ex.test"); err != nil || !vac.Enabled || vac.Subject != "Away" {
		t.Fatalf("GetVacation: %+v err=%v", vac, err)
	}
	if blob, err := dst.GetUserConfig("a@ex.test", "OWA.UserOptions"); err != nil || blob.Dictionary != `{"theme":"dark"}` {
		t.Fatalf("GetUserConfig: %+v err=%v", blob, err)
	}
}
