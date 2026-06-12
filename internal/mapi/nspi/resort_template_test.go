package nspi

import (
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestGetTemplateInfoEmpty verifies GetTemplateInfo succeeds without a template
// row, so the client uses its built-in templates.
func TestGetTemplateInfoEmpty(t *testing.T) {
	srv := NewServer()

	body := wire.NewPush(0)
	body.Uint32(0) // flags
	body.Uint32(0) // template type
	body.Uint8(0)  // no distinguished name
	body.Uint32(0) // code page
	body.Uint32(0) // locale id
	body.Uint32(0) // cb_auxin

	q := nspiRequest(t, srv, "GetTemplateInfo", body.Bytes())
	q.Uint32() // status
	if result := q.Uint32(); result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	q.Uint32() // code page
	if q.Uint8() != 0 {
		t.Error("expected no template row")
	}
}

// TestResortRestrictionSorts verifies ResortRestriction returns the input ids in
// display-name order.
func TestResortRestrictionSorts(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "c@x.test", DisplayName: "Carol", ObjectClass: "User"},
		{Email: "a@x.test", DisplayName: "Alice", ObjectClass: "User"},
		{Email: "b@x.test", DisplayName: "Bob", ObjectClass: "User"},
	}})
	// The GAL sorts to Alice(0), Bob(1), Carol(2). Feed two ids out of order.

	body := wire.NewPush(0)
	body.Uint32(0)   // reserved
	body.Uint8(0xFF) // state block present
	Stat{}.Push(body)
	body.Uint8(0xFF) // minimal-id list present
	body.Uint32(2)
	body.Uint32(entryMid(2)) // Carol
	body.Uint32(entryMid(0)) // Alice
	body.Uint32(0)           // cb_auxin

	q := nspiRequest(t, srv, "ResortRestriction", body.Bytes())
	q.Uint32() // status
	if result := q.Uint32(); result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	q.Uint8() // state-block marker
	stat := PullStat(q)
	if stat.TotalRec != 2 {
		t.Errorf("total_rec = %d, want 2", stat.TotalRec)
	}
	q.Uint8() // minimal-id marker
	n := q.Uint32()
	if n != 2 {
		t.Fatalf("sorted count = %d, want 2", n)
	}
	if first := q.Uint32(); first != entryMid(0) {
		t.Errorf("first sorted id = %#x, want Alice (entry 0)", first)
	}
	if second := q.Uint32(); second != entryMid(2) {
		t.Errorf("second sorted id = %#x, want Carol (entry 2)", second)
	}
}
