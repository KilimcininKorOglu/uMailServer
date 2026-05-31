package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/storage"
)

// sendAndReadBack sends a message through handleMailSend and returns the raw
// stored bytes so a test can assert on the generated headers.
func sendAndReadBack(t *testing.T, reqBody string) string {
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/send", strings.NewReader(reqBody))
	req = req.WithContext(context.WithValue(req.Context(), "user", "alice@test.com")) //nolint:staticcheck // matches handler lookup
	rec := httptest.NewRecorder()
	h.handleMailSend(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("send: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	raw, err := msgStore.ReadMessage("alice@test.com", resp.ID)
	if err != nil {
		t.Fatalf("read back stored message: %v", err)
	}
	return string(raw)
}

func TestHandleMailSend_RequestsReadReceipt(t *testing.T) {
	raw := sendAndReadBack(t, `{"to":["bob@test.com"],"subject":"Hi","body":"hello","requestReadReceipt":true}`)
	if !strings.Contains(raw, "Disposition-Notification-To: alice@test.com") {
		t.Errorf("expected read-receipt header addressed to the sender, got:\n%s", raw)
	}
}

func TestHandleMailSend_NoReadReceiptByDefault(t *testing.T) {
	raw := sendAndReadBack(t, `{"to":["bob@test.com"],"subject":"Hi","body":"hello"}`)
	if strings.Contains(raw, "Disposition-Notification-To") {
		t.Errorf("did not expect a read-receipt header, got:\n%s", raw)
	}
}
