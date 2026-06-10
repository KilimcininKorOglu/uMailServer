package server

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/metrics"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/smtp"
	"github.com/umailserver/umailserver/internal/storage"
	"github.com/umailserver/umailserver/internal/tracing"
	"github.com/umailserver/umailserver/internal/webhook"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/bcrypt"
)

func isAccountLookupMiss(err error) bool {
	return errors.Is(err, db.ErrNotFound)
}

// baseLocalPart strips an RFC 5233 "+detail" subaddress suffix from a local part,
// returning the bare mailbox local part used for account resolution. The full
// recipient address is left intact in the message headers, so Sieve and clients
// can still filter on the detail. "bob+tag" -> "bob"; "bob" -> "bob".
// A leading "+" (empty base) is left untouched as it is not a valid mailbox.
func baseLocalPart(localPart string) string {
	if i := strings.IndexByte(localPart, '+'); i > 0 {
		return localPart[:i]
	}
	return localPart
}

func (s *Server) loadLocalAccount(email string) (user, domain string, account *db.AccountData, err error) {
	user, domain = parseEmail(email)
	account, err = s.database.GetAccount(domain, user)
	if err != nil {
		return user, domain, nil, err
	}
	return user, domain, account, nil
}

func (s *Server) validateAccountAuthentication(account *db.AccountData) error {
	if !account.IsActive {
		return fmt.Errorf("account is not active")
	}
	if account.MustChangePassword {
		return fmt.Errorf("password change required")
	}
	// Block all protocol auth (SMTP/IMAP/POP3/CalDAV/CardDAV — every surface that
	// authenticates through s.authenticate) for accounts whose owning tenant is
	// suspended. This is the single canonical credential-validation point, so the
	// suspend applies uniformly across protocols.
	if s.database != nil && account.Domain != "" {
		if dom, derr := s.database.GetDomain(account.Domain); derr == nil && dom.TenantID != "" {
			if tenant, terr := s.database.GetTenant(dom.TenantID); terr == nil && !tenant.IsActive {
				return fmt.Errorf("tenant is suspended")
			}
		}
	}
	return nil
}

// authenticate validates user credentials
func (s *Server) authenticate(username, password string) (bool, error) {
	// Create tracing span if tracing is enabled
	if s.tracingProvider != nil && s.tracingProvider.IsEnabled() {
		ctx, span := s.tracingProvider.StartSpanWithKind(context.Background(), "authenticate", tracing.SpanKindServer,
			attribute.String("auth.username", username),
		)
		defer span.End()
		_ = ctx // Use ctx if needed for future LDAP tracing
	}

	// Try LDAP authentication first if enabled
	if s.ldapClient != nil {
		ldapUser, err := s.ldapClient.Authenticate(username, password)
		if err == nil {
			_, _, localAccount, lookupErr := s.loadLocalAccount(ldapUser.Email)
			if lookupErr == nil {
				if err := s.validateAccountAuthentication(localAccount); err != nil {
					s.logger.Debug("LDAP authentication blocked by local account state",
						"username", username,
						"email", ldapUser.Email,
						"error", err,
					)
					return false, err
				}
			} else if !isAccountLookupMiss(lookupErr) {
				return false, lookupErr
			}
			s.logger.Debug("LDAP authentication successful",
				"username", username,
				"email", ldapUser.Email,
				"is_admin", ldapUser.IsAdmin,
			)
			return true, nil
		}
		// If LDAP returns "user not found", fall back to local DB
		// Other errors (connection failure, etc.) also fall back to local DB
		s.logger.Debug("LDAP auth failed, falling back to local DB", "username", username, "error", err)
	}

	// Fall back to local database authentication
	_, _, localAccount, err := s.loadLocalAccount(username)
	if err != nil {
		return false, err
	}
	account := localAccount

	if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)); err != nil {
		return false, nil
	}

	if err := s.validateAccountAuthentication(account); err != nil {
		return false, err
	}

	return true, nil
}

// getUserSecret returns the password hash for a user, used by CRAM-MD5 authentication
func (s *Server) getUserSecret(username string) (string, error) {
	_, _, localAccount, err := s.loadLocalAccount(username)
	if err != nil {
		return "", err
	}
	if err := s.validateAccountAuthentication(localAccount); err != nil {
		return "", err
	}
	return localAccount.PasswordHash, nil
}

