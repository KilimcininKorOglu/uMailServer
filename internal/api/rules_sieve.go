package api

import (
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
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
// canonical policy state (inbox rules + OOF) and installs it in the Sieve
// manager, so changes made through the webmail filter endpoints take effect at
// delivery. It mirrors the EWS recompile path (internal/ews/rules.go). It is a
// no-op when the Sieve manager or the canonical store is absent.
func (s *Server) recompileSieveForMailbox(mbid semcore.MailboxId) error {
	if s.sieveManager == nil || s.semStore == nil {
		return nil
	}

	rules, err := s.semStore.Policy().ListRules(mbid)
	if err != nil {
		return err
	}

	var oofPolicy *semcore.OOFPolicy
	if oofID, err := semcore.NewOOFId(mbid.String()); err == nil {
		oofPolicy, _ = s.semStore.Policy().GetOOF(oofID) //nolint:errcheck // absent OOF is fine
	}

	script := semcore.CompilePolicyToSieve(rules, oofPolicy)
	for _, userID := range sieveUserIDs(mbid.String()) {
		if err := s.sieveManager.StoreScript(userID, "active", script); err != nil {
			return err
		}
		if err := s.sieveManager.SetActiveScriptByName(userID, "active"); err != nil {
			return err
		}
	}
	return nil
}
