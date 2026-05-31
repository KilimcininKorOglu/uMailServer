package api

import (
	"net/http"
	"strconv"
	"strings"
)

// searchHit is the client-facing search result. It exposes `id` (the client
// keys off this, not `item_id`) and the displayable message metadata.
type searchHit struct {
	ID             string   `json:"id"`
	ItemID         string   `json:"item_id"`
	ConversationID string   `json:"conversation_id"`
	From           string   `json:"from"`
	To             []string `json:"to"`
	Subject        string   `json:"subject"`
	Preview        string   `json:"preview"`
	Date           string   `json:"date"`
	Folder         string   `json:"folder"`
	Read           bool     `json:"read"`
	Starred        bool     `json:"starred"`
	HasAttachments bool     `json:"hasAttachments"`
	Score          float64  `json:"score"`
}

// Search handlers

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get user from context
	user := r.Context().Value("user")
	if user == nil {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Parse query parameters
	query := r.URL.Query().Get("q")
	if query == "" {
		s.sendError(w, http.StatusBadRequest, "missing query parameter 'q'")
		return
	}

	// Validate query length
	if len(query) > 500 {
		s.sendError(w, http.StatusBadRequest, "query too long (max 500 characters)")
		return
	}

	folder := r.URL.Query().Get("folder")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 20
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			if l > 100 {
				l = 100 // Cap at 100 to prevent resource exhaustion
			}
			limit = l
		}
	}

	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// Perform search
	if s.searchSvc == nil {
		s.sendError(w, http.StatusServiceUnavailable, "search service not available")
		return
	}

	userStr, ok := user.(string)
	if !ok {
		s.sendError(w, http.StatusUnauthorized, "invalid user context")
		return
	}

	// The full-text index can hold stale entries whose ids no longer resolve to
	// a stored message, which produced blank, unclickable results. Scan the
	// user's mailboxes directly so every hit carries real, resolvable metadata.
	if s.mailHandler == nil {
		s.sendJSON(w, http.StatusOK, map[string]interface{}{
			"query": query, "folder": folder, "emails": []searchHit{},
			"total": 0, "limit": limit, "offset": offset,
		})
		return
	}

	mailboxes := allMailboxes
	if folder != "" {
		internal := folderMap[strings.ToLower(folder)]
		if internal == "" {
			internal = folder
		}
		mailboxes = []string{internal}
	}

	q := strings.ToLower(query)
	all := make([]searchHit, 0)
	for _, mb := range mailboxes {
		mails, merr := s.mailHandler.getEmailsFromStorage(userStr, mb)
		if merr != nil {
			continue
		}
		for _, m := range mails {
			haystack := strings.ToLower(m.From + " " + m.Subject + " " + m.Body + " " + strings.Join(m.To, " "))
			if !strings.Contains(haystack, q) {
				continue
			}
			all = append(all, searchHit{
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

	// Apply offset/limit to the matches.
	total := len(all)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	paged := all[start:end]

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"query":  query,
		"folder": folder,
		"emails": paged,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}
