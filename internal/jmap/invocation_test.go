package jmap

import (
	"encoding/json"
	"testing"
)

// A JMAP request must be accepted in the RFC 8620 §3.2 wire form, where each
// method call is a 3-element array [ name, arguments, methodCallId ] — not an
// object. This is what real JMAP clients send; decoding it wrong makes the
// server unreachable by any standards-compliant client.
func TestMethodCall_DecodesRFC8620Tuple(t *testing.T) {
	raw := `{"using":["urn:ietf:params:jmap:core"],` +
		`"methodCalls":[["Email/get",{"accountId":"a@b.test","ids":["x"]},"c1"]]}`
	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("decode tuple request: %v", err)
	}
	if len(req.MethodCalls) != 1 {
		t.Fatalf("MethodCalls = %d, want 1", len(req.MethodCalls))
	}
	mc := req.MethodCalls[0]
	if mc.Name != "Email/get" || mc.ID != "c1" {
		t.Errorf("name/id = %q/%q, want Email/get/c1", mc.Name, mc.ID)
	}
	if mc.Args["accountId"] != "a@b.test" {
		t.Errorf("accountId arg = %v, want a@b.test", mc.Args["accountId"])
	}
}

// The object form {name,args,id} is NOT valid RFC 8620 and must be rejected,
// so the server cannot silently accept its old non-standard dialect.
func TestMethodCall_RejectsObjectForm(t *testing.T) {
	var mc MethodCall
	err := json.Unmarshal([]byte(`{"name":"Email/get","args":{},"id":"c1"}`), &mc)
	if err == nil {
		t.Fatal("object-form method call must be rejected, got nil error")
	}
}

// A response must be emitted as an Invocation tuple so clients can index it as
// [name, args, id]; the round-trip back into a Response must recover the fields.
func TestResponse_MarshalsAsTupleAndRoundTrips(t *testing.T) {
	r := Response{Name: "Email/get", Args: map[string]interface{}{"state": "s1"}, ID: "c1"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	// Must be a JSON array, not an object.
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		t.Fatalf("response is not a JSON array: %v (%s)", err, b)
	}
	if len(arr) != 3 {
		t.Fatalf("response tuple len = %d, want 3 (%s)", len(arr), b)
	}
	var back Response
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("round-trip decode: %v", err)
	}
	if back.Name != "Email/get" || back.ID != "c1" || back.Args["state"] != "s1" {
		t.Errorf("round-trip = %+v, want Email/get/c1/state=s1", back)
	}
}

// nil Args must serialize as an empty object, not JSON null, so clients always
// receive a usable arguments object at tuple position 1.
func TestResponse_NilArgsBecomesEmptyObject(t *testing.T) {
	b, err := json.Marshal(Response{Name: "Foo/bar", ID: "c1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil || len(arr) != 3 {
		t.Fatalf("not a 3-tuple: %v (%s)", err, b)
	}
	if string(arr[1]) != "{}" {
		t.Errorf("args = %s, want {}", arr[1])
	}
}
