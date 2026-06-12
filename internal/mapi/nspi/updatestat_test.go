package nspi

import (
	"fmt"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestUpdateStatMovesCursor verifies UpdateStat advances the cursor by the
// requested delta, reports the entry at the new row, and clamps to the end of
// the table.
func TestUpdateStatMovesCursor(t *testing.T) {
	srv := NewServer()
	entries := make([]DirectoryEntry, 5)
	for i := range entries {
		entries[i] = DirectoryEntry{
			Email: fmt.Sprintf("u%d@x.test", i), DisplayName: fmt.Sprintf("User %d", i), ObjectClass: "User",
		}
	}
	srv.SetDirectory(fakeDir{entries: entries})

	update := func(curRec uint32, delta int32) (Stat, int32, bool) {
		body := wire.NewPush(0)
		body.Uint32(0)   // reserved
		body.Uint8(0xFF) // state block present
		Stat{CurrentRec: curRec, Delta: delta}.Push(body)
		body.Uint8(0xFF) // delta requested
		body.Uint32(0)   // cb_auxin
		q := nspiRequest(t, srv, "UpdateStat", body.Bytes())
		q.Uint32() // status
		if result := q.Uint32(); result != ecSuccess {
			t.Fatalf("result = %#x, want success", result)
		}
		q.Uint8() // state-block marker
		stat := PullStat(q)
		var movement int32
		hasDelta := q.Uint8() == 0xFF
		if hasDelta {
			movement = int32(q.Uint32())
		}
		return stat, movement, hasDelta
	}

	stat, movement, hasDelta := update(midBeginningOfTable, 3)
	if stat.NumPos != 3 {
		t.Errorf("num_pos = %d, want 3", stat.NumPos)
	}
	if stat.CurrentRec != entryMid(3) {
		t.Errorf("cur_rec = %#x, want entry 3", stat.CurrentRec)
	}
	if stat.TotalRec != 5 {
		t.Errorf("total_rec = %d, want 5", stat.TotalRec)
	}
	if !hasDelta || movement != 3 {
		t.Errorf("movement = %d (present=%v), want 3", movement, hasDelta)
	}

	stat, _, _ = update(midBeginningOfTable, 100)
	if stat.NumPos != 5 {
		t.Errorf("clamped num_pos = %d, want 5", stat.NumPos)
	}
	if stat.CurrentRec != midEnd {
		t.Errorf("clamped cur_rec = %#x, want END_OF_TABLE", stat.CurrentRec)
	}
}
