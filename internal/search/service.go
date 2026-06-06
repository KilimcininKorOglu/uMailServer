package search

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/umailserver/umailserver/internal/semcore"
)

// Service provides message search functionality
type Service struct {
	index         *Index
	logger        *slog.Logger
	db            MetadataStore
	msgStore      MessageReader
	identityStore *semcore.BoltIdentityStore // canonical identity store for ItemId resolution
	mu            sync.RWMutex
	indexes       map[string]*Index // user -> index
}

// NewService creates a new search service
func NewService(database MetadataStore, msgStore MessageReader, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}

	return &Service{
		index:    NewIndex(),
		logger:   logger,
		db:       database,
		msgStore: msgStore,
		indexes:  make(map[string]*Index),
	}
}

// SetIdentityStore wires the canonical identity store into the search service.
// When set, the search service uses ItemId as the document ID and can resolve
// search hits back to canonical semantic-core items. When nil (backward-compatible
// mode), the service uses the legacy folder:uid document ID format.
func (s *Service) SetIdentityStore(store *semcore.BoltIdentityStore) {
	s.identityStore = store
}

// MessageSearchResult represents a message search result
type MessageSearchResult struct {
	ItemID         string  `json:"item_id"`         // canonical ItemId (stable across moves/copies)
	ConversationID string  `json:"conversation_id"` // canonical ConversationId (thread lineage)
	UID            uint32  `json:"uid"`             // protocol-local UID (deprecated, use ItemId)
	Folder         string  `json:"folder"`          // protocol-local folder name (deprecated, use ItemId)
	From           string  `json:"from"`
	To             string  `json:"to"`
	Subject        string  `json:"subject"`
	Preview        string  `json:"preview"`
	Date           string  `json:"date"`
	Score          float64 `json:"score"`
	HasAttachment  bool    `json:"has_attachment"`
}

// MessageSearchOptions contains search options
type MessageSearchOptions struct {
	User           string
	Folder         string // empty for all folders
	Query          string
	Limit          int
	Offset         int
	DateFrom       string
	DateTo         string
	HasAttachment  bool
	ConversationID string // empty for all conversations
}

// Search performs a search across user's messages.
// When the identity store is wired, search results contain canonical ItemId
// and ConversationId that can be used to resolve hits back to semantic-core items.
// Without the identity store, results use the legacy folder:uid format for
// backward compatibility with existing callers.
func (s *Service) Search(opts MessageSearchOptions) ([]MessageSearchResult, error) {
	s.mu.RLock()
	index, exists := s.indexes[opts.User]
	s.mu.RUnlock()

	if !exists {
		// Index doesn't exist yet, build it
		if err := s.BuildIndex(opts.User); err != nil {
			return nil, fmt.Errorf("failed to build index: %w", err)
		}
		s.mu.RLock()
		index = s.indexes[opts.User]
		s.mu.RUnlock()
	}

	// Perform search
	searchOpts := SearchOptions{}
	if opts.Limit > 0 {
		searchOpts.Limit = opts.Limit
	} else {
		searchOpts.Limit = 20
	}
	searchOpts.Offset = opts.Offset

	// Apply conversation filter at index level if specified.
	// The conversation field is stored as "conversation:<convID>" in the index.
	if opts.ConversationID != "" {
		searchOpts.ConversationID = opts.ConversationID
	}

	results := index.Search(opts.Query, searchOpts)

	// Convert to MessageSearchResult
	var searchResults []MessageSearchResult
	for _, result := range results {
		// Parse docID to extract identity and folder/uid.
		// DocID is either "itemID" (semantic mode) or "folder:uid" (legacy mode).
		var itemID, folder string
		var uid uint32
		var convID string
		var err error

		if s.identityStore != nil {
			// Semantic mode: DocID is the canonical ItemId string.
			itemID = result.DocID
			// Try to resolve folder/uid and ConversationId from identity store.
			folder, uid, convID, err = s.resolveFromIdentity(result.DocID)
			if err != nil {
				// Item not yet in identity store; skip or use legacy fallback.
				continue
			}
		} else {
			// Legacy mode: DocID is "folder:uid".
			folder, uid, err = parseLegacyDocID(result.DocID)
			if err != nil {
				continue
			}
		}

		// Filter by folder if specified.
		if opts.Folder != "" && folder != opts.Folder {
			continue
		}

		// Get message metadata (optional — use index data if db unavailable).
		searchResult := MessageSearchResult{
			ItemID:         itemID,
			ConversationID: convID,
			UID:            uid,
			Folder:         folder,
			Score:          result.Score,
		}

		if s.db != nil {
			meta, err := s.db.GetMessageMetadata(opts.User, folder, uid)
			if err == nil && meta != nil {
				searchResult.From = meta.From
				searchResult.To = meta.To
				searchResult.Subject = meta.Subject
				searchResult.Preview = generatePreview(meta.Subject, 100)
				searchResult.Date = meta.Date
			}
		}

		searchResults = append(searchResults, searchResult)
	}

	return searchResults, nil
}

