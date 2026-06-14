package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
	"github.com/umailserver/umailserver/internal/db"
)

// gatedRequest sends a command through the full transport with an optional
// X-MS-PolicyKey and returns the recorder so a test can read the HTTP status —
// the provisioning gate answers 449 before any handler runs.
func gatedRequest(t *testing.T, s *Server, cmd string, body *wbxml.Element, key string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := wbxml.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/Microsoft-Server-ActiveSync?Cmd="+cmd+"&DeviceId=DEV1", bytes.NewReader(b))
	if key != "" {
		req.Header.Set("X-MS-PolicyKey", key)
	}
	s.ServeHTTP(rec, req)
	return rec
}

// TestProvisioningGateRejectsUnprovisioned proves a non-Provision command is
// answered with 449 unless it carries the device's current policy key. Without
// enforcement a client could skip Provision entirely, so this is what makes the
// policy handshake meaningful. A bare Ping is used because it returns quickly
// (Status 3) once it clears the gate, needing no folder/sync wiring.
func TestProvisioningGateRejectsUnprovisioned(t *testing.T) {
	s, database := provisionServer(t)
	if err := database.PutEASDevice(&db.EASDevice{Email: "bob@x.test", DeviceID: "DEV1", PolicyKey: "12345"}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	ping := &wbxml.Element{Page: wbxml.PagePing, Name: "Ping"}

	for _, tc := range []struct {
		name, key string
		want      int
	}{
		{"no key", "", statusProvisioningRequired},
		{"wrong key", "99999", statusProvisioningRequired},
		{"unprovisioned sentinel", "0", statusProvisioningRequired},
		{"valid key", "12345", http.StatusOK},
	} {
		if rec := gatedRequest(t, s, "Ping", ping, tc.key); rec.Code != tc.want {
			t.Fatalf("%s: code = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

// TestProvisioningGateOpenWithoutDeviceStore proves the gate is open when no
// device store is wired: a deployment that does not persist partnerships cannot
// enforce a policy, so it must not lock every client out.
func TestProvisioningGateOpenWithoutDeviceStore(t *testing.T) {
	s := NewServer(allowAuth) // no SetDeviceStore
	ping := &wbxml.Element{Page: wbxml.PagePing, Name: "Ping"}
	if rec := gatedRequest(t, s, "Ping", ping, ""); rec.Code == statusProvisioningRequired {
		t.Fatal("gate must be open when no device store is wired")
	}
}

// TestRemoteWipeForcesProvision proves a wipe-flagged device is forced back to
// Provision even when it presents a still-valid policy key. Without this the
// wipe would lie dormant until the device's own policy-refresh cycle — the
// failure mode that makes a remote wipe useless.
func TestRemoteWipeForcesProvision(t *testing.T) {
	s, database := provisionServer(t)
	if err := database.PutEASDevice(&db.EASDevice{
		Email: "bob@x.test", DeviceID: "DEV1", PolicyKey: "12345", WipeRequested: true,
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	ping := &wbxml.Element{Page: wbxml.PagePing, Name: "Ping"}
	if rec := gatedRequest(t, s, "Ping", ping, "12345"); rec.Code != statusProvisioningRequired {
		t.Fatalf("wipe-flagged valid-key command: code = %d, want 449", rec.Code)
	}
}

// TestRemoteWipeDirectiveAndAck proves the MS-ASPROV wipe exchange: a flagged
// device's Provision returns a RemoteWipe directive while the partnership
// survives, and the client's wipe acknowledgment returns the confirmation and
// removes the partnership so a returning device must provision from scratch.
func TestRemoteWipeDirectiveAndAck(t *testing.T) {
	s, database := provisionServer(t)
	if err := database.PutEASDevice(&db.EASDevice{
		Email: "bob@x.test", DeviceID: "DEV1", PolicyKey: "12345", WipeRequested: true,
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	directive := doProvision(t, s, &wbxml.Element{Page: wbxml.PageProvision, Name: "Provision"})
	if textOf(directive.Sub("Status")) != provStatusSuccess {
		t.Fatalf("directive Status = %q, want 1", textOf(directive.Sub("Status")))
	}
	if directive.Sub("RemoteWipe") == nil {
		t.Fatal("directive must carry RemoteWipe")
	}
	if _, err := database.GetEASDevice("bob@x.test", "DEV1"); err != nil {
		t.Fatalf("device removed before acknowledgment: %v", err)
	}

	ack := &wbxml.Element{Page: wbxml.PageProvision, Name: "Provision", Children: []*wbxml.Element{
		{Page: wbxml.PageProvision, Name: "RemoteWipe", Children: []*wbxml.Element{
			{Page: wbxml.PageProvision, Name: "Status", Text: provStatusSuccess},
		}},
	}}
	confirm := doProvision(t, s, ack)
	if confirm.Sub("RemoteWipe") == nil || textOf(confirm.Sub("Status")) != provStatusSuccess {
		t.Fatal("acknowledgment must return Status 1 + RemoteWipe")
	}
	if _, err := database.GetEASDevice("bob@x.test", "DEV1"); err == nil {
		t.Fatal("device must be removed after the wipe acknowledgment")
	}
}
