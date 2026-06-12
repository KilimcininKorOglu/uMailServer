package nspi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

func nspiRequest(t *testing.T, srv *Server, reqType string, body []byte) *wire.Pull {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(body))
	req.Header.Set("X-RequestType", reqType)
	req = req.WithContext(WithEmail(req.Context(), "qa.bob@local.test"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want 200", reqType, rec.Code)
	}
	return wire.NewPull(payloadAfterMeta(t, rec.Body.Bytes()), wire.FlagABK|wire.FlagUTF16)
}

// TestCompareMinIds verifies the comparison reflects the entries' table order:
// positive when the second id follows the first, negative when it precedes, zero
// when equal.
func TestCompareMinIds(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
		{Email: "bob@x.test", DisplayName: "Bob", ObjectClass: "User"},
	}})

	compare := func(mid1, mid2 uint32) int32 {
		body := wire.NewPush(0)
		body.Uint32(0) // reserved
		body.Uint8(0)  // no state block
		body.Uint32(mid1)
		body.Uint32(mid2)
		body.Uint32(0) // cb_auxin
		q := nspiRequest(t, srv, "CompareMinIds", body.Bytes())
		q.Uint32() // status
		cmp := int32(q.Uint32())
		if result := q.Uint32(); result != ecSuccess {
			t.Fatalf("result = %#x, want success", result)
		}
		return cmp
	}

	if cmp := compare(entryMid(0), entryMid(1)); cmp != 1 {
		t.Errorf("compare(pos0, pos1) = %d, want 1", cmp)
	}
	if cmp := compare(entryMid(1), entryMid(0)); cmp != -1 {
		t.Errorf("compare(pos1, pos0) = %d, want -1", cmp)
	}
	if cmp := compare(entryMid(0), entryMid(0)); cmp != 0 {
		t.Errorf("compare(pos0, pos0) = %d, want 0", cmp)
	}
}

// TestQueryColumnsReturnsDefaults verifies QueryColumns reports the GAL's served
// columns.
func TestQueryColumnsReturnsDefaults(t *testing.T) {
	srv := NewServer()

	body := wire.NewPush(0)
	body.Uint32(0) // reserved
	body.Uint32(0) // flags
	body.Uint32(0) // cb_auxin

	q := nspiRequest(t, srv, "QueryColumns", body.Bytes())
	q.Uint32() // status
	if result := q.Uint32(); result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	if q.Uint8() != 0xFF {
		t.Fatal("expected the column array")
	}
	cols := pullProptags(q)
	if len(cols) != len(defaultColumns) {
		t.Fatalf("column count = %d, want %d", len(cols), len(defaultColumns))
	}
	for i, c := range defaultColumns {
		if cols[i] != c {
			t.Errorf("column[%d] = %#x, want %#x", i, cols[i], c)
		}
	}
}

// TestModificationsRejected verifies the read-only address book rejects ModProps
// and ModLinkAtt as unsupported.
func TestModificationsRejected(t *testing.T) {
	srv := NewServer()
	for _, reqType := range []string{"ModProps", "ModLinkAtt"} {
		q := nspiRequest(t, srv, reqType, []byte{0, 0, 0, 0})
		q.Uint32() // status
		if result := q.Uint32(); result != ecNotSupported {
			t.Errorf("%s result = %#x, want ecNotSupported", reqType, result)
		}
	}
}
