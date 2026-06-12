package nspi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// fakeDir is an in-memory GAL for the address-book tests.
type fakeDir struct{ entries []DirectoryEntry }

func (f fakeDir) ResolveGAL(entry string) []DirectoryEntry {
	if entry == "" {
		return f.entries
	}
	var out []DirectoryEntry
	for _, e := range f.entries {
		if strings.Contains(strings.ToLower(e.DisplayName), strings.ToLower(entry)) ||
			strings.Contains(strings.ToLower(e.Email), strings.ToLower(entry)) {
			out = append(out, e)
		}
	}
	return out
}

// queryRows posts a QueryRows request enumerating from the cursor and returns the
// decoded response payload after the meta block.
func queryRows(t *testing.T, srv *Server, cols []wire.PropTag, count uint32) []byte {
	t.Helper()
	body := wire.NewPush(0)
	body.Uint32(0) // flags
	body.Uint8(0)  // no state block (cursor at position 0)
	body.Uint32(0) // explicit minimal-id count 0
	body.Uint32(count)
	body.Uint8(0xFF) // columns present
	pushProptags(body, cols)
	body.Uint32(0) // cb_auxin

	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(body.Bytes()))
	req.Header.Set("X-RequestType", "QueryRows")
	req = req.WithContext(WithEmail(req.Context(), "qa.bob@local.test"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("QueryRows status = %d, want 200", rec.Code)
	}
	return payloadAfterMeta(t, rec.Body.Bytes())
}

// TestQueryRowsReturnsGAL verifies QueryRows enumerates the GAL as a COLROW with
// the requested columns and an updated state block.
func TestQueryRowsReturnsGAL(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
		{Email: "bob@x.test", DisplayName: "Bob", ObjectClass: "DistributionList"},
	}})
	cols := []wire.PropTag{wire.PidTagDisplayName, wire.PidTagSmtpAddress, wire.PidTagDisplayType}

	q := wire.NewPull(queryRows(t, srv, cols, 10), wire.FlagABK|wire.FlagUTF16)
	q.Uint32() // status
	if result := q.Uint32(); result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	q.Uint8() // has-state marker
	stat := PullStat(q)
	if stat.TotalRec != 2 {
		t.Errorf("total_rec = %d, want 2", stat.TotalRec)
	}
	if stat.CurrentRec != midEnd {
		t.Errorf("cur_rec = %#x, want midEnd", stat.CurrentRec)
	}
	q.Uint8() // COLROW marker
	rcols := pullProptags(q)
	if len(rcols) != len(cols) {
		t.Fatalf("colrow columns = %d, want %d", len(rcols), len(cols))
	}
	if rowcount := q.Uint32(); rowcount != 2 {
		t.Fatalf("row count = %d, want 2", rowcount)
	}

	alice, err := wire.PullPropertyRow(q, rcols)
	if err != nil {
		t.Fatalf("row 0 decode: %v", err)
	}
	if dn, ok := alice.Values[0].(string); !ok || dn != "Alice" {
		t.Errorf("row 0 display name = %v, want Alice", alice.Values[0])
	}
	if sm, ok := alice.Values[1].(string); !ok || sm != "alice@x.test" {
		t.Errorf("row 0 smtp = %v, want alice@x.test", alice.Values[1])
	}
	if dt, ok := alice.Values[2].(uint32); !ok || dt != dtMailUser {
		t.Errorf("row 0 display type = %v, want mail user", alice.Values[2])
	}

	bob, err := wire.PullPropertyRow(q, rcols)
	if err != nil {
		t.Fatalf("row 1 decode: %v", err)
	}
	if dt, ok := bob.Values[2].(uint32); !ok || dt != dtDistList {
		t.Errorf("row 1 display type = %v, want dist list", bob.Values[2])
	}
	if q.Err() != nil {
		t.Fatalf("trailing parse error: %v", q.Err())
	}
}

// TestGetPropsReturnsEntry verifies GetProps returns the selected entry's
// properties as a tagged-value array.
func TestGetPropsReturnsEntry(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
	}})
	cols := []wire.PropTag{wire.PidTagDisplayName, wire.PidTagSmtpAddress}

	body := wire.NewPush(0)
	body.Uint32(0)   // flags
	body.Uint8(0xFF) // state block present
	Stat{CurrentRec: entryMid(0)}.Push(body)
	body.Uint8(0xFF) // proptags present
	pushProptags(body, cols)
	body.Uint32(0) // cb_auxin

	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(body.Bytes()))
	req.Header.Set("X-RequestType", "GetProps")
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
		t.Fatal("expected a property row")
	}
	if count := q.Uint32(); count != 2 {
		t.Fatalf("value count = %d, want 2", count)
	}
	v0, err := wire.PullTaggedPropertyValue(q)
	if err != nil {
		t.Fatalf("value 0 decode: %v", err)
	}
	if v0.Tag != wire.PidTagDisplayName {
		t.Errorf("value 0 tag = %#x, want PidTagDisplayName", v0.Tag)
	}
	if dn, ok := v0.Value.(string); !ok || dn != "Alice" {
		t.Errorf("display name = %v, want Alice", v0.Value)
	}
	v1, err := wire.PullTaggedPropertyValue(q)
	if err != nil {
		t.Fatalf("value 1 decode: %v", err)
	}
	if sm, ok := v1.Value.(string); !ok || sm != "alice@x.test" {
		t.Errorf("smtp = %v, want alice@x.test", v1.Value)
	}
}

// TestGetPropsUnknownMidFails verifies GetProps reports not-found for a minimal id
// that is not a GAL entry.
func TestGetPropsUnknownMidFails(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{{Email: "a@x.test", DisplayName: "A", ObjectClass: "User"}}})

	body := wire.NewPush(0)
	body.Uint32(0)
	body.Uint8(0xFF)
	Stat{CurrentRec: 0x9999}.Push(body) // no such entry
	body.Uint8(0)                       // default proptags
	body.Uint32(0)                      // cb_auxin

	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(body.Bytes()))
	req.Header.Set("X-RequestType", "GetProps")
	req = req.WithContext(WithEmail(req.Context(), "qa.bob@local.test"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	q := wire.NewPull(payloadAfterMeta(t, rec.Body.Bytes()), wire.FlagABK|wire.FlagUTF16)
	q.Uint32() // status
	if result := q.Uint32(); result != ecNotFound {
		t.Errorf("result = %#x, want ecNotFound", result)
	}
}

// TestQueryRowsWithoutDirectoryFails verifies QueryRows reports an error when no
// GAL source is configured.
func TestQueryRowsWithoutDirectoryFails(t *testing.T) {
	srv := NewServer() // no directory
	q := wire.NewPull(queryRows(t, srv, []wire.PropTag{wire.PidTagDisplayName}, 10), wire.FlagABK|wire.FlagUTF16)
	q.Uint32() // status
	if result := q.Uint32(); result != ecError {
		t.Errorf("result = %#x, want ecError", result)
	}
}
