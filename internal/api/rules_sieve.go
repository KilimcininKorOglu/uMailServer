package api

import (
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/sieve"
)

// sieveUserIDs returns the script-storage keys for a mailbox: the full email
// address and, when present, its local-part. This mirrors the EWS install path
// so the active script is found regardless of which key delivery looks up.
func sieveUserIDs(mailbox string) []string {
	ids := []string{mailbox}
	if localPart, _, ok := strings.Cut(mailbox, "@"); ok && localPart != "" && localPart != mailbox {
		ids = append(ids, localPart)
	}
	return ids
}

// recompileSieveForMailbox rebuilds a mailbox's active Sieve script from its
// canonical policy state (inbox rules + OOF + blocked senders) and installs it
// in the Sieve manager, so changes made through the webmail filter endpoints
// take effect at delivery. It mirrors the EWS recompile path (internal/ews/rules.go).
// It is a no-op when the Sieve manager or the canonical store is absent.
func (s *Server) recompileSieveForMailbox(mbid semcore.MailboxId) error {
	if s.sieveManager == nil || s.semStore == nil {
		return nil
	}

	var oofPolicy *semcore.OOFPolicy
	if oofID, err := semcore.NewOOFId(mbid.String()); err == nil {
		oofPolicy, _ = s.semStore.Policy().GetOOF(oofID) //nolint:errcheck // absent OOF is fine
	}

	// Admin-authored global rules are compiled in ahead of the user's own rules
	// (CompileEffectivePolicy) so they apply org-wide and a user editing their
	// own rules can never drop them.
	script := semcore.CompileEffectivePolicy(s.semStore.Policy(), mbid, oofPolicy)

	// Prepend blocked-sender rejections if any are configured.
	if s.db != nil {
		if blocked, err := s.db.ListBlockedSenders(mbid.String()); err == nil && len(blocked) > 0 {
			addrs := make([]string, len(blocked))
			for i, b := range blocked {
				addrs[i] = b.Address
			}
			script = semcore.CompileBlockedSenders(addrs) + "\n" + script
		}
	}

	for _, userID := range sieveUserIDs(mbid.String()) {
		if err := s.sieveManager.StoreScript(userID, sieve.ManagedScriptName, script); err != nil {
			return err
		}
		if err := s.sieveManager.SetActiveScriptByName(userID, sieve.ManagedScriptName); err != nil {
			return err
		}
		s.sieveManager.CleanupLegacyManagedScript(userID)
	}
	return nil
}
