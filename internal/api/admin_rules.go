package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
)

// adminRuleDTO mirrors the frontend PolicyRule interface
// (web/admin/src/pages/Policies.tsx), plus the owning mailbox for context.
type adminRuleDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Priority   int    `json:"priority"`
	Conditions string `json:"conditions"`
	Actions    string `json:"actions"`
	Mailbox    string `json:"mailbox"`
}

type adminRuleUpdateRequest struct {
	Enabled  *bool `json:"enabled"`
	Priority *int  `json:"priority"`
}

// handleAdminRules handles GET (list all inbox rules across mailboxes) on
// /api/v1/admin/rules.
func (s *Server) handleAdminRules(w http.ResponseWriter, r *http.Request) {
	if s.semStore == nil {
		s.sendError(w, http.StatusServiceUnavailable, "rule store not available")
		return
	}
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rules, err := s.semStore.Policy().ListAllRules()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list rules")
		return
	}
	emails, err := s.semStore.Identity().MailboxEmailsByID()
	if err != nil {
		emails = map[string]string{}
	}

	out := make([]adminRuleDTO, 0, len(rules))
	for _, rule := range rules {
		mailbox := emails[rule.MailboxID.String()]
		if mailbox == "" {
			mailbox = rule.MailboxID.String()
		}
		out = append(out, adminRuleDTO{
			ID:         rule.ID.String(),
			Name:       rule.Name,
			Enabled:    rule.Enabled,
			Priority:   rule.Priority,
			Conditions: conditionsSummary(rule.Conditions),
			Actions:    actionsSummary(rule.Actions),
			Mailbox:    mailbox,
		})
	}
	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"rules": out,
		"count": len(out),
	})
}

// handleAdminRuleDetail handles PUT (toggle/update) and DELETE on
// /api/v1/admin/rules/{id}.
func (s *Server) handleAdminRuleDetail(w http.ResponseWriter, r *http.Request) {
	if s.semStore == nil {
		s.sendError(w, http.StatusServiceUnavailable, "rule store not available")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/rules/")
	if id == "" {
		s.sendError(w, http.StatusBadRequest, "rule id required")
		return
	}
	ruleID, err := semcore.NewRuleId(id)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid rule id")
		return
	}

	switch r.Method {
	case http.MethodPut:
		rule, err := s.semStore.Policy().GetRule(ruleID)
		if err != nil {
			s.sendError(w, http.StatusNotFound, "rule not found")
			return
		}
		var req adminRuleUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Enabled != nil {
			rule.Enabled = *req.Enabled
		}
		if req.Priority != nil {
			rule.Priority = *req.Priority
		}
		// Force a new change key so the mutation is visible to sync consumers.
		rule.ChangeKey = semcore.RuleChangeKey{}
		if err := s.semStore.Policy().PutRule(rule); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to update rule")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{
			"id":      rule.ID.String(),
			"enabled": rule.Enabled,
		})
	case http.MethodDelete:
		if err := s.semStore.Policy().DeleteRule(ruleID); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to delete rule")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// conditionsSummary renders a rule's conditions into a short human-readable
// string for the admin UI.
func conditionsSummary(conds []semcore.RuleCondition) string {
	if len(conds) == 0 {
		return "Any message"
	}
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		label := conditionKindLabel(c.Kind)
		if c.Kind == semcore.RuleConditionKindHeader && c.HeaderName != "" {
			label = c.HeaderName
		}
		parts = append(parts, fmt.Sprintf("%s %s %q", label, c.MatchType, c.Value))
	}
	return strings.Join(parts, "; ")
}

// actionsSummary renders a rule's actions into a short human-readable string.
func actionsSummary(actions []semcore.RuleAction) string {
	if len(actions) == 0 {
		return "No action"
	}
	parts := make([]string, 0, len(actions))
	for _, a := range actions {
		switch {
		case a.Target != "":
			parts = append(parts, fmt.Sprintf("%s -> %s", a.Kind.String(), a.Target))
		case a.ForwardTo != "":
			parts = append(parts, fmt.Sprintf("%s -> %s", a.Kind.String(), a.ForwardTo))
		default:
			parts = append(parts, a.Kind.String())
		}
	}
	return strings.Join(parts, "; ")
}

func conditionKindLabel(k semcore.RuleConditionKind) string {
	switch k {
	case semcore.RuleConditionKindFrom:
		return "From"
	case semcore.RuleConditionKindTo:
		return "To"
	case semcore.RuleConditionKindSubject:
		return "Subject"
	case semcore.RuleConditionKindBody:
		return "Body"
	case semcore.RuleConditionKindHeader:
		return "Header"
	case semcore.RuleConditionKindSize:
		return "Size"
	case semcore.RuleConditionKindFlag:
		return "Flag"
	case semcore.RuleConditionKindAddress:
		return "Address"
	default:
		return "Condition"
	}
}
