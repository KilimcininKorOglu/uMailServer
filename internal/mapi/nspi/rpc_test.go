package nspi

import (
	"encoding/binary"
	"unicode/utf16"

	"testing"

	"github.com/umailserver/umailserver/internal/mapi/ndr"
	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// rd decodes an NSPI RPC response body with raw little-endian reads, kept
// deliberately independent of internal/mapi/ndr so a marshaling regression in
// the encoder under test cannot be masked by a matching bug in the decoder.
type rd struct {
	b   []byte
	off int
	t   *testing.T
}

func (r *rd) align4() {
	if rem := r.off % 4; rem != 0 {
		r.off += 4 - rem
	}
}

func (r *rd) u8() uint8 {
	r.t.Helper()
	if r.off+1 > len(r.b) {
		r.t.Fatalf("truncated reading u8 at %d (len %d)", r.off, len(r.b))
	}
	v := r.b[r.off]
	r.off++
	return v
}

// u32 reads a 4-aligned little-endian uint32, mirroring ndr.Push.Uint32, which
// self-aligns every 32-bit write. Only u8 and raw byte runs stay unaligned.
func (r *rd) u32() uint32 {
	r.t.Helper()
	r.align4()
	if r.off+4 > len(r.b) {
		r.t.Fatalf("truncated reading u32 at %d (len %d)", r.off, len(r.b))
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *rd) raw(n int) []byte {
	r.t.Helper()
	if r.off+n > len(r.b) {
		r.t.Fatalf("truncated reading %d bytes at %d (len %d)", n, r.off, len(r.b))
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}

// statBytes builds a zeroed NDR STAT block (nine 4-byte fields).
func statBytes(p *ndr.Push) {
	for range 9 {
		p.Uint32(0)
	}
}

// bindRequest builds an NspiBind request: dwFlags, a zeroed STAT, and a NULL
// server-GUID hint.
func bindRequest() []byte {
	p := ndr.NewPush()
	p.Uint32(0) // dwFlags
	statBytes(p)
	p.Uint32(0) // pServerGuid referent: NULL
	return p.Bytes()
}

// specialTableRequest builds an NspiGetSpecialTable request carrying the bound
// 20-byte context handle.
func specialTableRequest(handle []byte, flags uint32) []byte {
	p := ndr.NewPush()
	p.Raw(handle) // context handle (handle_type + GUID)
	p.Uint32(flags)
	statBytes(p)
	p.Uint32(0) // cached hierarchy version
	return p.Bytes()
}

// unbindRequest builds an NspiUnbind request carrying the context handle.
func unbindRequest(handle []byte) []byte {
	p := ndr.NewPush()
	p.Raw(handle)
	p.Uint32(0) // reserved
	return p.Bytes()
}

// bindRPC runs NspiBind and returns the 20-byte context handle the response
// carries, failing the test on any structural problem.
func bindRPC(t *testing.T, s *RPCServer) []byte {
	t.Helper()
	resp, ok := s.HandleRPC(opNspiBind, "user@test.local", bindRequest())
	if !ok {
		t.Fatal("NspiBind reported not-ok")
	}
	r := &rd{b: resp, t: t}
	if ref := r.u32(); ref == 0 {
		t.Fatal("NspiBind server-GUID referent is NULL")
	}
	r.raw(16) // server FlatUID
	handle := r.raw(20)
	if res := r.u32(); res != ecSuccess {
		t.Fatalf("NspiBind result = %#x, want ecSuccess", res)
	}
	if r.off != len(resp) {
		t.Fatalf("NspiBind decoded %d of %d bytes", r.off, len(resp))
	}
	return handle
}

func TestNSPIHandleRPCUnknownOpnumFaults(t *testing.T) {
	s := NewRPCServer()
	if _, ok := s.HandleRPC(99, "user@test.local", nil); ok {
		t.Fatal("opnum 99 reported ok; the tunnel must fault an unimplemented opnum")
	}
}

func TestNSPIBindRequiresEmail(t *testing.T) {
	s := NewRPCServer()
	resp, ok := s.HandleRPC(opNspiBind, "", bindRequest())
	if !ok {
		t.Fatal("NspiBind reported not-ok")
	}
	r := &rd{b: resp, t: t}
	if ref := r.u32(); ref != 0 {
		t.Fatalf("unauthenticated NspiBind server-GUID referent = %#x, want 0", ref)
	}
	r.raw(20) // zeroed handle
	if res := r.u32(); res != ecError {
		t.Fatalf("unauthenticated NspiBind result = %#x, want ecError", res)
	}
}

func TestNSPIGetSpecialTableReturnsGALContainer(t *testing.T) {
	s := NewRPCServer()
	handle := bindRPC(t, s)

	resp, ok := s.HandleRPC(opNspiGetSpecialTable, "user@test.local", specialTableRequest(handle, 0))
	if !ok {
		t.Fatal("NspiGetSpecialTable reported not-ok")
	}
	r := &rd{b: resp, t: t}
	if v := r.u32(); v != specialTableVersion {
		t.Fatalf("version = %d, want %d", v, specialTableVersion)
	}
	if ref := r.u32(); ref == 0 {
		t.Fatal("ppRows referent is NULL; want the GAL container rowset")
	}
	// Row set: conformant max_count, then cRows.
	r.u32() // max_count
	if cr := r.u32(); cr != 1 {
		t.Fatalf("cRows = %d, want 1 (the single GAL container)", cr)
	}
	// Single row header.
	r.align4()
	if res := r.u32(); res != 0 {
		t.Fatalf("row reserved = %d, want 0", res)
	}
	want := galContainerRow()
	if cv := r.u32(); cv != uint32(len(want)) {
		t.Fatalf("row cValues = %d, want %d", cv, len(want))
	}
	if ref := r.u32(); ref == 0 {
		t.Fatal("row value-array referent is NULL")
	}
	// Row content: conformant value count, then every value header, then every
	// value content.
	if mc := r.u32(); mc != uint32(len(want)) {
		t.Fatalf("value-array max_count = %d, want %d", mc, len(want))
	}
	type hdr struct {
		tag  uint32
		typ  uint32
		long uint32
		bcb  uint32
		bref uint32
		sref uint32
		bl   bool
	}
	hdrs := make([]hdr, len(want))
	for i := range want {
		r.align4()
		h := hdr{tag: r.u32()}
		if resv := r.u32(); resv != 0 {
			t.Fatalf("value %d ulReserved = %#x, want 0", i, resv)
		}
		h.typ = r.u32()
		switch wire.PropType(h.typ) {
		case wire.PtLong, wire.PtError:
			h.long = r.u32()
		case wire.PtBoolean:
			h.bl = r.u8() != 0
		case wire.PtUnicode, wire.PtString8:
			h.sref = r.u32()
		case wire.PtBinary:
			h.bcb = r.u32()
			h.bref = r.u32()
		default:
			t.Fatalf("value %d unexpected type %#x", i, h.typ)
		}
		hdrs[i] = h
	}
	// Verify headers carry exactly galContainerRow's tags, in order.
	for i, w := range want {
		if hdrs[i].tag != uint32(w.Tag) {
			t.Fatalf("value %d tag = %#x, want %#x", i, hdrs[i].tag, uint32(w.Tag))
		}
	}
	// Decode the deferred contents and check the values that carry payload.
	for i, w := range want {
		switch wire.PropType(hdrs[i].typ) {
		case wire.PtBinary:
			cb := r.u32()
			got := r.raw(int(cb))
			wantB, ok := w.Value.([]byte)
			if !ok || string(got) != string(wantB) {
				t.Fatalf("value %d binary mismatch (%d vs %d bytes)", i, len(got), len(wantB))
			}
		case wire.PtUnicode:
			count := r.u32()
			r.u32() // offset
			r.u32() // actual
			u := r.raw(int(count) * 2)
			wantS, ok := w.Value.(string)
			if got := decodeUTF16LE(u); !ok || got != wantS {
				t.Fatalf("value %d string = %q, want %q", i, got, w.Value)
			}
		case wire.PtLong:
			wantL, ok := w.Value.(uint32)
			if !ok || hdrs[i].long != wantL {
				t.Fatalf("value %d long = %d, want %v", i, hdrs[i].long, w.Value)
			}
		case wire.PtBoolean:
			wantBool, ok := w.Value.(bool)
			if !ok || hdrs[i].bl != wantBool {
				t.Fatalf("value %d bool = %v, want %v", i, hdrs[i].bl, w.Value)
			}
		}
	}
	if res := r.u32(); res != ecSuccess {
		t.Fatalf("GetSpecialTable result = %#x, want ecSuccess", res)
	}
}

func TestNSPIGetSpecialTableRejectsUnboundHandle(t *testing.T) {
	s := NewRPCServer()
	bogus := make([]byte, 20) // never returned by a bind
	resp, ok := s.HandleRPC(opNspiGetSpecialTable, "user@test.local", specialTableRequest(bogus, 0))
	if !ok {
		t.Fatal("NspiGetSpecialTable reported not-ok")
	}
	r := &rd{b: resp, t: t}
	r.u32() // version
	if ref := r.u32(); ref != 0 {
		t.Fatalf("ppRows referent = %#x, want 0 (NULL) for an unbound handle", ref)
	}
	if res := r.u32(); res != ecError {
		t.Fatalf("result = %#x, want ecError for an unbound handle", res)
	}
}

func TestNSPIUnbindDropsSession(t *testing.T) {
	s := NewRPCServer()
	handle := bindRPC(t, s)
	resp, ok := s.HandleRPC(opNspiUnbind, "user@test.local", unbindRequest(handle))
	if !ok {
		t.Fatal("NspiUnbind reported not-ok")
	}
	r := &rd{b: resp, t: t}
	r.raw(20) // invalidated handle
	if res := r.u32(); res != ecUnbindSuccess {
		t.Fatalf("NspiUnbind result = %#x, want ecUnbindSuccess", res)
	}
	// The handle must no longer resolve to a session.
	resp2, _ := s.HandleRPC(opNspiGetSpecialTable, "user@test.local", specialTableRequest(handle, 0))
	r2 := &rd{b: resp2, t: t}
	r2.u32() // version
	if ref := r2.u32(); ref != 0 {
		t.Fatal("GetSpecialTable succeeded after Unbind; the session was not dropped")
	}
}

// decodedVal is one property value decoded from an NSPI property row.
type decodedVal struct {
	tag, typ   uint32
	long       uint32
	str        string
	bin        []byte
	sref, bref uint32
}

// readPropRow decodes one NSPI property row — the two NDR passes, every value's
// header then every value's deferred content — and returns the decoded values. r
// must sit at the row header, i.e. just past the row's [unique] referent.
func readPropRow(r *rd) []decodedVal {
	r.t.Helper()
	r.align4()
	if res := r.u32(); res != 0 {
		r.t.Fatalf("row reserved = %d, want 0", res)
	}
	cv := r.u32()
	if aref := r.u32(); cv > 0 && aref == 0 {
		r.t.Fatal("row value-array referent is NULL with values present")
	}
	if cv == 0 {
		return nil
	}
	if mc := r.u32(); mc != cv {
		r.t.Fatalf("value-array max_count = %d, want %d", mc, cv)
	}
	vals := make([]decodedVal, cv)
	for i := range vals {
		r.align4()
		d := decodedVal{tag: r.u32()}
		if resv := r.u32(); resv != 0 {
			r.t.Fatalf("value %d ulReserved = %#x, want 0", i, resv)
		}
		// The value union's type discriminant must echo the proptag's type; decode
		// the arm from the proptag so the decoder never trusts the discriminant
		// slot to be self-consistent with the encoder.
		d.typ = r.u32()
		if pt := d.tag & 0xFFFF; d.typ != pt {
			r.t.Fatalf("value %d type discriminant = %#x, want %#x (the proptag's type)", i, d.typ, pt)
		}
		switch wire.PropType(uint16(d.tag)) {
		case wire.PtLong, wire.PtError:
			d.long = r.u32()
		case wire.PtBoolean:
			r.u8() // boolean value, inline in the header
		case wire.PtUnicode:
			d.sref = r.u32()
		case wire.PtBinary:
			r.u32() // cb, restated in the content pass
			d.bref = r.u32()
		default:
			r.t.Fatalf("value %d unexpected type %#x", i, d.typ)
		}
		vals[i] = d
	}
	for i := range vals {
		switch wire.PropType(uint16(vals[i].tag)) {
		case wire.PtUnicode:
			if vals[i].sref == 0 {
				continue
			}
			count := r.u32()
			r.u32() // offset
			r.u32() // actual
			vals[i].str = decodeUTF16LE(r.raw(int(count) * 2))
		case wire.PtBinary:
			if vals[i].bref == 0 {
				continue
			}
			cb := r.u32()
			vals[i].bin = r.raw(int(cb))
		}
	}
	return vals
}

// getPropsRequest builds an NspiGetProps request positioned at GAL entry mid rec,
// requesting an explicit property-tag array (max_count carries the cValues+1
// sentinel slot the transfer syntax requires).
func getPropsRequest(handle []byte, rec uint32, tags []wire.PropTag) []byte {
	p := ndr.NewPush()
	p.Raw(handle)
	p.Uint32(0)   // dwFlags
	p.Uint32(0)   // STAT.SortType
	p.Uint32(0)   // STAT.ContainerID
	p.Uint32(rec) // STAT.CurrentRec
	for range 6 {
		p.Uint32(0) // Delta, NumPos, TotalRec, CodePage, TemplateLocale, SortLocale
	}
	p.Uint32(0x20000)              // pPropTags [unique] referent: present
	p.ULong(uint32(len(tags)) + 1) // max_count = cValues+1
	p.Uint32(uint32(len(tags)))    // cValues
	p.ULong(0)                     // offset
	p.ULong(uint32(len(tags)))     // length
	for _, t := range tags {
		p.Uint32(uint32(t))
	}
	return p.Bytes()
}

func TestNSPIGetPropsReturnsEntryProperties(t *testing.T) {
	s := NewRPCServer()
	s.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "ann@test.local", DisplayName: "Ann Example", ObjectClass: "User"},
		{Email: "bob@test.local", DisplayName: "Bob Example", ObjectClass: "User"},
	}})
	handle := bindRPC(t, s)

	tags := []wire.PropTag{wire.PidTagDisplayName, wire.PidTagSmtpAddress, wire.PidTagObjectType, wire.PidTagEntryID}
	resp, ok := s.HandleRPC(opNspiGetProps, "user@test.local", getPropsRequest(handle, entryMid(0), tags))
	if !ok {
		t.Fatal("NspiGetProps reported not-ok")
	}
	r := &rd{b: resp, t: t}
	if ref := r.u32(); ref == 0 {
		t.Fatal("property-row referent is NULL; want the entry row")
	}
	vals := readPropRow(r)
	if len(vals) != len(tags) {
		t.Fatalf("decoded %d values, want %d", len(vals), len(tags))
	}
	for i, tg := range tags {
		if vals[i].tag != uint32(tg) {
			t.Fatalf("value %d tag = %#x, want %#x", i, vals[i].tag, uint32(tg))
		}
	}
	if vals[0].str != "Ann Example" {
		t.Fatalf("DisplayName = %q, want %q (GAL sorts Ann before Bob)", vals[0].str, "Ann Example")
	}
	if vals[1].str != "ann@test.local" {
		t.Fatalf("SmtpAddress = %q, want %q", vals[1].str, "ann@test.local")
	}
	if vals[2].long != objMailUser {
		t.Fatalf("ObjectType = %d, want %d (MAPI_MAILUSER)", vals[2].long, objMailUser)
	}
	if len(vals[3].bin) == 0 {
		t.Fatal("EntryID is empty; want the PermanentEntryID bytes")
	}
	if res := r.u32(); res != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", res)
	}
	if r.off != len(resp) {
		t.Fatalf("decoded %d of %d bytes; trailing bytes mean a layout mismatch", r.off, len(resp))
	}
}

