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
	"github.com/umailserver/umailserver/internal/metrics"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
	"github.com/umailserver/umailserver/internal/tracing"
	"github.com/umailserver/umailserver/internal/webhook"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/bcrypt"
)

func isAccountLookupMiss(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "key not found: ")
}

func (s *Server) loadLocalAccount(email string) (user, domain string, account *db.AccountData, err error) {
	user, domain = parseEmail(email)
	account, err = s.database.GetAccount(domain, user)
	if err != nil {
		return user, domain, nil, err
	}
	return user, domain, account, nil
}

func validateAccountAuthentication(account *db.AccountData) error {
	if !account.IsActive {
		return fmt.Errorf("account is not active")
	}
	if account.MustChangePassword {
		return fmt.Errorf("password change required")
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
				if err := validateAccountAuthentication(localAccount); err != nil {
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

	if err := validateAccountAuthentication(account); err != nil {
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
	if err := validateAccountAuthentication(localAccount); err != nil {
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
		} else if strings.HasPrefix(action, "addflag:\\Seen") {
			setRead = true
		}
	}

	// If no fileinto targets, use inbox as default (unless discard with no explicit keep)
	if len(targetFolders) == 0 {
		if hasKeep {
			targetFolders = []string{""} // keep overrides discard
		} else if !hasDiscard {
			targetFolders = []string{""} // implicit keep
		}
	} else if hasKeep {
		// copy behavior: keep in inbox AND fileinto target folders
		targetFolders = append(targetFolders, "")
	} else if hasDiscard && !hasKeep {
		// discard: don't add inbox, only deliver to explicit fileinto targets
	}

	s.logger.Info("SieveDeliver", "folders", targetFolders)

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
			if err := s.deliverLocal(redirectUser, redirectDomain, from, dataWithLoop, false); err != nil {
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
			if err := s.deliverLocal(user, domain, from, data, setRead, targetFolder); err != nil {
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
func (s *Server) deliverLocal(user, domain, from string, data []byte, isRead bool, targetFolders ...string) error {
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
				return s.deliverLocal(tUser, tDomain, from, data, isRead, targetFolders...)
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
			meta := &storage.MessageMetadata{
				MessageID:    messageID,
				UID:          uid,
				Flags:        []string{"\\Recent"},
				InternalDate: time.Now(),
				Size:         int64(len(data)),
				Subject:      subject,
				Date:         dateStr,
				From:         fromAddr,
				To:           toAddr,
			}
			if err := s.storageDB.StoreMessageMetadata(email, folder, uid, meta); err != nil {
				s.logger.Error("Failed to store message metadata", "email", email, "uid", uid, "folder", folder, "error", err)
			}

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

	// Send vacation auto-reply if configured
	if account.VacationSettings != "" && s.queue != nil {
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
	return nil
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

	if err := validateAccountAuthentication(localAccount); err != nil {
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
	case "OUTBOX":
		return "outbox"
	default:
		return ""
	}
}
