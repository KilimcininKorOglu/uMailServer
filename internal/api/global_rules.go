package api

import (
	"net/http"
	"path"
	"time"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Global rules are admin-authored mail rules applied to EVERY mailbox,
// independent of and ahead of each user's own rules. They are stored as semcore
// Rules under the reserved GlobalRulesOwner key (so they reuse the same canonical
// rule store, EmailFilter shape, and Sieve compiler as user filters) and are
// compiled into every user's managed Sieve via CompileEffectivePolicy. Mutating
// them recompiles every account's Sieve so the change takes effect org-wide.

// handleGlobalRules is the admin collection endpoint: GET lists, POST creates.
func (s *Server) handleGlobalRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rules, err := s.getUserFilters(semcore.GlobalRulesOwner)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to list global rules")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"rules": rules})
	case http.MethodPost:
		s.createGlobalRule(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleGlobalRuleDetail is the admin item endpoint: PUT updates, DELETE removes.
func (s *Server) handleGlobalRuleDetail(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		s.updateGlobalRule(w, r)
	case http.MethodDelete:
		s.deleteGlobalRule(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) createGlobalRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string            `json:"name"`
		MatchAll   bool              `json:"matchAll"`
		Conditions []FilterCondition `json:"conditions"`
		Actions    []FilterAction    `json:"actions"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		s.sendError(w, http.StatusBadRequest, "rule name is required")
		return
	}
	if len(req.Name) > 255 {
		s.sendError(w, http.StatusBadRequest, "rule name exceeds maximum length of 255")
		return
	}
	if msg, ok := validateFilterPayload(req.Conditions, req.Actions); !ok {
		s.sendError(w, http.StatusBadRequest, msg)
		return
	}

	existing, _ := s.getUserFilters(semcore.GlobalRulesOwner) //nolint:errcheck // empty on error is fine for ordering
	rule := &EmailFilter{
		ID:         uuid.New().String(),
		UserID:     semcore.GlobalRulesOwner,
		Name:       req.Name,
		Enabled:    true,
		MatchAll:   req.MatchAll,
		Conditions: req.Conditions,
		Actions:    req.Actions,
		Priority:   len(existing) + 1,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := s.saveFilter(rule); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to save global rule")
		return
	}
	s.recompileAllUsersSieve()
	s.sendJSON(w, http.StatusCreated, rule)
}

func (s *Server) updateGlobalRule(w http.ResponseWriter, r *http.Request) {
	id := path.Base(r.URL.Path)
	existing, err := s.getFilter(semcore.GlobalRulesOwner, id)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "global rule not found")
		return
	}
	var req struct {
		Name       string            `json:"name"`
		Enabled    *bool             `json:"enabled,omitempty"`
		MatchAll   bool              `json:"matchAll"`
		Conditions []FilterCondition `json:"conditions"`
		Actions    []FilterAction    `json:"actions"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	existing.MatchAll = req.MatchAll
	if len(req.Conditions) > 0 {
		existing.Conditions = req.Conditions
	}
	if len(req.Actions) > 0 {
		existing.Actions = req.Actions
	}
	if msg, ok := validateFilterPayload(existing.Conditions, existing.Actions); !ok {
		s.sendError(w, http.StatusBadRequest, msg)
		return
	}
	existing.UpdatedAt = time.Now()
	if err := s.saveFilter(existing); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to update global rule")
		return
	}
	s.recompileAllUsersSieve()
	s.sendJSON(w, http.StatusOK, existing)
}

func (s *Server) deleteGlobalRule(w http.ResponseWriter, r *http.Request) {
	id := path.Base(r.URL.Path)
	if err := s.deleteFilter(semcore.GlobalRulesOwner, id); err != nil {
		s.sendError(w, http.StatusNotFound, "global rule not found")
		return
	}
	s.recompileAllUsersSieve()
	s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// recompileAllUsersSieve recompiles every account's managed Sieve so a global
// rule change reaches all mailboxes (their compiled script embeds the global
// rules ahead of their own). Best-effort per account.
func (s *Server) recompileAllUsersSieve() {
	if s.db == nil {
		return
	}
	domains, err := s.db.ListDomains()
	if err != nil {
		return
	}
	for _, d := range domains {
		accounts, aerr := s.db.ListAccountsByDomain(d.Name)
		if aerr != nil {
			continue
		}
		for _, a := range accounts {
			if mbid, merr := semcore.NewMailboxId(a.Email); merr == nil {
				//nolint:errcheck // best-effort: a single account's recompile failure must not block the rest
				s.recompileSieveForMailbox(mbid)
			}
		}
	}
}
