package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
	"github.com/umailserver/umailserver/internal/db"
)

// provisionServer builds an EAS server backed by a real bbolt device store.
func provisionServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	})
	s := NewServer(allowAuth)
	s.SetDeviceStore(database)
	return s, database
}

// doProvision POSTs a Provision request body and returns the decoded response.
func doProvision(t *testing.T, s *Server, body *wbxml.Element) *wbxml.Element {
	t.Helper()
	reqBytes, err := wbxml.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/Microsoft-Server-ActiveSync?Cmd=Provision&DeviceId=DEV1&DeviceType=iPhone",
		bytes.NewReader(reqBytes))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Provision status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return resp
}

func provisionRequest(children ...*wbxml.Element) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageProvision, Name: "Provision", Children: []*wbxml.Element{
		{Page: wbxml.PageProvision, Name: "Policies", Children: []*wbxml.Element{
			{Page: wbxml.PageProvision, Name: "Policy", Children: children},
		}},
	}}
}

// TestProvisionTwoPhase exercises the MS-ASPROV handshake end to end: the
// initial request yields a temporary key plus the policy document, the
// acknowledgment of that key yields a distinct final key with no document, and
// the device store ends up holding the final key for later validation.
func TestProvisionTwoPhase(t *testing.T) {
	s, database := provisionServer(t)

	// Phase 1: PolicyType only, no PolicyKey.
	resp := doProvision(t, s, provisionRequest(
		&wbxml.Element{Page: wbxml.PageProvision, Name: "PolicyType", Text: policyType},
	))
	if st := resp.Sub("Status"); st == nil || st.Text != provStatusSuccess {
		t.Fatalf("phase 1 Status = %v, want 1", st)
	}
	policy := resp.Sub("Policies").Sub("Policy")
	tempKey := policy.Sub("PolicyKey")
	if tempKey == nil || tempKey.Text == "" {
		t.Fatalf("phase 1 returned no PolicyKey")
	}
	if data := policy.Sub("Data"); data == nil || data.Sub("EASProvisionDoc") == nil {
		t.Fatalf("phase 1 missing EASProvisionDoc")
	}
	if dev, err := database.GetEASDevice("bob@x.test", "DEV1"); err != nil || dev.PolicyKey != tempKey.Text {
		t.Fatalf("phase 1 did not persist the temp key (err=%v)", err)
	}

	// Phase 2: acknowledge the temporary key.
	resp = doProvision(t, s, provisionRequest(
		&wbxml.Element{Page: wbxml.PageProvision, Name: "PolicyType", Text: policyType},
		&wbxml.Element{Page: wbxml.PageProvision, Name: "PolicyKey", Text: tempKey.Text},
		&wbxml.Element{Page: wbxml.PageProvision, Name: "Status", Text: provStatusSuccess},
	))
	if st := resp.Sub("Status"); st == nil || st.Text != provStatusSuccess {
		t.Fatalf("phase 2 Status = %v, want 1", st)
	}
	policy = resp.Sub("Policies").Sub("Policy")
	finalKey := policy.Sub("PolicyKey")
	if finalKey == nil || finalKey.Text == "" || finalKey.Text == tempKey.Text {
		t.Fatalf("phase 2 final key = %v, want a fresh key distinct from %q", finalKey, tempKey.Text)
	}
	if policy.Sub("Data") != nil {
		t.Fatalf("phase 2 must not resend the policy document")
	}
	dev, err := database.GetEASDevice("bob@x.test", "DEV1")
	if err != nil || dev.PolicyKey != finalKey.Text {
		t.Fatalf("phase 2 did not persist the final key (err=%v)", err)
	}
}

// TestProvisionAckMismatch rejects an acknowledgment that echoes a key the
// server never issued, prompting the client to restart provisioning.
func TestProvisionAckMismatch(t *testing.T) {
	s, _ := provisionServer(t)
	// Seed a real partnership so the lookup succeeds but the key differs.
	doProvision(t, s, provisionRequest(
		&wbxml.Element{Page: wbxml.PageProvision, Name: "PolicyType", Text: policyType},
	))
	resp := doProvision(t, s, provisionRequest(
		&wbxml.Element{Page: wbxml.PageProvision, Name: "PolicyType", Text: policyType},
		&wbxml.Element{Page: wbxml.PageProvision, Name: "PolicyKey", Text: "999999999"},
		&wbxml.Element{Page: wbxml.PageProvision, Name: "Status", Text: provStatusSuccess},
	))
	if st := resp.Sub("Status"); st == nil || st.Text != provStatusProtoErr {
		t.Fatalf("mismatched ack Status = %v, want 2", st)
	}
}