// loginResult handles SMTP login success/failure events and triggers webhooks
// + audit. It exists for backwards compatibility with the SMTP wiring; new
// callers should use protoLoginHandler which is service-parameterized.
func (s *Server) loginResult(username string, success bool, ip string) {
	s.recordLoginResult("smtp", username, success, ip, "")
}

// recordLoginResult is the unified login-event sink for all auth-bearing
// protocols (smtp, imap, pop3). It writes to the audit log and fires the
// webhook event in one place so consumers stay consistent across protocols.
func (s *Server) recordLoginResult(service, username string, success bool, ip, reason string) {
	if s.apiServer != nil {
		if al := s.apiServer.AuditLogger(); al != nil {
			al.LogProtocolLogin(service, username, ip, success, reason)
		}
	}
	if s.webhookMgr != nil {
		eventType := "auth.login.success"
		if !success {
			eventType = "auth.login.failed"
		}
		payload := map[string]interface{}{
			"service":  service,
			"username": username,
			"ip":       ip,
		}
		if !success && reason != "" {
			payload["reason"] = reason
		}
		s.webhookMgr.Trigger(eventType, payload)
	}
}

// protoLoginHandler returns a SetLoginResultHandler-compatible callback that
// tags every event with the given protocol service ("smtp", "imap", "pop3").
func (s *Server) protoLoginHandler(service string) func(username string, success bool, ip, reason string) {
	return func(username string, success bool, ip, reason string) {
		s.recordLoginResult(service, username, success, ip, reason)
	}
}

// deliverMessage delivers an incoming message
func (s *Server) deliverMessage(from string, to []string, data []byte) error {
	return s.deliverMessageWithSieve(from, to, data, nil)
}

// deliverMessageWithSieve delivers an incoming message with optional Sieve filtering actions
// isAlreadySigned reports whether a message already carries an S/MIME signature
// or encryption, so the signing hook stays idempotent and never double-wraps.
func isAlreadySigned(data []byte) bool {
	end := len(data)
	if idx := strings.Index(string(data), "\r\n\r\n"); idx >= 0 {
		end = idx
	}
	headers := strings.ToLower(string(data[:end]))
	return strings.Contains(headers, "multipart/signed") ||
		strings.Contains(headers, "application/pkcs7-signature") ||
		strings.Contains(headers, "application/pkcs7-mime")
}

// isLocalSender reports whether the sender address belongs to a local, active
// domain. Used to enforce a group's internal-only sender policy.
func (s *Server) isLocalSender(from string) bool {
	_, domain := parseEmail(from)
	if domain == "" {
		return false
	}
	d, err := s.database.GetDomain(domain)
	return err == nil && d != nil && d.IsActive
}

// expandMailGroups replaces any local mail-group recipients with their member
// addresses, enforcing each group's sender policy. Non-group recipients pass
// through unchanged; nested groups are expanded with cycle and depth guards.
func (s *Server) expandMailGroups(from string, to []string) []string {
	result := make([]string, 0, len(to))
	seen := make(map[string]bool)    // dedup of final recipients
	visited := make(map[string]bool) // group cycle guard

	var expand func(addr string, depth int)
	expand = func(addr string, depth int) {
		addr = strings.TrimSpace(addr)
		if addr == "" || depth > 10 {
			return
		}
		user, domain := parseEmail(addr)
		if user != "" && domain != "" {
			if group, err := s.database.GetMailGroup(domain, user); err == nil && group != nil && group.IsActive {
				key := strings.ToLower(addr)
				if visited[key] {
					return
				}
				visited[key] = true
				if group.SenderPolicy == "internal" && !s.isLocalSender(from) {
					s.logger.Warn("rejected external sender to internal-only group", "from", from, "group", addr)
					return
				}
				members, mErr := s.database.ExpandMailGroup(group)
				if mErr != nil {
					s.logger.Error("failed to expand mail group", "group", addr, "error", mErr)
					return
				}
				for _, m := range members {
					expand(m, depth+1)
				}
				return
			}
		}
		if !seen[strings.ToLower(addr)] {
			seen[strings.ToLower(addr)] = true
			result = append(result, addr)
		}
	}

	for _, addr := range to {
		expand(addr, 0)
	}
	return result
}

