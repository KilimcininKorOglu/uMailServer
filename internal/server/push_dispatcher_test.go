package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/ews"
	"github.com/umailserver/umailserver/internal/semcore"
)

// TestDispatchPushNotifications exercises the full push-delivery loop end to end
// in-process: a push subscription + a pending lifecycle event → one tick POSTs a
// SendNotification to the (httptest) callback, advances the cursor, and a second
// tick with no new events POSTs nothing. A single node is always cluster leader,
// so the leader gate lets the loop run.
func TestDispatchPushNotifications(t *testing.T) {
	store, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }() //nolint:errcheck

	mboxID, err := store.Identity().EnsureMailboxId("alice@local.test")
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}

	var mu sync.Mutex
	var bodies []string
	status := "OK"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body) //nolint:errcheck
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		reply := `<Envelope><Body><SendNotificationResponse><ResponseMessages>` +
			`<SendNotificationResponseMessage><SubscriptionStatus>` + status + `</SubscriptionStatus>` +
			`</SendNotificationResponseMessage></ResponseMessages></SendNotificationResponse></Body></Envelope>`
		_, _ = w.Write([]byte(reply)) //nolint:errcheck
	}))
	defer ts.Close()

	subID, err := store.Subscriptions().CreateSubscription(semcore.Subscription{
		MailboxID: mboxID, Kind: semcore.SubscriptionKindPush, PushURL: ts.URL,
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	item, _ := semcore.NewItemId("evt-item-1")     //nolint:errcheck
	folder, _ := semcore.NewFolderId("evt-fold-1") //nolint:errcheck
	if err := store.Lifecycle().AppendLifecycle(semcore.Lifecycle{
		MailboxID: mboxID, Kind: semcore.LifecycleKindCreated, ItemID: item, FolderID: folder, At: time.Now(),
	}); err != nil {
		t.Fatalf("AppendLifecycle: %v", err)
	}

	// DeliverPushNotification only needs the flag + the pure event converter, so a
	// bare EWS server (no stores wired) is sufficient; relax the SSRF guard so the
	// 127.0.0.1 httptest sink is accepted.
	ewsServer := ews.NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ewsServer.SetAllowPrivatePushTargets(true)

	srv := &Server{semcoreStore: boltSemantic{store}, ewsServer: ewsServer, logger: slog.Default()}
	srv.dispatchPushNotifications()

	mu.Lock()
	n := len(bodies)
	var body string
	if n > 0 {
		body = bodies[0]
	}
	mu.Unlock()
	if n != 1 {
		t.Fatalf("sink received %d posts, want 1", n)
	}
	if !strings.Contains(body, "<t:CreatedEvent>") || !strings.Contains(body, "<m:SendNotification>") {
		t.Errorf("pushed body missing SendNotification/CreatedEvent:\n%s", body)
	}

	sub, err := store.Subscriptions().GetSubscription(subID)
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if sub.LastSeq == 0 {
		t.Error("cursor (LastSeq) was not advanced after a successful delivery")
	}

	// A second tick with no new events must POST nothing (no re-delivery).
	srv.dispatchPushNotifications()
	mu.Lock()
	n2 := len(bodies)
	mu.Unlock()
	if n2 != 1 {
		t.Errorf("second tick re-POSTed: %d total posts, want 1", n2)
	}
}

// TestDispatchPushNotifications_Unsubscribe verifies an "Unsubscribe" reply drops
// the subscription so it is no longer delivered to.
func TestDispatchPushNotifications_Unsubscribe(t *testing.T) {
	store, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }() //nolint:errcheck

	mboxID, err := store.Identity().EnsureMailboxId("bob@local.test")
	if err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := `<Envelope><Body><SendNotificationResponse><ResponseMessages>` +
			`<SendNotificationResponseMessage><SubscriptionStatus>Unsubscribe</SubscriptionStatus>` +
			`</SendNotificationResponseMessage></ResponseMessages></SendNotificationResponse></Body></Envelope>`
		_, _ = w.Write([]byte(reply)) //nolint:errcheck
	}))
	defer ts.Close()

	subID, err := store.Subscriptions().CreateSubscription(semcore.Subscription{
		MailboxID: mboxID, Kind: semcore.SubscriptionKindPush, PushURL: ts.URL,
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	item, _ := semcore.NewItemId("evt-item-2") //nolint:errcheck
	if err := store.Lifecycle().AppendLifecycle(semcore.Lifecycle{
		MailboxID: mboxID, Kind: semcore.LifecycleKindCreated, ItemID: item, At: time.Now(),
	}); err != nil {
		t.Fatalf("AppendLifecycle: %v", err)
	}

	ewsServer := ews.NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ewsServer.SetAllowPrivatePushTargets(true)
	srv := &Server{semcoreStore: boltSemantic{store}, ewsServer: ewsServer, logger: slog.Default()}
	srv.dispatchPushNotifications()

	if _, err := store.Subscriptions().GetSubscription(subID); err == nil {
		t.Error("subscription should have been removed after an Unsubscribe reply")
	}
}
