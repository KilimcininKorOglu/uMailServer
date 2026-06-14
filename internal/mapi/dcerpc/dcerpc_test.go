package dcerpc

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// emsmdbUUID is the EMSMDB interface (MS-OXCRPC). nrd32UUID is the standard NDR
// transfer syntax. Both appear in the impacket-generated BIND vector below.
var (
	emsmdbUUID = wire.GUID{TimeLow: 0xa4f1db00, TimeMid: 0xca47, TimeHiAndVersion: 0x1067, ClockSeq: [2]byte{0xb3, 0x1f}, Node: [6]byte{0x00, 0xdd, 0x01, 0x06, 0x62, 0xda}}
	ndr32UUID  = wire.GUID{TimeLow: 0x8a885d04, TimeMid: 0x1ceb, TimeHiAndVersion: 0x11c9, ClockSeq: [2]byte{0x9f, 0xe8}, Node: [6]byte{0x08, 0x00, 0x2b, 0x10, 0x48, 0x60}}
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex vector: %v", err)
	}
	return b
}

// TestPullBindFromImpacket decodes a real BIND PDU produced by impacket's
// rpcrt.MSRPCBind for the EMSMDB interface. impacket is an independent
// implementation, so matching its bytes is genuine interop evidence rather than
// a self-consistency check.
func TestPullBindFromImpacket(t *testing.T) {
	raw := mustHex(t, "05000b03100000004800000001000000b810b81000000000010000000000010000dbf1a447ca6710b31f00dd010662da00005100045d888aeb1cc9119fe808002b10486002000000")
	pkt, err := Pull(raw)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if pkt.Type != PktBind {
		t.Fatalf("type = %d, want BIND(%d)", pkt.Type, PktBind)
	}
	if pkt.PFCFlags != PFCFirstFrag|PFCLastFrag {
		t.Fatalf("pfc_flags = %#x", pkt.PFCFlags)
	}
	if pkt.FragLength != 72 || pkt.AuthLength != 0 || pkt.CallID != 1 {
		t.Fatalf("header: frag=%d auth=%d call=%d", pkt.FragLength, pkt.AuthLength, pkt.CallID)
	}
	b := pkt.Bind
	if b == nil {
		t.Fatal("Bind body nil")
	}
	if b.MaxXmitFrag != 4280 || b.MaxRecvFrag != 4280 || b.AssocGroupID != 0 {
		t.Fatalf("bind: xmit=%d recv=%d assoc=%d", b.MaxXmitFrag, b.MaxRecvFrag, b.AssocGroupID)
	}
	if len(b.Contexts) != 1 {
		t.Fatalf("contexts = %d, want 1", len(b.Contexts))
	}
	c := b.Contexts[0]
	if c.ContextID != 0 {
		t.Fatalf("context_id = %d", c.ContextID)
	}
	if c.AbstractSyntax.UUID != emsmdbUUID || c.AbstractSyntax.Version != 0x00510000 {
		t.Fatalf("abstract syntax = %+v ver %#x", c.AbstractSyntax.UUID, c.AbstractSyntax.Version)
	}
	if len(c.TransferSyntaxes) != 1 {
		t.Fatalf("transfer syntaxes = %d, want 1", len(c.TransferSyntaxes))
	}
	if c.TransferSyntaxes[0].UUID != ndr32UUID || c.TransferSyntaxes[0].Version != 2 {
		t.Fatalf("transfer syntax = %+v ver %d", c.TransferSyntaxes[0].UUID, c.TransferSyntaxes[0].Version)
	}
}

// TestPullRequestFromImpacket decodes a REQUEST PDU produced by impacket for
// opnum 11 (EcDoRpcExt2) with a 4-byte stub, confirming the 8-aligned stub
// boundary and that the stub is carried verbatim.
func TestPullRequestFromImpacket(t *testing.T) {
	raw := mustHex(t, "05000003100000001c000000020000000000000000000b00aabbccdd")
	pkt, err := Pull(raw)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if pkt.Type != PktRequest {
		t.Fatalf("type = %d, want REQUEST", pkt.Type)
	}
	r := pkt.Request
	if r == nil {
		t.Fatal("Request body nil")
	}
	if r.AllocHint != 0 || r.ContextID != 0 || r.Opnum != 11 {
		t.Fatalf("request: alloc=%d ctx=%d opnum=%d", r.AllocHint, r.ContextID, r.Opnum)
	}
	if r.Object != nil {
		t.Fatalf("object present unexpectedly: %v", r.Object)
	}
	if !bytes.Equal(r.Stub, []byte{0xaa, 0xbb, 0xcc, 0xdd}) {
		t.Fatalf("stub = % x", r.Stub)
	}
}