func TestNSPIGetPropsMarksAbsentProperty(t *testing.T) {
	s := NewRPCServer()
	s.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "ann@test.local", DisplayName: "Ann Example", ObjectClass: "User"},
	}})
	handle := bindRPC(t, s)

	absent := wire.MakeTag(0x3004, wire.PtUnicode) // PidTagComment, not served for a GAL entry
	tags := []wire.PropTag{wire.PidTagDisplayName, absent}
	resp, ok := s.HandleRPC(opNspiGetProps, "user@test.local", getPropsRequest(handle, entryMid(0), tags))
	if !ok {
		t.Fatal("NspiGetProps reported not-ok")
	}
	r := &rd{b: resp, t: t}
	if ref := r.u32(); ref == 0 {
		t.Fatal("property-row referent is NULL; the warning row must still be returned")
	}
	vals := readPropRow(r)
	if len(vals) != 2 {
		t.Fatalf("decoded %d values, want 2", len(vals))
	}
	if vals[0].str != "Ann Example" {
		t.Fatalf("DisplayName = %q, want %q", vals[0].str, "Ann Example")
	}
	if wire.PropType(vals[1].typ) != wire.PtError {
		t.Fatalf("absent property type = %#x, want PtError", vals[1].typ)
	}
	if vals[1].long != ecNotFound {
		t.Fatalf("absent property error = %#x, want ecNotFound", vals[1].long)
	}
	if vals[1].tag != uint32(wire.MakeTag(absent.ID(), wire.PtError)) {
		t.Fatalf("absent property tag = %#x, want it re-tagged to PtError", vals[1].tag)
	}
	if res := r.u32(); res != ecWarnWithErrors {
		t.Fatalf("result = %#x, want ecWarnWithErrors (some properties absent)", res)
	}
}

