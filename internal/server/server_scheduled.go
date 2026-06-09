package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/imap"
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
			if _, _, err := storage.FileMessage(s.msgStore, s.storageDB, owner, "Sent", data, []string{"\\Seen"}); err != nil {
				s.logger.Warn("scheduled: immediate-send Sent filing failed", "owner", owner, "error", err)
			}
		}
		return "", nil
	}

	// File the visible Scheduled projection (content blob + folder metadata) so
	// the pending message shows on webmail/IMAP/EWS.
	uid, blobKey, err := storage.FileMessage(s.msgStore, s.storageDB, owner, scheduledFolder, data, nil)
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
		// Roll back the projection so a rejected schedule leaves no orphan entry.
		_ = s.storageDB.DeleteMessage(owner, scheduledFolder, uid) //nolint:errcheck // best-effort rollback
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
			if uid, _, ferr := storage.FileMessage(s.msgStore, s.storageDB, m.Owner, "Sent", raw, []string{"\\Seen"}); ferr != nil {
				s.logger.Warn("scheduled: Sent filing failed", "owner", m.Owner, "error", ferr)
			} else {
				imap.GetNotificationHub().NotifyNewMessage(m.Owner, "Sent", uid, uid)
			}
		}
		if derr := s.storageDB.DeleteMessage(m.Owner, scheduledFolder, m.FolderUID); derr == nil {
			imap.GetNotificationHub().NotifyMailboxUpdate(m.Owner, scheduledFolder)
		}
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
	return s.database.DeleteScheduledMessage(id)
}

// cancelScheduledOnExpunge cancels a scheduled send when its Scheduled-folder
// projection is expunged from any surface (IMAP/EWS), so deleting the visible
// message cancels the send. It is a no-op for other folders.
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