// TestEncodeResponseMatchesImpacket asserts a RESPONSE we emit is byte-identical
// to one impacket's rpcrt.MSRPCRespHeader emits for the same call: alloc_hint,
// context id, cancel count, the reserved byte aligning the stub to 8, then the
// stub.
func TestEncodeResponseMatchesImpacket(t *testing.T) {
	want := mustHex(t, "05000203100000001c000000020000000400000000000000aabbccdd")
	got := EncodeResponse(2, 0, []byte{0xaa, 0xbb, 0xcc, 0xdd})
	if !bytes.Equal(got, want) {
		t.Fatalf("got  % x\nwant % x", got, want)
	}
}

// TestEncodeBindAckInteroperable asserts our BIND_ACK matches the byte layout
// impacket's rpcrt.MSRPCBindAck parser accepts (verified out-of-band): the
// secondary address length-prefixed string, 4-aligned result list, and the
// accepted transfer syntax.
func TestEncodeBindAckInteroperable(t *testing.T) {
	want := mustHex(t, "05000c0310000000"+"3c000000"+"01000000"+"b810b810"+"78563412"+"0500363030310000"+"01000000"+"00000000"+"045d888aeb1cc9119fe808002b104860"+"02000000")
	got := EncodeBindAck(1, 4280, 4280, 0x12345678, "6001", []AckResult{{Result: 0, Reason: 0, Syntax: SyntaxID{UUID: ndr32UUID, Version: 2}}})
	if !bytes.Equal(got, want) {
		t.Fatalf("got  % x\nwant % x", got, want)
	}
}

// TestEncodeFaultLayout pins the FAULT body: alloc_hint, context id, cancel
// count, a reserved byte (from 4-aligning status), then the 32-bit status.
func TestEncodeFaultLayout(t *testing.T) {
	got := EncodeFault(7, 0, 0x000004B6) // ecRpcFailed-style status
	want := mustHex(t, "05000303100000001c00000007000000"+"00000000"+"0000"+"00"+"00"+"b6040000")
	if !bytes.Equal(got, want) {
		t.Fatalf("got  % x\nwant % x", got, want)
	}
}

func TestPullRejectsBigEndian(t *testing.T) {
	raw := mustHex(t, "05000b00000000004800000001000000")
	if _, err := Pull(raw); err == nil {
		t.Fatal("expected error for big-endian DREP")
	}
}

func TestPullRejectsOversizeFragLength(t *testing.T) {
	// frag_length claims 72 bytes but only 16 are present.
	raw := mustHex(t, "05000b03100000004800000001000000")
	if _, err := Pull(raw); err == nil {
		t.Fatal("expected error for frag_length past buffer")
	}
}

// TestPullRequestWithObjectUUID exercises the PFC_OBJECT_UUID branch: a 16-byte
// object GUID precedes the stub, which then 8-aligns.
func TestPullRequestWithObjectUUID(t *testing.T) {
	p := encodeRequestWithObject(t)
	pkt, err := Pull(p)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if pkt.Request.Object == nil || *pkt.Request.Object != ndr32UUID {
		t.Fatalf("object = %v, want %v", pkt.Request.Object, ndr32UUID)
	}
	if !bytes.Equal(pkt.Request.Stub, []byte{0x11, 0x22, 0x33, 0x44}) {
		t.Fatalf("stub = % x", pkt.Request.Stub)
	}
}

// encodeRequestWithObject builds a REQUEST PDU carrying an object UUID, matching
// the request push layout: header, alloc_hint, ctx_id, opnum, object GUID, then
// 8-aligned stub.
func encodeRequestWithObject(t *testing.T) []byte {
	t.Helper()
	// header: vers5 minor0 type0 pfc(First|Last|ObjectUUID) drep call_id
	hdr := []byte{5, 0, PktRequest, PFCFirstFrag | PFCLastFrag | PFCObjectUUID, drepLE, 0, 0, 0, 0, 0, 0, 0, 9, 0, 0, 0}
	body := []byte{4, 0, 0, 0, 0, 0, 0, 0} // alloc_hint=4, ctx_id=0, opnum=0
	guid := []byte{0x04, 0x5d, 0x88, 0x8a, 0xeb, 0x1c, 0xc9, 0x11, 0x9f, 0xe8, 0x08, 0x00, 0x2b, 0x10, 0x48, 0x60}
	// offset after header(16)+body(8)+guid(16) = 40, already 8-aligned, no pad.
	stub := []byte{0x11, 0x22, 0x33, 0x44}
	pdu := append(append(append(hdr, body...), guid...), stub...)
	pdu[8] = byte(len(pdu)) // frag_length low byte
	return pdu
}
