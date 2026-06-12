package nspi

import (
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestSeekEntriesPositionsAtName verifies SeekEntries positions the cursor at the
// first display-name-ordered entry at or after the target and returns that row,
// and reports not-found when the target sorts past every entry.
func TestSeekEntriesPositionsAtName(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "c@x.test", DisplayName: "Carol", ObjectClass: "User"},
		{Email: "a@x.test", DisplayName: "Alice", ObjectClass: "User"},
		{Email: "b@x.test", DisplayName: "Bob", ObjectClass: "User"},
	}})
	cols := []wire.PropTag{wire.PidTagDisplayName, wire.PidTagSmtpAddress}

	seek := func(target string) *wire.Pull {
		body := wire.NewPush(wire.FlagABK | wire.FlagUTF16)
		body.Uint32(0)   // reserved
		body.Uint8(0xFF) // state block present
		Stat{SortType: sortTypeDisplayName}.Push(body)
		body.Uint8(0xFF) // target present
		if err := (wire.TaggedPropertyValue{Tag: wire.PidTagDisplayName, Value: target}).Push(body); err != nil {
			t.Fatalf("push target: %v", err)
		}
		body.Uint8(0)    // no explicit table
		body.Uint8(0xFF) // columns present
		pushProptags(body, cols)
		body.Uint32(0) // cb_auxin
		return nspiRequest(t, srv, "SeekEntries", body.Bytes())
	}

	// The GAL sorts to Alice(0), Bob(1), Carol(2); seeking "Bob" lands on Bob.
	q := seek("Bob")
	q.Uint32() // status
	if result := q.Uint32(); result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	q.Uint8() // state-block marker
	stat := PullStat(q)
	if stat.NumPos != 1 {
		t.Errorf("num_pos = %d, want 1", stat.NumPos)
	}
	if stat.CurrentRec != entryMid(1) {
		t.Errorf("cur_rec = %#x, want entry 1", stat.CurrentRec)
	}
	q.Uint8() // COLROW marker
	rcols := pullProptags(q)
	if rowCount := q.Uint32(); rowCount != 1 {
		t.Fatalf("row count = %d, want 1", rowCount)
	}
	row, err := wire.PullPropertyRow(q, rcols)
	if err != nil {
		t.Fatalf("row decode: %v", err)
	}
	if dn, ok := row.Values[0].(string); !ok || dn != "Bob" {
		t.Errorf("positioned row = %v, want Bob", row.Values[0])
	}

	// A target past the last entry is not found.
	q = seek("Zzz")
	q.Uint32() // status
	if result := q.Uint32(); result != ecNotFound {
		t.Errorf("seek past end result = %#x, want ecNotFound", result)
	}
}
