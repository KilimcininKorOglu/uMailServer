package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/mail"
	"path"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// NotesHandler bridges the webmail REST surface to Outlook-style notes. Notes are
// IPM.StickyNote messages kept in the "Notes" mail folder — the same model EWS
// uses — so a note created in webmail is the SAME record EWS/IMAP/JMAP see. To
// keep all surfaces consistent, a create writes the blob store, the semcore
// identity store (read by EWS), AND the IMAP mailstore index (read by
// IMAP/JMAP/webmail); a delete removes it from all three.
type NotesHandler struct {
	msgStore *storage.MessageStore
	mailDB   *storage.Database
	identity *semcore.BoltIdentityStore
	pipe     *semcore.MutationPipeline
}

const notesMailbox = "Notes"

// NewNotesHandler creates an unwired notes handler. SetStores must be called
// before it can serve requests.
func NewNotesHandler() *NotesHandler {
	return &NotesHandler{}
}

// SetStores wires the message blob store, the IMAP mailstore index, the semcore
// identity store, and a mutation pipeline so notes round-trip across protocols.
func (h *NotesHandler) SetStores(msgStore *storage.MessageStore, mailDB *storage.Database, identity *semcore.BoltIdentityStore, pipe *semcore.MutationPipeline) {
	h.msgStore = msgStore
	h.mailDB = mailDB
	h.identity = identity
	h.pipe = pipe
}

func (h *NotesHandler) ready() bool {
	return h.msgStore != nil && h.mailDB != nil && h.identity != nil && h.pipe != nil
}

// NoteDTO is the JSON projection of a note exchanged with webmail.
type NoteDTO struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Created string `json:"created,omitempty"`
	Updated string `json:"updated,omitempty"`
}

func (h *NotesHandler) sendJSON(w http.ResponseWriter, code int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return
	}
}

func (h *NotesHandler) sendError(w http.ResponseWriter, code int, msg string) {
	h.sendJSON(w, code, map[string]string{"error": msg})
}

