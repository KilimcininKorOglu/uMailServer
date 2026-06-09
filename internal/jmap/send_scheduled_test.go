package jmap

import (
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/storage"
)

// TestEmailSubmissionSet_FutureSendAtSchedules verifies a future sendAt routes the
// submission to the scheduler (not the immediate submit path) and reports
// undoStatus "pending".
func TestEmailSubmissionSet_FutureSendAtSchedules(t *testing.T) {
	msgStore, err := storage.NewMessageStore(t.TempDir() + "/messages")
	if err != nil {
		t.Fatalf("NewMessageStore: %v", err)
	}
	t.Cleanup(func() {
		if err := msgStore.Close(); err != nil {
			t.Errorf("close message store: %v", err)
		}
	})
	srv := NewServer(nil, msgStore, nil, Config{})

	raw := []byte("From: alice@test.com\r\nTo: bob@test.com\r\nSubject: Later\r\n\r\nhi\r\n")
	blobKey, err := msgStore.StoreMessage("alice@test.com", raw)
	if err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	submitCalled := false
	srv.SetSubmitMessageFunc(func(_ string, _ []string, _ []byte) error { submitCalled = true; return nil })
	var schedOwner string
	var schedAt time.Time
	srv.SetScheduleMessageFunc(func(owner, _ string, _ []string, _ []byte, sendAt time.Time, _ bool) (string, error) {
		schedOwner, schedAt = owner, sendAt
		return "sched-1", nil
	})

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	call := MethodCall{Name: "EmailSubmission/set", ID: "c0", Args: map[string]interface{}{
		"accountId": "alice@test.com",
		"create": map[string]interface{}{
			"sub1": map[string]interface{}{"emailId": blobKey, "sendAt": future},
		},
	}}

	resp := srv.handleEmailSubmissionSet("alice@test.com", call, nil)

	if submitCalled {
		t.Error("a future sendAt must NOT submit immediately")
	}
	if schedOwner != "alice@test.com" || schedAt.IsZero() {
		t.Errorf("scheduler not called with owner/sendAt: owner=%q zero=%v", schedOwner, schedAt.IsZero())
	}
	created := asMap(resp.Args["created"])
	sub1 := asMap(created["sub1"])
	if sub1 == nil {
		t.Fatalf("submission not created: %+v", resp.Args)
	}
	if got := asString(sub1["undoStatus"]); got != "pending" {
		t.Errorf("undoStatus = %q, want pending", got)
	}
}

// TestEmailSubmissionSet_PastSendAtSubmitsNow verifies a past sendAt submits
// immediately rather than scheduling.
func TestEmailSubmissionSet_PastSendAtSubmitsNow(t *testing.T) {
	msgStore, err := storage.NewMessageStore(t.TempDir() + "/messages")
	if err != nil {
		t.Fatalf("NewMessageStore: %v", err)
	}
	t.Cleanup(func() {
		if err := msgStore.Close(); err != nil {
			t.Errorf("close message store: %v", err)
		}
	})
	srv := NewServer(nil, msgStore, nil, Config{})

	raw := []byte("From: alice@test.com\r\nTo: bob@test.com\r\nSubject: Now\r\n\r\nhi\r\n")
	blobKey, err := msgStore.StoreMessage("alice@test.com", raw)
	if err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	submitCalled := false
	srv.SetSubmitMessageFunc(func(_ string, _ []string, _ []byte) error { submitCalled = true; return nil })
	scheduled := false
	srv.SetScheduleMessageFunc(func(_, _ string, _ []string, _ []byte, _ time.Time, _ bool) (string, error) {
		scheduled = true
		return "", nil
	})

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	call := MethodCall{Name: "EmailSubmission/set", ID: "c0", Args: map[string]interface{}{
		"create": map[string]interface{}{
			"sub1": map[string]interface{}{"emailId": blobKey, "sendAt": past},
		},
	}}

	srv.handleEmailSubmissionSet("alice@test.com", call, nil)

	if !submitCalled {
		t.Error("a past sendAt must submit immediately")
	}
	if scheduled {
		t.Error("a past sendAt must NOT be scheduled")
	}
}
