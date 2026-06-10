package api

import (
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/umailserver/umailserver/internal/semcore"
)

// FilterCondition is a single condition in an inbox filter. It is a JSON
// projection of semcore.RuleCondition: Field maps to RuleConditionKind and
// Operator maps to RuleMatchType.
type FilterCondition struct {
	Field      string `json:"field"`    // from,to,subject,body,header,size,flag,address
	Operator   string `json:"operator"` // contains,equals,startsWith,endsWith,matches
	Value      string `json:"value"`
	HeaderName string `json:"headerName,omitempty"`
}

// FilterAction is a single action in an inbox filter. It is a JSON projection
// of semcore.RuleAction; Type is the canonical RuleActionKind.String() value so
// the full rule vocabulary round-trips without loss.
type FilterAction struct {
	Type        string `json:"type"`                  // moveToFolder,copyToFolder,delete,markRead,markImportant,forward,forwardAsAttachment,redirect,reject,addHeader,deleteHeader,flag,stop,vacation
	Target      string `json:"target,omitempty"`      // folder for move/copy
	ForwardTo   string `json:"forwardTo,omitempty"`   // address for forward/redirect
	Message     string `json:"message,omitempty"`     // rejection message
	HeaderName  string `json:"headerName,omitempty"`  // add/delete header
	HeaderValue string `json:"headerValue,omitempty"` // add header
	FlagName    string `json:"flagName,omitempty"`    // flag action
	ClearFlag   bool   `json:"clearFlag,omitempty"`   // flag: true = clear, false = set
}

