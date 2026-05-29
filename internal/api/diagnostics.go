package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// DiagnosticEntry represents a single diagnostic or error entry
type DiagnosticEntry struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"` // error, warning, info
	Category  string `json:"category"` // policy, sync, delivery, auth, access
	Message   string `json:"message"`
	Mailbox   string `json:"mailbox,omitempty"`
	Timestamp string `json:"timestamp"`
	Retryable bool   `json:"retryable"`
	NextStep  string `json:"nextStep,omitempty"`
}

// handleMailDiagnostics returns diagnostic information about mail operations
// GET /api/v1/mail/diagnostics
func (s *Server) handleMailDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Check for specific mailbox query parameter
	mailbox := r.URL.Query().Get("mailbox")

	// Build diagnostics based on current state
	entries := s.buildMailDiagnostics(user, mailbox)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"errors": entries}); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// buildMailDiagnostics generates diagnostic entries for a user's mailbox
func (s *Server) buildMailDiagnostics(user, mailbox string) []DiagnosticEntry {
	var entries []DiagnosticEntry
	now := time.Now()

	// Check MustChangePassword status - this blocks all mail operations
	if s.db != nil {
		// Extract domain and local part from email
		parts := strings.SplitN(user, "@", 2)
		if len(parts) == 2 {
			account, err := s.db.GetAccount(parts[1], parts[0])
			if err == nil && account != nil {
				if account.MustChangePassword {
					entries = append(entries, DiagnosticEntry{
						ID:        "auth-must-change-password",
						Severity:  "error",
						Category:  "auth",
						Message:   "Account requires password change before mail operations",
						Timestamp: now.Format(time.RFC3339),
						Retryable: false,
						NextStep:  "Change your password in the account settings",
					})
				}
				if !account.IsActive {
					entries = append(entries, DiagnosticEntry{
						ID:        "auth-account-inactive",
						Severity:  "error",
						Category:  "auth",
						Message:   "Account is inactive and cannot perform mail operations",
						Timestamp: now.Format(time.RFC3339),
						Retryable: false,
						NextStep:  "Contact your administrator to reactivate the account",
					})
				}
			}
		}
	}

	// If specific mailbox requested, check shared mailbox access
	if mailbox != "" && mailbox != user {
		// Check if user has access to this shared mailbox
		hasAccess, err := s.checkSharedMailboxAccess(user, mailbox)
		if err != nil || !hasAccess {
			entries = append(entries, DiagnosticEntry{
				ID:        "access-shared-denied",
				Severity:  "error",
				Category:  "access",
				Message:   "No access to shared mailbox",
				Mailbox:   mailbox,
				Timestamp: now.Format(time.RFC3339),
				Retryable: false,
				NextStep:  "Request access from the mailbox owner or administrator",
			})
		} else {
			// Check what rights user has for send permissions
			rights, err := s.getMailboxRights(user, mailbox)
			if err != nil {
				// Log error but continue with empty rights check
				rights = ""
			}
			if rights != "" && !strings.Contains(rights, "w") {
				// User can read but not write
				entries = append(entries, DiagnosticEntry{
					ID:        "access-shared-readonly",
					Severity:  "info",
					Category:  "access",
					Message:   "Read-only access to shared mailbox. Send not permitted.",
					Mailbox:   mailbox,
					Timestamp: now.Format(time.RFC3339),
					Retryable: false,
					NextStep:  "Contact owner for write access if needed",
				})
			}
		}
	}

	return entries
}

// checkSharedMailboxAccess checks if user has any ACL access to a shared mailbox
func (s *Server) checkSharedMailboxAccess(user, mailboxOwner string) (bool, error) {
	if s.mailDB == nil {
		return false, nil
	}
	rights, err := s.mailDB.GetACL(mailboxOwner, "INBOX", user)
	return rights > 0, err
}

// getMailboxRights returns the rights string for user's ACL access to a mailbox
func (s *Server) getMailboxRights(user, mailboxOwner string) (string, error) {
	if s.mailDB == nil {
		return "", nil
	}
	rights, err := s.mailDB.GetACL(mailboxOwner, "INBOX", user)
	if err != nil {
		return "", err
	}
	return rights.String(), nil
}
