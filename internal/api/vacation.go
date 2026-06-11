package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/vacation"
)

// parseVacationAudience maps the webmail audience selector to the canonical OOF
// audience. An empty value defaults to "all" so a webmail auto-reply replies to
// everyone (preserving the behavior before the internal/external split); "known"
// is folded into external (its contacts-only refinement is not yet enforced).
func parseVacationAudience(s string) semcore.OOFAudience {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "internal":
		return semcore.OOFAudienceInternal
	case "external", "known":
		return semcore.OOFAudienceExternal
	default: // "", "all", "everyone"
		return semcore.OOFAudienceEveryone
	}
}

// vacationAudienceString renders the canonical OOF audience for the webmail API.
func vacationAudienceString(a semcore.OOFAudience) string {
	switch a {
	case semcore.OOFAudienceInternal:
		return "internal"
	case semcore.OOFAudienceExternal:
		return "external"
	default:
		return "all"
	}
}

// VacationConfig represents vacation auto-reply configuration in API
type VacationConfig struct {
	Enabled          bool     `json:"enabled"`
	StartDate        *string  `json:"start_date,omitempty"`
	EndDate          *string  `json:"end_date,omitempty"`
	Subject          string   `json:"subject"`
	Message          string   `json:"message"`
	HTMLMessage      string   `json:"html_message,omitempty"`
	ExternalMessage  string   `json:"external_message,omitempty"` // reply to senders outside the org
	Audience         string   `json:"audience,omitempty"`         // "internal" | "external" | "all"
	SendInterval     int      `json:"send_interval,omitempty"`    // in hours
	ExcludeAddresses []string `json:"exclude_addresses,omitempty"`
	IgnoreLists      bool     `json:"ignore_lists,omitempty"`
	IgnoreBulk       bool     `json:"ignore_bulk,omitempty"`
}

// handleVacation handles GET/PUT /api/v1/vacation
func (s *Server) handleVacation(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetVacation(w, r)
	case http.MethodPut:
		s.handleSetVacation(w, r)
	case http.MethodDelete:
		s.handleDeleteVacation(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleGetVacation gets vacation configuration
func (s *Server) handleGetVacation(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Get vacation manager from server (we need to add this field)
	config, err := s.getVacationConfig(user)
	if err != nil {
		s.logger.Error("Failed to get vacation config", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to get vacation config")
		return
	}

	// Convert to API response
	response := VacationConfig{
		Enabled:          config.Enabled,
		Subject:          config.Subject,
		Message:          config.Message,
		HTMLMessage:      config.HTMLMessage,
		ExternalMessage:  config.ExternalMessage,
		Audience:         config.Audience,
		SendInterval:     int(config.SendInterval.Hours()),
		ExcludeAddresses: config.ExcludeAddresses,
		IgnoreLists:      config.IgnoreLists,
		IgnoreBulk:       config.IgnoreBulk,
	}

	if !config.StartDate.IsZero() {
		startStr := config.StartDate.Format(time.RFC3339)
		response.StartDate = &startStr
	}
	if !config.EndDate.IsZero() {
		endStr := config.EndDate.Format(time.RFC3339)
		response.EndDate = &endStr
	}

	s.sendJSON(w, http.StatusOK, response)
}

// handleSetVacation sets vacation configuration
func (s *Server) handleSetVacation(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Parse request body
	var req VacationConfig
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Convert to internal config
	config := &vacation.Config{
		Enabled:          req.Enabled,
		Subject:          req.Subject,
		Message:          req.Message,
		HTMLMessage:      req.HTMLMessage,
		ExternalMessage:  req.ExternalMessage,
		Audience:         req.Audience,
		SendInterval:     time.Duration(req.SendInterval) * time.Hour,
		ExcludeAddresses: req.ExcludeAddresses,
		IgnoreLists:      req.IgnoreLists,
		IgnoreBulk:       req.IgnoreBulk,
	}

	if req.StartDate != nil {
		if startDate, err := time.Parse(time.RFC3339, *req.StartDate); err == nil {
			config.StartDate = startDate
		}
	}
	if req.EndDate != nil {
		if endDate, err := time.Parse(time.RFC3339, *req.EndDate); err == nil {
			config.EndDate = endDate
		}
	}

	// Validate
	if config.Enabled && config.Subject == "" {
		s.sendError(w, http.StatusBadRequest, "subject is required when vacation is enabled")
		return
	}
	if config.Enabled && config.Message == "" {
		s.sendError(w, http.StatusBadRequest, "message is required when vacation is enabled")
		return
	}

	// Save config
	if err := s.setVacationConfig(user, config); err != nil {
		s.logger.Error("Failed to set vacation config", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to set vacation config")
		return
	}

	s.sendJSON(w, http.StatusOK, map[string]string{
		"status": "success",
	})
}

// handleDeleteVacation deletes vacation configuration
func (s *Server) handleDeleteVacation(w http.ResponseWriter, r *http.Request) {
	// Get user from context
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Delete config
	if err := s.deleteVacationConfig(user); err != nil {
		s.logger.Error("Failed to delete vacation config", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to delete vacation config")
		return
	}

	s.sendJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
	})
}

// handleAdminVacations handles GET /api/v1/admin/vacations (admin only)
func (s *Server) handleAdminVacations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Check admin (should be done by middleware, but double-check)
	isAdmin, _ := r.Context().Value("isAdmin").(bool)
	if !isAdmin {
		s.sendError(w, http.StatusForbidden, "admin access required")
		return
	}

	// Get active vacations
	activeVacations := s.listActiveVacations()

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"active_vacations": activeVacations,
		"count":            len(activeVacations),
	})
}