func (s *Server) deliverMessageWithSieve(from string, to []string, data []byte, sieveActions []string) error {
	// Create tracing span if tracing is enabled
	var ctx context.Context = context.Background()
	if s.tracingProvider != nil && s.tracingProvider.IsEnabled() {
		var span trace.Span
		ctx, span = s.tracingProvider.StartSpanWithKind(ctx, "deliverMessage", tracing.SpanKindServer,
			attribute.String("mail.from", from),
			attribute.Int("mail.recipients", len(to)),
			attribute.Int("mail.size", len(data)),
		)
		defer span.End()
	}

	// Parse sieve actions for fileinto, redirect, and keep
	var targetFolders []string
	var redirectAddrs []string
	var extraFlags []string
	hasKeep := false
	hasDiscard := false
	setRead := false

	for _, action := range sieveActions {
		if strings.HasPrefix(action, "fileinto:") {
			targetFolders = append(targetFolders, strings.TrimPrefix(action, "fileinto:"))
		} else if strings.HasPrefix(action, "redirect:") {
			redirectAddr := strings.TrimPrefix(action, "redirect:")
			if redirectAddr != "" {
				redirectAddrs = append(redirectAddrs, redirectAddr)
			}
		} else if action == "keep" {
			hasKeep = true
		} else if action == "discard" {
			hasDiscard = true
		} else if strings.HasPrefix(action, "addflag:") {
			// Sieve imap4flags: apply the flag to the stored message. \Seen also
			// marks the canonical identity read (for EWS), the rest are IMAP keywords.
			flag := strings.TrimPrefix(action, "addflag:")
			if flag != "" {
				extraFlags = append(extraFlags, flag)
				if flag == "\\Seen" {
					setRead = true
				}
			}
		}
	}

	// If no fileinto targets, use inbox as default (unless discard or redirect
	// cancels the implicit keep). RFC 5228: a bare redirect (no :copy) forwards
	// the message WITHOUT keeping a local copy; an explicit `keep` after redirect
	// still keeps one (hasKeep below).
	if len(targetFolders) == 0 {
		if hasKeep {
			targetFolders = []string{""} // keep overrides discard
		} else if !hasDiscard && len(redirectAddrs) == 0 {
			targetFolders = []string{""} // implicit keep
		}
	} else if hasKeep {
		// copy behavior: keep in inbox AND fileinto target folders
		targetFolders = append(targetFolders, "")
	} else if hasDiscard && !hasKeep {
		// discard: don't add inbox, only deliver to explicit fileinto targets
	}

	s.logger.Info("SieveDeliver", "folders", targetFolders)

	// Outbound S/MIME signing: if the local sender has a provisioned key and the
	// message is not already signed, sign it before relay/delivery. The
	// has-key gate naturally excludes inbound external mail (no key). Fail-open:
	// on any error the original message is delivered unsigned (never dropped).
	if s.cfg().Signing.Enabled && s.smimeKeystore != nil && !isAlreadySigned(data) {
		sender := strings.ToLower(strings.TrimSpace(from))
		if s.smimeKeystore.GetKeys(sender) != nil {
			signTo := ""
			if len(to) > 0 {
				signTo = to[0]
			}
			if signed, signErr := smtp.NewSMIMEStage(s.smimeKeystore).SignMessage(sender, from, signTo, data); signErr != nil {
				s.logger.Warn("S/MIME signing failed; delivering unsigned", "from", from, "error", signErr)
			} else {
				data = signed
				s.logger.Info("S/MIME signed outbound message", "from", from)
			}
		}
	}

	// Handle redirects - queue copies to redirect addresses
	for _, redirectAddr := range redirectAddrs {
		// Check for forwarding loop
		loopAddrs := getMailLoopHeaders(data)
		for _, loopAddr := range loopAddrs {
			if strings.EqualFold(loopAddr, redirectAddr) {
				s.logger.Warn("Forwarding loop detected, skipping redirect", "loop_addr", loopAddr, "redirect_to", redirectAddr)
				continue
			}
		}
		// Add this sender to the loop tracking header
		dataWithLoop := addMailLoopHeader(data, from)

		// Deliver redirect locally if recipient domain is local
		_, redirectDomain := parseEmail(redirectAddr)
		domainData, _ := s.database.GetDomain(redirectDomain)
		if domainData != nil && domainData.IsActive {
			redirectUser, _ := parseEmail(redirectAddr)
			if err := s.deliverLocal(redirectUser, redirectDomain, from, dataWithLoop, false, nil); err != nil {
				s.logger.Error("Failed to deliver redirect locally", "to", redirectAddr, "error", err)
			} else {
				s.logger.Debug("Redirect delivered locally", "from", from, "to", redirectAddr)
			}
		} else if err := s.relayMessage(from, redirectAddr, dataWithLoop); err != nil {
			s.logger.Error("Failed to queue redirect message", "to", redirectAddr, "error", err)
		} else {
			s.logger.Debug("Message queued for redirect", "from", from, "to", redirectAddr)
		}
	}

	// Expand any mail-group recipients into their members before delivery, so a
	// single group address fans out to every member (static or dynamic).
	to = s.expandMailGroups(from, to)

	var errs []error
	for _, recipient := range to {
		user, domain := parseEmail(recipient)

		domainData, err := s.database.GetDomain(domain)
		if err != nil || domainData == nil || !domainData.IsActive {
			if relayErr := s.relayMessage(from, recipient, data); relayErr != nil {
				s.logger.Error("Failed to relay message", "to", recipient, "error", relayErr)
				errs = append(errs, fmt.Errorf("relay %s: %w", recipient, relayErr))
			}
			continue
		}

		// Resolve alias
		target, aliasErr := s.database.ResolveAlias(domain, user)
		if aliasErr != nil {
			s.logger.Debug("Alias resolution failed, trying direct delivery", "domain", domain, "user", user, "error", aliasErr)
		}
		if target != "" {
			tUser, tDomain := parseEmail(target)
			if tUser != "" && tDomain != "" {
				user = tUser
				domain = tDomain
			}
		}

		// Deliver with optional target folder from sieve
		for _, targetFolder := range targetFolders {
			if err := s.deliverLocal(user, domain, from, data, setRead, extraFlags, targetFolder); err != nil {
				s.logger.Error("Failed to deliver locally", "user", user, "domain", domain, "target", targetFolder, "error", err)
				errs = append(errs, fmt.Errorf("deliver %s->%s: %w", recipient, targetFolder, err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("delivery had %d failure(s): %w", len(errs), errors.Join(errs...))
	}
	return nil
}

// relayMessage relays a message to a remote server
func (s *Server) relayMessage(from, to string, data []byte) error {
	if s.queue != nil {
		_, err := s.queue.Enqueue(from, []string{to}, data)
		if err != nil {
			s.logger.Error("Failed to enqueue relay message", "error", err)
			return fmt.Errorf("failed to queue message: %w", err)
		}
		s.logger.Debug("Message queued for relay", "from", from, "to", to)
		return nil
	}
	s.logger.Debug("Relaying message (queue not available)", "from", from, "to", to)
	return nil
}

// getMailLoopHeaders returns all addresses in existing X-Mail-Loop headers.
func getMailLoopHeaders(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return nil
	}
	return msg.Header["X-Mail-Loop"]
}

// addMailLoopHeader appends an X-Mail-Loop header with the given address.
func addMailLoopHeader(data []byte, addr string) []byte {
	if len(data) == 0 {
		return data
	}
	idx := strings.Index(string(data), "\r\n\r\n")
	if idx == -1 {
		idx = strings.Index(string(data), "\n\n")
		if idx == -1 {
			return data
		}
		// Insert before blank line (Unix newline)
		headerPart := string(data[:idx+1])
		bodyPart := string(data[idx+1:])
		return []byte(headerPart + "X-Mail-Loop: " + addr + "\n" + bodyPart)
	}
	// Insert before blank line (Windows newline)
	headerPart := string(data[:idx+2])
	bodyPart := string(data[idx+2:])
	return []byte(headerPart + "X-Mail-Loop: " + addr + "\r\n" + bodyPart)
}

// deliverLocal delivers a message to a local mailbox
func (s *Server) deliverLocal(user, domain, from string, data []byte, isRead bool, extraFlags []string, targetFolders ...string) error {
	// RFC 5233 subaddressing: "user+detail" resolves to the "user" mailbox.
	user = baseLocalPart(user)
	email := user + "@" + domain

	// Determine target folder - default to INBOX if not specified
	folder := "INBOX"
	if len(targetFolders) > 0 && targetFolders[0] != "" {
		folder = targetFolders[0]
	}

	// Check if user exists
	account, err := s.database.GetAccount(domain, user)
	if err != nil {
		return fmt.Errorf("user does not exist: %s", email)
	}

	if account == nil || !account.IsActive {
		// Check catch-all target for the domain
		if domainData, derr := s.database.GetDomain(domain); derr == nil && domainData != nil && domainData.CatchAllTarget != "" {
			tUser, tDomain := parseEmail(domainData.CatchAllTarget)
			if tUser != "" && tDomain != "" {
				return s.deliverLocal(tUser, tDomain, from, data, isRead, extraFlags, targetFolders...)
			}
		}
		return fmt.Errorf("user does not exist or is not active: %s", email)
	}

	// Canonical mutation: assign semantic identity through the unified pipeline.
	// This is the single authoritative write path for all mail mutations.
	// It assigns ItemId, ChangeKey, and ConversationId consistently for
	// SMTP-delivered and mailbox-authored messages.
	//
	// If the semcore store is not yet initialized (nil), we skip semantic
	// identity assignment and fall back to the existing storage path.
	var mutationResult *semcore.MutationResult
	if s.mutationPipe != nil {
		// Resolve or create mailbox and folder identities.
		mboxID, mboxErr := s.mutationPipe.Identity().EnsureMailboxId(email)
		if mboxErr != nil {
			s.logger.Warn("Failed to ensure mailbox identity, skipping semantic mutation",
				"email", email, "error", mboxErr)
		} else {
			// Determine distinguished role for known folders.
			role := distinguishedRole(folder)
			fldID, fldErr := s.mutationPipe.Identity().EnsureFolderId(email, folder, role)
			if fldErr != nil {
				s.logger.Warn("Failed to ensure folder identity, skipping semantic mutation",
					"email", email, "folder", folder, "error", fldErr)
			} else {
				in := &semcore.MutationInput{
					MailboxID:    mboxID,
					FolderID:     fldID,
					RawMessage:   data,
					InternalDate: time.Now(),
					Actor:        from,
					Email:        email,
					Source:       semcore.MutationSourceSMTP,
					IsRead:       isRead,
				}
				mutationResult, mboxErr = s.mutationPipe.MutateItem(in)
				if mboxErr != nil {
					s.logger.Warn("Canonical mutation failed, falling back to legacy path",
						"email", email, "error", mboxErr)
					mutationResult = nil
				} else {
					s.logger.Debug("Canonical mutation succeeded",
						"email", email, "folder", folder, "role", role,
						"item_id", mutationResult.ItemID.String(),
						"change_key", mutationResult.ChangeKey.String(),
						"conversation_id", mutationResult.ConversationID.String())
				}
			}
		}
	}

	// Reserve quota atomically before storing
	if err := s.database.IncrementQuota(domain, user, int64(len(data))); err != nil {
		return fmt.Errorf("quota exceeded for user: %s", email)
	}

	// Handle mail forwarding (before storing, so we skip local store if not keeping copy)
	if account.ForwardTo != "" {
		// Check for forwarding loop
		loopAddrs := getMailLoopHeaders(data)
		for _, loopAddr := range loopAddrs {
			if strings.EqualFold(loopAddr, email) {
				s.logger.Warn("Forwarding loop detected, skipping forward", "from", from, "to", email)
				return nil
			}
		}
		// Add this sender to the loop tracking header
		dataWithLoop := addMailLoopHeader(data, email)
		forwardTargets := strings.Split(account.ForwardTo, ",")
		for _, fwd := range forwardTargets {
			fwd = strings.TrimSpace(fwd)
			if fwd == "" {
				continue
			}
			if s.queue != nil {
				if _, err := s.queue.Enqueue(email, []string{fwd}, dataWithLoop); err != nil {
					s.logger.Error("Failed to enqueue forwarded message", "from", email, "to", fwd, "error", err)
				}
			}
		}
		if !account.ForwardKeepCopy {
			// Release the quota we reserved since we're not storing locally
			s.database.IncrementQuota(domain, user, -int64(len(data)))
			s.logger.Debug("Message forwarded (no local copy)",
				"to", email,
				"from", from,
			)
			return nil
		}
	}

	// Store message locally
	messageID, err := s.msgStore.StoreMessage(email, data)
	if err != nil {
		// Release the quota we reserved since store failed
		s.database.IncrementQuota(domain, user, -int64(len(data)))
		return fmt.Errorf("failed to store message: %w", err)
	}

	s.logger.Debug("Message delivered",
		"to", email,
		"from", from,
		"message_id", messageID,
	)

	// Store metadata and index message for search
	if s.storageDB != nil {
		uid, uidErr := s.storageDB.GetNextUID(email, folder)
		if uidErr == nil {
			subject, fromAddr, toAddr, dateStr := parseBasicHeaders(data)
			hdrMsgID, hdrInReplyTo, hdrRefs := parseThreadingHeaders(data)
			// Deterministic thread id (RFC 2822 rooting) so this message groups
			// with the rest of its conversation across mailboxes and protocols.
			threadID, _ := s.storageDB.GetOrCreateThreadID(email, folder, subject, hdrMsgID, hdrInReplyTo, hdrRefs) //nolint:errcheck
			meta := &storage.MessageMetadata{
				MessageID:    messageID,
				UID:          uid,
				Flags:        append([]string{"\\Recent"}, extraFlags...),
				InternalDate: time.Now(),
				Size:         int64(len(data)),
				Subject:      subject,
				Date:         dateStr,
				From:         fromAddr,
				To:           toAddr,
				ThreadID:     threadID,
				InReplyTo:    hdrInReplyTo,
				References:   hdrRefs,
			}
			if err := s.storageDB.StoreMessageMetadata(email, folder, uid, meta); err != nil {
				s.logger.Error("Failed to store message metadata", "email", email, "uid", uid, "folder", folder, "error", err)
			}

			// Publish a new-message notification so real-time consumers react
			// immediately: IMAP IDLE clients get an untagged EXISTS, and the SSE
			// stream pushes a "new_mail" event to the webmail UI (push-to-pull —
			// the UI then fetches the message over HTTP). Best-effort signal.
			imap.GetNotificationHub().NotifyNewMessage(email, folder, uid, uid)

			if s.searchSvc != nil {
				// Extract canonical identity from mutation result when available.
				var itemID, conversationID string
				if mutationResult != nil {
					itemID = mutationResult.ItemID.String()
					conversationID = mutationResult.ConversationID.String()
				}
				select {
				case s.indexWork <- indexJob{email: email, uid: uid, itemID: itemID, conversationID: conversationID}:
				default:
					s.logger.Warn("Search index queue full, dropping index job", "email", email, "uid", uid)
				}
			}
		}
	}

	// Trigger webhook for mail received
	if s.webhookMgr != nil {
		s.webhookMgr.Trigger(webhook.EventMailReceived, map[string]interface{}{
			"message_id": messageID,
			"to":         email,
			"from":       from,
			"size":       len(data),
		})
	}

	// Send push notification for new mail
	if s.pushSvc != nil {
		select {
		case s.bgSem <- struct{}{}:
			go func() {
				defer func() {
					<-s.bgSem
					if r := recover(); r != nil {
						s.logger.Error("Panic in push notification", "error", r)
					}
				}()
				// Extract subject from message for notification
				subject, _, _, _ := parseBasicHeaders(data)
				if subject == "" {
					subject = "(No subject)"
				}
				// Send push notification (non-blocking)
				if err := s.pushSvc.SendNewMailNotification(email, from, subject, ""); err != nil {
					s.logger.Debug("Failed to send push notification", "to", email, "error", err)
				}
			}()
		default:
			s.logger.Warn("Background task semaphore full, dropping push notification", "email", email)
		}
	}

	// Track delivery metric
	metrics.Get().DeliverySuccess()

	// Send vacation auto-reply if configured. Skip the legacy
	// account.VacationSettings path when an out-of-office policy is active: that
	// policy is compiled to a Sieve vacation action which already auto-replies at
	// delivery, so running both would send two replies for the same message.
	if account.VacationSettings != "" && s.queue != nil && !s.hasActiveOOFPolicy(email) {
		select {
		case s.bgSem <- struct{}{}:
			go func() {
				defer func() {
					<-s.bgSem
					if r := recover(); r != nil {
						s.logger.Error("Panic in vacation reply", "error", r)
					}
				}()
				s.sendVacationReply(email, from, account.VacationSettings)
			}()
		default:
			s.logger.Warn("Background task semaphore full, dropping vacation reply", "email", email)
		}
	}

	// Graduated quota: once this delivery pushes usage past the IssueWarning
	// threshold, drop a one-time notice into the mailbox (and clear the latch when
	// usage falls back below). Best-effort; never affects the delivery outcome.
	s.maybeWarnQuota(email)

	return nil
}

// maybeWarnQuota files a one-time over-quota notice into the mailbox once a
// delivery pushes usage past the graduated IssueWarning threshold, and clears
// the latch when usage falls back below it so a later crossing warns again. It
// is best-effort and never affects the delivery outcome: a failure to file the
// notice or flip the latch is logged, not propagated. The notice is filed
// directly into the INBOX (not enqueued), so it neither re-enters quota
// accounting nor recurses through the delivery pipeline. Accounts with the
// warning disabled (effective warn == 0) are skipped.
//
// The account is re-read here rather than reusing the caller's copy because
// deliverLocal increments QuotaUsed before this call; the fresh read reflects
// the post-increment usage that the threshold comparison depends on.
func (s *Server) maybeWarnQuota(email string) {
	if s.database == nil {
		return
	}
	user, domain, acct, err := s.loadLocalAccount(email)
	if err != nil || acct == nil {
		return
	}
	var dom *db.DomainData
	if d, derr := s.database.GetDomain(domain); derr == nil {
		dom = d
	}
	warn, prohibitSend, hardCap := db.EffectiveQuotaThresholds(acct, dom)
	if warn == 0 {
		return // graduated warning disabled for this account
	}

	switch {
	case acct.QuotaUsed >= warn && !acct.QuotaWarnSent:
		raw := buildQuotaWarning(email, domain, acct.QuotaUsed, warn, prohibitSend, hardCap)
		uid, _, ferr := s.fileFolderCopy(email, "INBOX", raw, nil)
		if ferr != nil {
			s.logger.Error("quota: failed to file over-quota warning", "email", email, "error", ferr)
			return
		}
		imap.GetNotificationHub().NotifyNewMessage(email, "INBOX", uid, uid)
		if serr := s.database.SetQuotaWarnSent(domain, user, true); serr != nil {
			s.logger.Error("quota: failed to latch warning flag", "email", email, "error", serr)
		}
	case acct.QuotaUsed < warn && acct.QuotaWarnSent:
		// Usage fell back below the warn line; re-arm so the next crossing warns.
		if serr := s.database.SetQuotaWarnSent(domain, user, false); serr != nil {
			s.logger.Error("quota: failed to re-arm warning flag", "email", email, "error", serr)
		}
	}
}

// buildQuotaWarning renders the postmaster over-quota notice as a self-contained
// RFC 5322 message. It states the current usage and the graduated thresholds
// that apply (sending blocked at prohibitSend, receiving blocked at the hard
// cap); a zero threshold is reported as not configured.
func buildQuotaWarning(email, domain string, used, warn, prohibitSend, hardCap int64) []byte {
	now := time.Now()
	var body strings.Builder
	fmt.Fprintf(&body, "Your mailbox has reached its quota warning threshold.\r\n\r\n")
	fmt.Fprintf(&body, "Current usage: %s\r\n", formatQuotaBytes(used))
	fmt.Fprintf(&body, "Warning threshold: %s\r\n", formatQuotaBytes(warn))
	if prohibitSend > 0 {
		fmt.Fprintf(&body, "Sending is disabled at: %s\r\n", formatQuotaBytes(prohibitSend))
	}
	if hardCap > 0 {
		fmt.Fprintf(&body, "Receiving is disabled at: %s\r\n", formatQuotaBytes(hardCap))
	}
	fmt.Fprintf(&body, "\r\nPlease delete unneeded messages to free space.\r\n")

	msg := "From: " + sanitizeHeaderValue("postmaster@"+domain) + "\r\n" +
		"To: " + sanitizeHeaderValue(email) + "\r\n" +
		"Subject: Mailbox quota warning\r\n" +
		"Auto-Submitted: auto-generated\r\n" +
		"Precedence: bulk\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Date: " + now.Format(time.RFC1123Z) + "\r\n" +
		"\r\n" +
		body.String()
	return []byte(msg)
}

// formatQuotaBytes renders a byte count with a binary-prefix unit (KiB/MiB/GiB)
// for the quota notice, falling back to a raw byte count below 1 KiB.
func formatQuotaBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// parseEmail splits an email address into user and domain
func parseEmail(email string) (user, domain string) {
	at := -1
	for i := len(email) - 1; i >= 0; i-- {
		if email[i] == '@' {
			at = i
			break
		}
	}
	if at == -1 {
		return email, ""
	}
	return email[:at], email[at+1:]
}

// parseBasicHeaders extracts subject, from, to, date from raw message data.
func parseBasicHeaders(data []byte) (subject, from, to, date string) {
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return "", "", "", ""
	}
	subject = msg.Header.Get("Subject")
	from = msg.Header.Get("From")
	to = msg.Header.Get("To")
	date = msg.Header.Get("Date")
	return
}

// parseThreadingHeaders extracts the RFC 2822 threading headers (Message-ID,
// In-Reply-To, References) from raw message data, stripped of angle brackets.
func parseThreadingHeaders(data []byte) (messageID, inReplyTo string, references []string) {
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return "", "", nil
	}
	trim := func(s string) string { return strings.Trim(strings.TrimSpace(s), "<>") }
	messageID = trim(msg.Header.Get("Message-ID"))
	inReplyTo = trim(msg.Header.Get("In-Reply-To"))
	for _, ref := range strings.Fields(msg.Header.Get("References")) {
		if r := trim(ref); r != "" {
			references = append(references, r)
		}
	}
	return messageID, inReplyTo, references
}

// generateSecureToken generates a cryptographically random 32-byte hex token.
func generateSecureToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// authenticateClientCert authenticates a user based on client certificate
// Returns the email address extracted from the certificate and true if valid
func (s *Server) authenticateClientCert(cert *x509.Certificate) (string, bool) {
	if cert == nil {
		return "", false
	}

	// Extract email from certificate
	var email string
	if len(cert.EmailAddresses) > 0 {
		email = cert.EmailAddresses[0]
	} else if cert.Subject.CommonName != "" {
		// Try to use CommonName as email if it looks like one
		if strings.Contains(cert.Subject.CommonName, "@") {
			email = cert.Subject.CommonName
		}
	}

	if email == "" {
		s.logger.Debug("Client certificate has no email address")
		return "", false
	}

	// Verify the account exists
	_, _, localAccount, err := s.loadLocalAccount(email)
	if err != nil {
		s.logger.Debug("Account not found for client certificate", "email", email)
		return "", false
	}

	if err := s.validateAccountAuthentication(localAccount); err != nil {
		s.logger.Debug("Client certificate blocked by account state", "email", email, "error", err)
		return "", false
	}

	// Log successful client cert auth
	s.logger.Info("Client certificate authentication successful", "email", email)

	return email, true
}

// distinguishedRole returns the canonical distinguished folder role for well-known
// IMAP folder names. This is used when registering folder identity in the
// semantic-core store so that distinguished folders have stable semantic IDs
// regardless of the mailbox or the client's view of the folder name.
func distinguishedRole(folderName string) string {
	switch strings.ToUpper(folderName) {
	case "INBOX":
		return "inbox"
	case "DRAFTS", "DRAFT":
		return "drafts"
	case "SENT", "SENT ITEMS", "SENT MAIL":
		return "sent"
	case "TRASH", "DELETED ITEMS", "DELETED":
		return "trash"
	case "JUNK", "SPAM":
		return "junk"
	case "ARCHIVE", "ARCHIVES":
		return "archive"
	case "NOTES":
		return "notes"
	case "OUTBOX":
		return "outbox"
	case "SCHEDULED":
		return "scheduled"
	case "RECOVERABLE ITEMS":
		return "recoverableitems"
	default:
		return ""
	}
}
