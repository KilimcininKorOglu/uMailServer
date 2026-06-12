package nspi

import (
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestGetPropListReturnsAvailableTags verifies GetPropList reports the entry's
// available property tags for a known minimal id and errors for an unknown one.
func TestGetPropListReturnsAvailableTags(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
	}})

	body := wire.NewPush(0)
	body.Uint32(0) // flags
	body.Uint32(entryMid(0))
	body.Uint32(0) // code page
	body.Uint32(0) // cb_auxin

	q := nspiRequest(t, srv, "GetPropList", body.Bytes())
	q.Uint32() // status
	if result := q.Uint32(); result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	if q.Uint8() != 0xFF {
		t.Fatal("expected the tag array")
	}
	tags := pullProptags(q)
	if len(tags) != len(availableEntryTags) {
		t.Fatalf("tag count = %d, want %d", len(tags), len(availableEntryTags))
	}
	if tags[0] != wire.PidTagEntryID {
		t.Errorf("first tag = %#x, want PidTagEntryID", tags[0])
	}

	bad := wire.NewPush(0)
	bad.Uint32(0)
	bad.Uint32(0x9999) // no such entry
	bad.Uint32(0)
	bad.Uint32(0)
	q = nspiRequest(t, srv, "GetPropList", bad.Bytes())
	q.Uint32() // status
	if result := q.Uint32(); result != ecError {
		t.Errorf("unknown-mid result = %#x, want ecError", result)
	}
}
