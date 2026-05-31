package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/storage"
)

// Mail represents an email message
type Mail struct {
	ID             string   `json:"id"`
	From           string   `json:"from"`
	FromName       string   `json:"fromName"`
	To             []string `json:"to"`
	Subject        string   `json:"subject"`
	Body           string   `json:"body"`
	Preview        string   `json:"preview"`
	Date           string   `json:"date"`
	Read           bool     `json:"read"`
	Starred        bool     `json:"starred"`
	Folder         string   `json:"folder"`
	HasAttachments bool     `json:"hasAttachments"`
	Size           int64    `json:"size"`
}

// SendMailRequest represents a request to send an email
type SendMailRequest struct {
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	BCC     []string `json:"bcc"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
	From    string   `json:"from,omitempty"` // Sender identity for send-as or send-on-behalf
}

// MailHandler handles mail-related API requests
type MailHandler struct {
	msgStore *storage.MessageStore
	mailDB   *storage.Database
}

// NewMailHandler creates a new mail handler
func NewMailHandler() *MailHandler {
	return &MailHandler{}
}

// SetStorage sets the storage backends for mail operations
func (h *MailHandler) SetStorage(msgStore *storage.MessageStore, mailDB *storage.Database) {
	h.msgStore = msgStore
	h.mailDB = mailDB
}

// sendError sends a JSON error response
func (h *MailHandler) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		// Best-effort - headers already sent, log error
		fmt.Printf("ERROR: failed to encode error response: %v\n", err)
	}
}

// folderMap maps webmail folder names to internal mailbox names
var folderMap = map[string]string{
	"inbox":   "INBOX",
	"sent":    "Sent",
	"drafts":  "Drafts",
	"trash":   "Trash",
	"spam":    "Junk",
	"archive": "Archive",
}

// reverseFolderMap maps internal mailbox names to webmail folder names
var reverseFolderMap = map[string]string{
	"INBOX":  "Inbox",
	"Sent":   "Sent",
	"Drafts": "Drafts",
	"Trash":  "Trash",
	"Junk":   "Spam",
}

// handleMailList lists emails in a folder
// handleMailList returns emails in a mailbox folder
//
//	@Summary List emails in folder
//	@Description Returns a list of emails from the specified mailbox folder
//	@Tags Mail
//	@Produce json
//	@Security BearerAuth
//	@Param folder query string false "Folder name (default INBOX)"
//	@Success 200 {array} Mail "List of emails"
//	@Failure 401 {object} map[string]interface{} "Unauthorized"
//	@Router /api/v1/mail [get]
func (h *MailHandler) handleMailList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Get user from context (set by auth middleware)
	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// Parse folder from the query, falling back to the URL path segment
	// (routes are path-based: /api/v1/mail/<folder>). Without this the path
	// is ignored and every folder resolves to INBOX.
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = strings.TrimPrefix(r.URL.Path, "/api/v1/mail/")
	}
	if folder == "" {
		folder = "INBOX"
	}

	// Map folder name to internal folder
	internalFolder := folderMap[strings.ToLower(folder)]
	if internalFolder == "" {
		internalFolder = folder
	}

	emails, err := h.getEmailsFromStorage(userEmail, internalFolder)
	if err != nil {
		// If storage not available, return empty list
		emails = []Mail{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"emails": emails,
		"total":  len(emails),
		"folder": folder,
	}); err != nil {
		fmt.Printf("ERROR: failed to encode mail list response: %v\n", err)
	}
}

// getEmailsFromStorage retrieves emails from real storage
func (h *MailHandler) getEmailsFromStorage(userEmail, mailbox string) ([]Mail, error) {
	if h.mailDB == nil || h.msgStore == nil {
		return []Mail{}, nil
	}

	// Ensure mailbox exists - try to get it
	_, err := h.mailDB.GetMailbox(userEmail, mailbox)
	if err != nil {
		// Create mailbox if it doesn't exist (only for INBOX)
		if mailbox == "INBOX" {
			_ = h.mailDB.CreateMailbox(userEmail, mailbox) // Best-effort
		}
		return []Mail{}, nil
	}

	// Get message UIDs
	uids, err := h.mailDB.GetMessageUIDs(userEmail, mailbox)
	if err != nil {
		return []Mail{}, nil
	}

	emails := make([]Mail, 0, len(uids))
	for _, uid := range uids {
		meta, err := h.mailDB.GetMessageMetadata(userEmail, mailbox, uid)
		if err != nil {
			continue
		}

		// Read message body
		var body string
		if h.msgStore != nil {
			data, err := h.msgStore.ReadMessage(userEmail, meta.MessageID)
			if err == nil {
				body = h.extractBody(string(data))
			}
		}

		// Determine preview
		preview := body
		if len(preview) > 100 {
			preview = preview[:100] + "..."
		}

		// Map folder name for response
		folderName := reverseFolderMap[mailbox]
		if folderName == "" {
			folderName = mailbox
		}

		email := Mail{
			ID:      meta.MessageID,
			From:    meta.From,
			To:      strings.Split(meta.To, ","),
			Subject: meta.Subject,
			Body:    body,
			Preview: preview,
			Date:    meta.InternalDate.Format(time.RFC1123Z),
			Read:    hasFlag(meta.Flags, "\\Seen"),
			Starred: hasFlag(meta.Flags, "\\Flagged"),
			Folder:  folderName,
			Size:    meta.Size,
		}
		emails = append(emails, email)
	}

	return emails, nil
}

// extractBody extracts the body from a raw email message
func (h *MailHandler) extractBody(raw string) string {
	// Find the header/body separator
	sep := "\r\n\r\n"
	idx := strings.Index(raw, sep)
	if idx == -1 {
		sep = "\n\n"
		idx = strings.Index(raw, sep)
	}
	if idx == -1 {
		return raw
	}
	return strings.TrimSpace(raw[idx+len(sep):])
}

// hasFlag checks if a flag is present
func hasFlag(flags []string, flag string) bool {
	for _, f := range flags {
		if f == flag || f == strings.ToLower(flag) {
			return true
		}
	}
	return false
}

// handleMailGet gets a single email
func (h *MailHandler) handleMailGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	emailID := r.URL.Query().Get("id")

	// Build the list of mailboxes to search. If the caller named a folder, try
	// it first; otherwise (and as a fallback) search every mailbox, because the
	// message list views navigate to a message by id without folder context.
	search := allMailboxes
	if folder := r.URL.Query().Get("folder"); folder != "" {
		internalFolder := folderMap[strings.ToLower(folder)]
		if internalFolder == "" {
			internalFolder = folder
		}
		search = append([]string{internalFolder}, allMailboxes...)
	}

	for _, mailbox := range search {
		email, err := h.getEmailFromStorage(userEmail, mailbox, emailID)
		if err != nil || email == nil {
			continue
		}
		// Mark as read in the mailbox where it was found.
		h.markAsRead(userEmail, mailbox, emailID)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(email); err != nil {
			fmt.Printf("ERROR: failed to encode email response: %v\n", err)
		}
		return
	}

	h.sendError(w, http.StatusNotFound, "Email not found")
}

// allMailboxes is the set of mailboxes searched when resolving a message by id.
var allMailboxes = []string{"INBOX", "Sent", "Drafts", "Trash", "Junk", "Archive"}

// getEmailFromStorage retrieves a single email from storage
func (h *MailHandler) getEmailFromStorage(userEmail, mailbox, messageID string) (*Mail, error) {
	if h.mailDB == nil || h.msgStore == nil {
		return nil, fmt.Errorf("storage not available")
	}

	uids, err := h.mailDB.GetMessageUIDs(userEmail, mailbox)
	if err != nil {
		return nil, err
	}

	for _, uid := range uids {
		meta, err := h.mailDB.GetMessageMetadata(userEmail, mailbox, uid)
		if err != nil {
			continue
		}

		if meta.MessageID == messageID {
			// Read message body (strip headers so the client shows the body,
			// not the raw MIME message).
			var body string
			data, err := h.msgStore.ReadMessage(userEmail, meta.MessageID)
			if err == nil {
				body = h.extractBody(string(data))
			}

			folderName := reverseFolderMap[mailbox]
			if folderName == "" {
				folderName = mailbox
			}

			return &Mail{
				ID:      meta.MessageID,
				From:    meta.From,
				To:      strings.Split(meta.To, ","),
				Subject: meta.Subject,
				Body:    body,
				Preview: body,
				Date:    meta.InternalDate.Format(time.RFC1123Z),
				Read:    hasFlag(meta.Flags, "\\Seen"),
				Starred: hasFlag(meta.Flags, "\\Flagged"),
				Folder:  folderName,
				Size:    meta.Size,
			}, nil
		}
	}

	return nil, fmt.Errorf("email not found")
}

// markAsRead marks a message as read
func (h *MailHandler) markAsRead(userEmail, mailbox, messageID string) {
	if h.mailDB == nil {
		return
	}

	uids, _ := h.mailDB.GetMessageUIDs(userEmail, mailbox)
	for _, uid := range uids {
		meta, err := h.mailDB.GetMessageMetadata(userEmail, mailbox, uid)
		if err != nil {
			continue
		}

		if meta.MessageID == messageID {
			if !hasFlag(meta.Flags, "\\Seen") {
				meta.Flags = append(meta.Flags, "\\Seen")
				_ = h.mailDB.UpdateMessageMetadata(userEmail, mailbox, uid, meta)
			}
			break
		}
	}
}

// sanitizeHeaderValue removes CR/LF characters to prevent SMTP header injection.
func sanitizeHeaderValue(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", "")
}

// sanitizeHeaderValues applies sanitizeHeaderValue to each element in a slice.
func sanitizeHeaderValues(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = sanitizeHeaderValue(v)
	}
	return out
}

// handleMailSend sends an email and stores it in Sent folder
//
//	@Summary Send email
//	@Description Sends an email and stores it in the Sent folder
//	@Tags Mail
//	@Accept json
//	@Produce json
//	@Security BearerAuth
//	@Param email body map[string]interface{} true "Email data"
//	@Success 200 {object} map[string]interface{} "Email sent"
//	@Failure 401 {object} map[string]interface{} "Unauthorized"
//	@Router /api/v1/mail/send [post]
func (h *MailHandler) handleMailSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req SendMailRequest
	if err := decodeJSON(r, &req); err != nil {
		h.sendError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	if len(req.To) == 0 {
		h.sendError(w, http.StatusBadRequest, "Recipient required")
		return
	}

	// Validate recipient count
	if len(req.To) > 100 {
		h.sendError(w, http.StatusBadRequest, "Too many recipients (max 100)")
		return
	}

	// Validate subject length
	if len(req.Subject) > 998 {
		h.sendError(w, http.StatusBadRequest, "Subject too long (max 998 characters)")
		return
	}

	// Validate body length (prevent memory issues)
	if len(req.Body) > 25*1024*1024 {
		h.sendError(w, http.StatusBadRequest, "Message body too large (max 25MB)")
		return
	}

	// Determine the sender identity
	// If req.From is provided and differs from user's own email, validate send-as permission
	senderEmail := userEmail
	if req.From != "" && req.From != userEmail {
		// User wants to send as a different identity - validate permission
		canSend, authErr := h.validateSendAs(userEmail, req.From)
		if authErr != nil {
			h.sendError(w, http.StatusForbidden, authErr.Error())
			return
		}
		if !canSend {
			h.sendError(w, http.StatusForbidden, "You don't have permission to send as this identity. Request send-as or send-on-behalf access from the mailbox owner.")
			return
		}
		senderEmail = req.From
	}

	// Build RFC 2822 email
	now := time.Now()
	dateStr := now.Format("Mon, 02 Jan 2006 15:04:05 -0700")

	// Sanitize header values to prevent CRLF injection
	safeSubject := sanitizeHeaderValue(req.Subject)
	safeTo := sanitizeHeaderValues(req.To)
	safeCC := sanitizeHeaderValues(req.CC)

	// Build headers
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", senderEmail)
	sb.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(safeTo, ", ")))
	if len(safeCC) > 0 {
		sb.WriteString(fmt.Sprintf("Cc: %s\r\n", strings.Join(safeCC, ", ")))
	}
	sb.WriteString(fmt.Sprintf("Subject: %s\r\n", safeSubject))
	sb.WriteString(fmt.Sprintf("Date: %s\r\n", dateStr))
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(req.Body)

	rawEmail := sb.String()

	// Store the message
	var msgID string
	if h.msgStore != nil && h.mailDB != nil {
		// Ensure Sent mailbox exists
		_ = h.mailDB.CreateMailbox(userEmail, "Sent")

		// Store message file - msgID is the hash-based ID returned by StoreMessage
		storedMsgID, err := h.msgStore.StoreMessage(userEmail, []byte(rawEmail))
		if err != nil {
			h.sendError(w, http.StatusInternalServerError, "Failed to store message")
			return
		}
		if storedMsgID == "" {
			h.sendError(w, http.StatusInternalServerError, "Failed to store message: no ID returned")
			return
		}
		msgID = storedMsgID

		// Get next UID for Sent mailbox
		uid, err := h.mailDB.GetNextUID(userEmail, "Sent")
		if err != nil {
			h.sendError(w, http.StatusInternalServerError, "Failed to get next UID")
			return
		}

		// Parse headers for metadata
		subject := safeSubject
		from := userEmail
		to := strings.Join(safeTo, ", ")

		// Store metadata with the hash-based message ID
		meta := &storage.MessageMetadata{
			MessageID:    msgID,
			UID:          uid,
			Flags:        []string{"\\Seen"},
			InternalDate: now,
			Size:         int64(len(rawEmail)),
			Subject:      subject,
			Date:         dateStr,
			From:         from,
			To:           to,
		}
		if err := h.mailDB.StoreMessageMetadata(userEmail, "Sent", uid, meta); err != nil {
			fmt.Printf("ERROR: failed to store message metadata: %v\n", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{
		"message": "Email sent successfully",
		"id":      msgID,
	}); err != nil {
		fmt.Printf("ERROR: failed to encode send confirmation: %v\n", err)
	}
}

// handleMailDelete deletes an email (moves to trash)
//
//	@Summary Delete email
//	@Description Moves an email to the Trash folder
//	@Tags Mail
//	@Produce json
//	@Security BearerAuth
//	@Param id query string true "Message ID"
//	@Success 200 {object} map[string]string "Email deleted"
//	@Failure 401 {object} map[string]interface{} "Unauthorized"
//	@Router /api/v1/mail/delete [delete]
func (h *MailHandler) handleMailDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	userVal := r.Context().Value("user")
	if userVal == nil {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	userEmail := userVal.(string)

	// Extract message ID from query string or request body
	var messageID string

	// Try query string first (DELETE /api/v1/mail/delete?id=xxx)
	messageID = r.URL.Query().Get("id")

	// If not in query, try form/body
	if messageID == "" {
		if r.FormValue("id") != "" {
			messageID = r.FormValue("id")
		}
	}

	if messageID == "" {
		h.sendError(w, http.StatusBadRequest, "Message ID required")
		return
	}

	// A first delete moves the message to Trash; deleting again from Trash (or
	// passing permanent=true, as the Trash view does) purges it for good. The
	// message file is shared across mailboxes, so a soft delete only moves
	// metadata and the file is removed only on a permanent delete.
	permanent := r.URL.Query().Get("permanent") == "true"
	mailbox, uid, meta, found := h.findMessage(userEmail, messageID)

	if found && !permanent && mailbox != "Trash" {
		if err := h.moveMessageMetadata(userEmail, mailbox, "Trash", uid, meta); err != nil {
			h.sendError(w, http.StatusInternalServerError, "Failed to move message to trash")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{
			"message": "Email moved to trash",
			"id":      messageID,
		}); err != nil {
			fmt.Printf("ERROR: failed to encode delete confirmation: %v\n", err)
		}
		return
	}

	// Permanent delete: remove the shared message file and its metadata.
	if h.msgStore != nil {
		if err := h.msgStore.DeleteMessage(userEmail, messageID); err != nil {
			// Log but don't fail - message might already be deleted
			fmt.Printf("Warning: failed to delete message file: %v\n", err)
		}
	}
	if h.mailDB != nil {
		h.deleteMessageMetadata(userEmail, messageID)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{
		"message": "Email deleted",
		"id":      messageID,
	}); err != nil {
		fmt.Printf("ERROR: failed to encode delete confirmation: %v\n", err)
	}
}

// findMessage locates a message by id across the user's mailboxes, returning the
// mailbox it lives in along with its UID and metadata.
func (h *MailHandler) findMessage(userEmail, messageID string) (string, uint32, *storage.MessageMetadata, bool) {
	if h.mailDB == nil {
		return "", 0, nil, false
	}
	for _, mailbox := range allMailboxes {
		uids, err := h.mailDB.GetMessageUIDs(userEmail, mailbox)
		if err != nil {
			continue
		}
		for _, uid := range uids {
			meta, err := h.mailDB.GetMessageMetadata(userEmail, mailbox, uid)
			if err != nil {
				continue
			}
			if meta.MessageID == messageID {
				return mailbox, uid, meta, true
			}
		}
	}
	return "", 0, nil, false
}

// moveMessageMetadata moves a message from src to dst by writing its metadata
// under a fresh UID in dst and removing it from src. The message file is keyed
// by user+id and shared across mailboxes, so it is intentionally left in place.
func (h *MailHandler) moveMessageMetadata(userEmail, src, dst string, srcUID uint32, meta *storage.MessageMetadata) error {
	if src == dst || meta == nil {
		return nil
	}
	if err := h.mailDB.CreateMailbox(userEmail, dst); err != nil {
		return fmt.Errorf("ensure mailbox %s: %w", dst, err)
	}
	newUID, err := h.mailDB.GetNextUID(userEmail, dst)
	if err != nil {
		return fmt.Errorf("next uid for %s: %w", dst, err)
	}
	newMeta := *meta
	newMeta.UID = newUID
	if err := h.mailDB.StoreMessageMetadata(userEmail, dst, newUID, &newMeta); err != nil {
		return fmt.Errorf("store metadata in %s: %w", dst, err)
	}
	return h.mailDB.DeleteMessage(userEmail, src, srcUID)
}

// flagRequest sets or clears an IMAP flag on a message.
type flagRequest struct {
	ID    string `json:"id"`
	Flag  string `json:"flag"`
	Value bool   `json:"value"`
}

// allowedFlags is the set of IMAP flags the web client is permitted to toggle.
var allowedFlags = map[string]bool{
	"\\Seen":    true,
	"\\Flagged": true,
}

// handleMailFlag sets or clears a single IMAP flag (read/unread via \Seen,
// star via \Flagged) on a message so those states persist server-side.
func (h *MailHandler) handleMailFlag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req flagRequest
	if err := decodeJSON(r, &req); err != nil {
		h.sendError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.ID == "" {
		h.sendError(w, http.StatusBadRequest, "Message ID required")
		return
	}
	if !allowedFlags[req.Flag] {
		h.sendError(w, http.StatusBadRequest, "Unsupported flag")
		return
	}
	if h.mailDB == nil {
		h.sendError(w, http.StatusInternalServerError, "Storage not available")
		return
	}

	mailbox, uid, meta, found := h.findMessage(userEmail, req.ID)
	if !found {
		h.sendError(w, http.StatusNotFound, "Email not found")
		return
	}

	has := hasFlag(meta.Flags, req.Flag)
	switch {
	case req.Value && !has:
		meta.Flags = append(meta.Flags, req.Flag)
	case !req.Value && has:
		meta.Flags = storage.RemoveFlag(meta.Flags, req.Flag)
	}

	if err := h.mailDB.UpdateMessageMetadata(userEmail, mailbox, uid, meta); err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to update message")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"id":    req.ID,
		"flag":  req.Flag,
		"value": req.Value,
	}); err != nil {
		fmt.Printf("ERROR: failed to encode flag response: %v\n", err)
	}
}

// moveRequest moves a message to a different folder.
type moveRequest struct {
	ID string `json:"id"`
	To string `json:"to"`
}

// isStandardMailbox reports whether name is one of the built-in mailboxes.
func isStandardMailbox(name string) bool {
	return slices.Contains(allMailboxes, name)
}

// handleMailMove moves a message to another folder (e.g. Trash -> INBOX to
// restore, or any folder -> Archive). It only metadata-moves; the shared
// message file is left untouched.
func (h *MailHandler) handleMailMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req moveRequest
	if err := decodeJSON(r, &req); err != nil {
		h.sendError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.ID == "" || req.To == "" {
		h.sendError(w, http.StatusBadRequest, "Message ID and target folder required")
		return
	}
	if h.mailDB == nil {
		h.sendError(w, http.StatusInternalServerError, "Storage not available")
		return
	}

	// Resolve the destination mailbox. Allow the standard mailboxes or any
	// folder the user already owns; reject unknown names so move cannot create
	// arbitrary mailboxes.
	dst := folderMap[strings.ToLower(req.To)]
	if dst == "" {
		dst = req.To
	}
	if !isStandardMailbox(dst) {
		// GetMailbox synthesizes a default for missing mailboxes, so check the
		// real mailbox list instead to reject folders the user does not own.
		owned, err := h.mailDB.ListMailboxes(userEmail)
		if err != nil || !slices.Contains(owned, dst) {
			h.sendError(w, http.StatusBadRequest, "Unknown target folder")
			return
		}
	}

	mailbox, uid, meta, found := h.findMessage(userEmail, req.ID)
	if !found {
		h.sendError(w, http.StatusNotFound, "Email not found")
		return
	}

	if mailbox != dst {
		if err := h.moveMessageMetadata(userEmail, mailbox, dst, uid, meta); err != nil {
			h.sendError(w, http.StatusInternalServerError, "Failed to move message")
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{
		"id":     req.ID,
		"folder": dst,
	}); err != nil {
		fmt.Printf("ERROR: failed to encode move response: %v\n", err)
	}
}

// deleteMessageMetadata finds and deletes message metadata by messageID
func (h *MailHandler) deleteMessageMetadata(userEmail, messageID string) {
	mailboxes := []string{"INBOX", "Sent", "Drafts", "Trash", "Junk", "Archive"}
	for _, mailbox := range mailboxes {
		// Get all UIDs in this mailbox
		uids, err := h.mailDB.GetMessageUIDs(userEmail, mailbox)
		if err != nil {
			continue
		}
		// Find the message with matching ID and delete it
		for _, uid := range uids {
			meta, err := h.mailDB.GetMessageMetadata(userEmail, mailbox, uid)
			if err != nil {
				continue
			}
			if meta.MessageID == messageID {
				_ = h.mailDB.DeleteMessage(userEmail, mailbox, uid)
				return
			}
		}
	}
}

// validateSendAs checks if user has permission to send as the specified identity.
// Returns (canSend, error). If error is non-nil, it's an auth error. If canSend is false,
// the user doesn't have send-as or send-on-behalf permission.
func (h *MailHandler) validateSendAs(user, targetIdentity string) (bool, error) {
	// If targeting own identity, always allowed
	if user == targetIdentity {
		return true, nil
	}

	// If mailDB is not available, deny by default
	if h.mailDB == nil {
		return false, fmt.Errorf("mail storage not available")
	}

	// Check if user has ACL write rights on the target mailbox
	// The targetIdentity is the owner's email, and we check ACL on INBOX
	rights, err := h.mailDB.GetACL(targetIdentity, "INBOX", user)
	if err != nil {
		return false, fmt.Errorf("failed to check permissions: %v", err)
	}

	// ACLWrite is bit 3 (value 8) - user needs write rights to send
	// For send-on-behalf, ACLWrite is sufficient
	// For send-as, we would need additional validation (not yet implemented in ACL model)
	if rights&storage.ACLWrite != 0 {
		return true, nil
	}

	return false, nil
}

// InitDemoEmails is a no-op - demo emails are now stored in real storage
// This function is kept for backwards compatibility
func InitDemoEmails(userEmail string) {
	// No-op: demo emails are now in real storage
	// Users receive demo emails when accounts are first created via setup scripts
}