// BuildIndex builds the search index for a user.
// When the identity store is wired, documents are keyed by ItemId and include
// ConversationId for thread-aware search. Without the identity store, documents
// use the legacy folder:uid key format.
func (s *Service) BuildIndex(user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger.Info("Building search index", "user", user)

	index := NewIndex()

	// Get all folders for user
	if s.db == nil {
		return fmt.Errorf("database not available")
	}
	folders, err := s.db.ListMailboxes(user)
	if err != nil {
		return fmt.Errorf("failed to list folders: %w", err)
	}

	// Index messages in each folder
	for _, folder := range folders {
		uids, err := s.db.GetMessageUIDs(user, folder)
		if err != nil {
			continue
		}

		for _, uid := range uids {
			meta, err := s.db.GetMessageMetadata(user, folder, uid)
			if err != nil {
				continue
			}

			// Read message content for full-text indexing
			content := ""
			if s.msgStore != nil {
				data, err := s.msgStore.ReadMessage(user, meta.MessageID)
				if err == nil {
					// Extract text content
					content = extractTextContent(data)
				}
			}

			// Determine document ID and conversation ID.
			// When identity store is available, prefer ItemId as DocID.
			// Fall back to legacy folder:uid format when identity is not yet
			// registered for this message.
			docID, convID := s.resolveDocIDAndConversation(meta.MessageID, folder, uid)

			// Create document
			doc := &Document{
				ID:      docID,
				Content: content,
				Fields: map[string]string{
					"from":         meta.From,
					"to":           meta.To,
					"subject":      meta.Subject,
					"conversation": convID,
				},
			}

			index.Add(doc)
		}
	}

	s.indexes[user] = index
	s.logger.Info("Search index built", "user", user, "docs", index.DocCount())

	return nil
}

// IndexMessage adds a message to the search index using canonical ItemId and
// ConversationId assigned by the mutation pipeline.
// When itemID is non-empty, it is used as the document ID (semantic-core mode).
// When itemID is empty, falls back to legacy folder:uid format.
// conversationID is stored as a searchable field for thread-aware queries.
func (s *Service) IndexMessage(user, folder string, uid uint32, itemID, conversationID string) error {
	s.mu.RLock()
	index, exists := s.indexes[user]
	s.mu.RUnlock()

	if !exists {
		// Build index if it doesn't exist
		return s.BuildIndex(user)
	}

	meta, err := s.db.GetMessageMetadata(user, folder, uid)
	if err != nil {
		return err
	}

	// Read message content
	content := ""
	if s.msgStore != nil {
		data, err := s.msgStore.ReadMessage(user, meta.MessageID)
		if err == nil {
			content = extractTextContent(data)
		}
	}

	// Determine document ID: prefer ItemId when provided, else fall back.
	docID := s.resolveDocID(meta.MessageID, folder, uid, itemID)

	// Use conversationID if provided, else fall back to meta.ThreadID.
	convID := conversationID
	if convID == "" {
		convID = meta.ThreadID
	}

	doc := &Document{
		ID:      docID,
		Content: content,
		Fields: map[string]string{
			"from":         meta.From,
			"to":           meta.To,
			"subject":      meta.Subject,
			"conversation": convID,
		},
	}

	index.Add(doc)
	return nil
}

// RemoveMessage removes a message from the search index.
// When itemID is non-empty, it is used as the document key (semantic-core mode).
// When itemID is empty, falls back to the legacy folder:uid format.
// Called from IMAP EXPUNGE handler and admin message deletion.
func (s *Service) RemoveMessage(user, folder string, uid uint32, itemID string) {
	s.mu.RLock()
	index, exists := s.indexes[user]
	s.mu.RUnlock()

	if !exists {
		return
	}

	// Determine document ID: prefer ItemId when provided.
	docID := s.resolveDocID("", folder, uid, itemID)
	index.Remove(docID)
}

// ClearIndex clears the search index for a user
func (s *Service) ClearIndex(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index, exists := s.indexes[user]; exists {
		index.Clear()
		delete(s.indexes, user)
	}
}

