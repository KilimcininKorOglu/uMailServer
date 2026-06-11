package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/sieve"
)

// sanitizeHeaderValue removes CR/LF characters to prevent SMTP header injection.
func sanitizeHeaderValue(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// hasActiveOOFPolicy reports whether the recipient has an out-of-office policy
// that is currently active. When true, the Sieve vacation action compiled from
// that policy (handleSieveVacation) already sends the auto-reply at delivery, so
// the legacy account.VacationSettings path must be skipped to avoid sending two
// auto-replies for the same message.
func (s *Server) hasActiveOOFPolicy(email string) bool {
	if s.semcoreStore == nil {
		return false
	}
	oofID, err := semcore.NewOOFId(email)
	if err != nil {
		return false
	}
	policy, err := s.semcoreStore.Policy().GetOOF(oofID)
	if err != nil || policy == nil {
		return false
	}
	return policy.IsActiveNow()
}

// handleSieveVacation handles Sieve vacation action by sending a vacation auto-reply
func (s *Server) handleSieveVacation(sender, recipient string, vacation sieve.VacationAction) {
	if s.queue == nil {
		return
	}

	// Enforce the OOF schedule window at delivery time. The compiled Sieve
	// script fires whenever OOF is enabled; the actual start/end window is
	// evaluated here (server-side) so a Scheduled policy only
	// auto-replies while it is genuinely active. Gate ONLY on an *enabled*
	// policy that is currently out of window: a disabled OOF policy never
	// contributes a vacation action to the compiled script, so a vacation
	// firing here came from the user's own ManageSieve script and must not be
	// suppressed by a leftover disabled OOF policy.
	var oofPolicy *semcore.OOFPolicy
	if s.semcoreStore != nil {
		if oofID, err := semcore.NewOOFId(recipient); err == nil {
			if policy, err := s.semcoreStore.Policy().GetOOF(oofID); err == nil && policy != nil && policy.Enabled {
				if !policy.IsActiveNow() {
					return
				}
				oofPolicy = policy
			}
		}
	}

	// Don't send vacation to mailing lists or bounces
	senderLower := strings.ToLower(sender)
	for _, prefix := range []string{"mailer-daemon@", "postmaster@", "noreply@", "no-reply@", "bounce@"} {
		if strings.HasPrefix(senderLower, prefix) {
			return
		}
	}

	// Build vacation message content
	subject := vacation.Subject
	if subject == "" {
		subject = "Automated reply"
	}
	body := vacation.Body
	if body == "" {
		body = "I'm currently on vacation and will reply when I return."
	}

	// When this vacation comes from an OOF policy, honor its internal/external
	// reply split and external audience — the compiled Sieve script carries only
	// a single body, so the per-sender choice is made here. A vacation with no
	// OOF policy (the user's own ManageSieve vacation) keeps the single body.
	if oofPolicy != nil {
		oofSubject, oofBody, send := oofReplyFor(oofPolicy, s.isLocalSender(sender))
		if !send {
			return
		}
		if oofBody != "" {
			body = oofBody
		}
		if oofSubject != "" {
			subject = oofSubject
		}
	}

	// Create vacation message - From is the recipient (who's on vacation)
	fromAddr := recipient
	if vacation.From != "" {
		fromAddr = vacation.From
	}
	safeSubject := sanitizeHeaderValue(subject)
	safeFrom := sanitizeHeaderValue(fromAddr)
	// The reply body lives after the header separator, so it only needs SMTP
	// CRLF line endings — not header sanitization, which would flatten a
	// multi-line internal/external OOF reply onto a single line.
	bodyCRLF := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	contentType := "text/plain; charset=utf-8"
	if looksLikeHTML(bodyCRLF) {
		contentType = "text/html; charset=utf-8"
	}
	vacationMsg := fmt.Sprintf("From: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: %s\r\nX-Mail-Loop: <%s>\r\n\r\n%s",
		safeFrom,
		safeSubject,
		contentType,
		recipient,
		bodyCRLF)

	// Deliver the vacation reply TO the sender FROM the recipient. Use the
	// shared delivery handler so a local sender is written straight to their
	// mailbox and a remote sender is relayed via the queue — the bare queue
	// path only does remote MX delivery and would never reach a local inbox.
	if err := s.deliverMessageWithSieve(fromAddr, []string{sender}, []byte(vacationMsg), nil); err != nil {
		s.logger.Error("Failed to deliver vacation reply", "to", sender, "from", fromAddr, "error", err)
	} else {
		s.logger.Info("Vacation reply delivered", "to", sender, "from", fromAddr)
	}
}

// oofReplyFor selects the out-of-office reply for a sender from an OOF policy,
// honoring the external audience. internal reports whether the sender is inside
// the organization (a locally hosted domain). It returns the subject and body to
// send and whether to send at all: an external sender is suppressed when the
// policy's audience is internal-only (Exchange "None"); "Known" and "All" both
// let external senders through. An empty internal/external body falls back to the
// policy's shared TextBody (which mirrors the internal reply).
func oofReplyFor(policy *semcore.OOFPolicy, internal bool) (subject, body string, send bool) {
	if policy == nil {
		return "", "", false
	}
	if !internal && policy.Audience == semcore.OOFAudienceInternal {
		return "", "", false
	}
	if internal {
		body = policy.InternalReply
	} else {
		body = policy.ExternalReply
	}
	if body == "" {
		body = policy.TextBody
	}
	return policy.Subject, body, true
}

// looksLikeHTML reports whether a reply body should be sent as text/html. OOF
// bodies set from Outlook over EWS are HTML; a plain-text reply stays text/plain.
func looksLikeHTML(body string) bool {
	l := strings.ToLower(body)
	for _, tag := range []string{"<html", "<body", "<p>", "<p ", "<div", "<br", "<span", "<table"} {
		if strings.Contains(l, tag) {
			return true
		}
	}
	return false
}

// sendVacationReply generates and enqueues an auto-reply message.
func (s *Server) sendVacationReply(recipientEmail, senderEmail, settingsJSON string) {
	senderLower := strings.ToLower(senderEmail)
	for _, prefix := range []string{"mailer-daemon@", "postmaster@", "noreply@", "no-reply@", "bounce@"} {
		if strings.HasPrefix(senderLower, prefix) {
			return
		}
	}

	// Parse settings first to get SendInterval for deduplication
	var settings struct {
		Enabled      bool          `json:"enabled"`
		Message      string        `json:"message"`
		StartDate    string        `json:"start_date"`
		EndDate      string        `json:"end_date"`
		SendInterval time.Duration `json:"send_interval"`
	}
	if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil || !settings.Enabled {
		return
	}

	// Use SendInterval from settings, default to 24h if not set or too small
	sendInterval := settings.SendInterval
	if sendInterval < 24*time.Hour {
		sendInterval = 24 * time.Hour
	}

	// sanitizeForDedup replaces the pipe delimiter with a double-underscore
	// to prevent key collisions when email addresses contain '|'.
	safeRecipient := strings.ReplaceAll(recipientEmail, "|", "__")
	safeSender := strings.ReplaceAll(senderEmail, "|", "__")
	key := safeRecipient + "|" + safeSender
	s.vacationRepliesMu.Lock()
	if s.vacationReplies == nil {
		s.vacationReplies = make(map[string]time.Time)
	}
	if lastSent, ok := s.vacationReplies[key]; ok && time.Since(lastSent) < sendInterval {
		s.vacationRepliesMu.Unlock()
		return
	}
	s.vacationReplies[key] = time.Now()

	// Cleanup old entries every 100 entries to prevent unbounded growth
	if len(s.vacationReplies) > 100 {
		// Must release lock before calling cleanupVacationRepliesLocked
		// which acquires the lock internally - sync.Mutex is not reentrant
		s.vacationRepliesMu.Unlock()
		s.cleanupVacationRepliesLocked()
		s.vacationRepliesMu.Lock()
	}

	s.vacationRepliesMu.Unlock()

	now := time.Now()
	if settings.StartDate != "" {
		if start, err := time.Parse("2006-01-02", settings.StartDate); err == nil && now.Before(start) {
			return
		}
	}
	if settings.EndDate != "" {
		if end, err := time.Parse("2006-01-02", settings.EndDate); err == nil && !now.Before(end.Add(24*time.Hour)) {
			return
		}
	}

	// Guard against nil queue
	if s.queue == nil {
		return
	}

	autoReply := "From: " + sanitizeHeaderValue(recipientEmail) + "\r\n" +
		"To: " + sanitizeHeaderValue(senderEmail) + "\r\n" +
		"Subject: Auto: Out of Office\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"Precedence: bulk\r\n" +
		"Date: " + now.Format(time.RFC1123Z) + "\r\n" +
		"\r\n" +
		sanitizeHeaderValue(settings.Message)

	if _, err := s.queue.Enqueue(recipientEmail, []string{senderEmail}, []byte(autoReply)); err != nil {
		s.logger.Error("Failed to enqueue vacation reply", "error", err)
	}
}

// cleanupVacationReplies removes entries older than 48 hours from vacationReplies map.
// It acquires the lock only for the minimum time needed: marking keys, then releases
// before deletion to avoid blocking sendVacationReply during long cleanup runs.
func (s *Server) cleanupVacationReplies() {
	cutoff := time.Now().Add(-48 * time.Hour)

	// Phase 1: Mark keys to delete while holding lock briefly
	s.vacationRepliesMu.Lock()
	var toDelete []string
	for key, lastSent := range s.vacationReplies {
		if lastSent.Before(cutoff) {
			toDelete = append(toDelete, key)
		}
	}
	s.vacationRepliesMu.Unlock()

	// Phase 2: Delete outside the lock to avoid blocking sendVacationReply
	for _, key := range toDelete {
		s.vacationRepliesMu.Lock()
		delete(s.vacationReplies, key)
		s.vacationRepliesMu.Unlock()
	}
}

// cleanupVacationRepliesLocked removes entries older than 48 hours.
// Caller must hold s.vacationRepliesMu.
func (s *Server) cleanupVacationRepliesLocked() {
	cutoff := time.Now().Add(-48 * time.Hour)
	for key, lastSent := range s.vacationReplies {
		if lastSent.Before(cutoff) {
			delete(s.vacationReplies, key)
		}
	}
}

// startVacationCleanup runs a hourly goroutine that removes vacation reply entries
// older than 48 hours, preventing unbounded map growth independent of the
// threshold-based cleanup in trackVacationReply.
func (s *Server) startVacationCleanup() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.cleanupVacationReplies()
			}
		}
	}()
}
