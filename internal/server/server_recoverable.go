package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/imap"
)

// Recoverable-items tuning. The enable flag, retention window, and tick are
// hot-reloadable config (recoverable_items.*); the folder name is a fixed
// internal constant.
const (
	recoverableFolder    = "Recoverable Items"
	recoverableTickFloor = 5 * time.Minute
)

// captureForRecovery files a permanently deleted message into the owner's
// Recoverable Items dumpster (a cross-protocol folder projection) and records it
// for retention and restore, BEFORE the caller unlinks the message. It returns
// true when the message was captured, in which case the caller MUST NOT unlink
// the shared content blob: the dumpster copy now references the same blob (the
// store is content-addressed), and the retention cleaner unlinks it at purge.
//
// It returns false — caller deletes as before — when the feature is disabled,
// the source IS the dumpster (no recursion), or capture fails. Capture is
// best-effort: a bookkeeping failure must never block a user's delete (fail
// open), but it is logged loudly (Rule 10) and any half-written projection is
// rolled back so no untracked dumpster copy lingers.
func (s *Server) captureForRecovery(owner, srcFolder string, raw []byte) bool {
	rc := s.cfg().RecoverableItems
	if !rc.Enabled || strings.EqualFold(srcFolder, recoverableFolder) {
		return false
	}
	uid, blobKey, err := s.fileFolderCopy(owner, recoverableFolder, raw, []string{"\\Seen"})
	if err != nil {
		s.logger.Error("recoverable: capture failed; proceeding with permanent delete",
			"owner", owner, "folder", srcFolder, "error", err)
		return false
	}
	subject, _, _, _ := scheduledHeaders(raw)
	rec := &db.RecoverableItem{
		ID:             uuid.New().String(),
		Owner:          owner,
		OriginalFolder: srcFolder,
		BlobKey:        blobKey,
		FolderUID:      uid,
		DeletedAt:      time.Now().UTC(),
		Size:           int64(len(raw)),
		Subject:        subject,
	}
	if err := s.database.CreateRecoverableItem(rec); err != nil {
		// Roll back the projection (both stores) so a failed record leaves no
		// untracked dumpster copy.
		_ = s.storageDB.DeleteMessage(owner, recoverableFolder, uid) //nolint:errcheck // best-effort rollback
		s.removeFolderCopySemcore(owner, recoverableFolder, blobKey)
		s.logger.Error("recoverable: record failed; proceeding with permanent delete", "owner", owner, "error", err)
		return false
	}
	imap.GetNotificationHub().NotifyNewMessage(owner, recoverableFolder, uid, uid)
	return true
}

// startRecoverableItemsCleaner launches the leader-gated background loop that
// purges Recoverable Items past their retention window. It mirrors
// startScheduledSender: own cancelable context (so a config change can restart
// it) derived from s.ctx, and a no-op when the feature is disabled.
func (s *Server) startRecoverableItemsCleaner() {
	rc := s.cfg().RecoverableItems
	if !rc.Enabled {
		s.logger.Info("recoverable-items disabled; retention cleaner not started")
		return
	}
	tick := time.Duration(rc.TickSeconds) * time.Second
	if tick <= 0 {
		tick = recoverableTickFloor
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.recoverableCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cleanExpiredRecoverableItems()
			}
		}
	}()
}

// restartRecoverableItemsCleaner stops the running cleaner (if any) and starts a
// fresh one from the live config, so toggling recoverable_items.enabled or
// changing the tick takes effect without a full restart.
func (s *Server) restartRecoverableItemsCleaner() {
	if s.recoverableCancel != nil {
		s.recoverableCancel()
		s.recoverableCancel = nil
	}
	s.startRecoverableItemsCleaner()
}

