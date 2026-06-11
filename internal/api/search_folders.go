package api

import (
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// search_folders.go exposes persistent saved-query folders (MAPI search
// folders) to the webmail. The definition is the same canonical
// semcore.SearchFolderDef the EWS surface stores, so a search folder created in
// Outlook and one created here are the same object; only the result execution
// differs per surface (here it scans the stored mailboxes and applies the
// definition's Matches, mirroring the full-text search the webmail already uses).

// searchFolderDTO is the webmail wire shape for a saved search: an id, a display
// name, and the saved criteria.
type searchFolderDTO struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	From          string   `json:"from,omitempty"`
	Subject       string   `json:"subject,omitempty"`
	Body          string   `json:"body,omitempty"`
	DateFrom      string   `json:"date_from,omitempty"`
	DateTo        string   `json:"date_to,omitempty"`
	HasAttachment *bool    `json:"has_attachment,omitempty"`
	BaseFolders   []string `json:"base_folders,omitempty"`
}

// defFromDTO builds a canonical definition from a request DTO.
func defFromDTO(d searchFolderDTO) *semcore.SearchFolderDef {
	return &semcore.SearchFolderDef{
		From:          strings.TrimSpace(d.From),
		Subject:       strings.TrimSpace(d.Subject),
		Body:          strings.TrimSpace(d.Body),
		DateFrom:      strings.TrimSpace(d.DateFrom),
		DateTo:        strings.TrimSpace(d.DateTo),
		HasAttachment: d.HasAttachment,
		BaseFolders:   d.BaseFolders,
	}
}

// dtoFromDef builds a wire DTO from a stored search folder.
func dtoFromDef(id, name string, def *semcore.SearchFolderDef) searchFolderDTO {
	dto := searchFolderDTO{ID: id, Name: name}
	if def != nil {
		dto.From = def.From
		dto.Subject = def.Subject
		dto.Body = def.Body
		dto.DateFrom = def.DateFrom
		dto.DateTo = def.DateTo
		dto.HasAttachment = def.HasAttachment
		dto.BaseFolders = def.BaseFolders
	}
	return dto
}

// handleSearchFolders lists and creates the user's search folders.
// GET/POST /api/v1/search-folders.
func (s *Server) handleSearchFolders(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.semStore == nil {
		s.sendError(w, http.StatusInternalServerError, "search folders not available")
		return
	}
	identity := s.semStore.Identity()

	switch r.Method {
	case http.MethodGet:
		recs, err := identity.ListSearchFolders(user)
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to list search folders")
			return
		}
		out := make([]searchFolderDTO, 0, len(recs))
		for _, rec := range recs {
			name, err := identity.FolderNameByID(user, rec.FolderID)
			if err != nil {
				continue
			}
			out = append(out, dtoFromDef(rec.FolderID.String(), name, rec.SearchDefinition))
		}
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"search_folders": out})

	case http.MethodPost:
		var req searchFolderDTO
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		name, valid := folderName(req.Name)
		if !valid {
			s.sendError(w, http.StatusBadRequest, "invalid search folder name")
			return
		}
		if _, err := identity.EnsureMailboxId(user); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to resolve mailbox")
			return
		}
		folderID, err := identity.EnsureFolderId(user, name, "")
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to create search folder")
			return
		}
		if err := identity.SetFolderSearchDefinition(folderID, defFromDTO(req)); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to save search folder")
			return
		}
		s.sendJSON(w, http.StatusCreated, dtoFromDef(folderID.String(), name, defFromDTO(req)))

	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSearchFolderPath updates, deletes, or runs a single search folder.
// PUT/DELETE /api/v1/search-folders/{id}; GET /api/v1/search-folders/{id}/results.
func (s *Server) handleSearchFolderPath(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.semStore == nil {
		s.sendError(w, http.StatusInternalServerError, "search folders not available")
		return
	}
	identity := s.semStore.Identity()

	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/search-folders/")
	idStr, action, _ := strings.Cut(rest, "/")
	folderID, err := semcore.NewFolderId(idStr)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid search folder id")
		return
	}

	// Resolve and authorize: the folder must exist, belong to the caller, and be
	// a search folder.
	rec, err := identity.GetFolderByID(folderID)
	if err != nil || rec == nil {
		s.sendError(w, http.StatusNotFound, "search folder not found")
		return
	}
	if owner := strings.TrimPrefix(rec.MailboxID.String(), "e:"); owner != user {
		s.sendError(w, http.StatusNotFound, "search folder not found")
		return
	}
	if rec.SearchDefinition == nil {
		s.sendError(w, http.StatusNotFound, "search folder not found")
		return
	}

	if action == "results" {
		if r.Method != http.MethodGet {
			s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.runSearchFolder(w, user, rec.SearchDefinition)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req searchFolderDTO
		if err := decodeJSON(r, &req); err != nil {
			s.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := identity.SetFolderSearchDefinition(folderID, defFromDTO(req)); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to update search folder")
			return
		}
		name, err := identity.FolderNameByID(user, folderID)
		if err != nil {
			name = req.Name
		}
		s.sendJSON(w, http.StatusOK, dtoFromDef(folderID.String(), name, defFromDTO(req)))

	case http.MethodDelete:
		if err := identity.DeleteFolder(folderID); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to delete search folder")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// runSearchFolder evaluates a search folder's definition over its scope and
// returns the matching messages in the same envelope as /api/v1/search. The
// scope is the definition's base folders, or every standard and custom mailbox
// when none are named.
func (s *Server) runSearchFolder(w http.ResponseWriter, user string, def *semcore.SearchFolderDef) {
	if s.mailHandler == nil {
		s.sendJSON(w, http.StatusOK, map[string]interface{}{"emails": []searchHit{}, "total": 0})
		return
	}

	mailboxes := def.BaseFolders
	if len(mailboxes) == 0 {
		mailboxes = append(mailboxes, allMailboxes...)
		if s.mailDB != nil {
			if custom, err := s.mailDB.ListMailboxes(user); err == nil {
				for _, c := range custom {
					if !isStandardMailbox(c) {
						mailboxes = append(mailboxes, c)
					}
				}
			}
		}
	}

	hits := make([]searchHit, 0)
	for _, mb := range mailboxes {
		mails, err := s.mailHandler.getEmailsFromStorage(user, mb)
		if err != nil {
			continue
		}
		for _, m := range mails {
			if !def.Matches(m.From, m.Subject, m.Body, parseMailDate(m.Date), m.HasAttachments) {
				continue
			}
			hits = append(hits, searchHit{
				ID:             m.ID,
				ItemID:         m.ID,
				From:           m.From,
				To:             m.To,
				Subject:        m.Subject,
				Preview:        m.Preview,
				Date:           m.Date,
				Folder:         m.Folder,
				Read:           m.Read,
				Starred:        m.Starred,
				HasAttachments: m.HasAttachments,
			})
		}
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"emails": hits,
		"total":  len(hits),
	})
}

// parseMailDate parses a stored message date into a time, tolerating the common
// header and ISO formats. A zero time is returned when the date is absent or
// unparseable, which a date-bounded definition treats as out of range.
func parseMailDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := mail.ParseDate(s); err == nil {
		return t
	}
	for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