// EmailFilter is a user's inbox filter. It is the webmail-facing projection of
// a canonical semcore.Rule; the two are mapped by ruleToFilter/filterToRule so
// webmail filters are compiled to Sieve and enforced at delivery, and are
// visible to the admin and EWS surfaces from the same store.
type EmailFilter struct {
	ID         string            `json:"id"`
	UserID     string            `json:"user_id"`
	Name       string            `json:"name"`
	Enabled    bool              `json:"enabled"`
	MatchAll   bool              `json:"matchAll"`
	Conditions []FilterCondition `json:"conditions"`
	Actions    []FilterAction    `json:"actions"`
	Priority   int               `json:"priority"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

// ---------------------------------------------------------------------------
// Filter <-> canonical Rule mapping
// ---------------------------------------------------------------------------

// conditionFieldToKind maps a filter Field string to a semcore condition kind.
func conditionFieldToKind(field string) (semcore.RuleConditionKind, error) {
	switch field {
	case "from":
		return semcore.RuleConditionKindFrom, nil
	case "to":
		return semcore.RuleConditionKindTo, nil
	case "subject":
		return semcore.RuleConditionKindSubject, nil
	case "body":
		return semcore.RuleConditionKindBody, nil
	case "header":
		return semcore.RuleConditionKindHeader, nil
	case "size":
		return semcore.RuleConditionKindSize, nil
	case "flag":
		return semcore.RuleConditionKindFlag, nil
	case "address":
		return semcore.RuleConditionKindAddress, nil
	default:
		return 0, fmt.Errorf("unknown condition field %q", field)
	}
}

// conditionKindToField is the reverse of conditionFieldToKind.
func conditionKindToField(k semcore.RuleConditionKind) string {
	switch k {
	case semcore.RuleConditionKindTo:
		return "to"
	case semcore.RuleConditionKindSubject:
		return "subject"
	case semcore.RuleConditionKindBody:
		return "body"
	case semcore.RuleConditionKindHeader:
		return "header"
	case semcore.RuleConditionKindSize:
		return "size"
	case semcore.RuleConditionKindFlag:
		return "flag"
	case semcore.RuleConditionKindAddress:
		return "address"
	default:
		return "from"
	}
}

// validMatchType reports whether op is one of the supported RuleMatchType values.
func validMatchType(op string) bool {
	switch semcore.RuleMatchType(op) {
	case semcore.RuleMatchTypeContains, semcore.RuleMatchTypeEquals,
		semcore.RuleMatchTypeStartsWith, semcore.RuleMatchTypeEndsWith,
		semcore.RuleMatchTypeMatches:
		return true
	default:
		return false
	}
}

// actionTypeToKind maps a filter action Type string to a semcore action kind.
// Legacy webmail strings (move/copy/markSpam) are accepted for tolerance and
// normalized to their canonical kinds.
func actionTypeToKind(t string) (semcore.RuleActionKind, error) {
	switch t {
	case "moveToFolder", "move", "markSpam":
		return semcore.RuleActionKindMoveToFolder, nil
	case "copyToFolder", "copy":
		return semcore.RuleActionKindCopyToFolder, nil
	case "delete":
		return semcore.RuleActionKindDelete, nil
	case "markRead":
		return semcore.RuleActionKindMarkRead, nil
	case "markImportant":
		return semcore.RuleActionKindMarkImportant, nil
	case "forward":
		return semcore.RuleActionKindForward, nil
	case "forwardAsAttachment":
		return semcore.RuleActionKindForwardAsAttachment, nil
	case "redirect":
		return semcore.RuleActionKindRedirect, nil
	case "reject":
		return semcore.RuleActionKindReject, nil
	case "addHeader":
		return semcore.RuleActionKindAddHeader, nil
	case "deleteHeader":
		return semcore.RuleActionKindDeleteHeader, nil
	case "flag":
		return semcore.RuleActionKindFlag, nil
	case "stop":
		return semcore.RuleActionKindStop, nil
	case "vacation":
		return semcore.RuleActionKindVacation, nil
	default:
		return 0, fmt.Errorf("unknown action type %q", t)
	}
}

// filterToRule converts a webmail EmailFilter to a canonical semcore Rule for
// the given mailbox. The filter ID becomes the RuleId.
func filterToRule(f *EmailFilter, mbid semcore.MailboxId) (*semcore.Rule, error) {
	id, err := semcore.NewRuleId(f.ID)
	if err != nil {
		return nil, err
	}

	conds := make([]semcore.RuleCondition, 0, len(f.Conditions))
	for _, c := range f.Conditions {
		kind, err := conditionFieldToKind(c.Field)
		if err != nil {
			return nil, err
		}
		if !validMatchType(c.Operator) {
			return nil, fmt.Errorf("unknown operator %q", c.Operator)
		}
		conds = append(conds, semcore.RuleCondition{
			Kind:       kind,
			MatchType:  semcore.RuleMatchType(c.Operator),
			Value:      c.Value,
			HeaderName: c.HeaderName,
		})
	}

	actions := make([]semcore.RuleAction, 0, len(f.Actions))
	for _, a := range f.Actions {
		kind, err := actionTypeToKind(a.Type)
		if err != nil {
			return nil, err
		}
		target := a.Target
		// "markSpam" is a convenience for filing into the Junk folder.
		if a.Type == "markSpam" && target == "" {
			target = "Junk"
		}
		actions = append(actions, semcore.RuleAction{
			Kind:        kind,
			Target:      target,
			ForwardTo:   a.ForwardTo,
			Message:     a.Message,
			HeaderName:  a.HeaderName,
			HeaderValue: a.HeaderValue,
			FlagName:    a.FlagName,
			ClearFlag:   a.ClearFlag,
		})
	}

	return &semcore.Rule{
		ID:         id,
		MailboxID:  mbid,
		Name:       f.Name,
		Enabled:    f.Enabled,
		Priority:   f.Priority,
		MatchAll:   f.MatchAll,
		Conditions: conds,
		Actions:    actions,
	}, nil
}

// ruleToFilter converts a canonical semcore Rule to a webmail EmailFilter.
func ruleToFilter(r *semcore.Rule) *EmailFilter {
	conds := make([]FilterCondition, 0, len(r.Conditions))
	for _, c := range r.Conditions {
		conds = append(conds, FilterCondition{
			Field:      conditionKindToField(c.Kind),
			Operator:   string(c.MatchType),
			Value:      c.Value,
			HeaderName: c.HeaderName,
		})
	}

	actions := make([]FilterAction, 0, len(r.Actions))
	for _, a := range r.Actions {
		actions = append(actions, FilterAction{
			Type:        a.Kind.String(),
			Target:      a.Target,
			ForwardTo:   a.ForwardTo,
			Message:     a.Message,
			HeaderName:  a.HeaderName,
			HeaderValue: a.HeaderValue,
			FlagName:    a.FlagName,
			ClearFlag:   a.ClearFlag,
		})
	}

	return &EmailFilter{
		ID:         r.ID.String(),
		UserID:     r.MailboxID.String(),
		Name:       r.Name,
		Enabled:    r.Enabled,
		MatchAll:   r.MatchAll,
		Conditions: conds,
		Actions:    actions,
		Priority:   r.Priority,
		CreatedAt:  r.Created,
		UpdatedAt:  r.Modified,
	}
}

// handleFilters handles GET/POST /api/v1/filters
func (s *Server) handleFilters(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetFilters(w, r)
	case http.MethodPost:
		s.handleCreateFilter(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleFilter handles GET/PUT/DELETE /api/v1/filters/:id
func (s *Server) handleFilter(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetFilter(w, r)
	case http.MethodPut:
		s.handleUpdateFilter(w, r)
	case http.MethodDelete:
		s.handleDeleteFilter(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleGetFilters gets all filters for the current user
func (s *Server) handleGetFilters(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	filters, err := s.getUserFilters(user)
	if err != nil {
		s.logger.Error("Failed to get filters", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to get filters")
		return
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"filters": filters,
	})
}

// handleGetFilter gets a single filter
func (s *Server) handleGetFilter(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	filterID := path.Base(r.URL.Path)

	filter, err := s.getFilter(user, filterID)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "filter not found")
		return
	}

	s.sendJSON(w, http.StatusOK, filter)
}

// validateFilterPayload validates the conditions and actions of a filter
// create/update request, returning an error message and false when invalid.
func validateFilterPayload(conditions []FilterCondition, actions []FilterAction) (string, bool) {
	if len(conditions) == 0 {
		return "at least one condition is required", false
	}
	if len(conditions) > 50 {
		return "too many conditions (max 50)", false
	}
	if len(actions) == 0 {
		return "at least one action is required", false
	}
	if len(actions) > 20 {
		return "too many actions (max 20)", false
	}
	for i, cond := range conditions {
		if cond.Value == "" {
			return fmt.Sprintf("condition %d has empty value", i+1), false
		}
		if len(cond.Value) > 1000 {
			return fmt.Sprintf("condition %d value exceeds maximum length", i+1), false
		}
		if _, err := conditionFieldToKind(cond.Field); err != nil {
			return fmt.Sprintf("condition %d has %s", i+1, err.Error()), false
		}
		if !validMatchType(cond.Operator) {
			return fmt.Sprintf("condition %d has unknown operator %q", i+1, cond.Operator), false
		}
		if cond.Field == "header" && cond.HeaderName == "" {
			return fmt.Sprintf("condition %d requires headerName", i+1), false
		}
	}
	for i, act := range actions {
		if _, err := actionTypeToKind(act.Type); err != nil {
			return fmt.Sprintf("action %d has %s", i+1, err.Error()), false
		}
	}
	return "", true
}

// handleCreateFilter creates a new filter
func (s *Server) handleCreateFilter(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

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
		s.sendError(w, http.StatusBadRequest, "filter name is required")
		return
	}
	if len(req.Name) > 255 {
		s.sendError(w, http.StatusBadRequest, "filter name exceeds maximum length of 255")
		return
	}
	if msg, ok := validateFilterPayload(req.Conditions, req.Actions); !ok {
		s.sendError(w, http.StatusBadRequest, msg)
		return
	}

	// Append to the end of the existing rule order.
	existing, _ := s.getUserFilters(user) //nolint:errcheck // empty on error is fine for ordering

	filter := &EmailFilter{
		ID:         uuid.New().String(),
		UserID:     user,
		Name:       req.Name,
		Enabled:    true,
		MatchAll:   req.MatchAll,
		Conditions: req.Conditions,
		Actions:    req.Actions,
		Priority:   len(existing) + 1,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := s.saveFilter(filter); err != nil {
		s.logger.Error("Failed to save filter", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to save filter")
		return
	}
	s.recompileUserSieve(user)

	s.sendJSON(w, http.StatusCreated, filter)
}

// handleUpdateFilter updates an existing filter
func (s *Server) handleUpdateFilter(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	filterID := path.Base(r.URL.Path)

	existing, err := s.getFilter(user, filterID)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "filter not found")
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
		s.logger.Error("Failed to update filter", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to update filter")
		return
	}
	s.recompileUserSieve(user)

	s.sendJSON(w, http.StatusOK, existing)
}

// handleDeleteFilter deletes a filter
func (s *Server) handleDeleteFilter(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	filterID := path.Base(r.URL.Path)

	if err := s.deleteFilter(user, filterID); err != nil {
		s.sendError(w, http.StatusNotFound, "filter not found")
		return
	}
	s.recompileUserSieve(user)

	s.sendJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
	})
}

// handleFilterToggle handles POST /api/v1/filters/:id/toggle
func (s *Server) handleFilterToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		s.sendError(w, http.StatusBadRequest, "invalid filter id")
		return
	}
	filterID := pathParts[len(pathParts)-2]

	existing, err := s.getFilter(user, filterID)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "filter not found")
		return
	}

	existing.Enabled = !existing.Enabled
	existing.UpdatedAt = time.Now()

	if err := s.saveFilter(existing); err != nil {
		s.logger.Error("Failed to toggle filter", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to toggle filter")
		return
	}
	s.recompileUserSieve(user)

	s.sendJSON(w, http.StatusOK, existing)
}

// handleFilterPath routes filter requests including toggle paths
func (s *Server) handleFilterPath(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(p, "/")

	// Check if this is a toggle request: /api/v1/filters/{id}/toggle
	if len(parts) >= 4 && parts[len(parts)-1] == "toggle" {
		s.handleFilterToggle(w, r)
		return
	}

	s.handleFilter(w, r)
}

// handleFilterReorder handles POST /api/v1/filters/reorder
func (s *Server) handleFilterReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		FilterIDs []string `json:"filterIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.reorderFilters(user, req.FilterIDs); err != nil {
		s.logger.Error("Failed to reorder filters", "error", err, "user", user)
		s.sendError(w, http.StatusInternalServerError, "failed to reorder filters")
		return
	}
	s.recompileUserSieve(user)

	s.sendJSON(w, http.StatusOK, map[string]string{
		"status": "reordered",
	})
}

