package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/umailserver/umailserver/internal/carddav"
)

// ContactsHandler handles contact-related API requests via CardDAV storage
type ContactsHandler struct {
	dataDir string
	// store, when set, is the canonical semcore-backed contacts store so webmail
	// reads/writes the same contact data as EWS and CardDAV. When nil, the legacy
	// filesystem store rooted at dataDir is used.
	store carddav.Store
}

// SetStore wires the canonical contacts store (semcore-backed), unifying the
// webmail contacts list with the EWS/CardDAV source of truth.
func (h *ContactsHandler) SetStore(store carddav.Store) {
	h.store = store
}

// wireCollabContactsStore points the contacts handler at the canonical
// semcore-backed contacts store when both the handler and the semcore store are
// present, so webmail shares one source of truth with EWS and CardDAV.
func (s *Server) wireCollabContactsStore() {
	if s.semStore == nil || s.contactsHandler == nil {
		return
	}
	s.contactsHandler.SetStore(carddav.NewCollabStore(s.semStore.Collaboration(), s.semStore.Identity()))
}

// Contact represents a contact in the API response
type Contact struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Email     string   `json:"email"`
	Phone     string   `json:"phone,omitempty"`
	Company   string   `json:"company,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	DisplayAs string   `json:"display_as,omitempty"`
	// IsGroup marks this contact as a distribution list (vCard KIND:group)
	IsGroup bool     `json:"is_group,omitempty"`
	Members []string `json:"members,omitempty"` // member emails for distribution lists
}

// ContactsResponse is the API response structure for contacts list
type ContactsResponse struct {
	Contacts []Contact `json:"contacts"`
	Total    int       `json:"total"`
}

// NewContactsHandler creates a new contacts handler with the server's data directory
func NewContactsHandler(dataDir string) *ContactsHandler {
	return &ContactsHandler{dataDir: dataDir}
}

// getStorage returns the contacts store. When the canonical semcore-backed
// store is wired (production), it is returned so webmail shares one source of
// truth with EWS and CardDAV. Otherwise it falls back to the filesystem store.
func (h *ContactsHandler) getStorage() carddav.Store {
	if h.store != nil {
		return h.store
	}
	// CardDAV storage path is {dataDir}/carddav (NewStorage adds carddav subdirectory)
	carddavDataDir := filepath.Join(h.dataDir, "carddav")
	return carddav.NewStorage(carddavDataDir)
}

// handleContactsList handles GET /api/v1/contacts
func (h *ContactsHandler) handleContactsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok || userEmail == "" {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// Get search query for filtering
	query := r.URL.Query().Get("q")

	// Get addressbooks for the user
	addressbooks, err := h.getAddressbooks(userEmail)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to get addressbooks")
		return
	}

	contacts := []Contact{}
	for _, ab := range addressbooks {
		// Get contacts from each addressbook
		abContacts, err := h.getContactsFromAddressbook(userEmail, ab.ID)
		if err != nil {
			continue
		}
		contacts = append(contacts, abContacts...)
	}

	// Filter by search query if provided
	if query != "" {
		queryLower := strings.ToLower(query)
		filtered := []Contact{}
		for _, c := range contacts {
			if strings.Contains(strings.ToLower(c.Name), queryLower) ||
				strings.Contains(strings.ToLower(c.Email), queryLower) {
				filtered = append(filtered, c)
			}
		}
		contacts = filtered
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ContactsResponse{
		Contacts: contacts,
		Total:    len(contacts),
	}); err != nil {
		fmt.Printf("ERROR: failed to encode contacts response: %v\n", err)
	}
}

// handleContactCreate handles POST /api/v1/contacts
func (h *ContactsHandler) handleContactCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok || userEmail == "" {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req struct {
		Name    string   `json:"name"`
		Email   string   `json:"email"`
		Phone   string   `json:"phone,omitempty"`
		Company string   `json:"company,omitempty"`
		IsGroup bool     `json:"is_group,omitempty"`
		Members []string `json:"members,omitempty"`
	}

	if err := decodeJSON(r, &req); err != nil {
		h.sendError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	if req.Email == "" && !req.IsGroup {
		h.sendError(w, http.StatusBadRequest, "Email is required")
		return
	}

	// Ensure default addressbook exists
	if err := h.ensureDefaultAddressbook(userEmail); err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to create addressbook")
		return
	}

	// Build vCard data
	vcardData := h.buildVCard(req)
	contactID := extractVCardUID(vcardData)

	// Save contact through CardDAV storage
	storage := h.getStorage()
	if storage != nil {
		contact := &carddav.Contact{
			UID:      contactID,
			Modified: time.Now(),
			Created:  time.Now(),
		}
		_ = storage.SaveContact(userEmail, "default", contact, vcardData)
	}

	// Return created contact
	contact := Contact{
		ID:      contactID,
		Name:    req.Name,
		Email:   req.Email,
		Phone:   req.Phone,
		Company: req.Company,
		Labels:  []string{},
		IsGroup: req.IsGroup,
		Members: req.Members,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"contact": contact,
		"status":  "created",
	}); err != nil {
		fmt.Printf("ERROR: failed to encode contact response: %v\n", err)
	}
}

// handleContactUpdate handles PUT /api/v1/contacts/{id}
func (h *ContactsHandler) handleContactUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok || userEmail == "" {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	contactID := r.URL.Path[len("/api/v1/contacts/"):]
	if contactID == "" {
		h.sendError(w, http.StatusBadRequest, "Contact ID required")
		return
	}

	var req struct {
		Name    string   `json:"name"`
		Email   string   `json:"email"`
		Phone   string   `json:"phone,omitempty"`
		Company string   `json:"company,omitempty"`
		IsGroup bool     `json:"is_group,omitempty"`
		Members []string `json:"members,omitempty"`
	}

	if err := decodeJSON(r, &req); err != nil {
		h.sendError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	// Build updated vCard
	vcardData := h.buildVCard(req)

	// Update through CardDAV storage
	storage := h.getStorage()
	if storage != nil {
		contact := &carddav.Contact{
			UID:      contactID,
			Modified: time.Now(),
			Created:  time.Now(),
		}
		_ = storage.SaveContact(userEmail, "default", contact, vcardData)
	}

	// Return updated contact
	contact := Contact{
		ID:      contactID,
		Name:    req.Name,
		Email:   req.Email,
		Phone:   req.Phone,
		Company: req.Company,
		Labels:  []string{},
		IsGroup: req.IsGroup,
		Members: req.Members,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"contact": contact,
		"status":  "updated",
	}); err != nil {
		fmt.Printf("ERROR: failed to encode contact response: %v\n", err)
	}
}

// handleContactDelete handles DELETE /api/v1/contacts/{id}
func (h *ContactsHandler) handleContactDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok || userEmail == "" {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	contactID := r.URL.Path[len("/api/v1/contacts/"):]
	if contactID == "" {
		h.sendError(w, http.StatusBadRequest, "Contact ID required")
		return
	}

	// Delete from CardDAV storage
	storage := h.getStorage()
	if storage != nil {
		_ = storage.DeleteContact(userEmail, "default", contactID)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status": "deleted",
		"id":     contactID,
	}); err != nil {
		fmt.Printf("ERROR: failed to encode delete response: %v\n", err)
	}
}

// handleContactsExport serves all contacts as a multi-vCard .vcf file.
// GET /api/v1/contacts/export
func (h *ContactsHandler) handleContactsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok || userEmail == "" {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	if err := h.ensureDefaultAddressbook(userEmail); err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to access contacts")
		return
	}

	contacts, err := h.getContactsFromAddressbook(userEmail, "default")
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to load contacts")
		return
	}

	var buf strings.Builder
	for _, c := range contacts {
		buf.WriteString(h.buildVCard(struct {
			Name    string   `json:"name"`
			Email   string   `json:"email"`
			Phone   string   `json:"phone,omitempty"`
			Company string   `json:"company,omitempty"`
			IsGroup bool     `json:"is_group,omitempty"`
			Members []string `json:"members,omitempty"`
		}{Name: c.Name, Email: c.Email, Phone: c.Phone, Company: c.Company, IsGroup: c.IsGroup, Members: c.Members}))
		buf.WriteString("\r\n")
	}

	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"contacts.vcf\"")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
	if _, err := w.Write([]byte(buf.String())); err != nil {
		fmt.Printf("ERROR: failed to write contacts export: %v\n", err)
	}
}

// sendError sends a JSON error response
func (h *ContactsHandler) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		fmt.Printf("ERROR: failed to encode error response: %v\n", err)
	}
}

// getAddressbooks retrieves addressbooks for a user from CardDAV storage
func (h *ContactsHandler) getAddressbooks(userEmail string) ([]*carddav.Addressbook, error) {
	storage := h.getStorage()
	if storage == nil {
		return []*carddav.Addressbook{}, fmt.Errorf("storage not available")
	}

	addressbooks, err := storage.GetAddressbooks(userEmail)
	if err != nil {
		return []*carddav.Addressbook{}, err
	}

	// Always include the "default" addressbook, which is where webmail saves
	// contacts. It may hold .vcf files without registered metadata (e.g. when
	// the user already had other addressbooks), so listing must still read it.
	hasDefault := false
	for _, ab := range addressbooks {
		if ab.ID == "default" {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		addressbooks = append(addressbooks, &carddav.Addressbook{ID: "default", Name: "Contacts"})
	}

	return addressbooks, nil
}

// getContactsFromAddressbook retrieves contacts from a specific addressbook
func (h *ContactsHandler) getContactsFromAddressbook(userEmail, addressbookID string) ([]Contact, error) {
	storage := h.getStorage()
	if storage == nil {
		return []Contact{}, fmt.Errorf("storage not available")
	}

	vcardDataList, err := storage.GetContacts(userEmail, addressbookID)
	if err != nil {
		return []Contact{}, err
	}

	contacts := []Contact{}
	for _, vcardData := range vcardDataList {
		contact := h.parseVCard(vcardData)
		if contact.Email != "" {
			contacts = append(contacts, contact)
		}
	}

	return contacts, nil
}

// ensureDefaultAddressbook creates a default addressbook if none exists
func (h *ContactsHandler) ensureDefaultAddressbook(userEmail string) error {
	storage := h.getStorage()
	if storage == nil {
		return fmt.Errorf("storage not available")
	}

	addressbooks, err := storage.GetAddressbooks(userEmail)
	if err != nil {
		return err
	}

	if len(addressbooks) == 0 {
		defaultAB := &carddav.Addressbook{
			ID:   "default",
			Name: "Contacts",
		}
		return storage.CreateAddressbook(userEmail, defaultAB)
	}

	return nil
}

// parseVCard parses a vCard string into a Contact struct
func (h *ContactsHandler) parseVCard(vcardData string) Contact {
	contact := Contact{
		ID:     extractVCardUID(vcardData),
		Labels: []string{},
	}

	lines := strings.Split(vcardData, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse FN (Formatted Name)
		if strings.HasPrefix(line, "FN:") || strings.HasPrefix(line, "FN;") {
			contact.Name = extractVCardValue(line)
			if contact.DisplayAs == "" {
				contact.DisplayAs = contact.Name
			}
		}

		// Parse N (Name components) - format: LastName;FirstName;;;
		if strings.HasPrefix(line, "N:") || strings.HasPrefix(line, "N;") {
			name := extractVCardValue(line)
			parts := strings.Split(name, ";")
			if len(parts) >= 2 {
				lastName := parts[0]
				firstName := parts[1]
				if contact.Name == "" {
					contact.Name = strings.TrimSpace(firstName + " " + lastName)
				}
			}
		}

		// Parse EMAIL
		if strings.HasPrefix(line, "EMAIL:") || strings.HasPrefix(line, "EMAIL;") {
			contact.Email = extractVCardValue(line)
		}

		// Parse TEL
		if strings.HasPrefix(line, "TEL:") || strings.HasPrefix(line, "TEL;") {
			contact.Phone = extractVCardValue(line)
		}

		// Parse ORG
		if strings.HasPrefix(line, "ORG:") || strings.HasPrefix(line, "ORG;") {
			contact.Company = extractVCardValue(line)
		}

		// Parse KIND (RFC 6477) — distribution list marker
		if strings.HasPrefix(line, "KIND:") || strings.HasPrefix(line, "KIND;") {
			val := extractVCardValue(line)
			contact.IsGroup = val == "group"
		}

		// Parse MEMBER (RFC 6477) — distribution list member UIDs/emails
		if strings.HasPrefix(line, "MEMBER:") || strings.HasPrefix(line, "MEMBER;") {
			contact.Members = append(contact.Members, extractVCardValue(line))
		}
	}

	// If no name, use email as name
	if contact.Name == "" {
		contact.Name = contact.Email
	}

	return contact
}

// buildVCard creates vCard data from contact or distribution list request
func (h *ContactsHandler) buildVCard(req struct {
	Name    string   `json:"name"`
	Email   string   `json:"email"`
	Phone   string   `json:"phone,omitempty"`
	Company string   `json:"company,omitempty"`
	IsGroup bool     `json:"is_group,omitempty"`
	Members []string `json:"members,omitempty"`
}) string {
	uid := uuid.New().String()
	nameParts := strings.SplitN(req.Name, " ", 2)
	firstName := ""
	lastName := ""
	if len(nameParts) >= 2 {
		firstName = nameParts[0]
		lastName = nameParts[1]
	} else {
		firstName = req.Name
	}

	var sb strings.Builder
	sb.WriteString("BEGIN:VCARD\r\n")
	sb.WriteString("VERSION:3.0\r\n")
	sb.WriteString(fmt.Sprintf("UID:%s\r\n", uid))
	sb.WriteString(fmt.Sprintf("FN:%s\r\n", req.Name))
	sb.WriteString(fmt.Sprintf("N:%s;%s;;;\r\n", lastName, firstName))
	if req.IsGroup {
		sb.WriteString("KIND:group\r\n")
		for _, member := range req.Members {
			sb.WriteString(fmt.Sprintf("MEMBER:%s\r\n", member))
		}
	} else {
		sb.WriteString(fmt.Sprintf("EMAIL:%s\r\n", req.Email))
		if req.Phone != "" {
			sb.WriteString(fmt.Sprintf("TEL:%s\r\n", req.Phone))
		}
		if req.Company != "" {
			sb.WriteString(fmt.Sprintf("ORG:%s\r\n", req.Company))
		}
	}
	sb.WriteString("END:VCARD\r\n")

	return sb.String()
}

// extractVCardUID extracts the UID from vCard data
func extractVCardUID(vcardData string) string {
	lines := strings.Split(vcardData, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "UID:") {
			return strings.TrimPrefix(line, "UID:")
		}
	}
	return uuid.New().String()
}

// extractVCardValue extracts the value from a vCard property line
func extractVCardValue(line string) string {
	// Handle parameters (e.g., EMAIL;TYPE=WORK:value)
	if idx := strings.Index(line, ":"); idx != -1 {
		return line[idx+1:]
	}
	return ""
}
