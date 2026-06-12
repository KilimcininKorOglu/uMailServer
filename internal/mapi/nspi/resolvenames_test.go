package nspi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestResolveNamesWClassifiesEachName verifies ResolveNamesW reports a status per
// requested name (resolved / ambiguous / unresolved) and returns a COLROW row
// only for the names that match exactly one entry, so the id array and the row
// set have different lengths.
func TestResolveNamesWClassifiesEachName(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
		{Email: "bob@x.test", DisplayName: "Bob", ObjectClass: "User"},
	}})
	cols := []wire.PropTag{wire.PidTagDisplayName, wire.PidTagSmtpAddress}

	// "alice@x.test" matches one entry, "x.test" matches both (ambiguous),
	// "nobody" matches none. The leading "SMTP:" is stripped before resolution.
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint32(0)   // reserved
	body.Uint8(0)    // no state block
	body.Uint8(0xFF) // proptags present
	pushProptags(body, cols)
	body.Uint8(0xFF) // names present
	body.Uint32(3)   // name count
	body.WStr("SMTP:alice@x.test")
	body.WStr("x.test")
	body.WStr("nobody")
	body.Uint32(0) // cb_auxin

	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(body.Bytes()))
	req.Header.Set("X-RequestType", "ResolveNamesW")
	req = req.WithContext(WithEmail(req.Context(), "qa.bob@local.test"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	q := wire.NewPull(payloadAfterMeta(t, rec.Body.Bytes()), wire.FlagABK|wire.FlagUTF16)
	q.Uint32() // status
	if result := q.Uint32(); result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	q.Uint32() // code page
	if q.Uint8() != 0xFF {
		t.Fatal("expected the minimal-id array")
	}
	if n := q.Uint32(); n != 3 {
		t.Fatalf("minimal-id count = %d, want 3 (one per name)", n)
	}
	wantMids := []uint32{nameResolved, nameAmbiguous, nameUnresolved}
	for i, want := range wantMids {
		if got := q.Uint32(); got != want {
			t.Errorf("mid[%d] = %d, want %d", i, got, want)
		}
	}
	if q.Uint8() != 0xFF {
		t.Fatal("expected a COLROW")
	}
	rcols := pullProptags(q)
	if rowCount := q.Uint32(); rowCount != 1 {
		t.Fatalf("COLROW row count = %d, want 1 (only the resolved name)", rowCount)
	}
	row, err := wire.PullPropertyRow(q, rcols)
	if err != nil {
		t.Fatalf("row decode: %v", err)
	}
	if dn, ok := row.Values[0].(string); !ok || dn != "Alice" {
		t.Errorf("resolved row display name = %v, want Alice", row.Values[0])
	}
	if q.Err() != nil {
		t.Fatalf("trailing parse error: %v", q.Err())
	}
}

// TestResolveNamesWWithoutDirectoryFails verifies ResolveNamesW reports an error
// when no GAL source is configured.
func TestResolveNamesWWithoutDirectoryFails(t *testing.T) {
	srv := NewServer() // no directory

	body := wire.NewPush(wire.FlagUTF16)
	body.Uint32(0)   // reserved
	body.Uint8(0)    // no state block
	body.Uint8(0)    // default proptags
	body.Uint8(0xFF) // names present
	body.Uint32(1)
	body.WStr("alice@x.test")
	body.Uint32(0)

	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(body.Bytes()))
	req.Header.Set("X-RequestType", "ResolveNamesW")
	req = req.WithContext(WithEmail(req.Context(), "qa.bob@local.test"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	q := wire.NewPull(payloadAfterMeta(t, rec.Body.Bytes()), wire.FlagABK|wire.FlagUTF16)
	q.Uint32() // status
	if result := q.Uint32(); result != ecError {
		t.Errorf("result = %#x, want ecError", result)
	}
}
