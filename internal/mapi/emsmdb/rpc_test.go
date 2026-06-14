package emsmdb

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/ndr"
	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// fakeRPCDispatcher records what the EcDoRpcExt2 bridge handed to the ROP layer
// and returns a canned response, so a test can assert the NDR-to-ROP-buffer
// plumbing without depending on real ROP semantics.
type fakeRPCDispatcher struct {
	gotRop      []byte
	gotHandles  []uint32
	gotMaxOut   int
	respRop     []byte
	respHandles []uint32
}

func (f *fakeRPCDispatcher) Dispatch(_ *Session, ropData []byte, handlesIn []uint32, maxOut int) ([]byte, []uint32) {
	f.gotRop = ropData
	f.gotHandles = handlesIn
	f.gotMaxOut = maxOut
	return f.respRop, f.respHandles
}

// TestEncodeServerVersionHighBit checks the MS-OXCRPC 3.1.4.1.1 "high bit"
// version encoding against hand-computed words — an oracle independent of the
// encoder under test.
func TestEncodeServerVersionHighBit(t *testing.T) {
	got := encodeServerVersion("15.00.0847.4040")
	want := [3]uint16{0x0F00, 0x834F, 0x0FC8} // (15<<8)|0, 847|0x8000, 4040
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if bad := encodeServerVersion("not-a-version"); bad != ([3]uint16{}) {
		t.Fatalf("malformed version = %#v, want zero", bad)
	}
}

func TestConnectExCreatesSessionAndHandle(t *testing.T) {
	s := NewRPCServer(&fakeRPCDispatcher{})
	in := buildConnectExIn("/o=org/ou=ag/cn=Recipients/cn=user", [3]uint16{15, 0, 1})
	out := decodeConnectExOut(s.connectEx("user@example.com", in))

	if out.result != ecSuccess {
		t.Fatalf("result = %#x, want success", out.result)
	}
	if out.handle.GUID == (wire.GUID{}) {
		t.Fatal("EcDoConnectEx returned a null context handle")
	}
	if out.displayName != "user@example.com" {
		t.Fatalf("display name = %q", out.displayName)
	}
	if out.dnPrefix != orgDnPrefix {
		t.Fatalf("dn prefix = %q, want %q", out.dnPrefix, orgDnPrefix)
	}
	if out.bestVers != ([3]uint16{15, 0, 1}) {
		t.Fatalf("best version = %#v, want client's {15,0,1}", out.bestVers)
	}
	if s.getSession(out.handle.GUID) == nil {
		t.Fatal("no session registered for the returned handle")
	}
}

func TestConnectExWithoutEmailDenied(t *testing.T) {
	s := NewRPCServer(&fakeRPCDispatcher{})
	out := decodeConnectExOut(s.connectEx("", buildConnectExIn("dn", [3]uint16{})))
	if out.result != ecAccessDenied {
		t.Fatalf("result = %#x, want access denied", out.result)
	}
	if out.handle.GUID != (wire.GUID{}) {
		t.Fatal("denied connect must return a null handle")
	}
}

// TestRpcExt2BridgesRopBuffer is the core L4 check: EcDoRpcExt2 must unwrap the
// ROP buffer from pin, dispatch it, and re-wrap the response into pout. It runs
// against the real ROP-buffer codec (not a fake) so it verifies actual framing.
func TestRpcExt2BridgesRopBuffer(t *testing.T) {
	disp := &fakeRPCDispatcher{respRop: []byte{0x01, 0x02, 0x03}, respHandles: []uint32{0xdeadbeef}}
	s := NewRPCServer(disp)
	h := mustConnect(t, s, "user@example.com")

	ropIn := []byte{0xAA, 0xBB}
	handlesIn := []uint32{0x11223344, 0xffffffff}
	pin := EncodeROPBuffer(0, ropIn, handlesIn, false)

	handleOut, pout, result := decodeRpcExt2Out(s.rpcExt2(buildRpcExt2In(h, pin, 0x10000)))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want success", result)
	}
	if handleOut.GUID != h.GUID {
		t.Fatal("EcDoRpcExt2 must echo the context handle")
	}
	if !bytes.Equal(disp.gotRop, ropIn) {
		t.Fatalf("dispatcher saw rop % x, want % x", disp.gotRop, ropIn)
	}
	if !reflect.DeepEqual(disp.gotHandles, handlesIn) {
		t.Fatalf("dispatcher saw handles %v, want %v", disp.gotHandles, handlesIn)
	}
	if disp.gotMaxOut != 0x10000 {
		t.Fatalf("dispatcher max out = %d, want 0x10000", disp.gotMaxOut)
	}
	_, respRop, respHandles, err := DecodeROPBuffer(pout)
	if err != nil {
		t.Fatalf("pout is not a valid ROP buffer: %v", err)
	}
	if !bytes.Equal(respRop, disp.respRop) {
		t.Fatalf("pout rop = % x, want % x", respRop, disp.respRop)
	}
	if !reflect.DeepEqual(respHandles, disp.respHandles) {
		t.Fatalf("pout handles = %v, want %v", respHandles, disp.respHandles)
	}
}

