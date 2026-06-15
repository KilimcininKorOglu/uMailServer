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
	vals := readValueHeaders(r, cv)
	readValueContents(r, vals)
	return vals
}

// readValueHeaders decodes n NSPI property-value headers — proptag, the reserved
// field, the type discriminant (which must echo the proptag's type), then the
// inline scalar or the deferred-data referent. The arm is decoded from the
// proptag, never the discriminant slot, so the decoder cannot mask an encoder
// that writes the wrong discriminant.
func readValueHeaders(r *rd, n uint32) []decodedVal {
	r.t.Helper()
	vals := make([]decodedVal, n)
	for i := range vals {
		r.align4()
		d := decodedVal{tag: r.u32()}
		if resv := r.u32(); resv != 0 {
			r.t.Fatalf("value %d ulReserved = %#x, want 0", i, resv)
		}
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
	return vals
}

// readValueContents decodes the deferred content (strings, binaries) for the
// values whose headers carried a non-NULL referent; scalars carried their value
// inline and contribute nothing here.
func readValueContents(r *rd, vals []decodedVal) {
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
}

// readRowSet decodes an NSP_ROWSET: the conformant row count, every row's fixed
// header (reserved, value count, value-array referent), then every row's deferred
// content (the value headers followed by the value contents). It mirrors the two
// NDR passes pushRowSet writes.
func readRowSet(r *rd) [][]decodedVal {
	r.t.Helper()
	r.u32() // conformant max_count
	crows := r.u32()
	cvs := make([]uint32, crows)
	for i := range cvs {
		r.align4()
		if resv := r.u32(); resv != 0 {
			r.t.Fatalf("row %d reserved = %#x, want 0", i, resv)
		}
		cvs[i] = r.u32()
		r.u32() // value-array referent
	}
	rows := make([][]decodedVal, crows)
	for i := range rows {
		if cvs[i] == 0 {
			continue
		}
		if mc := r.u32(); mc != cvs[i] {
			r.t.Fatalf("row %d value-array max_count = %d, want %d", i, mc, cvs[i])
		}
		vals := readValueHeaders(r, cvs[i])
		readValueContents(r, vals)
		rows[i] = vals
	}
	return rows
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

// queryRowsRequest builds an NspiQueryRows cursor request: no explicit minimal-id
// table, reading up to count rows forward from numPos with an explicit column set.
func queryRowsRequest(handle []byte, numPos, count uint32, cols []wire.PropTag) []byte {
	p := ndr.NewPush()
	p.Raw(handle)
	p.Uint32(0)      // dwFlags
	p.Uint32(0)      // STAT.SortType
	p.Uint32(0)      // STAT.ContainerID
	p.Uint32(0)      // STAT.CurrentRec
	p.Uint32(0)      // STAT.Delta
	p.Uint32(numPos) // STAT.NumPos
	p.Uint32(0)      // STAT.TotalRec
	p.Uint32(0)      // STAT.CodePage
	p.Uint32(0)      // STAT.TemplateLocale
	p.Uint32(0)      // STAT.SortLocale
	p.Uint32(0)      // explicit minimal-id count
	p.Uint32(0)      // explicit minimal-id table [unique] referent: NULL
	p.Uint32(count)
	p.Uint32(0x20000) // pPropTags referent: present
	p.ULong(uint32(len(cols)) + 1)
	p.Uint32(uint32(len(cols)))
	p.ULong(0)
	p.ULong(uint32(len(cols)))
	for _, c := range cols {
		p.Uint32(uint32(c))
	}
	return p.Bytes()
}

// queryRowsExplicitRequest builds an NspiQueryRows request carrying an explicit
// minimal-id table, returning exactly those entries.
func queryRowsExplicitRequest(handle []byte, mids []uint32, cols []wire.PropTag) []byte {
	p := ndr.NewPush()
	p.Raw(handle)
	p.Uint32(0) // dwFlags
	for range 9 {
		p.Uint32(0) // zeroed STAT
	}
	p.Uint32(uint32(len(mids))) // explicit minimal-id count
	p.Uint32(0x40000)           // table [unique] referent: present
	p.ULong(uint32(len(mids)))  // conformant size == count
	for _, m := range mids {
		p.Uint32(m)
	}
	p.Uint32(uint32(len(mids))) // row count
	p.Uint32(0x20000)           // pPropTags referent
	p.ULong(uint32(len(cols)) + 1)
	p.Uint32(uint32(len(cols)))
	p.ULong(0)
	p.ULong(uint32(len(cols)))
	for _, c := range cols {
		p.Uint32(uint32(c))
	}
	return p.Bytes()
}

// readQueryRows decodes an NspiQueryRows response: the advanced STAT, the row-set
// referent, the row set, then the result code, asserting the whole body is
// consumed so a layout mismatch surfaces.
func readQueryRows(t *testing.T, resp []byte) (stat Stat, rows [][]decodedVal, result uint32) {
	t.Helper()
	r := &rd{b: resp, t: t}
	stat = Stat{
		SortType:       r.u32(),
		ContainerID:    r.u32(),
		CurrentRec:     r.u32(),
		Delta:          int32(r.u32()),
		NumPos:         r.u32(),
		TotalRec:       r.u32(),
		CodePage:       r.u32(),
		TemplateLocale: r.u32(),
		SortLocale:     r.u32(),
	}
	if ref := r.u32(); ref != 0 {
		rows = readRowSet(r)
	}
	result = r.u32()
	if r.off != len(resp) {
		t.Fatalf("decoded %d of %d bytes; trailing bytes mean a layout mismatch", r.off, len(resp))
	}
	return stat, rows, result
}

func TestNSPIQueryRowsEnumeratesGAL(t *testing.T) {
	s := NewRPCServer()
	s.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "ann@test.local", DisplayName: "Ann Example", ObjectClass: "User"},
		{Email: "bob@test.local", DisplayName: "Bob Example", ObjectClass: "User"},
		{Email: "cara@test.local", DisplayName: "Cara Example", ObjectClass: "User"},
	}})
	handle := bindRPC(t, s)
	cols := []wire.PropTag{wire.PidTagDisplayName, wire.PidTagSmtpAddress}

	// First page: two rows from the start; the cursor advances to the third entry.
	resp, ok := s.HandleRPC(opNspiQueryRows, "user@test.local", queryRowsRequest(handle, 0, 2, cols))
	if !ok {
		t.Fatal("NspiQueryRows reported not-ok")
	}
	stat, rows, result := readQueryRows(t, resp)
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	if len(rows) != 2 {
		t.Fatalf("page 1 returned %d rows, want 2", len(rows))
	}
	if rows[0][0].str != "Ann Example" || rows[1][0].str != "Bob Example" {
		t.Fatalf("page 1 display names = %q,%q; want Ann,Bob (sorted GAL order)", rows[0][0].str, rows[1][0].str)
	}
	if rows[0][1].str != "ann@test.local" {
		t.Fatalf("page 1 row 0 smtp = %q, want ann@test.local", rows[0][1].str)
	}
	if stat.TotalRec != 3 {
		t.Fatalf("TotalRec = %d, want 3", stat.TotalRec)
	}
	if stat.NumPos != 2 {
		t.Fatalf("NumPos = %d, want 2 (cursor advanced past two rows)", stat.NumPos)
	}
	if stat.CurrentRec != entryMid(2) {
		t.Fatalf("CurrentRec = %#x, want %#x (the third entry)", stat.CurrentRec, entryMid(2))
	}

	// Second page from the advanced cursor: the last row, cursor parks at MID_END.
	resp2, _ := s.HandleRPC(opNspiQueryRows, "user@test.local", queryRowsRequest(handle, stat.NumPos, 2, cols))
	stat2, rows2, result2 := readQueryRows(t, resp2)
	if result2 != ecSuccess || len(rows2) != 1 {
		t.Fatalf("page 2 result=%#x rows=%d, want ecSuccess and 1 row", result2, len(rows2))
	}
	if rows2[0][0].str != "Cara Example" {
		t.Fatalf("page 2 display name = %q, want Cara Example", rows2[0][0].str)
	}
	if stat2.CurrentRec != midEnd {
		t.Fatalf("CurrentRec = %#x, want MID_END %#x", stat2.CurrentRec, midEnd)
	}
}

