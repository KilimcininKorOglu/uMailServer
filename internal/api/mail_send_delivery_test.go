package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/storage"
)

// newSendHandler builds a MailHandler backed by temp storage for the given user.
func newSendHandler(t *testing.T) *MailHandler {
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
	h.SetStorage(msgStore, mailDB)
	return h
}

func sendReq(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/send", strings.NewReader(body))
	//nolint:staticcheck // context key matches the handler's lookup
	return req.WithContext(context.WithValue(req.Context(), "user", "alice@test.com"))
}

func TestHandleMailSend_DeliversToAllEnvelopeRecipients(t *testing.T) {
	h := newSendHandler(t)
	var gotFrom string
	var gotTo []string
	h.SetDeliveryFunc(func(from string, to []string, _ []byte) error {
		gotFrom = from
		gotTo = to
		return nil
	})

	rec := httptest.NewRecorder()
	h.handleMailSend(rec, sendReq(`{"to":["bob@test.com"],"cc":["carol@test.com"],"bcc":["dave@test.com"],"subject":"Hi","body":"hello"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotFrom != "alice@test.com" {
		t.Errorf("from wrong: %q", gotFrom)
	}
	sort.Strings(gotTo)
	want := []string{"bob@test.com", "carol@test.com", "dave@test.com"}
	if len(gotTo) != len(want) {
		t.Fatalf("envelope recipients wrong: got %v want %v", gotTo, want)
	}
	for i := range want {
		if gotTo[i] != want[i] {
			t.Fatalf("envelope recipients wrong: got %v want %v", gotTo, want)
		}
	}
}

func TestHandleMailSend_NoDeliveryFuncIsUnavailable(t *testing.T) {
	h := newSendHandler(t)
	// No delivery func wired: send must not silently file-and-forget.
	rec := httptest.NewRecorder()
	h.handleMailSend(rec, sendReq(`{"to":["bob@test.com"],"subject":"Hi","body":"hello"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when delivery is unavailable, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleMailSend_DeliveryFailureSurfaces(t *testing.T) {
	h := newSendHandler(t)
	h.SetDeliveryFunc(func(_ string, _ []string, _ []byte) error {
		return context.DeadlineExceeded
	})
	rec := httptest.NewRecorder()
	h.handleMailSend(rec, sendReq(`{"to":["bob@test.com"],"subject":"Hi","body":"hello"}`))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on delivery failure, got %d: %s", rec.Code, rec.Body.String())
	}
}