func TestRpcExt2UnknownHandleFails(t *testing.T) {
	s := NewRPCServer(&fakeRPCDispatcher{})
	var bogus ndr.ContextHandle
	bogus.GUID.TimeLow = 0x12345678
	in := buildRpcExt2In(bogus, EncodeROPBuffer(0, []byte{0x01}, nil, false), 0x1000)
	_, _, result := decodeRpcExt2Out(s.rpcExt2(in))
	if result != ecError {
		t.Fatalf("result = %#x, want ecError for an unknown handle", result)
	}
}

func TestDisconnectDropsSession(t *testing.T) {
	s := NewRPCServer(&fakeRPCDispatcher{})
	h := mustConnect(t, s, "user@example.com")
	if s.getSession(h.GUID) == nil {
		t.Fatal("session missing after connect")
	}
	s.disconnect(buildDisconnectIn(h))
	if s.getSession(h.GUID) != nil {
		t.Fatal("session not dropped after disconnect")
	}
}

func TestHandleRPCUnknownOpnumFaults(t *testing.T) {
	s := NewRPCServer(&fakeRPCDispatcher{})
	if _, ok := s.HandleRPC(99, "user@example.com", nil); ok {
		t.Fatal("an unimplemented opnum must report ok=false so the caller faults")
	}
}

// --- NDR test fixtures (mirror the marshaling in rpc.go) ---

func buildConnectExIn(userDN string, clientVers [3]uint16) []byte {
	p := ndr.NewPush()
	dn := append([]byte(userDN), 0)
	length := uint32(len(dn))
	p.ULong(length) // userdn max_count
	p.ULong(0)      // offset
	p.ULong(length) // actual_count
	p.Raw(dn)
	p.Uint32(0)    // flags
	p.Uint32(0)    // conmod
	p.Uint32(0)    // limit
	p.Uint32(1252) // cpid
	p.Uint32(0)    // lcid_string
	p.Uint32(0)    // lcid_sort
	p.Uint32(0)    // cxr_link
	p.Uint16(0)    // cnvt_cps
	p.Uint16(clientVers[0])
	p.Uint16(clientVers[1])
	p.Uint16(clientVers[2])
	p.Uint32(0) // timestamp
	p.ULong(0)  // pauxin max_count
	p.Uint32(0) // cb_auxin
	p.Uint32(0) // cb_auxout
	return p.Bytes()
}

func buildRpcExt2In(h ndr.ContextHandle, pin []byte, cbOut uint32) []byte {
	p := ndr.NewPush()
	p.CtxHandle(h)
	p.Uint32(0) // flags
	p.ULong(uint32(len(pin)))
	p.Raw(pin)
	p.Uint32(uint32(len(pin))) // cb_in
	p.Uint32(cbOut)
	p.ULong(0)  // pauxin max_count
	p.Uint32(0) // cb_auxin
	p.Uint32(0) // cb_auxout
	return p.Bytes()
}

func buildDisconnectIn(h ndr.ContextHandle) []byte {
	p := ndr.NewPush()
	p.CtxHandle(h)
	return p.Bytes()
}

type connectExOutFields struct {
	handle      ndr.ContextHandle
	dnPrefix    string
	displayName string
	bestVers    [3]uint16
	result      uint32
}

func decodeConnectExOut(b []byte) connectExOutFields {
	p := ndr.NewPull(b)
	var o connectExOutFields
	o.handle = p.CtxHandle()
	p.Uint32() // max_polls
	p.Uint32() // max_retry
	p.Uint32() // retry_delay
	p.Uint16() // cxr
	o.dnPrefix = readNDRString(p)
	o.displayName = readNDRString(p)
	p.Uint16() // server vers
	p.Uint16()
	p.Uint16()
	o.bestVers[0] = p.Uint16()
	o.bestVers[1] = p.Uint16()
	o.bestVers[2] = p.Uint16()
	p.Uint32()      // timestamp
	readNDRBytes(p) // aux out
	o.result = p.Uint32()
	return o
}

func decodeRpcExt2Out(b []byte) (handle ndr.ContextHandle, pout []byte, result uint32) {
	p := ndr.NewPull(b)
	handle = p.CtxHandle()
	p.Uint32() // flags
	pout = readNDRBytes(p)
	readNDRBytes(p) // aux out
	p.Uint32()      // trans_time
	result = p.Uint32()
	return
}

func readNDRString(p *ndr.Pull) string {
	p.ULong()           // referent id
	p.ULong()           // max_count
	p.ULong()           // offset
	length := p.ULong() // actual_count
	s := p.Str(int(length))
	return trimNUL(s)
}

func readNDRBytes(p *ndr.Pull) []byte {
	max := p.ULong()
	p.ULong() // offset
	p.ULong() // actual_count
	b := p.Bytes(int(max))
	p.Uint32() // trailing size
	return b
}

func trimNUL(s string) string {
	if i := len(s) - 1; i >= 0 && s[i] == 0 {
		return s[:i]
	}
	return s
}

func mustConnect(t *testing.T, s *RPCServer, email string) ndr.ContextHandle {
	t.Helper()
	out := decodeConnectExOut(s.connectEx(email, buildConnectExIn("dn", [3]uint16{15, 0, 1})))
	if out.result != ecSuccess {
		t.Fatalf("connect failed: %#x", out.result)
	}
	return out.handle
}
