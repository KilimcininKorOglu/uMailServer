// Package migratestore copies a uMailServer deployment from the embedded bbolt
// store to the relational PostgreSQL backend. It is the one-time tool an
// operator runs to move an existing install onto Postgres before switching
// database.backend. Maildir message bodies stay on disk as files and are not
// touched; only the metadata/state that bbolt held moves into Postgres.
//
// The copy is performed in foreign-key-safe order and fails loud on the first
// write error — a half-migrated database is reported, never silently accepted.
// The destination must be an empty Postgres database (a fresh schema); the
// idempotent ON CONFLICT upserts on the destination tolerate a re-run for the
// upsert-shaped records, but accounts error on a pre-existing row so a dirty
// target surfaces immediately.
package migratestore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/db/postgres"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// Report accumulates the per-record-type counts of a migration run so the
// caller can print exactly what moved (and prove nothing was silently skipped).
type Report struct {
	Tenants     int
	Domains     int
	Accounts    int
	Aliases     int
	MailGroups  int
	UIPrefs     int
	Signatures  int
	Categories  int
	Vacations   int
	UserConfigs int

	// Storage layer (mailbox/message metadata; Maildir bodies stay on disk).
	Mailboxes     int
	Messages      int
	Subscriptions int
	ACLs          int
	Threads       int

	// Semantic-core layer (canonical identity mappings).
	MailboxIdentities int
	FolderIdentities  int
	ItemIdentities    int
	Conversations     int

	// Semantic-core durable policy and delegation.
	Rules       int
	OOFPolicies int
	Resources   int
	RoomLists   int
	Delegates   int
}

// CopyDB copies the account/metadata layer (the data kept in umailserver.db)
// from a bbolt source store to any destination Store — in practice the
// relational *postgres.DB. The order is FK-safe: tenants → domains → accounts →
// aliases → mail groups, followed by the per-account typed preferences (webmail
// toggles, signature, categories, vacation) and the EWS UserConfiguration
// blobs. Counts land in r. The first write error aborts and is returned with
// the records copied so far recorded in r.
func CopyDB(src *db.DB, dst db.Store, r *Report) error {
	if src == nil || dst == nil || r == nil {
		return errors.New("migratestore: nil source, destination, or report")
	}

	// Tenants first: every domain references one.
	tenants, err := src.ListTenants()
	if err != nil {
		return fmt.Errorf("list tenants: %w", err)
	}
	for _, t := range tenants {
		if err := dst.CreateTenant(t); err != nil {
			return fmt.Errorf("copy tenant %q: %w", t.ID, err)
		}
		r.Tenants++
	}

	// Domains: depend on tenants. Collect them so we can walk their accounts.
	domains, err := src.ListDomains()
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	for _, d := range domains {
		if err := dst.CreateDomain(d); err != nil {
			return fmt.Errorf("copy domain %q: %w", d.Name, err)
		}
		r.Domains++
	}

	// Accounts: depend on domains. Remember every email for the per-account
	// preference pass that follows.
	var emails []string
	for _, d := range domains {
		accounts, err := src.ListAccountsByDomain(d.Name)
		if err != nil {
			return fmt.Errorf("list accounts for domain %q: %w", d.Name, err)
		}
		for _, a := range accounts {
			if err := dst.CreateAccount(a); err != nil {
				return fmt.Errorf("copy account %q: %w", a.Email, err)
			}
			r.Accounts++
			emails = append(emails, a.Email)
		}
	}

	// Aliases.
	aliases, err := src.ListAliases()
	if err != nil {
		return fmt.Errorf("list aliases: %w", err)
	}
	for _, a := range aliases {
		if err := dst.CreateAlias(a); err != nil {
			return fmt.Errorf("copy alias %q: %w", a.Alias, err)
		}
		r.Aliases++
	}

	// Mail groups.
	groups, err := src.ListMailGroups()
	if err != nil {
		return fmt.Errorf("list mail groups: %w", err)
	}
	for _, g := range groups {
		if err := dst.CreateMailGroup(g); err != nil {
			return fmt.Errorf("copy mail group %q: %w", g.Email, err)
		}
		r.MailGroups++
	}

	// Per-account typed preferences. Each getter returns the empty/zero value
	// when unset, so only non-empty values are written to avoid creating rows
	// the source never had.
	for _, email := range emails {
		if err := copyAccountPrefs(src, dst, email, r); err != nil {
			return err
		}
	}

	// EWS UserConfiguration blobs are keyed independently of accounts under the
	// preferences bucket; enumerate them directly.
	if err := copyUserConfigs(src, dst, r); err != nil {
		return err
	}

	return nil
}

