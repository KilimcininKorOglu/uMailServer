// Package mapi provides the policy-filtered Global Address List that the binary
// MAPI/HTTP address-book surfaces share.
//
// The NSPI address book (internal/mapi/nspi) and the Offline Address Book
// (internal/mapi/oab) both read this one source, so every Outlook directory
// surface agrees on the same visible recipients (HiddenFromGAL filtering,
// VAL-DIR-007) and the same EX identities.
package mapi

import (
	"sort"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Server is the policy-filtered Global Address List source for the binary
// MAPI/HTTP address-book surfaces (NSPI and OAB).
type Server struct {
	db          Store
	policyStore PolicyStore
}

// NewServer creates a GAL source wired to the account database and policy store.
func NewServer(db Store, policyStore PolicyStore) *Server {
	return &Server{
		db:          db,
		policyStore: policyStore,
	}
}

// directoryCandidate is a GAL lookup result.
type directoryCandidate struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	ObjectClass string `json:"object_class"` // "User", "Room", "Equipment", "DistributionList", "Contact"
}

// resolveCandidates searches the account database for GAL matches.
// It respects GAL visibility policy (VAL-DIR-007) by filtering hidden accounts
// and resources, and returns at most 100 results ranked by exact > alias > partial.
//
// Object class correctness satisfies VAL-DIR-015.
func (s *Server) resolveCandidates(entry string) []directoryCandidate {
	if s.db == nil {
		return nil
	}

	// An empty entry is the GetGAL case: every visible directory entry matches
	// (HasPrefix against "" is always true below), yielding the full GAL capped
	// at 100. Returning nil here made NSPI GetGAL hand Outlook an empty address
	// book. ResolveNames with an empty entry is separately collapsed to no-match
	// by the NSPI handler, so this does not leak the GAL into a name lookup.
	entryLower := strings.ToLower(strings.TrimSpace(entry))

	var candidates []directoryCandidate

	domains, err := s.db.ListDomains()
	if err != nil {
		return nil
	}

	for _, domain := range domains {
		if !domain.IsActive {
			continue
		}

		accounts, err := s.db.ListAccountsByDomain(domain.Name)
		if err != nil {
			continue
		}

		for _, acc := range accounts {
			if !acc.IsActive {
				continue
			}

			// Check HiddenFromGAL policy (VAL-DIR-007).
			//nolint:errcheck
			resourcePolicy, _ := s.policyStore.GetResource(semcore.MustResourceId(acc.Email))
			if resourcePolicy != nil && resourcePolicy.HiddenFromGAL {
				continue
			}

			email := acc.Email
			if email == "" {
				email = acc.LocalPart + "@" + acc.Domain
			}

			displayName := acc.DisplayName
			if displayName == "" {
				displayName = acc.LocalPart
			}
			if displayName == "" {
				displayName = email
			}

			// Determine object class (VAL-DIR-015).
			objClass := "User"
			if resourcePolicy != nil {
				switch resourcePolicy.Kind {
				case semcore.ResourceKindRoom:
					objClass = "Room"
				case semcore.ResourceKindEquipment:
					objClass = "Equipment"
				}
			}

			// Match: exact, alias (local part), prefix, contains.
			emailLower := strings.ToLower(email)
			displayLower := strings.ToLower(displayName)

			matched := emailLower == entryLower ||
				displayLower == entryLower ||
				strings.HasPrefix(emailLower, entryLower) ||
				strings.HasPrefix(displayLower, entryLower) ||
				strings.Contains(emailLower, entryLower) ||
				strings.Contains(displayLower, entryLower) ||
				(strings.HasPrefix(entryLower, "@") && strings.Contains(emailLower, entryLower))

			if !matched {
				continue
			}

			candidates = append(candidates, directoryCandidate{
				Email:       email,
				DisplayName: displayName,
				ObjectClass: objClass,
			})
		}
	}

	// Stable sort by email for determinism.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Email < candidates[j].Email
	})

	// Cap search results at 100 (VAL-DIR-006). The empty-entry full-GAL
	// enumeration the binary address book and the OAB read is the complete
	// directory, not a search result, so it is never capped — capping it shipped
	// a truncated address book and a truncated offline address book to any
	// organization with more than 100 visible recipients.
	if entryLower != "" && len(candidates) > 100 {
		candidates = candidates[:100]
	}

	return candidates
}

// GALEntry is one Global Address List entry exposed to the binary address-book
// (NSPI) surface. It mirrors the directory data the JSON ResolveNames/GetGAL
// responses carry, sourced from the same policy-filtered account search so every
// MAPI/HTTP address-book surface agrees on one source.
type GALEntry struct {
	Email       string
	DisplayName string
	ObjectClass string // "User", "Room", "Equipment", "DistributionList", "Contact"
}

// ResolveGAL returns the Global Address List entries matching entry, or the full
// GAL when entry is empty. It applies the same GAL visibility policy
// (HiddenFromGAL, VAL-DIR-007) and 100-entry cap (VAL-DIR-006) the JSON NSPI
// surface enforced, exposed to the binary NSPI endpoint.
func (s *Server) ResolveGAL(entry string) []GALEntry {
	candidates := s.resolveCandidates(entry)
	if candidates == nil {
		return nil
	}
	out := make([]GALEntry, len(candidates))
	for i, c := range candidates {
		out[i] = GALEntry(c)
	}
	return out
}

// GALSequence returns a monotonic version number for the Global Address List —
// the OAB sequence number. It is the latest account modification time across the
// active directory, folded to 31 bits, so it advances whenever an account is
// added or changed and stays stable otherwise. Deleting the most recently
// modified account can lower it; that still changes the value, so Outlook
// re-downloads the book.
func (s *Server) GALSequence() uint32 {
	if s.db == nil {
		return 0
	}
	domains, err := s.db.ListDomains()
	if err != nil {
		return 0
	}
	var latest time.Time
	for _, domain := range domains {
		if !domain.IsActive {
			continue
		}
		accounts, err := s.db.ListAccountsByDomain(domain.Name)
		if err != nil {
			continue
		}
		for _, acc := range accounts {
			if acc.IsActive && acc.UpdatedAt.After(latest) {
				latest = acc.UpdatedAt
			}
		}
	}
	if latest.IsZero() {
		return 0
	}
	return uint32(latest.Unix()) & 0x7FFFFFFF
}
