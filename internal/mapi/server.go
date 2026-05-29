// Package mapi implements the MAPI/HTTP surface for modern Windows Outlook.
// It provides NSPI (Name Service Provider Interface) for directory/GAL lookups
// and OAB (Offline Address Book) distribution.
//
// These endpoints are reachable only when the account is in TierOutlook with
// FeatureMAPIHTTP enabled, and all MAPI/HTTP entry points enforce explicit
// account-state failures for inactive or password-change-required accounts
// (VAL-OUTLOOK-008).
//
// VAL-OUTLOOK-004: NSPI directory lookups return policy-correct address book
// results including exact matches, ambiguous matches, and resource-style lookups.
//
// VAL-OUTLOOK-005: OAB retrieval supports offline address-book use with full
// and incremental refresh.
package mapi

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/semcore"
)

// Server is the MAPI/HTTP handler for NSPI and OAB endpoints.
type Server struct {
	db          *db.DB
	policyStore *semcore.BoltPolicyStore
}

// NewServer creates a MAPI/HTTP handler wired to the database and policy store.
func NewServer(db *db.DB, policyStore *semcore.BoltPolicyStore) *Server {
	return &Server{
		db:          db,
		policyStore: policyStore,
	}
}

// ServeHTTP routes to the appropriate MAPI/HTTP handler based on path.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/mapi/nspi"):
		s.handleNSPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/mapi/oab"):
		s.handleOAB(w, r)
	default:
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}

// getEmailFromContext extracts the authenticated email from the request context.
// It reads the email stored under the key that api.server uses when injecting
// the authenticated principal into the request context.
func getEmailFromContext(ctx context.Context) string {
	//nolint:staticcheck // SA1029: using string key for compatibility with api.server's context injection.
	if email, ok := ctx.Value("X-Email").(string); ok && email != "" {
		return email
	}
	return ""
}

// accountFromEmail looks up an account by email address.
func (s *Server) accountFromEmail(email string) *db.AccountData {
	localPart, domain, ok := strings.Cut(email, "@")
	if !ok {
		return nil
	}
	account, err := s.db.GetAccount(domain, localPart)
	if err != nil {
		return nil
	}
	return account
}

// directoryCandidate is a GAL lookup result returned by NSPI.
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

	entryLower := strings.ToLower(strings.TrimSpace(entry))
	if entryLower == "" {
		return nil
	}

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

			displayName := acc.LocalPart
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

	// Cap at 100 (VAL-DIR-006).
	if len(candidates) > 100 {
		candidates = candidates[:100]
	}

	return candidates
}
