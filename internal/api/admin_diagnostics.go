package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// mailboxDiagnosticsDTO mirrors the frontend MailboxDiagnostics interface
// (web/admin/src/pages/Diagnostics.tsx).
type mailboxDiagnosticsDTO struct {
	Email               string `json:"email"`
	SyncState           string `json:"syncState"` // healthy | degraded | error
	LastSync            string `json:"lastSync"`
	SubscriptionBacklog int    `json:"subscriptionBacklog"`
	ProtocolFailures    int    `json:"protocolFailures"`
	PolicyBlocks        int    `json:"policyBlocks"`
	OOFActive           bool   `json:"oofActive"`
	RulesCount          int    `json:"rulesCount"`
	TotalFolders        int    `json:"totalFolders"`
	TotalItems          int    `json:"totalItems"`
}

// subscriptionInfoDTO mirrors the frontend SubscriptionInfo interface.
type subscriptionInfoDTO struct {
	ID        string `json:"id"`
	Mailbox   string `json:"mailbox"`
	Type      string `json:"type"`
	Status    string `json:"status"` // active | expiring | expired
	Watermark string `json:"watermark"`
	CreatedAt string `json:"createdAt"`
	LastEvent string `json:"lastEvent"`
}

// handleAdminDiagnostics handles GET /api/v1/admin/diagnostics. It returns a
// per-mailbox health overview, aggregated subscriptions, and recent protocol
// failures. Protocol failures and policy blocks are not tracked per mailbox, so
// they are reported as zero/empty rather than fabricated.
func (s *Server) handleAdminDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	emails := s.allAccountEmails()
	// Precompute rules-per-email from the same source as the Policies page
	// (ListAllRules + MailboxEmailsByID) so the two views never disagree.
	rulesByEmail := s.rulesCountByEmail()

	mailboxes := make([]mailboxDiagnosticsDTO, 0, len(emails))
	subscriptions := make([]subscriptionInfoDTO, 0)
	for _, email := range emails {
		mailboxes = append(mailboxes, s.mailboxDiagnostics(email, rulesByEmail[email]))
		subscriptions = append(subscriptions, s.mailboxSubscriptions(email)...)
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"mailboxes":     mailboxes,
		"subscriptions": subscriptions,
		"failures":      []subscriptionInfoDTO{}, // no per-mailbox protocol-failure log exists
	})
}

// handleAdminDiagnosticsDetail handles GET /api/v1/admin/diagnostics/{email},
// returning the diagnostics for a single mailbox.
func (s *Server) handleAdminDiagnosticsDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	email := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/diagnostics/"))
	if email == "" {
		s.sendError(w, http.StatusBadRequest, "email required")
		return
	}
	s.sendJSON(w, http.StatusOK, s.mailboxDiagnostics(email, s.rulesCountByEmail()[email]))
}

// rulesCountByEmail returns a map of account email -> number of inbox rules,
// derived from the same ListAllRules + MailboxEmailsByID source as the Policies
// page so the two admin views never report different counts.
func (s *Server) rulesCountByEmail() map[string]int {
	out := map[string]int{}
	if s.semStore == nil {
		return out
	}
	rules, err := s.semStore.Policy().ListAllRules()
	if err != nil {
		return out
	}
	emails, err := s.semStore.Identity().MailboxEmailsByID()
	if err != nil {
		return out
	}
	for _, rule := range rules {
		// Resolve the owning email the same way the Policies page does: prefer
		// the identity map, and fall back to the raw MailboxId, which for
		// rules created via some paths is the email address itself.
		email := emails[rule.MailboxID.String()]
		if email == "" {
			email = rule.MailboxID.String()
		}
		out[strings.ToLower(email)]++
	}
	return out
}

// allAccountEmails enumerates every account across all domains.
func (s *Server) allAccountEmails() []string {
	var emails []string
	domains, err := s.db.ListDomains()
	if err != nil {
		return emails
	}
	for _, d := range domains {
		accounts, err := s.db.ListAccountsByDomain(d.Name)
		if err != nil {
			continue
		}
		for _, a := range accounts {
			emails = append(emails, a.Email)
		}
	}
	return emails
}

// mailboxDiagnostics builds a single mailbox's diagnostics. Folder and item
// counts come from the authoritative mail store; rules, OOF state, and
// subscription backlog come from the canonical semcore store.
func (s *Server) mailboxDiagnostics(email string, rulesCount int) mailboxDiagnosticsDTO {
	dto := mailboxDiagnosticsDTO{Email: email, SyncState: "healthy", RulesCount: rulesCount}

	// Folder and item counts from the authoritative mail store.
	if s.mailDB != nil {
		if mailboxes, err := s.mailDB.ListMailboxes(email); err == nil {
			dto.TotalFolders = len(mailboxes)
			for _, mbox := range mailboxes {
				if exists, _, _, err := s.mailDB.GetMailboxCounts(email, mbox); err == nil {
					dto.TotalItems += exists
				}
			}
		}
	}

	// OOF state and subscription backlog keyed by MailboxId.
	if s.semStore != nil {
		mid, err := s.semStore.Identity().GetMailboxIDByEmail(email)
		if err == nil {
			if oofID, err := semcore.NewOOFId(mid.String()); err == nil {
				if oof, err := s.semStore.Policy().GetOOF(oofID); err == nil && oof != nil {
					dto.OOFActive = oof.Enabled
				}
			}
			if subs, err := s.semStore.Subscriptions().ListSubscriptionsByMailbox(mid); err == nil {
				dto.SubscriptionBacklog = len(subs)
			}
		}
	}

	return dto
}

// mailboxSubscriptions returns the active subscriptions for one mailbox mapped
// to the admin-UI shape.
func (s *Server) mailboxSubscriptions(email string) []subscriptionInfoDTO {
	if s.semStore == nil {
		return nil
	}
	mid, err := s.semStore.Identity().GetMailboxIDByEmail(email)
	if err != nil {
		return nil
	}
	subs, err := s.semStore.Subscriptions().ListSubscriptionsByMailbox(mid)
	if err != nil {
		return nil
	}
	now := time.Now()
	out := make([]subscriptionInfoDTO, 0, len(subs))
	for _, sub := range subs {
		status := "active"
		switch {
		case !sub.ExpiresAt.IsZero() && sub.ExpiresAt.Before(now):
			status = "expired"
		case !sub.ExpiresAt.IsZero() && sub.ExpiresAt.Sub(now) < time.Hour:
			status = "expiring"
		}
		expires := ""
		if !sub.ExpiresAt.IsZero() {
			expires = sub.ExpiresAt.UTC().Format(time.RFC3339)
		}
		out = append(out, subscriptionInfoDTO{
			ID:        sub.ID.ID,
			Mailbox:   email,
			Type:      sub.Kind.String(),
			Status:    status,
			Watermark: strconv.FormatUint(sub.LastSeq, 10),
			CreatedAt: expires,
			LastEvent: expires,
		})
	}
	return out
}