// getVacationConfig gets vacation config for a user
func (s *Server) getVacationConfig(user string) (*vacation.Config, error) {
	// Check for mock error injection (used in tests)
	if s.vacationGetError != nil {
		return nil, s.vacationGetError
	}
	// Canonical OOF path: when the semcore policy store is wired (production),
	// the out-of-office policy is the single source of truth. It is compiled to
	// the user's active Sieve script so vacation auto-replies actually fire at
	// delivery. The legacy stores below are only consulted when no OOF exists.
	if s.semStore != nil {
		if mbid, err := semcore.NewMailboxId(user); err == nil {
			if oofID, err := semcore.NewOOFId(mbid.String()); err == nil {
				if policy, err := s.semStore.Policy().GetOOF(oofID); err == nil && policy != nil && !policy.IsZero() {
					return oofToVacationConfig(policy), nil
				}
			}
		}
	}
	// Use interface if set
	if s.vacationMgr != nil {
		return s.vacationMgr.GetConfig(user)
	}
	// Default config returned when the user has nothing stored yet.
	defaultConfig := &vacation.Config{
		Enabled:      false,
		Subject:      "Out of Office",
		Message:      "I am currently out of office. I will respond to your email when I return.",
		SendInterval: 7 * 24 * time.Hour,
		IgnoreLists:  true,
		IgnoreBulk:   true,
	}
	if s.db == nil {
		return defaultConfig, nil
	}
	// Load from the database; a missing key means no config is set yet.
	config, err := s.db.GetVacation(user)
	if err != nil {
		return defaultConfig, nil
	}
	return config, nil
}

// setVacationConfig sets vacation config for a user
func (s *Server) setVacationConfig(user string, config *vacation.Config) error {
	// Check for mock error injection (used in tests)
	if s.vacationSetError != nil {
		return s.vacationSetError
	}
	// Canonical OOF path: persist to the semcore policy store and recompile the
	// user's active Sieve script so the vacation auto-reply takes effect at
	// delivery (the legacy db.BucketVacation store is never read at delivery).
	if s.semStore != nil {
		mbid, err := semcore.NewMailboxId(user)
		if err != nil {
			return err
		}
		policy, err := vacationConfigToOOF(mbid, config)
		if err != nil {
			return err
		}
		if err := s.semStore.Policy().PutOOF(policy); err != nil {
			return err
		}
		return s.recompileSieveForMailbox(mbid)
	}
	// Use interface if set
	if s.vacationMgr != nil {
		return s.vacationMgr.SetConfig(user, config)
	}
	if s.db == nil {
		return fmt.Errorf("database not available")
	}
	return s.db.PutVacation(user, config)
}

// deleteVacationConfig deletes vacation config for a user
func (s *Server) deleteVacationConfig(user string) error {
	// Check for mock error injection (used in tests)
	if s.vacationDeleteError != nil {
		return s.vacationDeleteError
	}
	// Canonical OOF path: disable the policy and recompile so the vacation
	// action is dropped from the active Sieve script.
	if s.semStore != nil {
		mbid, err := semcore.NewMailboxId(user)
		if err != nil {
			return err
		}
		oofID, err := semcore.NewOOFId(mbid.String())
		if err != nil {
			return err
		}
		if policy, err := s.semStore.Policy().GetOOF(oofID); err == nil && policy != nil && !policy.IsZero() {
			policy.Enabled = false
			policy.State = "Disabled"
			if err := s.semStore.Policy().PutOOF(policy); err != nil {
				return err
			}
		}
		return s.recompileSieveForMailbox(mbid)
	}
	// Use interface if set
	if s.vacationMgr != nil {
		return s.vacationMgr.DeleteConfig(user)
	}
	if s.db == nil {
		return fmt.Errorf("database not available")
	}
	return s.db.DeleteVacation(user)
}