// copyAccountPrefs copies one account's webmail toggles, signature, categories,
// and vacation config when present.
func copyAccountPrefs(src *db.DB, dst db.Store, email string, r *Report) error {
	prefs, err := src.GetUIPrefs(email)
	if err != nil {
		return fmt.Errorf("read UI prefs for %q: %w", email, err)
	}
	if len(prefs) > 0 {
		if err := dst.PutUIPrefs(email, prefs); err != nil {
			return fmt.Errorf("copy UI prefs for %q: %w", email, err)
		}
		r.UIPrefs++
	}

	sig, err := src.GetSignature(email)
	if err != nil {
		return fmt.Errorf("read signature for %q: %w", email, err)
	}
	if sig != "" {
		if err := dst.PutSignature(email, sig); err != nil {
			return fmt.Errorf("copy signature for %q: %w", email, err)
		}
		r.Signatures++
	}

	cats, err := src.GetCategories(email)
	if err != nil {
		return fmt.Errorf("read categories for %q: %w", email, err)
	}
	if len(cats) > 0 {
		if err := dst.PutCategories(email, cats); err != nil {
			return fmt.Errorf("copy categories for %q: %w", email, err)
		}
		r.Categories++
	}

	// GetVacation errors when none is stored; that is the "absent" signal, not a
	// failure, so it is skipped rather than aborting the run.
	if vac, err := src.GetVacation(email); err == nil && vac != nil {
		if err := dst.PutVacation(email, vac); err != nil {
			return fmt.Errorf("copy vacation for %q: %w", email, err)
		}
		r.Vacations++
	}

	return nil
}

// copyUserConfigs enumerates the EWS UserConfiguration entries (keyed
// "ewsuserconfig:<owner>:<name>" in the preferences bucket) and copies each
// through the typed getter/putter so the decode logic is shared with the store.
func copyUserConfigs(src *db.DB, dst db.Store, r *Report) error {
	const prefix = "ewsuserconfig:"
	err := src.ForEachPrefix(db.BucketPreferences, prefix, func(key string, _ []byte) error {
		// key = "ewsuserconfig:" + owner + ":" + name. Owner is an email (no
		// colon), so the first colon after the prefix separates owner from name.
		rest := strings.TrimPrefix(key, prefix)
		owner, name, ok := strings.Cut(rest, ":")
		if !ok {
			return fmt.Errorf("malformed user-config key %q", key)
		}
		blob, err := src.GetUserConfig(owner, name)
		if err != nil {
			return fmt.Errorf("read user config %q/%q: %w", owner, name, err)
		}
		if err := dst.PutUserConfig(owner, name, blob); err != nil {
			return fmt.Errorf("copy user config %q/%q: %w", owner, name, err)
		}
		r.UserConfigs++
		return nil
	})
	if err != nil {
		// A fresh bbolt store may never have created the preferences bucket; that
		// just means there is nothing to copy.
		if strings.Contains(err.Error(), "bucket not found") {
			return nil
		}
		return fmt.Errorf("enumerate user configs: %w", err)
	}
	return nil
}

// CopyStorage copies the message-metadata layer (mail.db) from a bbolt storage
// database to the relational *postgres.DB for each of the given users (the
// account emails produced by the db-layer copy). Maildir message bodies stay on
// disk and are not touched — only the metadata, mailbox counters, subscriptions,
// ACLs, and thread index move.
//
// Per mailbox the order is RestoreMailbox (which fixes UIDVALIDITY, uid_next,
// and highest-modseq from the source so IMAP clients keep their caches) BEFORE
// the messages, so the message inserts find an existing row and never mint a
// fresh UIDVALIDITY. Each message keeps its exact UID and mod-seq. The JMAP
// change journal is not copied; it starts fresh on the destination (clients
// resync from current state, standard on a server migration).
func CopyStorage(src *storage.Database, dst *postgres.DB, users []string, r *Report) error {
	if src == nil || dst == nil || r == nil {
		return errors.New("migratestore: nil source, destination, or report")
	}
	for _, user := range users {
		mailboxes, err := src.ListMailboxes(user)
		if err != nil {
			return fmt.Errorf("list mailboxes for %q: %w", user, err)
		}
		for _, name := range mailboxes {
			if err := copyMailbox(src, dst, user, name, r); err != nil {
				return err
			}
		}

		// Subscriptions are independent of mailbox existence (RFC 3501).
		subs, err := src.ListSubscribed(user)
		if err != nil {
			return fmt.Errorf("list subscriptions for %q: %w", user, err)
		}
		for _, name := range subs {
			if err := dst.SetSubscribed(user, name, true); err != nil {
				return fmt.Errorf("copy subscription %q/%q: %w", user, name, err)
			}
			r.Subscriptions++
		}

		// ACL grants are keyed (owner, mailbox); enumerate the owner's mailboxes.
		for _, name := range mailboxes {
			entries, err := src.ListACL(user, name)
			if err != nil {
				return fmt.Errorf("list ACL for %q/%q: %w", user, name, err)
			}
			for _, e := range entries {
				if err := dst.SetACL(user, name, e.Grantee, e.Rights, e.GrantedBy); err != nil {
					return fmt.Errorf("copy ACL %q/%q grantee %q: %w", user, name, e.Grantee, err)
				}
				r.ACLs++
			}
		}

		if err := copyThreads(src, dst, user, r); err != nil {
			return err
		}
	}
	return nil
}