// cleanExpiredRecoverableItems permanently purges every Recoverable Item whose
// retention window has elapsed: it removes the dumpster projection from both
// stores, unlinks the content blob, and deletes the canonical record. It is
// leader-gated so a cluster purges each item exactly once; a single node is
// always its own leader.
func (s *Server) cleanExpiredRecoverableItems() {
	if !s.IsClusterLeader() {
		return
	}
	rc := s.cfg().RecoverableItems
	if !rc.Enabled {
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(rc.RetentionDays) * 24 * time.Hour)
	expired, err := s.database.ListExpiredRecoverableItems(cutoff)
	if err != nil {
		s.logger.Error("recoverable: list expired failed", "error", err)
		return
	}
	for _, it := range expired {
		if derr := s.storageDB.DeleteMessage(it.Owner, recoverableFolder, it.FolderUID); derr == nil {
			imap.GetNotificationHub().NotifyMailboxUpdate(it.Owner, recoverableFolder)
		}
		s.removeFolderCopySemcore(it.Owner, recoverableFolder, it.BlobKey)
		if derr := s.msgStore.DeleteMessage(it.Owner, it.BlobKey); derr != nil {
			s.logger.Warn("recoverable: purge blob failed", "owner", it.Owner, "id", it.ID, "error", derr)
		}
		if derr := s.database.DeleteRecoverableItem(it.ID); derr != nil {
			s.logger.Warn("recoverable: delete record failed", "id", it.ID, "error", derr)
		}
		s.logger.Info("recoverable: purged expired item", "id", it.ID, "owner", it.Owner)
	}
}

// dropRecoverableOnExpunge clears the canonical record when its Recoverable Items
// projection is expunged from any surface (IMAP/EWS), so manually emptying the
// dumpster does not leave a dangling record the cleaner would later chase. A
// no-op for other folders. The dumpster folder is excluded from capture, so the
// expunge itself is a normal permanent delete (blob + index + identity removed);
// only the record must be dropped here.
func (s *Server) dropRecoverableOnExpunge(owner, mailbox string, uid uint32) {
	if !strings.EqualFold(mailbox, recoverableFolder) {
		return
	}
	rec, err := s.database.FindRecoverableByFolderRef(owner, uid)
	if err != nil {
		s.logger.Warn("recoverable: lookup on expunge failed", "owner", owner, "uid", uid, "error", err)
		return
	}
	if rec == nil {
		return
	}
	if derr := s.database.DeleteRecoverableItem(rec.ID); derr != nil {
		s.logger.Warn("recoverable: delete record on expunge failed", "id", rec.ID, "error", derr)
	}
}

// recoverDeletedItem restores a soft-deleted message from the owner's Recoverable
// Items dumpster back to the folder it was deleted from (or INBOX when that
// folder no longer applies), identified by the message's blob key (the id the
// webmail shows for the dumpster item). It refiles the retained blob into the
// destination, removes the dumpster projection (index + identity, NOT the shared
// blob — the restored copy references it), and deletes the canonical record.
// Returns the destination folder.
func (s *Server) recoverDeletedItem(owner, blobKey string) (string, error) {
	items, err := s.database.ListRecoverableByOwner(owner)
	if err != nil {
		return "", err
	}
	var rec *db.RecoverableItem
	for _, it := range items {
		if it.BlobKey == blobKey {
			rec = it
			break
		}
	}
	if rec == nil {
		return "", fmt.Errorf("recoverable item not found")
	}
	dest := rec.OriginalFolder
	if dest == "" || strings.EqualFold(dest, recoverableFolder) {
		dest = "INBOX"
	}
	raw, err := s.msgStore.ReadMessage(owner, rec.BlobKey)
	if err != nil {
		return "", fmt.Errorf("read recoverable blob: %w", err)
	}
	uid, _, err := s.fileFolderCopy(owner, dest, raw, nil)
	if err != nil {
		return "", fmt.Errorf("refile to %s: %w", dest, err)
	}
	imap.GetNotificationHub().NotifyNewMessage(owner, dest, uid, uid)
	// Remove the dumpster projection (index + identity) but NOT the shared blob,
	// then drop the canonical record.
	if derr := s.storageDB.DeleteMessage(owner, recoverableFolder, rec.FolderUID); derr == nil {
		imap.GetNotificationHub().NotifyMailboxUpdate(owner, recoverableFolder)
	}
	s.removeFolderCopySemcore(owner, recoverableFolder, rec.BlobKey)
	if derr := s.database.DeleteRecoverableItem(rec.ID); derr != nil {
		s.logger.Warn("recoverable: delete record on restore failed", "id", rec.ID, "error", derr)
	}
	s.logger.Info("recoverable: restored item", "id", rec.ID, "owner", owner, "dest", dest)
	return dest, nil
}
