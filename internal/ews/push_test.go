package ews

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

func pushTestEvents(t *testing.T) []semcore.Lifecycle {
	t.Helper()
	item, err := semcore.NewItemId("item-push-1")
	if err != nil {
		t.Fatalf("NewItemId: %v", err)
	}
	folder, err := semcore.NewFolderId("folder-push-1")
	if err != nil {
		t.Fatalf("NewFolderId: %v", err)
	}
	return []semcore.Lifecycle{{
		Kind:     semcore.LifecycleKindCreated,
		ItemID:   item,
		FolderID: folder,
		At:       time.Unix(1780000000, 0).UTC(),
	}}
}

// sink is a callback endpoint that records the pushed body and replies with a
// configurable SubscriptionStatus.
type sink struct {
	mu     sync.Mutex
	bodies []string
	status string // "OK" or "Unsubscribe"
}

func (s *sink) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body) //nolint:errcheck
		s.mu.Lock()
		s.bodies = append(s.bodies, string(b))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		reply := `<?xml version="1.0"?><Envelope><Body><SendNotificationResponse><ResponseMessages>` +
			`<SendNotificationResponseMessage><SubscriptionStatus>` + s.status + `</SubscriptionStatus>` +
			`</SendNotificationResponseMessage></ResponseMessages></SendNotificationResponse></Body></Envelope>`
		_, _ = w.Write([]byte(reply)) //nolint:errcheck
	}
}

// TestDeliverPushNotification_OK verifies a SendNotification envelope is POSTed
// to the callback URL with the event payload and that an "OK" reply keeps the
// subscription (remove=false, no error).
func TestDeliverPushNotification_OK(t *testing.T) {
	sk := &sink{status: "OK"}
	ts := httptest.NewServer(sk.handler())
	defer ts.Close()

	srv := &Server{allowPrivatePushTargets: true} // accept the 127.0.0.1 sink
	sub := &semcore.Subscription{ID: semcore.SubscriptionId{ID: "sub-1"}, PushURL: ts.URL, LastSeq: 5}

	remove, err := srv.DeliverPushNotification(sub, pushTestEvents(t))
	if err != nil {
		t.Fatalf("DeliverPushNotification: %v", err)
	}
	if remove {
		t.Error("OK reply must not remove the subscription")
	}
	sk.mu.Lock()
	defer sk.mu.Unlock()
	if len(sk.bodies) != 1 {
		t.Fatalf("sink received %d posts, want 1", len(sk.bodies))
	}
	body := sk.bodies[0]
	for _, want := range []string{"<m:SendNotification>", "<t:SubscriptionId>sub-1</t:SubscriptionId>", "<t:CreatedEvent>", `<t:ItemId Id="item-push-1"/>`} {
		if !strings.Contains(body, want) {
			t.Errorf("pushed body missing %q:\n%s", want, body)
		}
	}
}

// TestDeliverPushNotification_Unsubscribe verifies an "Unsubscribe" reply signals
// the caller to drop the subscription.
func TestDeliverPushNotification_Unsubscribe(t *testing.T) {
	sk := &sink{status: "Unsubscribe"}
	ts := httptest.NewServer(sk.handler())
	defer ts.Close()

	srv := &Server{allowPrivatePushTargets: true}
	sub := &semcore.Subscription{ID: semcore.SubscriptionId{ID: "sub-2"}, PushURL: ts.URL}
	remove, err := srv.DeliverPushNotification(sub, pushTestEvents(t))
	if err != nil {
		t.Fatalf("DeliverPushNotification: %v", err)
	}
	if !remove {
		t.Error("Unsubscribe reply must remove the subscription")
	}
}

// TestDeliverPushNotification_Non2xx verifies a non-2xx reply is a transient
// error (retry; do not remove, do not advance).
func TestDeliverPushNotification_Non2xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	srv := &Server{allowPrivatePushTargets: true}
	sub := &semcore.Subscription{ID: semcore.SubscriptionId{ID: "sub-3"}, PushURL: ts.URL}
	remove, err := srv.DeliverPushNotification(sub, pushTestEvents(t))
	if err == nil {
		t.Error("non-2xx reply must be a transient error")
	}
	if remove {
		t.Error("non-2xx reply must not remove the subscription")
	}
}

// TestPushURLAllowed verifies the SSRF guard: loopback/private targets are
// rejected by default but accepted once the test flag relaxes it; a public host
// is always accepted, and a non-http scheme is always rejected.
func TestPushURLAllowed(t *testing.T) {
	strict := &Server{}
	relaxed := &Server{allowPrivatePushTargets: true}

	if strict.pushURLAllowed("http://127.0.0.1:9/cb") {
		t.Error("strict guard must reject loopback")
	}
	if strict.pushURLAllowed("https://localhost/cb") {
		t.Error("strict guard must reject localhost")
	}
	if strict.pushURLAllowed("ftp://example.com/cb") {
		t.Error("must reject non-http(s) scheme")
	}
	if strict.pushURLAllowed("") {
		t.Error("must reject empty URL")
	}
	// Use a public IP literal: the strict guard resolves hostnames, and the
	// test sandbox has no DNS, so a hostname would be rejected for being
	// unresolvable rather than for being private.
	if !strict.pushURLAllowed("https://8.8.8.8/notify") {
		t.Error("strict guard must accept a public target")
	}
	if !relaxed.pushURLAllowed("http://127.0.0.1:9/cb") {
		t.Error("relaxed guard must accept loopback for tests")
	}
}

// TestSubscribe_RejectsLoopbackPushURL proves a PushSubscription whose callback
// URL is loopback is rejected at Subscribe (the SSRF guard runs before the
// subscription is recorded). The error precedes the subscription-store check,
// so a nil store does not mask it.
func TestSubscribe_RejectsLoopbackPushURL(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()
	if _, err := srv.identity.EnsureMailboxId("alice@example.com"); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}

	body := `<Subscribe xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <PushSubscriptionRequest>
    <t:StatusFrequency>1</t:StatusFrequency>
    <t:URL>http://127.0.0.1:9/callback</t:URL>
  </PushSubscriptionRequest>
</Subscribe>`
	rec := ewsRequest(t, srv, "alice@example.com", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), string(ErrErrorInvalidPushSubscriptionURL)) {
		t.Errorf("expected %s for loopback push URL, got:\n%s", ErrErrorInvalidPushSubscriptionURL, rec.Body.String())
	}
}
