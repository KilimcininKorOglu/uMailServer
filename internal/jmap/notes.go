package jmap

import (
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// JMAP Note is a vendor extension (no IETF JMAP notes RFC) for Outlook-style
// sticky notes. A Note is an IPM.StickyNote message in the "Notes" folder — the
// same record EWS, IMAP, and webmail use — so a note is identical across every
// surface. Note/set writes the blob store, the semcore identity store (read by
// EWS), and the IMAP mailstore index (read by IMAP/JMAP/webmail).

const (
	notesCapabilityURN = "urn:umailserver:params:jmap:notes"
	notesFolderName    = "Notes"
)

// noteToJMAP projects a stored note message onto the JMAP Note object.
func noteToJMAP(id, title, body string, created time.Time) map[string]interface{} {
	return map[string]interface{}{
		"id":      id,
		"title":   title,
		"body":    body,
		"created": created.UTC().Format(time.RFC3339),
		"updated": created.UTC().Format(time.RFC3339),
	}
}

// listNotes reads the Notes folder from the IMAP mailstore index and returns the
// JMAP Note objects keyed by id (the message blob key).
func (s *Server) listNotes(user string) []map[string]interface{} {
	uids, err := s.db.GetMessageUIDs(user, notesFolderName)
	if err != nil {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(uids))
	for _, uid := range uids {
		meta, err := s.db.GetMessageMetadata(user, notesFolderName, uid)
		if err != nil || meta == nil {
			continue
		}
		raw, err := s.msgStore.ReadMessage(user, meta.MessageID)
		if err != nil {
			continue
		}
		title, body := parseJMAPNote(raw)
		if title == "" {
			title = meta.Subject
		}
		out = append(out, noteToJMAP(meta.MessageID, title, body, meta.InternalDate))
	}
	return out
}

func (s *Server) handleNoteGet(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "Note/get", call.ID); !valid {
		return resp
	}
	if !s.notesEnabled() {
		return jmapError(call.ID, "notSupported", "notes are not available")
	}

	all := s.listNotes(user)
	list := []interface{}{}
	notFound := []string{}
	if ids := argSlice(call.Args, "ids"); ids != nil {
		byID := make(map[string]map[string]interface{}, len(all))
		for _, n := range all {
			if id, ok := n["id"].(string); ok {
				byID[id] = n
			}
		}
		for _, raw := range ids {
			id, ok := raw.(string)
			if !ok {
				continue
			}
			if n, found := byID[id]; found {
				list = append(list, n)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		for _, n := range all {
			list = append(list, n)
		}
	}

	return Response{
		Name: "Note/get",
		Args: map[string]interface{}{
			"accountId": accountID,
			"state":     fmt.Sprintf("state-%d", time.Now().Unix()),
			"list":      list,
			"notFound":  notFound,
		},
		ID: call.ID,
	}
}

func (s *Server) handleNoteQuery(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "Note/query", call.ID); !valid {
		return resp
	}
	if !s.notesEnabled() {
		return jmapError(call.ID, "notSupported", "notes are not available")
	}
	all := s.listNotes(user)
	ids := make([]string, 0, len(all))
	for _, n := range all {
		if id, ok := n["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return Response{
		Name: "Note/query",
		Args: map[string]interface{}{
			"accountId":           accountID,
			"queryState":          fmt.Sprintf("state-%d", time.Now().Unix()),
			"canCalculateChanges": false,
			"position":            0,
			"total":               len(ids),
			"ids":                 ids,
		},
		ID: call.ID,
	}
}

func (s *Server) handleNoteSet(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "Note/set", call.ID); !valid {
		return resp
	}
	if !s.notesEnabled() {
		return jmapError(call.ID, "notSupported", "notes are not available")
	}

	created := map[string]interface{}{}
	notCreated := map[string]interface{}{}
	for tmpID, raw := range argMap(call.Args, "create") {
		props, ok := raw.(map[string]interface{})
		if !ok {
			notCreated[tmpID] = map[string]interface{}{"type": "invalidProperties"}
			continue
		}
		title := argString(props, "title")
		body := argString(props, "body")
		if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
			notCreated[tmpID] = map[string]interface{}{"type": "invalidProperties", "description": "title or body required"}
			continue
		}
		id, err := s.createNote(user, title, body)
		if err != nil {
			notCreated[tmpID] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		created[tmpID] = map[string]interface{}{"id": id}
	}

	updated := map[string]interface{}{}
	notUpdated := map[string]interface{}{}
	for id, raw := range argMap(call.Args, "update") {
		props, ok := raw.(map[string]interface{})
		if !ok {
			notUpdated[id] = map[string]interface{}{"type": "invalidProperties"}
			continue
		}
		// An update replaces the note content, so its id (content hash) changes.
		title := argString(props, "title")
		body := argString(props, "body")
		s.deleteNote(user, id)
		newID, err := s.createNote(user, title, body)
		if err != nil {
			notUpdated[id] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		updated[id] = map[string]interface{}{"id": newID}
	}

	destroyed := []string{}
	notDestroyed := map[string]interface{}{}
	for _, raw := range argSlice(call.Args, "destroy") {
		id, ok := raw.(string)
		if !ok {
			continue
		}
		if s.deleteNote(user, id) {
			destroyed = append(destroyed, id)
		} else {
			notDestroyed[id] = map[string]interface{}{"type": "notFound"}
		}
	}

	return Response{
		Name: "Note/set",
		Args: map[string]interface{}{
			"accountId":    accountID,
			"oldState":     nil,
			"newState":     fmt.Sprintf("state-%d", time.Now().Unix()),
			"created":      created,
			"updated":      updated,
			"destroyed":    destroyed,
			"notCreated":   notCreated,
			"notUpdated":   notUpdated,
			"notDestroyed": notDestroyed,
		},
		ID: call.ID,
	}
}

func (s *Server) handleNoteChanges(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "Note/changes", call.ID); !valid {
		return resp
	}
	if !s.notesEnabled() {
		return jmapError(call.ID, "notSupported", "notes are not available")
	}
	// Notes are not change-tracked incrementally; report no delta and let clients
	// re-fetch via Note/get when needed.
	state := fmt.Sprintf("state-%d", time.Now().Unix())
	return Response{
		Name: "Note/changes",
		Args: map[string]interface{}{
			"accountId":      accountID,
			"oldState":       argString(call.Args, "sinceState"),
			"newState":       state,
			"hasMoreChanges": false,
			"created":        []string{},
			"updated":        []string{},
			"destroyed":      []string{},
		},
		ID: call.ID,
	}
}

// createNote writes a new IPM.StickyNote message to the Notes folder across the
// blob store, the semcore identity store (EWS visibility), and the IMAP
// mailstore index (IMAP/JMAP/webmail visibility), returning its id (blob key).
func (s *Server) createNote(user, title, body string) (string, error) {
	raw := buildJMAPNoteMIME(user, title, body)
	blobKey, err := s.msgStore.StoreMessage(user, raw)
	if err != nil {
		return "", err
	}
	mboxID, err := s.notesIdentity.EnsureMailboxId(user)
	if err != nil {
		return "", err
	}
	folderID, err := s.notesIdentity.EnsureFolderId(user, notesFolderName, "notes")
	if err != nil {
		return "", err
	}
	now := time.Now()
	if _, err := s.notesPipe.MutateItem(&semcore.MutationInput{
		MailboxID:    mboxID,
		FolderID:     folderID,
		RawMessage:   raw,
		InternalDate: now,
		Actor:        user,
		Email:        user,
		Source:       semcore.MutationSourceJMAP,
		IsRead:       true,
	}); err != nil {
		return "", err
	}
	uid, err := s.db.GetNextUID(user, notesFolderName)
	if err != nil {
		return "", err
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
	if err := s.db.StoreMessageMetadata(user, notesFolderName, uid, meta); err != nil {
		return "", err
	}
	return blobKey, nil
}

// deleteNote removes a note (by blob key) from the IMAP mailstore index and the
// semcore identity store. Returns false if no matching note was found.
func (s *Server) deleteNote(user, id string) bool {
	found := false
	if uids, err := s.db.GetMessageUIDs(user, notesFolderName); err == nil {
		for _, uid := range uids {
			meta, err := s.db.GetMessageMetadata(user, notesFolderName, uid)
			if err != nil || meta == nil || meta.MessageID != id {
				continue
			}
			if err := s.db.DeleteMessage(user, notesFolderName, uid); err == nil {
				found = true
			}
			break
		}
	}
	if folderID, err := s.notesIdentity.EnsureFolderId(user, notesFolderName, "notes"); err == nil {
		if items, err := s.notesIdentity.ListItemIdentitiesByFolder(folderID); err == nil {
			for _, it := range items {
				if it.MsgKey == id {
					if err := s.notesIdentity.DeleteItemIdentity(it.ItemID); err == nil {
						found = true
					}
					break
				}
			}
		}
	}
	return found
}

func buildJMAPNoteMIME(from, title, body string) []byte {
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

func parseJMAPNote(raw []byte) (title, body string) {
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
