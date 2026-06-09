package server

import (
	"bytes"
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// Scheduled-send tuning. The enable flag, horizon, tick, and per-user cap are
// hot-reloadable config (scheduled_send.*); the retry cap, claim lease, and the
// projection folder name are fixed internals.
const (
	scheduledMaxRetries = 5
	scheduledLease      = 10 * time.Minute
	scheduledFolder     = "Scheduled"
	scheduledTickFloor  = 30 * time.Second
)

// scheduledClaimer is the optional capability the relational backend exposes to
// claim a due scheduled message atomically (the cluster two-leader guard). The
// single-writer bbolt store does not implement it and relies on the leader gate
// plus the sequential release loop instead.
type scheduledClaimer interface {
	ClaimScheduledMessage(id string, now time.Time) (bool, error)
}

// fileFolderCopy files raw into folder so the copy is visible on EVERY surface:
// the content-addressed blob store, the semcore identity store (read by EWS
// FindItem/GetItem), and the IMAP mailstore index (read by IMAP/POP3/JMAP/
// webmail). It mirrors the Notes cross-protocol create. role is the folder's
// distinguished role ("scheduled"/"sent") used to provision the semcore folder;
// read marks the copy \Seen. Returns the assigned IMAP UID and blob key.
func (s *Server) fileFolderCopy(owner, folder, role string, raw []byte, read bool) (uint32, string, error) {
	if s.storageDB == nil {
		return 0, "", fmt.Errorf("storage backend unavailable")
	}
	_ = s.storageDB.CreateMailbox(owner, folder) //nolint:errcheck // idempotent: absent is created, present is fine

	blobKey, err := s.msgStore.StoreMessage(owner, raw)
	if err != nil {
		return 0, "", err
	}
	now := time.Now()

	// Semcore identity write makes the copy visible to EWS. Best-effort: a
	// failure is logged but never fails the filing (storageDB is authoritative
	// for IMAP/webmail; semcore drives EWS visibility only).
	if s.semcoreStore != nil && s.mutationPipe != nil {
		id := s.semcoreStore.Identity()
		if mboxID, merr := id.EnsureMailboxId(owner); merr == nil {
			if folderID, ferr := id.EnsureFolderId(owner, folder, role); ferr == nil {
				if _, perr := s.mutationPipe.MutateItem(&semcore.MutationInput{
					MailboxID:    mboxID,
					FolderID:     folderID,
					RawMessage:   raw,
					InternalDate: now,
					Actor:        owner,
					Email:        owner,
					Source:       semcore.MutationSourceAPI,
					IsRead:       read,
				}); perr != nil {
					s.logger.Warn("scheduled: semcore projection failed", "owner", owner, "folder", folder, "error", perr)
				}
			}
		}
	}

	// IMAP mailstore index write makes the copy visible to IMAP/POP3/JMAP/webmail.
	uid, err := s.storageDB.GetNextUID(owner, folder)
	if err != nil {
		return 0, "", err
	}
	var flags []string
	if read {
		flags = []string{"\\Seen"}
	}
	subject, date, fromHdr, toHdr := scheduledHeaders(raw)
	meta := &storage.MessageMetadata{
		MessageID:    blobKey,
		UID:          uid,
		Flags:        flags,
		InternalDate: now,
		Size:         int64(len(raw)),
		Subject:      subject,
		Date:         date,
		From:         fromHdr,
		To:           toHdr,
	}
	if err := s.storageDB.StoreMessageMetadata(owner, folder, uid, meta); err != nil {
		return 0, "", err
	}
	return uid, blobKey, nil
}

// removeFolderCopySemcore removes the semcore identity for a folder copy (matched
// by blob key) so a released or canceled Scheduled projection does not linger as
// a ghost in EWS. Best-effort: the storageDB removal is the authoritative cancel.
func (s *Server) removeFolderCopySemcore(owner, folder, role, blobKey string) {
	if s.semcoreStore == nil || blobKey == "" {
		return
	}
	id := s.semcoreStore.Identity()
	folderID, err := id.EnsureFolderId(owner, folder, role)
	if err != nil {
		return
	}
	items, err := id.ListItemIdentitiesByFolder(folderID)
	if err != nil {
		return
	}
	for _, it := range items {
		if it.MsgKey == blobKey {
			if derr := id.DeleteItemIdentity(it.ItemID); derr != nil {
				s.logger.Warn("scheduled: semcore cleanup failed", "owner", owner, "folder", folder, "error", derr)
			}
		}
	}
}

// scheduledHeaders parses Subject/Date/From/To from a raw RFC 5322 message for the
// mailstore metadata, returning empties when the message cannot be parsed.
func scheduledHeaders(raw []byte) (subject, date, from, to string) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", "", "", ""
	}
	return msg.Header.Get("Subject"), msg.Header.Get("Date"), msg.Header.Get("From"), msg.Header.Get("To")
}

