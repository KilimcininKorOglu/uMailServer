package api

import (
	"net/http"
	"strings"
)

// folderName validates and normalizes a user-supplied folder name. It rejects
// empty names, over-long names, and names containing control or path-separator
// characters that would break the flat mailbox keyspace.
func folderName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > 255 {
		return "", false
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 {
			return "", false
		}
	}
	return name, true
}

// handleFolders creates a user mailbox folder. POST /api/v1/folders {name}.
func (s *Server) handleFolders(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.mailDB == nil {
		s.sendError(w, http.StatusInternalServerError, "mailbox store not available")
		return
	}

	switch r.Method {
	case http.MethodGet:
		mailboxes, err := s.mailDB.ListMailboxes(user)
		if err != nil {
			mailboxes = []string{}
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"mailboxes": mailboxes})
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		name, valid := folderName(req.Name)
		if !valid {
			s.sendError(w, http.StatusBadRequest, "invalid folder name")
			return
		}
		if isStandardMailbox(name) {
			s.sendError(w, http.StatusConflict, "a built-in folder with that name already exists")
			return
		}
		if err := s.mailDB.CreateMailbox(user, name); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to create folder")
			return
		}
		s.sendJSON(w, http.StatusCreated, map[string]string{"name": name})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleFolderPath renames (PUT) or deletes (DELETE) a folder identified by the
// path segment after /api/v1/folders/. Built-in folders cannot be modified.
func (s *Server) handleFolderPath(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.mailDB == nil {
		s.sendError(w, http.StatusInternalServerError, "mailbox store not available")
		return
	}

	raw := strings.TrimPrefix(r.URL.Path, "/api/v1/folders/")
	current, valid := folderName(raw)
	if !valid {
		s.sendError(w, http.StatusBadRequest, "invalid folder name")
		return
	}
	if isStandardMailbox(current) {
		s.sendError(w, http.StatusForbidden, "built-in folders cannot be modified")
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		next, ok := folderName(req.Name)
		if !ok {
			s.sendError(w, http.StatusBadRequest, "invalid folder name")
			return
		}
		if isStandardMailbox(next) {
			s.sendError(w, http.StatusConflict, "a built-in folder with that name already exists")
			return
		}
		if err := s.mailDB.RenameMailbox(user, current, next); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to rename folder")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]string{"name": next})
	case http.MethodDelete:
		if err := s.mailDB.DeleteMailbox(user, current); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to delete folder")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