// listActiveVacations lists all active vacations
func (s *Server) listActiveVacations() []string {
	// Use interface if set
	if s.vacationMgr != nil {
		list, err := s.vacationMgr.ListActive()
		if err != nil {
			return []string{}
		}
		return list
	}
	// Placeholder - in real implementation, get from vacation manager
	return []string{}
}

// parseLegacyVacationSettings parses the raw account.VacationSettings JSON
// (the admin-set legacy field) into a vacation.Config, so the admin path can be
// bridged onto the canonical OOF policy shared with webmail/EWS/JMAP.
func parseLegacyVacationSettings(raw string) (*vacation.Config, error) {
	var s struct {
		Enabled      bool   `json:"enabled"`
		Subject      string `json:"subject"`
		Message      string `json:"message"`
		HTMLMessage  string `json:"html_message"`
		StartDate    string `json:"start_date"`
		EndDate      string `json:"end_date"`
		SendInterval int    `json:"send_interval"` // hours (matches the API VacationConfig convention)
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	cfg := &vacation.Config{
		Enabled:     s.Enabled,
		Subject:     s.Subject,
		Message:     s.Message,
		HTMLMessage: s.HTMLMessage,
		StartDate:   parseLegacyVacationDate(s.StartDate),
		EndDate:     parseLegacyVacationDate(s.EndDate),
	}
	if s.SendInterval > 0 {
		cfg.SendInterval = time.Duration(s.SendInterval) * time.Hour
	}
	return cfg, nil
}

// parseLegacyVacationDate parses a date that may be either a date-only
// ("2006-01-02") or an RFC3339 timestamp; returns the zero time when empty.
func parseLegacyVacationDate(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// vacationConfigToOOF maps the webmail VacationConfig (vacation.Config) onto the
// canonical semcore OOF policy. State and the schedule window mirror the EWS
// mapping in internal/ews/rules.go (oofPolicyFromEWS) so webmail and Outlook
// write the same policy shape, and the policy compiles to a Sieve vacation
// action that fires at delivery.
func vacationConfigToOOF(mbid semcore.MailboxId, config *vacation.Config) (*semcore.OOFPolicy, error) {
	oofID, err := semcore.NewOOFId(mbid.String())
	if err != nil {
		return nil, err
	}
	externalReply := config.ExternalMessage
	if externalReply == "" {
		externalReply = config.Message
	}
	policy := &semcore.OOFPolicy{
		ID:               oofID,
		MailboxID:        mbid,
		Enabled:          config.Enabled,
		Subject:          config.Subject,
		TextBody:         config.Message,
		InternalReply:    config.Message,
		ExternalReply:    externalReply,
		Audience:         parseVacationAudience(config.Audience),
		HTMLBody:         config.HTMLMessage,
		ExcludeAddresses: config.ExcludeAddresses,
		IgnoreLists:      config.IgnoreLists,
		IgnoreBulk:       config.IgnoreBulk,
		Timezone:         "UTC",
	}
	switch {
	case !config.Enabled:
		policy.State = "Disabled"
	case !config.StartDate.IsZero() || !config.EndDate.IsZero():
		policy.State = "Scheduled"
		policy.StartTime = config.StartDate
		policy.EndTime = config.EndDate
	default:
		policy.State = "Enabled"
	}
	if config.SendInterval > 0 {
		policy.SendIntervalSeconds = int64(config.SendInterval / time.Second)
	} else {
		policy.SendIntervalSeconds = 7 * 24 * 3600
	}
	return policy, nil
}

// oofToVacationConfig maps a canonical OOF policy back to the webmail
// VacationConfig shape for GET responses.
func oofToVacationConfig(policy *semcore.OOFPolicy) *vacation.Config {
	msg := policy.TextBody
	if msg == "" {
		msg = policy.InternalReply
	}
	return &vacation.Config{
		Enabled:          policy.Enabled,
		StartDate:        policy.StartTime,
		EndDate:          policy.EndTime,
		Subject:          policy.Subject,
		Message:          msg,
		HTMLMessage:      policy.HTMLBody,
		ExternalMessage:  policy.ExternalReply,
		Audience:         vacationAudienceString(policy.Audience),
		SendInterval:     policy.SendInterval(),
		ExcludeAddresses: policy.ExcludeAddresses,
		IgnoreLists:      policy.IgnoreLists,
		IgnoreBulk:       policy.IgnoreBulk,
	}
}
