package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// saveDraft posts a draft and returns the response recorder.
func postDraft(t *testing.T, h *MailHandler, user, bodyJSON string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/draft", strings.NewReader(bodyJSON))
	req = req.WithContext(withUser(req.Context(), user))
	w := httptest.NewRecorder()
	h.handleMailDraft(w, req)
	return w
}

// TestHandleMailDraft_SaveAndReplace verifies a draft is stored in Drafts and
// that re-saving with the returned id replaces it rather than duplicating it.
func TestHandleMailDraft_SaveAndReplace(t *testing.T) {
	const user = "user@example.com"
	h, _ := newMailHandlerWithInboxMessage(t, user)

	w := postDraft(t, h, user, `{"to":["bob@example.com"],"cc":[],"bcc":[],"subject":"Hi","body":"draft body","from":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save draft: expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	var first struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil || first.ID == "" {
		t.Fatalf("draft response missing id: %v (%s)", err, w.Body.String())
	}

	drafts, err := h.getEmailsFromStorage(user, "Drafts")
	if err != nil {
		t.Fatalf("list drafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft after first save, got %d", len(drafts))
	}

	// Re-save with the returned id; it should replace, not duplicate.
	w2 := postDraft(t, h, user, `{"id":"`+first.ID+`","to":["bob@example.com"],"cc":[],"bcc":[],"subject":"Hi again","body":"updated","from":""}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("replace draft: expected 200, got %d", w2.Code)
	}
	drafts, err = h.getEmailsFromStorage(user, "Drafts")
	if err != nil {
		t.Fatalf("list drafts after replace: %v", err)
	}
	if len(drafts) != 1 {
		t.Errorf("expected 1 draft after replace, got %d", len(drafts))
	}
	if drafts[0].Subject != "Hi again" {
		t.Errorf("expected replaced draft subject 'Hi again', got %q", drafts[0].Subject)
	}
}