// copyMailbox restores one mailbox with its source counters, then copies every
// message's metadata at its original UID.
func copyMailbox(src *storage.Database, dst *postgres.DB, user, name string, r *Report) error {
	mb, err := src.GetMailbox(user, name)
	if err != nil {
		return fmt.Errorf("get mailbox %q/%q: %w", user, name, err)
	}
	if err := dst.RestoreMailbox(user, name, mb.UIDValidity, mb.UIDNext, mb.HighestModSeq); err != nil {
		return fmt.Errorf("restore mailbox %q/%q: %w", user, name, err)
	}
	r.Mailboxes++

	uids, err := src.GetMessageUIDs(user, name)
	if err != nil {
		return fmt.Errorf("list message UIDs for %q/%q: %w", user, name, err)
	}
	for _, uid := range uids {
		meta, err := src.GetMessageMetadata(user, name, uid)
		if err != nil {
			return fmt.Errorf("get message %q/%q/%d: %w", user, name, uid, err)
		}
		if err := dst.StoreMessageMetadata(user, name, uid, meta); err != nil {
			return fmt.Errorf("copy message %q/%q/%d: %w", user, name, uid, err)
		}
		r.Messages++
	}
	return nil
}

// copyThreads copies the per-user thread index, paginating the source so a large
// mailbox does not load every thread at once.
func copyThreads(src *storage.Database, dst *postgres.DB, user string, r *Report) error {
	const page = 500
	for offset := 0; ; offset += page {
		threads, err := src.GetThreads(user, page, offset)
		if err != nil {
			return fmt.Errorf("list threads for %q: %w", user, err)
		}
		for _, th := range threads {
			if err := dst.UpdateThread(user, th); err != nil {
				return fmt.Errorf("copy thread %q/%q: %w", user, th.ThreadID, err)
			}
			r.Threads++
		}
		if len(threads) < page {
			return nil
		}
	}
}

// CopySemcoreIdentity copies the canonical semantic-core identity layer (mailbox,
// folder, item, and conversation identities) from a bbolt *semcore.Store to the
// relational *postgres.DB. Identity is the foundation every other semcore record
// references, so the source canonical ids are PRESERVED exactly via the restore
// writes — minting fresh ids (as EnsureMailboxId/EnsureFolderId would) would
// break every item, policy, and delegate that points at a mailbox or folder.
//
// Order: mailbox identities → folder identities (whose mbox_key is a mailbox id)
// → item identities (which reference both) → conversation identities (derived
// from the items). The JMAP/EWS sync cursors (lifecycle journal, sync state,
// tombstones, subscriptions) are deliberately NOT copied: they are session
// continuity state that resets on a server migration, and clients resync.
func CopySemcoreIdentity(src *semcore.Store, dst *postgres.DB, r *Report) error {
	if src == nil || dst == nil || r == nil {
		return errors.New("migratestore: nil source, destination, or report")
	}
	id := src.Identity()

	mailboxes, err := id.ListMailboxIdentities()
	if err != nil {
		return fmt.Errorf("list mailbox identities: %w", err)
	}
	for _, m := range mailboxes {
		// The bbolt store prefixes the stored email with "e:" (see
		// MailboxEmailsByID); the canonical key the Postgres store uses is the
		// bare email, so strip the prefix to keep the two backends in agreement.
		email := strings.TrimPrefix(m.Email, "e:")
		if err := dst.RestoreMailboxIdentity(email, m.MailboxID, m.UIDValidity, m.HighestModSeq); err != nil {
			return fmt.Errorf("copy mailbox identity %q: %w", email, err)
		}
		r.MailboxIdentities++
	}

	folders, err := id.ListFolderIdentities()
	if err != nil {
		return fmt.Errorf("list folder identities: %w", err)
	}
	// seenConv dedupes conversation identities across folders so the report
	// counts distinct conversations, not item→conversation links.
	seenConv := map[string]bool{}
	for _, f := range folders {
		name, err := id.FolderNameByID(f.MailboxID.String(), f.FolderID)
		if err != nil {
			return fmt.Errorf("resolve folder name for %s: %w", f.FolderID.String(), err)
		}
		if err := dst.RestoreFolderIdentity(name, f); err != nil {
			return fmt.Errorf("copy folder identity %s/%s: %w", f.MailboxID.String(), name, err)
		}
		r.FolderIdentities++

		if err := copyFolderItems(id, dst, f, seenConv, r); err != nil {
			return err
		}
	}
	return nil
}

