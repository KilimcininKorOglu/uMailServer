package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/storage"
)

// mustOK fails the test immediately if err is non-nil (setup helper).
func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// newMailHandlerWithInboxMessage wires a MailHandler over real storage and seeds
// one message in INBOX, returning the handler and the message id.
func newMailHandlerWithInboxMessage(t *testing.T, user string) (*MailHandler, string) {
	t.Helper()
	mailDB, err := storage.OpenDatabase(t.TempDir() + "/mail.db")
	mustOK(t, err)
	t.Cleanup(func() {
		if cerr := mailDB.Close(); cerr != nil {
			t.Errorf("close mail db: %v", cerr)
		}
	})
	msgStore, err := storage.NewMessageStore(t.TempDir() + "/messages")
	mustOK(t, err)
	t.Cleanup(func() {
		if cerr := msgStore.Close(); cerr != nil {
			t.Errorf("close msg store: %v", cerr)
		}
	})

	h := NewMailHandler()
	h.mailDB = mailDB
	h.msgStore = msgStore

	mustOK(t, mailDB.CreateMailbox(user, "INBOX"))
	msgID, err := msgStore.StoreMessage(user, []byte("From: sender\r\nSubject: Test\r\n\r\nBody"))
	mustOK(t, err)
	uid, err := mailDB.GetNextUID(user, "INBOX")
	mustOK(t, err)
	mustOK(t, mailDB.StoreMessageMetadata(user, "INBOX", uid, &storage.MessageMetadata{
		MessageID:    msgID,
		UID:          uid,
		Subject:      "Test",
		From:         "sender",
		To:           user,
		InternalDate: time.Now(),
		Size:         100,
		Flags:        []string{"\\Seen"},
	}))
	return h, msgID
}

// TestHandleMailDelete_MovesToTrash verifies that deleting a message from a
// normal folder moves it to Trash (metadata only) and keeps the shared file,
// rather than destroying it outright.
func TestHandleMailDelete_MovesToTrash(t *testing.T) {
	const user = "user@example.com"
	h, msgID := newMailHandlerWithInboxMessage(t, user)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/mail/delete?id="+msgID, nil)
	req = req.WithContext(withUser(req.Context(), user))
	w := httptest.NewRecorder()
	h.handleMailDelete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["message"] != "Email moved to trash" {
		t.Errorf("expected move-to-trash message, got %q", body["message"])
	}

	// The message must now live in Trash, not INBOX, and the file must survive.
	mailbox, _, _, found := h.findMessage(user, msgID)
	if !found || mailbox != "Trash" {
		t.Errorf("expected message in Trash, found=%v mailbox=%q", found, mailbox)
	}
	if _, err := h.msgStore.ReadMessage(user, msgID); err != nil {
		t.Errorf("message file should still exist after soft delete: %v", err)
	}
}

// TestHandleMailDelete_PurgesFromTrash verifies that deleting a message that is
// already in Trash permanently removes both its metadata and the shared file.
func TestHandleMailDelete_PurgesFromTrash(t *testing.T) {
	const user = "user@example.com"
	h, msgID := newMailHandlerWithInboxMessage(t, user)

	// First delete moves it to Trash.
	req1 := httptest.NewRequest(http.MethodDelete, "/api/v1/mail/delete?id="+msgID, nil)
	req1 = req1.WithContext(withUser(req1.Context(), user))
	h.handleMailDelete(httptest.NewRecorder(), req1)

	// Second delete (now in Trash) purges it.
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/mail/delete?id="+msgID, nil)
	req2 = req2.WithContext(withUser(req2.Context(), user))
	w := httptest.NewRecorder()
	h.handleMailDelete(w, req2)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["message"] != "Email deleted" {
		t.Errorf("expected permanent-delete message, got %q", body["message"])
	}

	if _, _, _, found := h.findMessage(user, msgID); found {
		t.Errorf("message metadata should be gone after permanent delete")
	}
	if _, err := h.msgStore.ReadMessage(user, msgID); err == nil {
		t.Errorf("message file should be gone after permanent delete")
	}
}
