package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/umailserver/umailserver/internal/search"
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

	results, err := s.searchSvc.Search(search.MessageSearchOptions{
		User:   userStr,
		Folder: folder,
		Query:  query,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "search failed")
		return
	}

	// Shape results for the web client: expose `id` and backfill any display
	// fields the search index left empty from the message store.
	hits := make([]searchHit, 0, len(results))
	for _, res := range results {
		hit := searchHit{
			ID:             res.ItemID,
			ItemID:         res.ItemID,
			ConversationID: res.ConversationID,
			From:           res.From,
			Subject:        res.Subject,
			Preview:        res.Preview,
			Date:           res.Date,
			Folder:         res.Folder,
			HasAttachments: res.HasAttachment,
			Score:          res.Score,
		}
		if res.To != "" {
			hit.To = strings.Split(res.To, ",")
		}
		if (hit.From == "" || hit.Subject == "") && s.mailHandler != nil {
			if mailbox, _, _, found := s.mailHandler.findMessage(userStr, res.ItemID); found {
				if m, merr := s.mailHandler.getEmailFromStorage(userStr, mailbox, res.ItemID); merr == nil && m != nil {
					hit.From = m.From
					hit.To = m.To
					hit.Subject = m.Subject
					hit.Preview = m.Preview
					hit.Date = m.Date
					hit.Folder = m.Folder
					hit.Read = m.Read
					hit.Starred = m.Starred
					hit.HasAttachments = m.HasAttachments
				}
			}
		}
		hits = append(hits, hit)
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"query":  query,
		"folder": folder,
		"emails": hits,
		"total":  len(hits),
		"limit":  limit,
		"offset": offset,
	})
}
