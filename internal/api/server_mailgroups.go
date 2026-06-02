package api

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/db"
)

// mailGroupToJSON renders a mail group for the admin API. Nothing here is
// sensitive, so the full configuration is returned.
func mailGroupToJSON(g *db.MailGroup) map[string]any {
	members := g.Members
	if members == nil {
		members = []string{}
	}
	m := map[string]any{
		"email":                 g.Email,
		"description":           g.Description,
		"is_active":             g.IsActive,
		"dynamic":               g.Dynamic,
		"sender_policy":         g.SenderPolicy,
		"members":               members,
		"dynamic_domain":        g.DynamicDomain,
		"dynamic_local_pattern": g.DynamicLocalPattern,
		"created_at":            g.CreatedAt,
		"updated_at":            g.UpdatedAt,
	}
	if g.DynamicAdminOnly != nil {
		m["dynamic_admin_only"] = *g.DynamicAdminOnly
	}
	return m
}

// normalizeSenderPolicy returns a valid sender policy, defaulting to "internal".
func normalizeSenderPolicy(p string) string {
	if strings.EqualFold(p, "anyone") {
		return "anyone"
	}
	return "internal"
}

// cleanMemberList trims and drops empty member addresses.
func cleanMemberList(members []string) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

func (s *Server) handleMailGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listMailGroups(w, r)
	case http.MethodPost:
		s.createMailGroup(w, r)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleMailGroupDetail(w http.ResponseWriter, r *http.Request) {
	suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/groups/")
	switch r.Method {
	case http.MethodGet:
		s.getMailGroup(w, r, suffix)
	case http.MethodPut:
		s.updateMailGroup(w, r, suffix)
	case http.MethodDelete:
		s.deleteMailGroup(w, r, suffix)
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) listMailGroups(w http.ResponseWriter, _ *http.Request) {
	groups, err := s.db.ListMailGroups()
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to list mail groups")
		return
	}
	result := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		result = append(result, mailGroupToJSON(g))
	}
	s.sendJSON(w, http.StatusOK, result)
}

func (s *Server) createMailGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email               string   `json:"email"`
		Description         string   `json:"description"`
		Dynamic             bool     `json:"dynamic"`
		Members             []string `json:"members"`
		DynamicDomain       string   `json:"dynamic_domain"`
		DynamicAdminOnly    *bool    `json:"dynamic_admin_only"`
		DynamicLocalPattern string   `json:"dynamic_local_pattern"`
		SenderPolicy        string   `json:"sender_policy"`
		IsActive            *bool    `json:"is_active"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	user, domain := parseEmail(req.Email)
	if user == "" || domain == "" {
		s.sendError(w, http.StatusBadRequest, "invalid group address format")
		return
	}
	if _, err := s.db.GetDomain(domain); err != nil {
		s.sendError(w, http.StatusBadRequest, "domain not found")
		return
	}
	// Reject duplicates so a create never silently overwrites an existing group.
	if _, err := s.db.GetMailGroup(domain, user); err == nil {
		s.sendError(w, http.StatusConflict, "mail group already exists")
		return
	}

	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	group := &db.MailGroup{
		Email:               req.Email,
		LocalPart:           user,
		Domain:              domain,
		Description:         req.Description,
		IsActive:            active,
		Dynamic:             req.Dynamic,
		SenderPolicy:        normalizeSenderPolicy(req.SenderPolicy),
		DynamicDomain:       strings.TrimSpace(req.DynamicDomain),
		DynamicAdminOnly:    req.DynamicAdminOnly,
		DynamicLocalPattern: strings.TrimSpace(req.DynamicLocalPattern),
	}
	if !req.Dynamic {
		group.Members = cleanMemberList(req.Members)
	}

	if err := s.db.CreateMailGroup(group); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to create mail group")
		return
	}
	s.sendJSON(w, http.StatusCreated, mailGroupToJSON(group))
}

func (s *Server) getMailGroup(w http.ResponseWriter, _ *http.Request, addr string) {
	user, domain := parseEmail(addr)
	if user == "" || domain == "" {
		s.sendError(w, http.StatusBadRequest, "invalid group address")
		return
	}
	group, err := s.db.GetMailGroup(domain, user)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "mail group not found")
		return
	}
	s.sendJSON(w, http.StatusOK, mailGroupToJSON(group))
}

func (s *Server) updateMailGroup(w http.ResponseWriter, r *http.Request, addr string) {
	user, domain := parseEmail(addr)
	if user == "" || domain == "" {
		s.sendError(w, http.StatusBadRequest, "invalid group address")
		return
	}
	group, err := s.db.GetMailGroup(domain, user)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "mail group not found")
		return
	}

	var req struct {
		Description         *string  `json:"description"`
		IsActive            *bool    `json:"is_active"`
		Dynamic             *bool    `json:"dynamic"`
		Members             []string `json:"members"`
		DynamicDomain       *string  `json:"dynamic_domain"`
		DynamicAdminOnly    *bool    `json:"dynamic_admin_only"`
		DynamicLocalPattern *string  `json:"dynamic_local_pattern"`
		SenderPolicy        *string  `json:"sender_policy"`
		ClearAdminOnly      bool     `json:"clear_admin_only"` // explicitly drop the admin filter
	}
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Description != nil {
		group.Description = *req.Description
	}
	if req.IsActive != nil {
		group.IsActive = *req.IsActive
	}
	if req.Dynamic != nil {
		group.Dynamic = *req.Dynamic
	}
	if req.Members != nil {
		group.Members = cleanMemberList(req.Members)
	}
	if req.DynamicDomain != nil {
		group.DynamicDomain = strings.TrimSpace(*req.DynamicDomain)
	}
	if req.DynamicLocalPattern != nil {
		group.DynamicLocalPattern = strings.TrimSpace(*req.DynamicLocalPattern)
	}
	if req.ClearAdminOnly {
		group.DynamicAdminOnly = nil
	} else if req.DynamicAdminOnly != nil {
		group.DynamicAdminOnly = req.DynamicAdminOnly
	}
	if req.SenderPolicy != nil {
		group.SenderPolicy = normalizeSenderPolicy(*req.SenderPolicy)
	}

	if err := s.db.UpdateMailGroup(group); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to update mail group")
		return
	}
	s.sendJSON(w, http.StatusOK, mailGroupToJSON(group))
}

func (s *Server) deleteMailGroup(w http.ResponseWriter, _ *http.Request, addr string) {
	user, domain := parseEmail(addr)
	if user == "" || domain == "" {
		s.sendError(w, http.StatusBadRequest, "invalid group address")
		return
	}
	if err := s.db.DeleteMailGroup(domain, user); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to delete mail group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