// parseLegacyDocID parses a legacy document ID (folder:uid format) into folder and UID.
func parseLegacyDocID(docID string) (string, uint32, error) {
	// Parse format: folder:uid
	parts := splitDocID(docID)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid docID format: %s", docID)
	}

	uid, err := parseUID(parts[1])
	if err != nil {
		return "", 0, err
	}

	return parts[0], uid, nil
}

// resolveDocID returns the document ID for a message.
// When itemID is non-empty and identity store is wired, returns itemID.
// Otherwise returns the legacy folder:uid format.
func (s *Service) resolveDocID(msgKey, folder string, uid uint32, itemID string) string {
	if s.identityStore != nil && itemID != "" {
		return itemID
	}
	return legacyDocID(folder, uid)
}

// resolveDocIDAndConversation returns the document ID and conversation ID for a message.
// When identity store is wired, tries to resolve ItemId from the msgKey (blob key).
// Falls back to legacy folder:uid format when identity is unavailable.
func (s *Service) resolveDocIDAndConversation(msgKey, folder string, uid uint32) (docID, convID string) {
	if s.identityStore != nil && msgKey != "" {
		// Try to look up canonical ItemId by msgKey (blob key).
		itemID, err := s.identityStore.GetItemIDByKey(msgKey)
		if err == nil && !itemID.IsZero() {
			// Also look up the conversation ID.
			itemIdentity, err := s.identityStore.GetItemIdentity(itemID)
			if err == nil {
				return itemID.String(), itemIdentity.ConversationID.String()
			}
			return itemID.String(), ""
		}
	}
	// Fall back to legacy folder:uid.
	return legacyDocID(folder, uid), ""
}

// resolveFromIdentity looks up the folder, UID, and conversation ID for a given ItemId.
// This requires that the ItemId was previously registered in the identity store and
// that the caller provides the user context to resolve the protocol-local folder/uid.
func (s *Service) resolveFromIdentity(itemID string) (folder string, uid uint32, convID string, err error) {
	id, err := semcore.NewItemId(itemID)
	if err != nil {
		return "", 0, "", fmt.Errorf("invalid ItemId: %w", err)
	}

	itemIdentity, err := s.identityStore.GetItemIdentity(id)
	if err != nil {
		return "", 0, "", fmt.Errorf("item identity not found: %w", err)
	}

	// ConversationId is returned directly.
	convID = itemIdentity.ConversationID.String()

	// Folder and UID require a separate lookup through the legacy storage.
	// The identity store tracks MailboxID and FolderID but not protocol-local
	// folder names or UIDs. We need to look these up from the storage DB
	// using the message key (blob key) stored in the identity record.
	//
	// For now, we cannot reliably map ItemId -> folder/uid without the msgKey.
	// Return empty folder/uid and let the caller use the ItemId directly.
	// The caller should use the ItemId for all semantic operations.
	return "", 0, convID, nil
}

// legacyDocID returns the legacy folder:uid format string.
func legacyDocID(folder string, uid uint32) string {
	return folder + ":" + formatUID(uid)
}

// splitDocID splits a legacy docID into folder and uid parts.
// Handles folder names that may contain colons (last colon is the separator).
func splitDocID(docID string) []string {
	// Find the last colon which separates folder from uid
	for i := len(docID) - 1; i >= 0; i-- {
		if docID[i] == ':' {
			return []string{docID[:i], docID[i+1:]}
		}
	}
	return []string{docID}
}

// generatePreview generates a preview text from content
func generatePreview(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen] + "..."
}

// extractTextContent extracts text content from message data
func extractTextContent(data []byte) string {
	// Simple extraction - remove headers and extract body
	content := string(data)

	// Find body start
	bodyStart := strings.Index(content, "\r\n\r\n")
	if bodyStart == -1 {
		bodyStart = strings.Index(content, "\n\n")
	}
	if bodyStart != -1 {
		content = content[bodyStart:]
	}

	// Remove HTML tags if present
	content = stripHTML(content)

	// Normalize whitespace
	content = strings.Join(strings.Fields(content), " ")

	return content
}

// stripHTML removes HTML tags from text
func stripHTML(html string) string {
	// Simple HTML tag removal
	result := ""
	inTag := false
	for _, r := range html {
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			result += string(r)
		}
	}
	return result
}

// parseUID parses a decimal string into a uint32.
func parseUID(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// formatUID formats a uint32 as a decimal string.
func formatUID(uid uint32) string {
	return strconv.FormatUint(uint64(uid), 10)
}