// scheduleSend records a message for future delivery, or sends it immediately
// when sendAt is already due. owner is the mailbox that owns the schedule (and
// the Scheduled/Sent projection); from is the envelope sender (they differ for
// send-on-behalf). fileSent controls whether a Sent copy is filed on release
// (EWS SendOnly sets false). Returns the scheduled-message id ("" for an
// immediate send).
func (s *Server) scheduleSend(owner, from string, to []string, data []byte, sendAt time.Time, source string, fileSent bool) (string, error) {
	sc := s.cfg().ScheduledSend
	now := time.Now().UTC()
	sendAt = sendAt.UTC()
	if sc.Enabled && sendAt.After(now.Add(time.Duration(sc.MaxHorizonDays)*24*time.Hour)) {
		return "", fmt.Errorf("scheduled send time is too far in the future")
	}
	if !sc.Enabled || !sendAt.After(now) {
		// Disabled, or already due: deliver immediately through the shared path
		// rather than holding a message the (stopped) loop would never release.
		if err := s.submitMessageWithSieve(from, to, data); err != nil {
			return "", err
		}
		if fileSent {
			if _, _, err := s.fileFolderCopy(owner, "Sent", "sent", data, true); err != nil {
				s.logger.Warn("scheduled: immediate-send Sent filing failed", "owner", owner, "error", err)
			}
		}
		return "", nil
	}

	// File the visible Scheduled projection (content blob + cross-protocol folder
	// metadata) so the pending message shows on webmail/IMAP/JMAP and EWS.
	uid, blobKey, err := s.fileFolderCopy(owner, scheduledFolder, "scheduled", data, false)
	if err != nil {
		return "", fmt.Errorf("file scheduled projection: %w", err)
	}
	m := &db.ScheduledMessage{
		ID:        uuid.New().String(),
		Owner:     owner,
		From:      from,
		To:        to,
		SendAt:    sendAt,
		Status:    "pending",
		Source:    source,
		FileSent:  fileSent,
		FolderUID: uid,
		BlobKey:   blobKey,
	}
	if err := s.database.CreateScheduledMessageWithLimit(m, sc.MaxPerUser); err != nil {
		// Roll back the projection (both stores) so a rejected schedule leaves no orphan.
		_ = s.storageDB.DeleteMessage(owner, scheduledFolder, uid) //nolint:errcheck // best-effort rollback
		s.removeFolderCopySemcore(owner, scheduledFolder, "scheduled", blobKey)
		return "", err
	}
	imap.GetNotificationHub().NotifyNewMessage(owner, scheduledFolder, uid, uid)
	return m.ID, nil
}

// startScheduledSender launches the leader-gated background loop that releases
// due scheduled messages. It mirrors startEWSPushDispatcher/startAlertChecker but
// runs on its own cancelable context so a config change can restart it. It is a
// no-op when scheduled-send is disabled.
func (s *Server) startScheduledSender() {
	sc := s.cfg().ScheduledSend
	if !sc.Enabled {
		s.logger.Info("scheduled-send disabled; release loop not started")
		return
	}
	tick := time.Duration(sc.TickSeconds) * time.Second
	if tick <= 0 {
		tick = scheduledTickFloor
	}
	// Derive from s.ctx so shutdown stops the loop; fall back to Background only
	// for bare test servers that have no lifecycle context.
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.scheduledCancel = cancel
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
				s.releaseDueScheduled()
			}
		}
	}()
}

// restartScheduledSender stops the running release loop (if any) and starts a
// fresh one from the live config, so toggling scheduled_send.enabled or changing
// scheduled_send.tick_seconds takes effect without a full restart.
func (s *Server) restartScheduledSender() {
	if s.scheduledCancel != nil {
		s.scheduledCancel()
		s.scheduledCancel = nil
	}
	s.startScheduledSender()
}

// releaseDueScheduled sends every scheduled message whose time has arrived. It is
// leader-gated so a cluster releases each message exactly once; a single node is
// always its own leader.
func (s *Server) releaseDueScheduled() {
	if !s.IsClusterLeader() {
		return
	}
	now := time.Now().UTC()

	// Recover messages orphaned in 'sending' by a node that crashed mid-release.
	if n, err := s.database.ResetStaleScheduledMessages(now.Add(-scheduledLease)); err != nil {
		s.logger.Warn("scheduled: reset stale failed", "error", err)
	} else if n > 0 {
		s.logger.Info("scheduled: reset stale claims", "count", n)
	}

	due, err := s.database.ListDueScheduledMessages(now)
	if err != nil {
		s.logger.Error("scheduled: list due failed", "error", err)
		return
	}
	for _, m := range due {
		if !s.claimScheduled(m, now) {
			continue
		}
		raw, err := s.msgStore.ReadMessage(m.Owner, m.BlobKey)
		if err != nil {
			s.failScheduled(m, fmt.Sprintf("read message: %v", err))
			continue
		}
		if err := s.submitMessageWithSieve(m.From, m.To, raw); err != nil {
			s.retryScheduled(m, now, err)
			continue
		}
		// On its way: move the projection to Sent (when requested) and clear the record.
		if m.FileSent {
			if uid, _, ferr := s.fileFolderCopy(m.Owner, "Sent", "sent", raw, true); ferr != nil {
				s.logger.Warn("scheduled: Sent filing failed", "owner", m.Owner, "error", ferr)
			} else {
				imap.GetNotificationHub().NotifyNewMessage(m.Owner, "Sent", uid, uid)
			}
		}
		// Remove the Scheduled projection from BOTH stores so the released message
		// stops showing as pending on every surface (no EWS ghost).
		if derr := s.storageDB.DeleteMessage(m.Owner, scheduledFolder, m.FolderUID); derr == nil {
			imap.GetNotificationHub().NotifyMailboxUpdate(m.Owner, scheduledFolder)
		}
		s.removeFolderCopySemcore(m.Owner, scheduledFolder, "scheduled", m.BlobKey)
		if derr := s.database.DeleteScheduledMessage(m.ID); derr != nil {
			s.logger.Warn("scheduled: delete record failed", "id", m.ID, "error", derr)
		}
		s.logger.Info("scheduled: message released", "id", m.ID, "owner", m.Owner)
	}
}