func TestNSPIGetPropsRejectsUnboundHandle(t *testing.T) {
	s := NewRPCServer()
	s.SetDirectory(fakeDir{entries: []DirectoryEntry{{Email: "a@x.test", DisplayName: "A", ObjectClass: "User"}}})
	bogus := make([]byte, 20) // never returned by a bind
	resp, ok := s.HandleRPC(opNspiGetProps, "user@test.local", getPropsRequest(bogus, entryMid(0), []wire.PropTag{wire.PidTagDisplayName}))
	if !ok {
		t.Fatal("NspiGetProps reported not-ok")
	}
	r := &rd{b: resp, t: t}
	if ref := r.u32(); ref != 0 {
		t.Fatalf("property-row referent = %#x, want 0 (NULL) for an unbound handle", ref)
	}
	if res := r.u32(); res != ecError {
		t.Fatalf("result = %#x, want ecError for an unbound handle", res)
	}
}

func TestNSPIGetPropsInvalidPosition(t *testing.T) {
	s := NewRPCServer()
	s.SetDirectory(fakeDir{entries: []DirectoryEntry{{Email: "a@x.test", DisplayName: "A", ObjectClass: "User"}}})
	handle := bindRPC(t, s)

	tags := []wire.PropTag{wire.PidTagDisplayName, wire.PidTagSmtpAddress}
	resp, ok := s.HandleRPC(opNspiGetProps, "user@test.local", getPropsRequest(handle, entryMid(99), tags))
	if !ok {
		t.Fatal("NspiGetProps reported not-ok")
	}
	r := &rd{b: resp, t: t}
	if ref := r.u32(); ref == 0 {
		t.Fatal("property-row referent is NULL; an out-of-range position must still return the all-error row")
	}
	vals := readPropRow(r)
	if len(vals) != len(tags) {
		t.Fatalf("decoded %d values, want %d", len(vals), len(tags))
	}
	for i := range vals {
		if wire.PropType(vals[i].typ) != wire.PtError {
			t.Fatalf("value %d type = %#x, want PtError for an out-of-range position", i, vals[i].typ)
		}
	}
	if res := r.u32(); res != ecWarnWithErrors {
		t.Fatalf("result = %#x, want ecWarnWithErrors", res)
	}
}

// decodeUTF16LE decodes little-endian UTF-16 and trims the trailing NUL.
func decodeUTF16LE(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(b[i:]))
	}
	for len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	return string(utf16.Decode(units))
}