// recompileUserSieve recompiles and installs the active Sieve script for the
// user's mailbox after a filter mutation. Failures are logged but do not fail
// the request: the canonical rule is already persisted and will be applied on
// the next successful recompile.
func (s *Server) recompileUserSieve(user string) {
	mbid, err := semcore.NewMailboxId(user)
	if err != nil {
		return
	}
	if err := s.recompileSieveForMailbox(mbid); err != nil {
		s.logger.Warn("failed to recompile sieve after filter change", "error", err, "user", user)
	}
}

// ---------------------------------------------------------------------------
// Storage: backed by the canonical semcore Policy store
//
// The webmail filter store is the canonical inbox-rule store. A filter is a
// projection of a semcore.Rule scoped to the user's mailbox (NewMailboxId(user)),
// the same mailbox EWS resolves, so rules created here are compiled to Sieve,
// enforced at delivery, and visible from the admin and EWS surfaces.
//
// The FilterManager interface branch is retained for tests that inject a mock;
// production never sets a FilterManager, so it always takes the semcore path.
// ---------------------------------------------------------------------------

// getUserFilters gets all filters for a user.
func (s *Server) getUserFilters(userID string) ([]*EmailFilter, error) {
	if s.filterMgr != nil {
		return s.filterMgr.GetUserFilters(userID)
	}
	if s.semStore == nil {
		return []*EmailFilter{}, nil
	}
	mbid, err := semcore.NewMailboxId(userID)
	if err != nil {
		return nil, err
	}
	rules, err := s.semStore.Policy().ListRules(mbid)
	if err != nil {
		return nil, err
	}
	filters := make([]*EmailFilter, 0, len(rules))
	for _, rule := range rules {
		filters = append(filters, ruleToFilter(rule))
	}
	return filters, nil
}

