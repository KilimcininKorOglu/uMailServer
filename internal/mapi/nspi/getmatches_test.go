package nspi

import (
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// getMatches posts a GetMatches request carrying the given filter bytes and
// returns the matched minimal ids and the decoded rows.
func getMatches(t *testing.T, srv *Server, filter []byte, cols []wire.PropTag) (uint32, []uint32, []wire.PropertyRow) {
	t.Helper()
	body := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	body.Uint32(0)   // reserved1
	body.Uint8(0xFF) // state block present
	Stat{SortType: sortTypeDisplayName}.Push(body)
	body.Uint8(0)  // no explicit minimal-id list
	body.Uint32(0) // reserved2
	body.Uint8(0xFF)
	body.Raw(filter) // the restriction
	body.Uint8(0)    // no property name
	body.Uint32(50)  // requested row count
	body.Uint8(0xFF) // columns present
	pushProptags(body, cols)
	body.Uint32(0) // cb_auxin

	q := nspiRequest(t, srv, "GetMatches", body.Bytes())
	q.Uint32() // status
	result := q.Uint32()
	q.Uint8() // state-block marker
	PullStat(q)
	if result != ecSuccess {
		return result, nil, nil
	}
	q.Uint8() // minimal-id marker
	mids := make([]uint32, q.Uint32())
	for i := range mids {
		mids[i] = q.Uint32()
	}
	q.Uint8() // COLROW marker
	rcols := pullProptags(q)
	rows := make([]wire.PropertyRow, q.Uint32())
	for i := range rows {
		row, err := wire.PullPropertyRow(q, rcols)
		if err != nil {
			t.Fatalf("row %d decode: %v", i, err)
		}
		rows[i] = row
	}
	return result, mids, rows
}

// anrFilter builds a RES_PROPERTY restriction targeting PidTagAnr (the GAL search
// key), with the address-book presence bytes the request format requires.
func anrFilter(t *testing.T, search string) []byte {
	t.Helper()
	p := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	p.Uint8(resProperty)
	p.Uint8(relopEQ)
	p.Uint32(uint32(wire.PidTagAnr))
	p.Uint8(0xFF) // value present
	if err := (wire.TaggedPropertyValue{Tag: wire.PidTagAnr, Value: search}).Push(p); err != nil {
		t.Fatalf("push ANR value: %v", err)
	}
	return p.Bytes()
}

// TestGetMatchesAnr verifies a PidTagAnr property restriction matches GAL entries
// by a case-insensitive substring of the display name or address.
func TestGetMatchesAnr(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
		{Email: "bob@x.test", DisplayName: "Bob", ObjectClass: "User"},
		{Email: "carol@x.test", DisplayName: "Carol", ObjectClass: "User"},
	}})
	cols := []wire.PropTag{wire.PidTagDisplayName, wire.PidTagSmtpAddress}

	result, mids, rows := getMatches(t, srv, anrFilter(t, "ali"), cols)
	if result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	if len(mids) != 1 || len(rows) != 1 {
		t.Fatalf("matched %d ids / %d rows, want 1 / 1", len(mids), len(rows))
	}
	if mids[0] != entryMid(0) {
		t.Errorf("matched mid = %#x, want entry 0 (Alice)", mids[0])
	}
	if dn, ok := rows[0].Values[0].(string); !ok || dn != "Alice" {
		t.Errorf("matched row = %v, want Alice", rows[0].Values[0])
	}

	// A substring of nothing matches no entry.
	result, mids, _ = getMatches(t, srv, anrFilter(t, "zzznomatch"), cols)
	if result != ecSuccess {
		t.Fatalf("no-match result = %#x, want success", result)
	}
	if len(mids) != 0 {
		t.Errorf("matched %d ids, want 0", len(mids))
	}
}

// TestGetMatchesAndExist verifies a RES_AND of an EXIST term and an ANR term
// matches only entries satisfying both.
func TestGetMatchesAndExist(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
		{Email: "bob@x.test", DisplayName: "Bob", ObjectClass: "User"},
	}})
	cols := []wire.PropTag{wire.PidTagDisplayName}

	// AND( EXIST(PidTagDisplayName), PROPERTY(ANR contains "bob") )
	p := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
	p.Uint8(resAnd)
	p.Uint16(2) // operand count
	p.Uint8(resExist)
	p.Uint32(uint32(wire.PidTagDisplayName))
	p.Raw(anrFilter(t, "bob"))

	result, mids, rows := getMatches(t, srv, p.Bytes(), cols)
	if result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	if len(mids) != 1 || len(rows) != 1 {
		t.Fatalf("matched %d ids / %d rows, want 1 / 1", len(mids), len(rows))
	}
	if dn, ok := rows[0].Values[0].(string); !ok || dn != "Bob" {
		t.Errorf("matched row = %v, want Bob", rows[0].Values[0])
	}
}
