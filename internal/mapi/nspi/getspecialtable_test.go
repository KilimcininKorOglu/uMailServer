package nspi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestGetSpecialTableReturnsGALContainer verifies GetSpecialTable advertises a
// single Global Address List container with its DT_CONTAINER permanent entry id
// and container metadata, decoded off the response.
func TestGetSpecialTableReturnsGALContainer(t *testing.T) {
	srv := NewServer()
	srv.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
	}})

	body := wire.NewPush(0)
	body.Uint32(0) // flags
	body.Uint8(0)  // no state block
	body.Uint8(0)  // no cached version
	body.Uint32(0) // cb_auxin

	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(body.Bytes()))
	req.Header.Set("X-RequestType", "GetSpecialTable")
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
		t.Fatal("expected the version block")
	}
	if v := q.Uint32(); v != specialTableVersion {
		t.Errorf("version = %d, want %d", v, specialTableVersion)
	}
	if q.Uint8() != 0xFF {
		t.Fatal("expected a container row")
	}
	if count := q.Uint32(); count != 1 {
		t.Fatalf("container count = %d, want 1", count)
	}

	n := q.Uint32()
	if n != 6 {
		t.Fatalf("container property count = %d, want 6", n)
	}
	props := make(map[wire.PropTag]any, n)
	for i := range n {
		tv, err := wire.PullTaggedPropertyValue(q)
		if err != nil {
			t.Fatalf("property %d decode: %v", i, err)
		}
		props[tv.Tag] = tv.Value
	}
	if q.Err() != nil {
		t.Fatalf("trailing parse error: %v", q.Err())
	}

	if dn, ok := props[wire.PidTagDisplayName].(string); !ok || dn != "Global Address List" {
		t.Errorf("display name = %v, want Global Address List", props[wire.PidTagDisplayName])
	}
	if cf, ok := props[wire.PidTagContainerFlags].(uint32); !ok || cf != abRecipients|abUnmodifiable {
		t.Errorf("container flags = %v, want %#x", props[wire.PidTagContainerFlags], abRecipients|abUnmodifiable)
	}
	if m, ok := props[wire.PidTagAddressBookIsMaster].(bool); !ok || m {
		t.Errorf("is-master = %v, want false", props[wire.PidTagAddressBookIsMaster])
	}

	eid, ok := props[wire.PidTagEntryID].([]byte)
	if !ok {
		t.Fatalf("entry id missing or wrong type: %T", props[wire.PidTagEntryID])
	}
	perm, err := wire.PullPermanentEntryID(wire.NewPull(eid, 0))
	if err != nil {
		t.Fatalf("entry id decode: %v", err)
	}
	if perm.DisplayType != dtContainer || perm.X500DN != "/" {
		t.Errorf("entry id = {dt=%#x dn=%q}, want {dt=%#x dn=\"/\"}", perm.DisplayType, perm.X500DN, dtContainer)
	}
}

// TestGetSpecialTableCreationTemplatesEmpty verifies the creation-templates flag
// yields an empty success rather than the address-book hierarchy.
func TestGetSpecialTableCreationTemplatesEmpty(t *testing.T) {
	srv := NewServer()

	body := wire.NewPush(0)
	body.Uint32(nspiAddressCreationTemplates) // flags
	body.Uint8(0)                             // no state block
	body.Uint8(0)                             // no cached version
	body.Uint32(0)                            // cb_auxin

	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(body.Bytes()))
	req.Header.Set("X-RequestType", "GetSpecialTable")
	req = req.WithContext(WithEmail(req.Context(), "qa.bob@local.test"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	q := wire.NewPull(payloadAfterMeta(t, rec.Body.Bytes()), wire.FlagABK|wire.FlagUTF16)
	q.Uint32() // status
	if result := q.Uint32(); result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	q.Uint32() // code page
	q.Uint8()  // version marker
	q.Uint32() // version
	if q.Uint8() != 0 {
		t.Error("expected no container rows for the creation-templates table")
	}
}