// getFilter gets a single filter by ID for the given user.
func (s *Server) getFilter(userID, filterID string) (*EmailFilter, error) {
	if s.filterGetError != nil {
		return nil, s.filterGetError
	}
	if s.filterMgr != nil {
		return s.filterMgr.GetFilter(userID, filterID)
	}
	if s.semStore == nil {
		return nil, fmt.Errorf("rule store not available")
	}
	mbid, err := semcore.NewMailboxId(userID)
	if err != nil {
		return nil, err
	}
	rid, err := semcore.NewRuleId(filterID)
	if err != nil {
		return nil, err
	}
	rule, err := s.semStore.Policy().GetRule(rid)
	if err != nil {
		return nil, err
	}
	if !rule.MailboxID.Equal(mbid) {
		return nil, fmt.Errorf("filter not found")
	}
	return ruleToFilter(rule), nil
}

// saveFilter persists a filter as a canonical rule.
func (s *Server) saveFilter(filter *EmailFilter) error {
	if s.filterSaveError != nil {
		return s.filterSaveError
	}
	if s.filterMgr != nil {
		return s.filterMgr.SaveFilter(filter)
	}
	if s.semStore == nil {
		return fmt.Errorf("rule store not available")
	}
	mbid, err := semcore.NewMailboxId(filter.UserID)
	if err != nil {
		return err
	}
	rule, err := filterToRule(filter, mbid)
	if err != nil {
		return err
	}
	return s.semStore.Policy().PutRule(rule)
}