// handleNotes lists (GET) or creates (POST) notes in the Notes folder.
func (h *NotesHandler) handleNotes(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !h.ready() {
		h.sendError(w, http.StatusServiceUnavailable, "notes storage not available")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.sendJSON(w, http.StatusOK, map[string]interface{}{"notes": h.listNotes(user)})
	case http.MethodPost:
		var dto NoteDTO
		if err := decodeJSON(r, &dto); err != nil {
			h.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(dto.Title) == "" && strings.TrimSpace(dto.Body) == "" {
			h.sendError(w, http.StatusBadRequest, "title or body is required")
			return
		}
		created, err := h.createNote(user, dto.Title, dto.Body)
		if err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to create note")
			return
		}
		h.sendJSON(w, http.StatusCreated, created)
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleNoteDetail updates (PUT) or deletes (DELETE) one note by id.
func (h *NotesHandler) handleNoteDetail(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !h.ready() {
		h.sendError(w, http.StatusServiceUnavailable, "notes storage not available")
		return
	}
	id := path.Base(r.URL.Path)
	if id == "" || id == "notes" {
		h.sendError(w, http.StatusBadRequest, "note id required")
		return
	}

	switch r.Method {
	case http.MethodPut:
		var dto NoteDTO
		if err := decodeJSON(r, &dto); err != nil {
			h.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// An edit replaces the note's content: remove the old record, then create
		// a fresh one (the note's content hash, and thus its id, changes).
		h.deleteNote(user, id)
		updated, err := h.createNote(user, dto.Title, dto.Body)
		if err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to update note")
			return
		}
		h.sendJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		if !h.deleteNote(user, id) {
			h.sendError(w, http.StatusNotFound, "note not found")
			return
		}
		h.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// listNotes reads the Notes folder from the IMAP mailstore index — the same
// index EWS-created notes are mirrored into — and projects each message onto a
// note DTO.
func (h *NotesHandler) listNotes(user string) []NoteDTO {
	uids, err := h.mailDB.GetMessageUIDs(user, notesMailbox)
	if err != nil {
		return []NoteDTO{}
	}
	notes := make([]NoteDTO, 0, len(uids))
	for _, uid := range uids {
		meta, err := h.mailDB.GetMessageMetadata(user, notesMailbox, uid)
		if err != nil || meta == nil {
			continue
		}
		raw, err := h.msgStore.ReadMessage(user, meta.MessageID)
		if err != nil {
			continue
		}
		title, body := parseNoteMessage(raw)
		if title == "" {
			title = meta.Subject
		}
		notes = append(notes, NoteDTO{
			ID:      meta.MessageID,
			Title:   title,
			Body:    body,
			Created: meta.InternalDate.Format(time.RFC3339),
			Updated: meta.InternalDate.Format(time.RFC3339),
		})
	}
	return notes
}

// createNote writes a new IPM.StickyNote message to the Notes folder across the
// blob store, the semcore identity store (EWS visibility), and the IMAP
// mailstore index (IMAP/JMAP/webmail visibility).
func (h *NotesHandler) createNote(user, title, body string) (NoteDTO, error) {
	raw := buildNoteMIME(user, title, body)
	blobKey, err := h.msgStore.StoreMessage(user, raw)
	if err != nil {
		return NoteDTO{}, err
	}

	mboxID, err := h.identity.EnsureMailboxId(user)
	if err != nil {
		return NoteDTO{}, err
	}
	folderID, err := h.identity.EnsureFolderId(user, notesMailbox, "notes")
	if err != nil {
		return NoteDTO{}, err
	}
	now := time.Now()
	if _, err := h.pipe.MutateItem(&semcore.MutationInput{
		MailboxID:    mboxID,
		FolderID:     folderID,
		RawMessage:   raw,
		InternalDate: now,
		Actor:        user,
		Email:        user,
		Source:       semcore.MutationSourceAPI,
		IsRead:       true,
	}); err != nil {
		return NoteDTO{}, err
	}

	uid, err := h.mailDB.GetNextUID(user, notesMailbox)
	if err != nil {
		return NoteDTO{}, err
	}
	meta := &storage.MessageMetadata{
		MessageID:    blobKey,
		UID:          uid,
		Flags:        []string{"\\Seen"},
		InternalDate: now,
		Size:         int64(len(raw)),
		Subject:      title,
		From:         user,
		Date:         now.UTC().Format(time.RFC1123Z),
	}
	if err := h.mailDB.StoreMessageMetadata(user, notesMailbox, uid, meta); err != nil {
		return NoteDTO{}, err
	}

	return NoteDTO{
		ID:      blobKey,
		Title:   title,
		Body:    body,
		Created: now.Format(time.RFC3339),
		Updated: now.Format(time.RFC3339),
	}, nil
}

// deleteNote removes a note (identified by its blob key) from the IMAP mailstore
// index and the semcore identity store so it disappears from every surface.
// Returns false if no matching note was found.
func (h *NotesHandler) deleteNote(user, id string) bool {
	found := false

	// IMAP mailstore index.
	if uids, err := h.mailDB.GetMessageUIDs(user, notesMailbox); err == nil {
		for _, uid := range uids {
			meta, err := h.mailDB.GetMessageMetadata(user, notesMailbox, uid)
			if err != nil || meta == nil || meta.MessageID != id {
				continue
			}
			if err := h.mailDB.DeleteMessage(user, notesMailbox, uid); err == nil {
				found = true
			}
			break
		}
	}

	// semcore identity store (so EWS GetItem/FindItem no longer surfaces it).
	// Resolve the notes folder by role so it matches the folder EWS uses even if
	// its stored name differs.
	if folderID, err := h.identity.EnsureFolderId(user, notesMailbox, "notes"); err == nil {
		if items, err := h.identity.ListItemIdentitiesByFolder(folderID); err == nil {
			for _, it := range items {
				if it.MsgKey == id {
					if err := h.identity.DeleteItemIdentity(it.ItemID); err == nil {
						found = true
					}
					break
				}
			}
		}
	}

	return found
}

// buildNoteMIME builds the RFC 5322 message for an Outlook note: a plain-text
// body tagged with the IPM.StickyNote message class, matching the EWS note
// representation so the two surfaces share one wire form.
func buildNoteMIME(from, title, body string) []byte {
	var b strings.Builder
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	if from != "" {
		b.WriteString("From: " + from + "\r\n")
	}
	if title != "" {
		b.WriteString("Subject: " + title + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("X-Message-Class: IPM.StickyNote\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// parseNoteMessage extracts the title (Subject) and plain-text body from a note
// message blob.
func parseNoteMessage(raw []byte) (title, body string) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return "", string(raw)
	}
	title = msg.Header.Get("Subject")
	if bodyBytes, err := io.ReadAll(msg.Body); err == nil {
		body = string(bodyBytes)
	}
	return title, body
}
