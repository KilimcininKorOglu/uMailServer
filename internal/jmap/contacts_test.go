package jmap

import (
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/carddav"
	"github.com/umailserver/umailserver/internal/semcore"
)

func newContactsTestServer(t *testing.T) (*Server, *carddav.CollabStore) {
	t.Helper()
	store, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	cs := carddav.NewCollabStore(store.Collaboration(), store.Identity())
	return &Server{cardStore: cs}, cs
}

// TestContactCardSetThenGet verifies a contact created over JMAP lands in the
// canonical store as a vCard (the EWS/CardDAV read path) and reads back with its
// core JSContact fields intact.
func TestContactCardSetThenGet(t *testing.T) {
	srv, cs := newContactsTestServer(t)
	user := "alice@ex.test"

	setResp := srv.handleContactCardSet(user, MethodCall{
		ID:   "c1",
		Name: "ContactCard/set",
		Args: map[string]interface{}{
			"accountId": user,
			"create": map[string]interface{}{
				"new1": map[string]interface{}{
					"@type": "Card",
					"uid":   "card-1",
					"name": map[string]interface{}{
						"@type": "Name",
						"full":  "Grace Hopper",
						"components": []interface{}{
							map[string]interface{}{"@type": "NameComponent", "kind": "given", "value": "Grace"},
							map[string]interface{}{"@type": "NameComponent", "kind": "surname", "value": "Hopper"},
						},
					},
					"emails": map[string]interface{}{
						"e1": map[string]interface{}{"@type": "EmailAddress", "address": "grace@ex.test"},
					},
					"phones": map[string]interface{}{
						"p1": map[string]interface{}{"@type": "Phone", "number": "+1-555-0100"},
					},
					"organizations": map[string]interface{}{
						"o1": map[string]interface{}{"@type": "Organization", "name": "Navy"},
					},
				},
			},
		},
	}, map[string]string{})

	created := asMap(setResp.Args["created"])
	obj := asMap(created["new1"])
	if obj == nil || obj["id"] != "card-1" {
		t.Fatalf("expected card-1 created, got: %+v", setResp.Args)
	}

	// Cross-protocol: the canonical store holds a vCard (CardDAV/EWS read path).
	raws, err := cs.GetContacts(user, jmapDefaultAddressBookID)
	if err != nil {
		t.Fatalf("GetContacts: %v", err)
	}
	if len(raws) != 1 || !strings.Contains(raws[0], "FN:Grace Hopper") || !strings.Contains(raws[0], "EMAIL:grace@ex.test") {
		t.Fatalf("canonical vCard missing or wrong: %v", raws)
	}

	getResp := srv.handleContactCardGet(user, MethodCall{
		ID: "c2", Name: "ContactCard/get",
		Args: map[string]interface{}{"accountId": user, "ids": []interface{}{"card-1"}},
	})
	list := asSlice(getResp.Args["list"])
	if len(list) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(list))
	}
	card := asMap(list[0])
	name := asMap(card["name"])
	if name["full"] != "Grace Hopper" {
		t.Errorf("name round-trip mismatch: %+v", card["name"])
	}
	emails := asMap(card["emails"])
	e1 := asMap(emails["e1"])
	if e1["address"] != "grace@ex.test" {
		t.Errorf("email round-trip mismatch: %+v", card["emails"])
	}
	orgs := asMap(card["organizations"])
	o1 := asMap(orgs["o1"])
	if o1["name"] != "Navy" {
		t.Errorf("organization round-trip mismatch: %+v", card["organizations"])
	}
}

// TestContactCardUpdatePreservesUnknownProperties verifies an update over JMAP
// carries verbatim any vCard property JMAP does not model, so CardDAV/EWS data
// is never silently lost.
func TestContactCardUpdatePreservesUnknownProperties(t *testing.T) {
	srv, cs := newContactsTestServer(t)
	user := "bob@ex.test"

	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c-9\r\nFN:Old Name\r\n" +
		"EMAIL:old@ex.test\r\nX-CUSTOM-FIELD:keepme\r\nEND:VCARD\r\n"
	if err := cs.SaveContact(user, jmapDefaultAddressBookID, &carddav.Contact{UID: "c-9"}, vcf); err != nil {
		t.Fatalf("seed SaveContact: %v", err)
	}

	resp := srv.handleContactCardSet(user, MethodCall{
		ID: "u1", Name: "ContactCard/set",
		Args: map[string]interface{}{
			"accountId": user,
			"update": map[string]interface{}{
				"c-9": map[string]interface{}{
					"name": map[string]interface{}{"@type": "Name", "full": "New Name"},
				},
			},
		},
	}, nil)
	updated := asMap(resp.Args["updated"])
	if _, ok := updated["c-9"]; !ok {
		t.Fatalf("expected c-9 updated, got: %+v", resp.Args)
	}

	raw, err := cs.GetContact(user, jmapDefaultAddressBookID, "c-9")
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if !strings.Contains(raw, "FN:New Name") {
		t.Errorf("name not updated: %v", raw)
	}
	if !strings.Contains(raw, "X-CUSTOM-FIELD:keepme") {
		t.Errorf("X-CUSTOM-FIELD dropped on update — cross-protocol data loss: %v", raw)
	}
}

func TestContactsNotSupportedWhenUnwired(t *testing.T) {
	srv := &Server{}
	resp := srv.handleContactCardGet("alice@ex.test", MethodCall{
		ID: "c1", Name: "ContactCard/get",
		Args: map[string]interface{}{"accountId": "alice@ex.test"},
	})
	if resp.Name != "error" || resp.Args["type"] != "notSupported" {
		t.Errorf("expected notSupported error, got %+v", resp)
	}
}
