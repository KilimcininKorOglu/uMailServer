package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/storage"
)

// TestHandleMailFlag_SetAndClear verifies that toggling \Flagged and \Seen
// persists to the message metadata.
func TestHandleMailFlag_SetAndClear(t *testing.T) {
	const user = "user@example.com"
	h, msgID := newMailHandlerWithInboxMessage(t, user)

	// Star the message.
	body := `{"id":"` + msgID + `","flag":"\\Flagged","value":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/flag", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), user))
	w := httptest.NewRecorder()
	h.handleMailFlag(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set \\Flagged: expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	if _, _, meta, _ := h.findMessage(user, msgID); !storage.HasFlag(meta.Flags, "\\Flagged") {
		t.Errorf("expected \\Flagged to be set, flags=%v", meta.Flags)
	}

	// The seeded message starts with \Seen; clear it (mark unread).
	body = `{"id":"` + msgID + `","flag":"\\Seen","value":false}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mail/flag", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), user))
	w = httptest.NewRecorder()
	h.handleMailFlag(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear \\Seen: expected 200, got %d", w.Code)
	}
	if _, _, meta, _ := h.findMessage(user, msgID); storage.HasFlag(meta.Flags, "\\Seen") {
		t.Errorf("expected \\Seen to be cleared, flags=%v", meta.Flags)
	}
}

// TestHandleMailFlag_UnsupportedFlag rejects flags outside the allowlist so the
// endpoint cannot be used to set arbitrary IMAP flags.
func TestHandleMailFlag_UnsupportedFlag(t *testing.T) {
	const user = "user@example.com"
	h, msgID := newMailHandlerWithInboxMessage(t, user)

	body := `{"id":"` + msgID + `","flag":"\\Deleted","value":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/flag", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), user))
	w := httptest.NewRecorder()
	h.handleMailFlag(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unsupported flag, got %d", w.Code)
	}
}

// TestHandleMailFlag_MissingID requires a message id.
func TestHandleMailFlag_MissingID(t *testing.T) {
	const user = "user@example.com"
	h, _ := newMailHandlerWithInboxMessage(t, user)

	body := `{"flag":"\\Seen","value":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/flag", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), user))
	w := httptest.NewRecorder()
	h.handleMailFlag(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing id, got %d", w.Code)
	}
}
