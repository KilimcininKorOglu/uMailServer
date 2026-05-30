package smtp

import (
	"fmt"
	"net/mail"
	"strings"

	"github.com/umailserver/umailserver/internal/sieve"
)

// VacationHandler is called when a Sieve vacation action is encountered
// Args: sender (original message from), recipient (vacation reply from), vacation action details
type VacationHandler func(sender, recipient string, vacation sieve.VacationAction)

// SieveStage implements Sieve mail filtering in the SMTP pipeline
type SieveStage struct {
	manager         *sieve.Manager
	vacationHandler VacationHandler
}

// NewSieveStage creates a new Sieve filtering stage
func NewSieveStage(manager *sieve.Manager) *SieveStage {
	return &SieveStage{
		manager: manager,
	}
}

// SetVacationHandler sets the callback for Sieve vacation actions
func (s *SieveStage) SetVacationHandler(h VacationHandler) {
	s.vacationHandler = h
}

func (s *SieveStage) Name() string { return "Sieve" }

func (s *SieveStage) Process(ctx *MessageContext) PipelineResult {
	// Get the envelope from
	from := ctx.From
	if from == "" {
		from = "<>"
	}

	// Get the recipients
	to := ctx.To
	if len(to) == 0 {
		return ResultAccept
	}

	// Build Sieve message context
	msg := &sieve.MessageContext{
		From:    from,
		To:      to,
		Headers: ctx.Headers,
		Body:    ctx.Data,
		Size:    int64(len(ctx.Data)),
	}

	// For each recipient, check if they have a sieve script
	for _, recipient := range to {
		// Try multiple user ID variants: the raw recipient, the parsed address,
		// and the local part. This ensures we find scripts stored under any
		// of these identifiers (see ews.sieveUserIDs).
		userIDs := sieveUserIDsForPipeline(recipient)
		if len(userIDs) == 0 {
			continue
		}

		// Check if any user ID has an active script
		var foundUser string
		for _, uid := range userIDs {
			if s.manager.HasActiveScript(uid) {
				foundUser = uid
				break
			}
		}
		if foundUser == "" {
			continue
		}

		// Execute sieve script
		actions, err := s.manager.ProcessMessage(foundUser, msg)
		if err != nil {
			// On error, continue with default action (keep)
			continue
		}

		// Process actions
		for _, action := range actions {
			switch a := action.(type) {
			case sieve.DiscardAction:
				// Silently discard
				return ResultReject
			case sieve.RejectAction:
				// Reject with message
				ctx.Rejected = true
				ctx.RejectionCode = 550
				ctx.RejectionMessage = a.Message
				return ResultReject
			case sieve.FileintoAction:
				// Mark for filing - this will be handled by deliverLocal
				if ctx.SpamResult.Reasons == nil {
					ctx.SpamResult.Reasons = make([]string, 0)
				}
				ctx.SpamResult.Reasons = append(ctx.SpamResult.Reasons, fmt.Sprintf("fileinto:%s", a.Folder))
			case sieve.RedirectAction:
				// Mark for redirect - handled by deliverLocal
				if ctx.SpamResult.Reasons == nil {
					ctx.SpamResult.Reasons = make([]string, 0)
				}
				ctx.SpamResult.Reasons = append(ctx.SpamResult.Reasons, fmt.Sprintf("redirect:%s", a.Address))
			case sieve.VacationAction:
				// Call vacation handler if set (for async vacation reply)
				// CheckAndRecordVacation is atomic to prevent race conditions
				if s.vacationHandler != nil && s.manager.CheckAndRecordVacation(from, a.Days) {
					// Call handler asynchronously to not block the pipeline
					go s.vacationHandler(from, recipient, a)
				}
			case sieve.AddHeaderAction:
				// Inject the header into the stored message so the delivered
				// copy carries it (e.g. X-Category from an assign-categories rule).
				ctx.Data = injectHeader(ctx.Data, a.Name, a.Value)
				if ctx.Headers == nil {
					ctx.Headers = make(map[string][]string)
				}
				ctx.Headers[a.Name] = append(ctx.Headers[a.Name], a.Value)
			case sieve.StopAction:
				// Stop processing
				return ResultAccept
			case sieve.KeepAction:
				// Keep in inbox: add a marker so deliverMessageWithSieve
				// knows to also deliver to INBOX even when other fileinto targets exist.
				if ctx.SpamResult.Reasons == nil {
					ctx.SpamResult.Reasons = make([]string, 0)
				}
				ctx.SpamResult.Reasons = append(ctx.SpamResult.Reasons, "keep")
			}
		}
	}

	return ResultAccept
}

// injectHeader prepends an RFC 5322 header field to a raw message. The new
// field is inserted at the top of the header block, which is valid since
// header order is not significant. CRLF line endings are used to match SMTP.
func injectHeader(data []byte, name, value string) []byte {
	line := []byte(name + ": " + value + "\r\n")
	if len(data) == 0 {
		return line
	}
	return append(line, data...)
}

// extractUserFromRecipient extracts the local part from an email address
func extractUserFromRecipient(recipient string) string {
	if addr, err := mail.ParseAddress(recipient); err == nil && addr.Address != "" {
		recipient = addr.Address
	}
	recipient = strings.Trim(recipient, "<>")

	// Remove any routing prefix
	if idx := strings.Index(recipient, "@"); idx > 0 {
		return recipient[:idx]
	}
	// Handle postmaster or other special addresses
	if recipient == "" {
		return ""
	}
	// Check for localhost style
	if idx := strings.Index(recipient, "!"); idx > 0 {
		return recipient[:idx]
	}
	return recipient
}

// sieveUserIDsForPipeline returns all possible sieve user IDs for a recipient.
// This mirrors ews.sieveUserIDs but is defined here to avoid a cross-package
// dependency from smtp to ews.
func sieveUserIDsForPipeline(recipient string) []string {
	// Normalize the address first
	if addr, err := mail.ParseAddress(recipient); err == nil && addr.Address != "" {
		recipient = addr.Address
	}
	recipient = strings.Trim(recipient, "<>")

	ids := []string{recipient}
	if localPart, _, ok := strings.Cut(recipient, "@"); ok && localPart != "" && localPart != recipient {
		ids = append(ids, localPart)
	}
	return ids
}