func TestNSPIQueryRowsExplicitMinimalIDs(t *testing.T) {
	s := NewRPCServer()
	s.SetDirectory(fakeDir{entries: []DirectoryEntry{
		{Email: "ann@test.local", DisplayName: "Ann Example", ObjectClass: "User"},
		{Email: "bob@test.local", DisplayName: "Bob Example", ObjectClass: "User"},
		{Email: "cara@test.local", DisplayName: "Cara Example", ObjectClass: "User"},
	}})
	handle := bindRPC(t, s)

	resp, ok := s.HandleRPC(opNspiQueryRows, "user@test.local",
		queryRowsExplicitRequest(handle, []uint32{entryMid(1)}, []wire.PropTag{wire.PidTagDisplayName}))
	if !ok {
		t.Fatal("NspiQueryRows reported not-ok")
	}
	_, rows, result := readQueryRows(t, resp)
	if result != ecSuccess || len(rows) != 1 {
		t.Fatalf("result=%#x rows=%d, want ecSuccess and 1 row", result, len(rows))
	}
	if rows[0][0].str != "Bob Example" {
		t.Fatalf("explicit-MID row = %q, want Bob Example (entry at minimal id %#x)", rows[0][0].str, entryMid(1))
	}
}

func TestNSPIQueryRowsRejectsUnboundHandle(t *testing.T) {
	s := NewRPCServer()
	s.SetDirectory(fakeDir{entries: []DirectoryEntry{{Email: "a@x.test", DisplayName: "A", ObjectClass: "User"}}})
	bogus := make([]byte, 20) // never returned by a bind
	resp, ok := s.HandleRPC(opNspiQueryRows, "user@test.local",
		queryRowsRequest(bogus, 0, 10, []wire.PropTag{wire.PidTagDisplayName}))
	if !ok {
		t.Fatal("NspiQueryRows reported not-ok")
	}
	_, rows, result := readQueryRows(t, resp)
	if result != ecError {
		t.Fatalf("result = %#x, want ecError for an unbound handle", result)
	}
	if len(rows) != 0 {
		t.Fatalf("unbound handle returned %d rows, want 0", len(rows))
	}
}

func TestNSPIWriteOpsRefused(t *testing.T) {
	s := NewRPCServer()
	handle := bindRPC(t, s)
	for _, op := range []uint16{opNspiModProps, opNspiModLinkAtt} {
		resp, ok := s.HandleRPC(op, "user@test.local", handle)
		if !ok {
			t.Fatalf("opnum %d reported not-ok", op)
		}
		if res := (&rd{b: resp, t: t}).u32(); res != ecNotSupported {
			t.Fatalf("opnum %d result = %#x, want ecNotSupported (read-only address book)", op, res)
		}
	}
	bogus := make([]byte, 20) // never returned by a bind
	resp, _ := s.HandleRPC(opNspiModProps, "user@test.local", bogus)
	if res := (&rd{b: resp, t: t}).u32(); res != ecError {
		t.Fatalf("unbound write result = %#x, want ecError", res)
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
