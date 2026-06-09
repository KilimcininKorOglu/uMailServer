package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// authedReq builds a request carrying the authenticated user the handlers expect.
func authedReq(method, target, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	//nolint:staticcheck // context key matches the handler's lookup
	return req.WithContext(context.WithValue(req.Context(), "user", "alice@test.com"))
}

// TestHandleMailSend_FutureSendAtSchedules verifies a future sendAt routes to the
// scheduler (and is NOT delivered now), returning a "scheduled" status.
func TestHandleMailSend_FutureSendAtSchedules(t *testing.T) {
	h := newSendHandler(t)
	delivered := false
	h.SetDeliveryFunc(func(_ string, _ []string, _ []byte) error { delivered = true; return nil })
	var gotOwner string
	var gotSendAt time.Time
	h.SetScheduleFunc(func(owner, _ string, _ []string, _ []byte, sendAt time.Time) (string, error) {
		gotOwner, gotSendAt = owner, sendAt
		return "sched-1", nil
	})

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rec := httptest.NewRecorder()
	h.handleMailSend(rec, authedReq(http.MethodPost, "/api/v1/mail/send",
		`{"to":["bob@test.com"],"subject":"Later","body":"hi","sendAt":"`+future+`"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if delivered {
		t.Error("a future-scheduled message must NOT be delivered immediately")
	}
	if gotOwner != "alice@test.com" || gotSendAt.IsZero() {
		t.Errorf("scheduler not called with owner/sendAt: owner=%q zero=%v", gotOwner, gotSendAt.IsZero())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "scheduled" {
		t.Errorf("response status = %q, want scheduled", resp["status"])
	}
}

// TestHandleMailSend_PastSendAtDeliversNow verifies a past sendAt sends now and is
// never handed to the scheduler.
func TestHandleMailSend_PastSendAtDeliversNow(t *testing.T) {
	h := newSendHandler(t)
	delivered := false
	h.SetDeliveryFunc(func(_ string, _ []string, _ []byte) error { delivered = true; return nil })
	scheduled := false
	h.SetScheduleFunc(func(_, _ string, _ []string, _ []byte, _ time.Time) (string, error) { scheduled = true; return "", nil })

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	rec := httptest.NewRecorder()
	h.handleMailSend(rec, authedReq(http.MethodPost, "/api/v1/mail/send",
		`{"to":["bob@test.com"],"subject":"Now","body":"hi","sendAt":"`+past+`"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !delivered {
		t.Error("a past sendAt must deliver immediately")
	}
	if scheduled {
		t.Error("a past sendAt must NOT be scheduled")
	}
}

// TestHandleMailSend_FutureSendAtNoSchedulerIsUnavailable verifies a future send
// with no scheduler wired fails loudly rather than dropping the message.
func TestHandleMailSend_FutureSendAtNoSchedulerIsUnavailable(t *testing.T) {
	h := newSendHandler(t)
	h.SetDeliveryFunc(func(_ string, _ []string, _ []byte) error { return nil })
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rec := httptest.NewRecorder()
	h.handleMailSend(rec, authedReq(http.MethodPost, "/api/v1/mail/send",
		`{"to":["bob@test.com"],"subject":"Later","body":"hi","sendAt":"`+future+`"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when scheduling unavailable, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleMailSend_InvalidSendAtRejected verifies a malformed sendAt is a 400.
func TestHandleMailSend_InvalidSendAtRejected(t *testing.T) {
	h := newSendHandler(t)
	h.SetDeliveryFunc(func(_ string, _ []string, _ []byte) error { return nil })
	h.SetScheduleFunc(func(_, _ string, _ []string, _ []byte, _ time.Time) (string, error) { return "x", nil })
	rec := httptest.NewRecorder()
	h.handleMailSend(rec, authedReq(http.MethodPost, "/api/v1/mail/send",
		`{"to":["bob@test.com"],"subject":"Bad","body":"hi","sendAt":"not-a-time"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid sendAt, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleScheduledListAndCancel exercises the Scheduled-view endpoints.
func TestHandleScheduledListAndCancel(t *testing.T) {
	h := newSendHandler(t)
	h.SetScheduledListFunc(func(_ string) ([]ScheduledMailItem, error) {
		return []ScheduledMailItem{{ID: "s1", To: []string{"bob@test.com"}, Subject: "Hi", SendAt: "2030-01-01T00:00:00Z", Status: "pending"}}, nil
	})
	var canceledID, canceledOwner string
	h.SetScheduledCancelFunc(func(owner, id string) error { canceledOwner, canceledID = owner, id; return nil })

	rec := httptest.NewRecorder()
	h.handleScheduledList(rec, authedReq(http.MethodGet, "/api/v1/scheduled", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"s1"`) {
		t.Errorf("list body missing scheduled id: %s", rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	h.handleScheduledCancel(rec2, authedReq(http.MethodPost, "/api/v1/scheduled/cancel", `{"id":"s1"}`))
	if rec2.Code != http.StatusOK {
		t.Fatalf("cancel: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if canceledID != "s1" || canceledOwner != "alice@test.com" {
		t.Errorf("cancel got owner=%q id=%q, want alice@test.com/s1", canceledOwner, canceledID)
	}
}
