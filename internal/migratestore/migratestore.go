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
