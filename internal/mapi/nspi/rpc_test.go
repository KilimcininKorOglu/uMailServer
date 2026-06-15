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
		h := hdr{tag: r.u32(), typ: r.u32()}
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