// deleteFilter removes a filter (canonical rule) owned by the user.
func (s *Server) deleteFilter(userID, filterID string) error {
	if s.filterMgr != nil {
		return s.filterMgr.DeleteFilter(userID, filterID)
	}
	if s.semStore == nil {
		return fmt.Errorf("rule store not available")
	}
	// Verify ownership before deleting.
	if _, err := s.getFilter(userID, filterID); err != nil {
		return err
	}
	rid, err := semcore.NewRuleId(filterID)
	if err != nil {
		return err
	}
	return s.semStore.Policy().DeleteRule(rid)
}

// reorderFilters updates the priority of the user's filters to the given order.
func (s *Server) reorderFilters(userID string, filterIDs []string) error {
	if s.filterMgr != nil {
		return s.filterMgr.ReorderFilters(userID, filterIDs)
	}
	if s.semStore == nil {
		return fmt.Errorf("rule store not available")
	}
	for priority, filterID := range filterIDs {
		filter, err := s.getFilter(userID, filterID)
		if err != nil {
			continue
		}
		filter.Priority = priority + 1
		filter.UpdatedAt = time.Now()
		if err := s.saveFilter(filter); err != nil {
			s.logger.Error("failed to update filter priority", "filterID", filterID, "error", err)
		}
	}
	return nil
}
