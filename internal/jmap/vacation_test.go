package jmap

import (
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestVacationResponseCanonicalOOF verifies JMAP VacationResponse/set writes the
// canonical semcore OOF policy (the same store EWS and webmail use) and that
// VacationResponse/get reads it back, so the out-of-office reply is one source
// of truth across protocols.
func TestVacationResponseCanonicalOOF(t *testing.T) {
	store, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})

	var recompiled int
	srv := &Server{
		policyStore:    store.Policy(),
		recompileSieve: func(string) error { recompiled++; return nil },
	}
	user := "alice@ex.test"

	// Set a vacation response over JMAP.
	setResp := srv.handleVacationResponseSet(user, MethodCall{
		ID:   "c1",
		Name: "VacationResponse/set",
		Args: map[string]interface{}{
			"accountId": user,
			"update": map[string]interface{}{
				"singleton": map[string]interface{}{
					"isEnabled": true,
					"subject":   "On leave",
					"textBody":  "Back next week.",
				},
			},
		},
	})
	updated, _ := setResp.Args["updated"].(map[string]interface{})
	if _, ok := updated["singleton"]; !ok {
		t.Fatalf("expected singleton updated, got args: %+v", setResp.Args)
	}
	if recompiled == 0 {
		t.Error("expected Sieve recompile after VacationResponse/set")
	}

	// Canonical OOF must hold the policy (the EWS/webmail read path).
	mbid, err := semcore.NewMailboxId(user)
	if err != nil {
		t.Fatalf("NewMailboxId: %v", err)
	}
	oofID, err := semcore.NewOOFId(mbid.String())
	if err != nil {
		t.Fatalf("NewOOFId: %v", err)
	}
	policy, err := store.Policy().GetOOF(oofID)
	if err != nil {
		t.Fatalf("GetOOF: %v", err)
	}
	if !policy.Enabled || policy.State != "Enabled" || policy.Subject != "On leave" || policy.TextBody != "Back next week." {
		t.Errorf("canonical OOF mismatch: %+v", policy)
	}
	if !policy.IsActiveNow() {
		t.Error("enabled non-scheduled OOF should be active now")
	}

	// VacationResponse/get must reflect the stored policy.
	getResp := srv.handleVacationResponseGet(user, MethodCall{
		ID: "c2", Name: "VacationResponse/get",
		Args: map[string]interface{}{"accountId": user},
	})
	list, _ := getResp.Args["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("expected 1 VacationResponse, got %d", len(list))
	}
	obj, _ := list[0].(map[string]interface{})
	if obj["isEnabled"] != true || obj["subject"] != "On leave" || obj["textBody"] != "Back next week." {
		t.Errorf("get round-trip mismatch: %+v", obj)
	}
	if obj["id"] != vacationSingletonID {
		t.Errorf("expected singleton id, got %v", obj["id"])
	}
}

// TestVacationResponseNotSupportedWhenUnwired verifies the handler reports
// notSupported when the canonical store is not wired.
func TestVacationResponseNotSupportedWhenUnwired(t *testing.T) {
	srv := &Server{}
	resp := srv.handleVacationResponseGet("alice@ex.test", MethodCall{
		ID: "c1", Name: "VacationResponse/get",
		Args: map[string]interface{}{"accountId": "alice@ex.test"},
	})
	if resp.Name != "error" || resp.Args["type"] != "notSupported" {
		t.Errorf("expected notSupported error, got %+v", resp)
	}
}