// claimScheduled marks a due message as 'sending'. On the relational backend it
// is the atomic cluster guard; on bbolt it is a leader-gated single-writer flip.
func (s *Server) claimScheduled(m *db.ScheduledMessage, now time.Time) bool {
	if claimer, ok := s.database.(scheduledClaimer); ok {
		won, err := claimer.ClaimScheduledMessage(m.ID, now)
		if err != nil {
			s.logger.Warn("scheduled: claim failed", "id", m.ID, "error", err)
			return false
		}
		return won
	}
	m.Status = "sending"
	m.ClaimedAt = now
	if err := s.database.UpdateScheduledMessage(m); err != nil {
		s.logger.Warn("scheduled: mark sending failed", "id", m.ID, "error", err)
		return false
	}
	return true
}

// retryScheduled backs a failed release off for another attempt, giving up after
// scheduledMaxRetries (status=failed, left visible — never dropped silently).
func (s *Server) retryScheduled(m *db.ScheduledMessage, now time.Time, cause error) {
	m.RetryCount++
	m.LastError = cause.Error()
	if m.RetryCount >= scheduledMaxRetries {
		m.Status = "failed"
		s.logger.Error("scheduled: giving up after retries", "id", m.ID, "owner", m.Owner, "error", cause)
	} else {
		m.Status = "pending"
		m.SendAt = now.Add(time.Duration(m.RetryCount) * 2 * time.Minute)
	}
	if err := s.database.UpdateScheduledMessage(m); err != nil {
		s.logger.Warn("scheduled: update after retry failed", "id", m.ID, "error", err)
	}
}

// failScheduled marks a message failed (e.g. its blob is missing) and leaves the
// record visible for follow-up rather than dropping it silently.
func (s *Server) failScheduled(m *db.ScheduledMessage, reason string) {
	m.Status = "failed"
	m.LastError = reason
	if err := s.database.UpdateScheduledMessage(m); err != nil {
		s.logger.Warn("scheduled: mark failed failed", "id", m.ID, "error", err)
	}
	s.logger.Error("scheduled: message failed", "id", m.ID, "owner", m.Owner, "reason", reason)
}

// cancelScheduledByID cancels one scheduled message for owner: it removes the
// Scheduled-folder projection and the canonical record so the send never fires.
// It is the dedicated webmail-cancel path; expunging the projection from the
// Scheduled folder (IMAP/EWS) cancels the same way. The owner check stops a user
// from canceling another mailbox's scheduled mail.
func (s *Server) cancelScheduledByID(owner, id string) error {
	m, err := s.database.GetScheduledMessage(id)
	if err != nil {
		return err
	}
	if m.Owner != owner {
		return fmt.Errorf("scheduled message not found")
	}
	if derr := s.storageDB.DeleteMessage(owner, scheduledFolder, m.FolderUID); derr == nil {
		imap.GetNotificationHub().NotifyMailboxUpdate(owner, scheduledFolder)
	}
	s.removeFolderCopySemcore(owner, scheduledFolder, "scheduled", m.BlobKey)
	return s.database.DeleteScheduledMessage(id)
}

// cancelScheduledOnExpunge cancels a scheduled send when its Scheduled-folder
// projection is expunged from any surface (IMAP/EWS), so deleting the visible
// message cancels the send. It is a no-op for other folders. The expunging
// surface removes its own store view (IMAP the mailstore index, EWS both); the
// canonical record is removed here. A lingering semcore item after a bare IMAP
// EXPUNGE follows the same delete semantics the project already has for any
// IMAP-expunged message (the dedicated webmail/EWS cancel paths clean both stores).
func (s *Server) cancelScheduledOnExpunge(owner, mailbox string, uid uint32) {
	if !strings.EqualFold(mailbox, scheduledFolder) {
		return
	}
	if ok, err := s.database.CancelScheduledByFolderRef(owner, uid); err != nil {
		s.logger.Warn("scheduled: cancel on expunge failed", "owner", owner, "uid", uid, "error", err)
	} else if ok {
		s.logger.Info("scheduled: canceled by folder expunge", "owner", owner, "uid", uid)
	}
}