// copyFolderItems copies every item identity in one folder, preserving each
// ItemId/ChangeKey/ConversationId and the read/category state, and registers the
// conversation identity each item belongs to (deduped per destination via the
// ON CONFLICT DO NOTHING on PutConversationIdentity).
func copyFolderItems(id *semcore.BoltIdentityStore, dst *postgres.DB, f semcore.StoredFolderIdentity, seenConv map[string]bool, r *Report) error {
	items, err := id.ListItemIdentitiesByFolder(f.FolderID)
	if err != nil {
		return fmt.Errorf("list items in folder %s: %w", f.FolderID.String(), err)
	}
	for _, it := range items {
		// Register the conversation first so the item's reference is backed by a
		// row. Zero conversation ids are skipped (no conversation); duplicates
		// across folders are written once and counted once.
		if !it.ConversationID.IsZero() && !seenConv[it.ConversationID.String()] {
			if err := dst.PutConversationIdentity(it.ConversationID, it.MailboxID); err != nil {
				return fmt.Errorf("copy conversation %s: %w", it.ConversationID.String(), err)
			}
			seenConv[it.ConversationID.String()] = true
			r.Conversations++
		}
		if err := dst.PutItemIdentity(it.MsgKey, it.Email, it.ItemID, it.MailboxID, it.FolderID, it.ChangeKey, it.ConversationID, it.IsRead); err != nil {
			return fmt.Errorf("copy item identity %s: %w", it.ItemID.String(), err)
		}
		if len(it.Categories) > 0 {
			if err := dst.UpdateItemState(it.ItemID, nil, it.Categories); err != nil {
				return fmt.Errorf("copy item categories %s: %w", it.ItemID.String(), err)
			}
		}
		r.ItemIdentities++
	}
	return nil
}

// CopySemcorePolicy copies the durable semantic-core policy records — inbox
// rules, out-of-office policies, resource mailboxes, and room lists — from the
// bbolt semcore.Store to the relational *postgres.DB. Each Put preserves the
// record's canonical id (ON CONFLICT (id)). Only currently-active OOF policies
// are enumerable through the store, so inactive ones are not copied; that is the
// store's read surface, not a silent drop. Notification policies (push
// registrations) are session/registration state and are not migrated.
func CopySemcorePolicy(src *semcore.Store, dst *postgres.DB, r *Report) error {
	if src == nil || dst == nil || r == nil {
		return errors.New("migratestore: nil source, destination, or report")
	}
	pol := src.Policy()

	rules, err := pol.ListAllRules()
	if err != nil {
		return fmt.Errorf("list rules: %w", err)
	}
	for _, rule := range rules {
		if err := dst.PutRule(rule); err != nil {
			return fmt.Errorf("copy rule %s: %w", rule.ID.String(), err)
		}
		r.Rules++
	}

	oofs, err := pol.ListActiveOOF()
	if err != nil {
		return fmt.Errorf("list OOF policies: %w", err)
	}
	for _, p := range oofs {
		if err := dst.PutOOF(p); err != nil {
			return fmt.Errorf("copy OOF %s: %w", p.ID.String(), err)
		}
		r.OOFPolicies++
	}

	resources, err := pol.ListResources()
	if err != nil {
		return fmt.Errorf("list resources: %w", err)
	}
	for _, p := range resources {
		if err := dst.PutResource(p); err != nil {
			return fmt.Errorf("copy resource %s: %w", p.ID.String(), err)
		}
		r.Resources++
	}

	roomLists, err := pol.ListRoomLists()
	if err != nil {
		return fmt.Errorf("list room lists: %w", err)
	}
	for _, rl := range roomLists {
		if err := dst.PutRoomList(rl); err != nil {
			return fmt.Errorf("copy room list %s: %w", rl.ID, err)
		}
		r.RoomLists++
	}
	return nil
}

// CopySemcoreDelegation copies every delegate grant from the bbolt semcore.Store
// to the relational *postgres.DB. PutDelegate mints a fresh grant id (delegate
// ids are leaf references nothing else points at) but preserves the owner
// MailboxId and all permission fields.
func CopySemcoreDelegation(src *semcore.Store, dst *postgres.DB, r *Report) error {
	if src == nil || dst == nil || r == nil {
		return errors.New("migratestore: nil source, destination, or report")
	}
	delegates, err := src.Delegation().ListAllDelegates()
	if err != nil {
		return fmt.Errorf("list delegates: %w", err)
	}
	for _, del := range delegates {
		if _, err := dst.PutDelegate(del); err != nil {
			return fmt.Errorf("copy delegate %s→%s: %w", del.OwnerID.String(), del.DelegateEmail, err)
		}
		r.Delegates++
	}
	return nil
}
