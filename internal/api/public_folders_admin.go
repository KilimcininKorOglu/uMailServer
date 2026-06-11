package api

import (
	"net/http"
	"strings"

	"github.com/umailserver/umailserver/internal/storage"
)

// publicFolderGrant is one ACL entry on a public folder, rendered for the admin UI.
type publicFolderGrant struct {
	Grantee string `json:"grantee"`
	Rights  string `json:"rights"`
}

// publicFolderInfo is a public folder with its full grant list.
type publicFolderInfo struct {
	Name   string              `json:"name"`
	Grants []publicFolderGrant `json:"grants"`
}

// publicOwnerForDomain validates that the domain exists and returns the reserved
// synthetic owner that hosts its public-folder tree. The owner is never a real
// account; it only keys the shared store and its ACL entries.
func (s *Server) publicOwnerForDomain(domain string) (string, bool) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return "", false
	}
	if _, err := s.db.GetDomain(domain); err != nil {
		return "", false
	}
	return storage.PublicFolderOwner(domain), true
}

// handlePublicFoldersAdmin manages the per-domain public-folder tree (super-admin
// only). It lists, creates, and deletes folders under the domain's reserved
// public owner, reusing the same mailbox store as user folders.
//
//	GET    /api/v1/admin/public-folders?domain=<d>          -> {owner, folders:[{name,grants}]}
//	POST   /api/v1/admin/public-folders {domain,name}       -> {name}
//	DELETE /api/v1/admin/public-folders?domain=<d>&name=<n> -> {status:"deleted"}
func (s *Server) handlePublicFoldersAdmin(w http.ResponseWriter, r *http.Request) {
	if s.mailDB == nil || s.db == nil {
		s.sendError(w, http.StatusInternalServerError, "store not available")
		return
	}

	switch r.Method {
	case http.MethodGet:
		owner, ok := s.publicOwnerForDomain(r.URL.Query().Get("domain"))
		if !ok {
			s.sendError(w, http.StatusBadRequest, "unknown domain")
			return
		}
		names, err := s.mailDB.ListMailboxes(owner)
		if err != nil {
			names = nil
		}
		folders := make([]publicFolderInfo, 0, len(names))
		for _, name := range names {
			// The synthetic owner is auto-provisioned with the built-in mailboxes
			// (INBOX, Sent, ...); they are meaningless for a public tree, so only
			// the admin-created named folders are surfaced.
			if isStandardMailbox(name) {
				continue
			}
			entries, aerr := s.mailDB.ListACL(owner, name)
			if aerr != nil {
				entries = nil
			}
			grants := make([]publicFolderGrant, 0, len(entries))
			for _, e := range entries {
				grants = append(grants, publicFolderGrant{Grantee: e.Grantee, Rights: e.Rights.String()})
			}
			folders = append(folders, publicFolderInfo{Name: name, Grants: grants})
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"owner": owner, "folders": folders})

	case http.MethodPost:
		var req struct {
			Domain string `json:"domain"`
			Name   string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		owner, ok := s.publicOwnerForDomain(req.Domain)
		if !ok {
			s.sendError(w, http.StatusBadRequest, "unknown domain")
			return
		}
		name, valid := folderName(req.Name)
		if !valid {
			s.sendError(w, http.StatusBadRequest, "invalid folder name")
			return
		}
		if isStandardMailbox(name) {
			s.sendError(w, http.StatusConflict, "that name is reserved for a built-in mailbox")
			return
		}
		if err := s.mailDB.CreateMailbox(owner, name); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to create folder")
			return
		}
		s.sendJSON(w, http.StatusCreated, map[string]string{"name": name})

	case http.MethodDelete:
		owner, ok := s.publicOwnerForDomain(r.URL.Query().Get("domain"))
		if !ok {
			s.sendError(w, http.StatusBadRequest, "unknown domain")
			return
		}
		name, valid := folderName(r.URL.Query().Get("name"))
		if !valid {
			s.sendError(w, http.StatusBadRequest, "invalid folder name")
			return
		}
		// Drop every grant first so no orphaned ACL keys survive the folder.
		if err := s.mailDB.DeleteACL(owner, name, ""); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to clear folder access")
			return
		}
		if err := s.mailDB.DeleteMailbox(owner, name); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to delete folder")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handlePublicFolderACL sets or clears a grant on a public folder (super-admin
// only). The grantee is the reserved "anyone" token for org-wide access or a
// specific user email; empty rights removes the grant.
//
//	PUT /api/v1/admin/public-folders/acl {domain,name,grantee,rights}
func (s *Server) handlePublicFolderACL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.mailDB == nil || s.db == nil {
		s.sendError(w, http.StatusInternalServerError, "store not available")
		return
	}

	var req struct {
		Domain  string `json:"domain"`
		Name    string `json:"name"`
		Grantee string `json:"grantee"`
		Rights  string `json:"rights"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	owner, ok := s.publicOwnerForDomain(req.Domain)
	if !ok {
		s.sendError(w, http.StatusBadRequest, "unknown domain")
		return
	}
	name, valid := folderName(req.Name)
	if !valid {
		s.sendError(w, http.StatusBadRequest, "invalid folder name")
		return
	}
	grantee := strings.ToLower(strings.TrimSpace(req.Grantee))
	if grantee == "" {
		s.sendError(w, http.StatusBadRequest, "grantee required")
		return
	}
	// A specific grantee must be a real address in the same domain; "anyone" is
	// the only allowed non-account token (org-wide access).
	if grantee != storage.ACLAnyone {
		if _, dom := parseEmail(grantee); dom == "" || storage.PublicFolderOwner(dom) != owner {
			s.sendError(w, http.StatusBadRequest, "grantee must be anyone or an address in the domain")
			return
		}
	}
	rights, err := storage.ParseACLRights(req.Rights)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid rights")
		return
	}
	grantingUser, _ := r.Context().Value("user").(string) //nolint:errcheck // optional audit attribution; empty granter is acceptable
	if err := s.mailDB.SetACL(owner, name, grantee, rights, grantingUser); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to update access")
		return
	}
	s.sendJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
