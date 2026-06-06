package migratestore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/db/postgres"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
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
			user_ui_prefs, user_signatures, user_categories, user_vacation, ews_user_config,
			mailboxes, mailbox_subscriptions, messages, threads, mailbox_acl, changes,
			semcore_mailbox_identity, semcore_folder_identity, semcore_item_identity,
			semcore_conversation_identity
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

func TestCopyStorage(t *testing.T) {
	dst := openTestPostgres(t)

	const user = "a@ex.test"
	src, err := storage.OpenDatabase(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatalf("storage.OpenDatabase: %v", err)
	}
	t.Cleanup(func() {
		if err := src.Close(); err != nil {
			t.Errorf("close storage source: %v", err)
		}
	})

	// Seed one mailbox with a message at a specific UID, a subscription, an ACL
	// grant, and a thread.
	if err := src.CreateMailbox(user, "INBOX"); err != nil {
		t.Fatalf("seed mailbox: %v", err)
	}
	srcMB, err := src.GetMailbox(user, "INBOX")
	if err != nil {
		t.Fatalf("read source mailbox: %v", err)
	}
	now := time.Now().Truncate(time.Second)
	if err := src.StoreMessageMetadata(user, "INBOX", 5, &storage.MessageMetadata{
		MessageID: "<m1@ex.test>", UID: 5, Flags: []string{"\\Seen"}, ModSeq: 3,
		InternalDate: now, Size: 1234, Subject: "Hello", ThreadID: "t-1", IsThreadRoot: true,
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := src.SetSubscribed(user, "INBOX", true); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if err := src.SetACL(user, "INBOX", "b@ex.test", storage.ACLLookup|storage.ACLRead, user); err != nil {
		t.Fatalf("seed ACL: %v", err)
	}
	if err := src.UpdateThread(user, &storage.Thread{
		ThreadID: "t-1", Subject: "Hello", Participants: []string{user},
		MessageCount: 1, UnreadCount: 0, LastActivity: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	var r Report
	if err := CopyStorage(src, dst, []string{user}, &r); err != nil {
		t.Fatalf("CopyStorage: %v", err)
	}

	want := Report{Mailboxes: 1, Messages: 1, Subscriptions: 1, ACLs: 1, Threads: 1}
	if r != want {
		t.Fatalf("report = %+v, want %+v", r, want)
	}

	// UIDVALIDITY, uid_next, and highest-modseq must match the source exactly so
	// IMAP clients keep their caches — the whole reason for the faithful restore.
	dstMB, err := dst.GetMailbox(user, "INBOX")
	if err != nil {
		t.Fatalf("dst GetMailbox: %v", err)
	}
	if dstMB.UIDValidity != srcMB.UIDValidity {
		t.Fatalf("UIDVALIDITY = %d, want %d (source)", dstMB.UIDValidity, srcMB.UIDValidity)
	}
	if dstMB.UIDNext != srcMB.UIDNext {
		t.Fatalf("uid_next = %d, want %d (source)", dstMB.UIDNext, srcMB.UIDNext)
	}

	meta, err := dst.GetMessageMetadata(user, "INBOX", 5)
	if err != nil || meta.MessageID != "<m1@ex.test>" || meta.ModSeq != 3 || meta.Subject != "Hello" {
		t.Fatalf("dst message at UID 5: %+v err=%v", meta, err)
	}
	if len(meta.Flags) != 1 || meta.Flags[0] != "\\Seen" {
		t.Fatalf("dst message flags = %v, want [\\Seen]", meta.Flags)
	}

	subs, err := dst.ListSubscribed(user)
	if err != nil || len(subs) != 1 || subs[0] != "INBOX" {
		t.Fatalf("dst ListSubscribed = %v err=%v", subs, err)
	}

	acl, err := dst.ListACL(user, "INBOX")
	if err != nil || len(acl) != 1 || acl[0].Grantee != "b@ex.test" || acl[0].Rights != (storage.ACLLookup|storage.ACLRead) {
		t.Fatalf("dst ListACL = %+v err=%v", acl, err)
	}

	th, err := dst.GetThread(user, "t-1")
	if err != nil || th.ThreadID != "t-1" || th.Subject != "Hello" {
		t.Fatalf("dst GetThread = %+v err=%v", th, err)
	}
}

func TestCopySemcoreIdentity(t *testing.T) {
	dst := openTestPostgres(t)

	const email = "a@ex.test"
	src, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("semcore.NewStore: %v", err)
	}
	t.Cleanup(func() {
		if err := src.Close(); err != nil {
			t.Errorf("close semcore source: %v", err)
		}
	})
	id := src.Identity()

	// Seed: a mailbox identity (random canonical id), a folder under it (keyed by
	// email, as the pipeline does), and one item with a conversation.
	mid, err := id.EnsureMailboxId(email)
	if err != nil {
		t.Fatalf("seed mailbox identity: %v", err)
	}
	fid, err := id.EnsureFolderId(email, "INBOX", "inbox")
	if err != nil {
		t.Fatalf("seed folder identity: %v", err)
	}
	itemID, ck := semcore.MustItemId("item-0001"), mustChangeKey(t, "1")
	convID := semcore.MustConversationId("conv-0001")
	if err := id.PutItemIdentity("msgkey-1", email, itemID, semcore.MustMailboxId(email), fid, ck, convID, true); err != nil {
		t.Fatalf("seed item identity: %v", err)
	}

	var r Report
	if err := CopySemcoreIdentity(src, dst, &r); err != nil {
		t.Fatalf("CopySemcoreIdentity: %v", err)
	}

	want := Report{MailboxIdentities: 1, FolderIdentities: 1, ItemIdentities: 1, Conversations: 1}
	if r != want {
		t.Fatalf("report = %+v, want %+v", r, want)
	}

	// Canonical ids must be PRESERVED, not regenerated — that is the contract.
	if got, err := dst.GetMailboxIDByEmail(email); err != nil || got.String() != mid.String() {
		t.Fatalf("dst mailbox id = %v (err=%v), want %v", got, err, mid)
	}
	if got, err := dst.GetFolderID(email, "INBOX"); err != nil || got.String() != fid.String() {
		t.Fatalf("dst folder id = %v (err=%v), want %v", got, err, fid)
	}
	it, err := dst.GetItemIdentity(itemID)
	if err != nil || it.ItemID.String() != itemID.String() || it.FolderID.String() != fid.String() {
		t.Fatalf("dst item identity = %+v err=%v", it, err)
	}
	if it.ConversationID.String() != convID.String() || !it.IsRead || it.MsgKey != "msgkey-1" {
		t.Fatalf("dst item state = %+v, want conv=%s read=true msgkey=msgkey-1", it, convID)
	}
}

func mustChangeKey(t *testing.T, raw string) semcore.ChangeKey {
	t.Helper()
	ck, err := semcore.NewChangeKey(raw)
	if err != nil {
		t.Fatalf("NewChangeKey: %v", err)
	}
	return ck
}
