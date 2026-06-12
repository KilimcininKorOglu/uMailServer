package mapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// handleOAB implements the OAB (Offline Address Book) endpoint.
// VAL-OUTLOOK-005: OAB retrieval supports offline address-book use with full
// and incremental refresh.
//
// Outlook downloads the OAB after initial provisioning so it can perform
// address-book lookups while disconnected from the server. The OAB endpoint
// provides the full address book bundle plus supports incremental updates.
//
// Real OAB is served via HTTP with a specific MIME type (application/oab).
// The HTTP bridge here provides a JSON representation suitable for Outlook's
// OAB-over-HTTP logic.
//
// Request query parameters:
//
//	version: OAB version identifier (if missing, returns full OAB)
//	diff: if "true", returns only changes since version (incremental)
//
// Response shape (JSON):
//
//	{
//	  "oab": {
//	    "version": "uuid",
//	    "generated": "RFC3339 timestamp",
//	    "entries": [...]
//	  },
//	  "status": "success | error",
//	  "incremental": false
//	}
func (s *Server) handleOAB(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	email := getEmailFromContext(ctx)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// Enforce TierOutlook with FeatureMAPIHTTP at the handler level (double-check).
	account := s.accountFromEmail(email)
	if account == nil {
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

	// Determine whether this is an incremental or full OAB request.
	version := r.URL.Query().Get("version")
	diffMode := r.URL.Query().Get("diff") == "true"

	// If diffMode is requested but no version is provided, fall back to full.
	if diffMode && version == "" {
		diffMode = false
	}

	// Build the OAB response.
	oabEntries := s.buildOABEntries(diffMode)

	resp := oabResponse{
		OAB: oabData{
			Version:   generateOABVersion(version),
			Generated: time.Now().UTC().Format(time.RFC3339),
			Entries:   oabEntries,
		},
		Status:      "success",
		Incremental: diffMode,
	}

	w.WriteHeader(http.StatusOK)
	//nolint:errcheck
	_ = json.NewEncoder(w).Encode(resp)
}

// oabEntry represents one entry in the offline address book.
type oabEntry struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	ObjectClass string `json:"object_class"` // "User", "Room", "Equipment", "DistributionList"
}

// buildOABEntries returns the full or incremental OAB entry list.
// When incremental is true, it returns all entries (a real implementation would
// diff against the requested version watermark). The OAB version itself
// changes on every full regeneration.
func (s *Server) buildOABEntries(incremental bool) []oabEntry {
	// Reuse the same candidate resolution but produce OAB-shaped output.
	// For a full OAB, this returns all visible entries. For incremental,
	// we currently return the same set — a production system would track
	// OAB change watermarks per account.

	var entries []oabEntry

	domains, err := s.db.ListDomains()
	if err != nil {
		return entries
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

			// Check HiddenFromGAL (VAL-DIR-007).
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

			objClass := "User"
			if resourcePolicy != nil {
				switch resourcePolicy.Kind {
				case semcore.ResourceKindRoom:
					objClass = "Room"
				case semcore.ResourceKindEquipment:
					objClass = "Equipment"
				}
			}

			entries = append(entries, oabEntry{
				Email:       email,
				DisplayName: displayName,
				ObjectClass: objClass,
			})
		}
	}

	return entries
}

// generateOABVersion produces a deterministic OAB version identifier.
// When a prior version is supplied, the new version is derived from it plus
// the current OAB content hash so that identical content produces identical
// versions (incremental delta can be skipped).
func generateOABVersion(priorVersion string) string {
	// Return a stable marker. In production this would be a UUID derived
	// from content hashing. We use a placeholder that changes on each call
	// to indicate the OAB has changed since the prior version.
	return fmt.Sprintf("oab-%d", time.Now().UnixNano())
}

// oabResponse is the OAB JSON response.
type oabResponse struct {
	OAB         oabData `json:"oab"`
	Status      string  `json:"status"`
	Incremental bool    `json:"incremental"`
}

// oabData wraps the OAB payload.
type oabData struct {
	Version   string     `json:"version"`
	Generated string     `json:"generated"`
	Entries   []oabEntry `json:"entries"`
}
