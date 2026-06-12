package nspi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestDNToMIDMapsKnownNames verifies DNToMId maps a known distinguished name to
// the GAL entry's minimal id and an unknown name to 0.
func TestDNToMIDMapsKnownNames(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
		{Email: "bob@x.test", DisplayName: "Bob", ObjectClass: "User"},
	}})

	body := wire.NewPush(0)
	body.Uint32(0)   // reserved
	body.Uint8(0xFF) // names present
	body.Uint32(2)   // name count
	body.Str(wire.BuildESSDN("alice"))
	body.Str(wire.BuildESSDN("nobody"))
	body.Uint32(0) // cb_auxin

	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(body.Bytes()))
	req.Header.Set("X-RequestType", "DNToMId")
	req = req.WithContext(WithEmail(req.Context(), "qa.bob@local.test"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	q := wire.NewPull(payloadAfterMeta(t, rec.Body.Bytes()), 0)
	q.Uint32() // status
	if result := q.Uint32(); result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	if q.Uint8() != 0xFF {
		t.Fatal("expected the minimal-id array")
	}
	if n := q.Uint32(); n != 2 {
		t.Fatalf("minimal-id count = %d, want 2", n)
	}
	if mid := q.Uint32(); mid != entryMid(0) {
		t.Errorf("alice mid = %#x, want %#x", mid, entryMid(0))
	}
	if mid := q.Uint32(); mid != nameUnresolved {
		t.Errorf("unknown-name mid = %#x, want 0", mid)
	}
}
