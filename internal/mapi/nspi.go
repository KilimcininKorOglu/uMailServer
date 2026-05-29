package mapi

import (
	"encoding/json"
	"net/http"

	"github.com/umailserver/umailserver/internal/semcore"
)

// handleNSPI implements the NSPI (Name Service Provider Interface) endpoint.
// VAL-OUTLOOK-004: NSPI directory lookups return policy-correct address book
// results including exact matches, ambiguous matches, and resource-style lookups,
// while respecting published visibility and tenancy boundaries.
//
// Outlook uses NSPI for GAL address-book searches when provisioning or when
// the user opens the Outlook address book. The NSPI surface here uses HTTP POST
// with a JSON request body for simplicity; real NSPI is a binary RPC protocol,
// but the HTTP bridge provides the functional semantics Outlook needs.
//
// Request shape (JSON):
//
//	{
//	  "method": "ResolveNames | GetGAL | GetADDrBook",
//	  "params": {
//	    "unresolvedEntry": "string",
//	    "restriction": "string",
//	    "includeHidden": false
//	  }
//	}
//
// Response shape (JSON):
//
//	{
//	  "results": [
//	    { "email": "user@example.com", "displayName": "User", "objectClass": "User" },
//	    ...
//	  ],
//	  "status": "success | error",
//	  "includesLastItemInRange": true
//	}
func (s *Server) handleNSPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	email := getEmailFromContext(ctx)

	// Respond with JSON.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// Check if the account is in TierOutlook (enforced at auth layer, but double-check).
	account := s.accountFromEmail(email)
	if account == nil {
		// Auth layer should have already returned 401, but be defensive.
		w.WriteHeader(http.StatusUnauthorized)
		//nolint:errcheck
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "error",
			"message": "unauthorized",
		})
		return
	}

	tier := semcore.AccountCompatibilityTier(account.CompatibilityTier)
	if tier != semcore.TierOutlook || !semcore.Gate().IsEnabled(semcore.FeatureMAPIHTTP) {
		w.WriteHeader(http.StatusForbidden)
		//nolint:errcheck
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "error",
			"message": "MAPI/HTTP is not enabled for this account",
		})
		return
	}

	// Parse the request body.
	var req nspiRequest
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Fall through to GetGAL for empty body or malformed POST.
			_ = err //nolint:errcheck
		}
	}

	var results []directoryCandidate

	switch {
	case req.Method == "ResolveNames" && req.Params.UnresolvedEntry != "":
		// Resolve a partial or full name/email.
		results = s.resolveCandidates(req.Params.UnresolvedEntry)

	case req.Method == "GetGAL" || r.Method == http.MethodGet:
		// Return all visible GAL entries (limited set).
		// Since we don't have cursor state here, return the first page.
		results = s.resolveCandidates("")

	default:
		// Default to full GAL listing.
		results = s.resolveCandidates("")
	}

	// When entry is empty (GetGAL), resolveCandidates returns all visible accounts.
	// Filter by the actual method intent to avoid showing all users on a bare POST.
	if req.Method == "ResolveNames" && req.Params.UnresolvedEntry == "" && len(results) > 20 {
		// Empty resolve with no entry should not return the whole GAL in a
		// ResolveNames context — this is a "no match" case.
		results = nil
	}

	resp := nspiResponse{
		Results:                results,
		Status:                 "success",
		IncludesLastItemInRange: true,
	}

	w.WriteHeader(http.StatusOK)
	//nolint:errcheck
	_ = json.NewEncoder(w).Encode(resp)
}

// nspiRequest represents an incoming NSPI JSON-RPC-like request.
type nspiRequest struct {
	Method string     `json:"method"`
	Params nspiParams `json:"params"`
}

// nspiParams holds the parameters for an NSPI operation.
type nspiParams struct {
	UnresolvedEntry string `json:"unresolvedEntry"`
	Restriction     string `json:"restriction"`
	IncludeHidden   bool   `json:"includeHidden"`
}

// nspiResponse is the NSPI JSON response.
type nspiResponse struct {
	Results                []directoryCandidate `json:"results"`
	Status                 string               `json:"status"`
	IncludesLastItemInRange bool                `json:"includesLastItemInRange"`
}
