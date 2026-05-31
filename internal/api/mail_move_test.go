package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleMailMove_BetweenFolders verifies a message can be moved to Trash and
// then restored to INBOX via the move endpoint.
func TestHandleMailMove_BetweenFolders(t *testing.T) {
	const user = "user@example.com"
	h, msgID := newMailHandlerWithInboxMessage(t, user)

	move := func(to string) int {
		body := `{"id":"` + msgID + `","to":"` + to + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/move", strings.NewReader(body))
		req = req.WithContext(withUser(req.Context(), user))
		w := httptest.NewRecorder()
		h.handleMailMove(w, req)
		return w.Code
	}

	if code := move("trash"); code != http.StatusOK {
		t.Fatalf("move to trash: expected 200, got %d", code)
	}
	if mailbox, _, _, found := h.findMessage(user, msgID); !found || mailbox != "Trash" {
		t.Errorf("expected message in Trash, found=%v mailbox=%q", found, mailbox)
	}

	if code := move("inbox"); code != http.StatusOK {
		t.Fatalf("restore to inbox: expected 200, got %d", code)
	}
	if mailbox, _, _, found := h.findMessage(user, msgID); !found || mailbox != "INBOX" {
		t.Errorf("expected message back in INBOX, found=%v mailbox=%q", found, mailbox)
	}
}

// TestHandleMailMove_UnknownFolder rejects a target the user does not own so the
// endpoint cannot create arbitrary mailboxes.
func TestHandleMailMove_UnknownFolder(t *testing.T) {
	const user = "user@example.com"
	h, msgID := newMailHandlerWithInboxMessage(t, user)

	body := `{"id":"` + msgID + `","to":"NotARealFolder"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/move", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), user))
	w := httptest.NewRecorder()
	h.handleMailMove(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown folder, got %d", w.Code)
	}
}
