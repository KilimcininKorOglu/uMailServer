package server

import (
	"log/slog"

	"github.com/umailserver/umailserver/internal/db"
)

// legacyJunkFolderName is the storage/identity name older EWS code used for the
// junk distinguished folder before it converged on the canonical "Junk"/"junk".
const legacyJunkFolderName = "junkemail"

// convergeLegacyJunkFolders re-homes any legacy spam-role junk folder onto the
// canonical "junk" role at startup. Older EWS code mapped the junkemail
// distinguished folder to role "spam" (the rest of the system, and
// canonicalFolderNameForRole, use "junk"), so items filed there via EWS
// mark-as-junk were stranded outside the Junk folder every other surface reads.
// It is idempotent and a no-op for any mailbox that never had a spam-role
// folder, so a fresh deployment pays nothing. Must run after the semcore store
// and the default mailboxes are ready.
func convergeLegacyJunkFolders(database db.Store, identity semIdentity, storageDB storageBackend, logger *slog.Logger) {
	if database == nil || identity == nil {
		return
	}
	domains, err := database.ListDomains()
	if err != nil {
		logger.Warn("junk convergence: list domains", "error", err)
		return
	}
	converged := 0
	for _, domain := range domains {
		accounts, err := database.ListAccountsByDomain(domain.Name)
		if err != nil {
			logger.Warn("junk convergence: list accounts", "domain", domain.Name, "error", err)
			continue
		}
		for _, account := range accounts {
			if convergeMailboxJunk(account.Email, identity, storageDB, logger) {
				converged++
			}
		}
	}
	if converged > 0 {
		logger.Info("Converged legacy spam-role junk folders", "mailboxes", converged)
	}
}

// convergeMailboxJunk converges one mailbox. It returns true when a spam-role
// folder was found and re-homed.
func convergeMailboxJunk(email string, identity semIdentity, storageDB storageBackend, logger *slog.Logger) bool {
	folders, err := identity.ListFolderIdentitiesForMailbox(email)
	if err != nil {
		return false
	}
	spamIdx := -1
	for i := range folders {
		if folders[i].Role == "spam" {
			spamIdx = i
			break
		}
	}
	if spamIdx == -1 {
		return false // no legacy folder — the common case, and the fresh-install case
	}
	spamFolderID := folders[spamIdx].FolderID

	// Resolve (or create) the canonical junk folder, then move every item there.
	junkID, err := identity.EnsureFolderId(email, "Junk", "junk")
	if err != nil {
		logger.Warn("junk convergence: ensure junk folder", "email", email, "error", err)
		return false
	}
	items, err := identity.ListItemIdentitiesByFolder(spamFolderID)
	if err != nil {
		logger.Warn("junk convergence: list spam items", "email", email, "error", err)
		return false
	}
	for _, it := range items {
		if err := identity.SetItemFolder(it.ItemID, junkID); err != nil {
			logger.Warn("junk convergence: move item to junk", "email", email, "error", err)
		}
	}
	// Drop the now-empty spam folder identity so EWS no longer surfaces it.
	if err := identity.DeleteFolder(spamFolderID); err != nil {
		logger.Warn("junk convergence: delete spam folder", "email", email, "error", err)
	}

	// Converge the mirrored storageDB mailbox too — IMAP/POP3/webmail read that
	// index, not the semcore identity store.
	convergeStorageJunkMailbox(email, storageDB, logger)
	return true
}

// convergeStorageJunkMailbox moves any messages the EWS mirror filed into the
// legacy "junkemail" storage mailbox into the canonical "Junk" mailbox, then
// removes the emptied legacy mailbox. Idempotent: a no-op when "junkemail" is
// absent or already empty.
func convergeStorageJunkMailbox(email string, storageDB storageBackend, logger *slog.Logger) {
	if storageDB == nil {
		return
	}
	mailboxes, err := storageDB.ListMailboxes(email)
	if err != nil {
		return
	}
	present := false
	for _, mb := range mailboxes {
		if mb == legacyJunkFolderName {
			present = true
			break
		}
	}
	if !present {
		return
	}
	uids, err := storageDB.GetMessageUIDs(email, legacyJunkFolderName)
	if err != nil {
		return
	}
	for _, uid := range uids {
		meta, err := storageDB.GetMessageMetadata(email, legacyJunkFolderName, uid)
		if err != nil {
			continue
		}
		newUID, err := storageDB.GetNextUID(email, "Junk")
		if err != nil {
			continue
		}
		meta.UID = newUID
		if err := storageDB.StoreMessageMetadata(email, "Junk", newUID, meta); err != nil {
			logger.Warn("junk convergence: store message in Junk", "email", email, "error", err)
			continue
		}
		if err := storageDB.DeleteMessage(email, legacyJunkFolderName, uid); err != nil {
			logger.Warn("junk convergence: delete legacy junkemail message", "email", email, "error", err)
		}
	}
	if err := storageDB.DeleteMailbox(email, legacyJunkFolderName); err != nil {
		logger.Warn("junk convergence: delete legacy junkemail mailbox", "email", email, "error", err)
	}
}
