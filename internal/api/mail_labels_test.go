package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/storage"
)

// newLabelTestHandler returns a mail handler backed by real storage holding one
// message in INBOX, and the message's id.
func newLabelTestHandler(t *testing.T) (*MailHandler, string) {
	t.Helper()
	mailDB, err := storage.OpenDatabase(t.TempDir() + "/mail.db")
	if err != nil {
		t.Fatalf("open mail db: %v", err)
	}
	t.Cleanup(func() {
		if err := mailDB.Close(); err != nil {
			t.Errorf("close mail db: %v", err)
		}
	})
	msgStore, err := storage.NewMessageStore(t.TempDir() + "/messages")
	if err != nil {
		t.Fatalf("create message store: %v", err)
	}
	t.Cleanup(func() {
		if err := msgStore.Close(); err != nil {
			t.Errorf("close message store: %v", err)
		}
	})

	h := NewMailHandler()
	h.mailDB = mailDB
	h.msgStore = msgStore

	// reqAsUser injects "admin@test.com" as the authenticated user, so the
	// message must be stored under that account for findMessage to locate it.
	const user = "admin@test.com"
	if err := mailDB.CreateMailbox(user, "INBOX"); err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	msgID, err := msgStore.StoreMessage(user, []byte("From: bob\r\nSubject: Hi\r\n\r\nbody"))
	if err != nil {
		t.Fatalf("store message: %v", err)
	}
	uid, err := mailDB.GetNextUID(user, "INBOX")
	if err != nil {
		t.Fatalf("get next uid: %v", err)
	}
	if err := mailDB.StoreMessageMetadata(user, "INBOX", uid, &storage.MessageMetadata{
		MessageID: msgID, UID: uid, Subject: "Hi", From: "bob",
	}); err != nil {
		t.Fatalf("store metadata: %v", err)
	}
	return h, msgID
}

func TestHandleMailLabels_SetAndNormalize(t *testing.T) {
	h, msgID := newLabelTestHandler(t)

	body := `{"id":"` + msgID + `","labels":["Work","  Urgent  ","Work",""]}`
	rec := httptest.NewRecorder()
	h.handleMailLabels(rec, reqAsUser(http.MethodPost, "/api/v1/mail/labels", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("set labels: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Response is deduped, trimmed, empties removed.
	rb := rec.Body.String()
	if !strings.Contains(rb, `"Work"`) || !strings.Contains(rb, `"Urgent"`) {
		t.Errorf("expected Work and Urgent in response, got %s", rb)
	}
	if strings.Count(rb, `"Work"`) != 1 {
		t.Errorf("expected Work deduped, got %s", rb)
	}

	// Persisted on the message metadata.
	_, _, meta, found := h.findMessage("admin@test.com", msgID)
	if !found {
		t.Fatal("message not found after labeling")
	}
	if len(meta.Labels) != 2 {
		t.Fatalf("expected 2 labels persisted, got %v", meta.Labels)
	}
}

func TestHandleMailLabels_NotFound(t *testing.T) {
	h, _ := newLabelTestHandler(t)
	rec := httptest.NewRecorder()
	h.handleMailLabels(rec, reqAsUser(http.MethodPost, "/api/v1/mail/labels", `{"id":"nope","labels":["X"]}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown message, got %d", rec.Code)
	}
}
