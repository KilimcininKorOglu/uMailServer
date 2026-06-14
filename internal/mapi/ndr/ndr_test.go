package ndr

import (
	"bytes"
	"errors"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// sampleGUID is a GUID whose little-endian NDR encoding is trivially verifiable
// by eye, so the byte-order expectations below act as a real oracle.
var sampleGUID = wire.GUID{
	TimeLow:          0x01020304,
	TimeMid:          0x0506,
	TimeHiAndVersion: 0x0708,
	ClockSeq:         [2]byte{0x09, 0x0a},
	Node:             [6]byte{0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
}

var sampleGUIDWire = []byte{
	0x04, 0x03, 0x02, 0x01, // TimeLow LE
	0x06, 0x05, // TimeMid LE
	0x08, 0x07, // TimeHiAndVersion LE
	0x09, 0x0a, // ClockSeq verbatim
	0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, // Node verbatim
}

// TestAlignmentPadsToTypeSize asserts the defining NDR property: a scalar is
// preceded by zero padding so its offset is a multiple of its size. Without
// this, every multi-field EMSMDB structure would desync against a real client.
func TestAlignmentPadsToTypeSize(t *testing.T) {
	cases := []struct {
		name string
		emit func(*Push)
		want []byte
	}{
		{"u16 after u8", func(p *Push) { p.Uint8(0xAA); p.Uint16(0xBBCC) }, []byte{0xAA, 0x00, 0xCC, 0xBB}},
		{"u32 after u8", func(p *Push) { p.Uint8(0xAA); p.Uint32(0x11223344) }, []byte{0xAA, 0x00, 0x00, 0x00, 0x44, 0x33, 0x22, 0x11}},
		{"u64 after u8", func(p *Push) { p.Uint8(0xAA); p.Uint64(0x1122334455667788) }, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewPush()
			c.emit(p)
			if !bytes.Equal(p.Bytes(), c.want) {
				t.Fatalf("got % x, want % x", p.Bytes(), c.want)
			}
		})
	}
}

// TestPullSkipsAlignmentPadding asserts the decoder mirrors the encoder: it
// steps over inserted padding so an offset lands on the next aligned scalar.
func TestPullSkipsAlignmentPadding(t *testing.T) {
	p := NewPull([]byte{0xAA, 0x00, 0x00, 0x00, 0x44, 0x33, 0x22, 0x11})
	if v := p.Uint8(); v != 0xAA {
		t.Fatalf("u8 = %#x", v)
	}
	if v := p.Uint32(); v != 0x11223344 {
		t.Fatalf("u32 = %#x", v)
	}
	if err := p.Err(); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestScalarRoundTrip(t *testing.T) {
	p := NewPush()
	p.Uint8(0x01)
	p.Uint16(0x0203)
	p.Uint32(0x04050607)
	p.Uint64(0x08090a0b0c0d0e0f)
	p.ULong(0x10111213)

	q := NewPull(p.Bytes())
	if v := q.Uint8(); v != 0x01 {
		t.Fatalf("u8 = %#x", v)
	}
	if v := q.Uint16(); v != 0x0203 {
		t.Fatalf("u16 = %#x", v)
	}
	if v := q.Uint32(); v != 0x04050607 {
		t.Fatalf("u32 = %#x", v)
	}
	if v := q.Uint64(); v != 0x08090a0b0c0d0e0f {
		t.Fatalf("u64 = %#x", v)
	}
	if v := q.ULong(); v != 0x10111213 {
		t.Fatalf("ulong = %#x", v)
	}
	if err := q.Err(); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestGUIDWireLayout(t *testing.T) {
	p := NewPush()
	p.GUID(sampleGUID)
	if !bytes.Equal(p.Bytes(), sampleGUIDWire) {
		t.Fatalf("got % x, want % x", p.Bytes(), sampleGUIDWire)
	}
	got := NewPull(sampleGUIDWire).GUID()
	if got != sampleGUID {
		t.Fatalf("round-trip GUID = %+v, want %+v", got, sampleGUID)
	}
}

// TestCtxHandleRoundTrip covers the handle EMSMDB returns from EcDoConnectEx and
// the client echoes back: a 4-byte type plus a GUID, 20 bytes total.
func TestCtxHandleRoundTrip(t *testing.T) {
	h := ContextHandle{HandleType: 0, GUID: sampleGUID}
	p := NewPush()
	p.CtxHandle(h)
	if got := p.Len(); got != 20 {
		t.Fatalf("encoded length = %d, want 20", got)
	}
	want := append([]byte{0, 0, 0, 0}, sampleGUIDWire...)
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("got % x, want % x", p.Bytes(), want)
	}
	got := NewPull(p.Bytes()).CtxHandle()
	if got != h {
		t.Fatalf("round-trip handle = %+v, want %+v", got, h)
	}
}

// TestUniquePtrReferentIds asserts present pointers get non-zero, monotonically
// advancing referent ids and absent pointers get zero — the contract a client
// relies on to tell "value present" from "null".
func TestUniquePtrReferentIds(t *testing.T) {
	p := NewPush()
	p.UniquePtr(true)
	p.UniquePtr(true)
	p.UniquePtr(false)
	want := []byte{
		0x00, 0x00, 0x02, 0x00, // 0x00020000
		0x04, 0x00, 0x02, 0x00, // 0x00020004
		0x00, 0x00, 0x00, 0x00, // null
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("got % x, want % x", p.Bytes(), want)
	}
}

// TestConformantVaryingComposition exercises the counted-array idiom L4 builds
// inline for EMSMDB string/byte buffers: max_count, offset(0), actual_count,
// then the opaque payload with no interior alignment.
func TestConformantVaryingComposition(t *testing.T) {
	data := []byte{0xde, 0xad, 0xbe, 0xef}
	p := NewPush()
	p.ULong(uint32(len(data)))
	p.ULong(0)
	p.ULong(uint32(len(data)))
	p.Raw(data)

	want := []byte{
		0x04, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x04, 0x00, 0x00, 0x00,
		0xde, 0xad, 0xbe, 0xef,
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("got % x, want % x", p.Bytes(), want)
	}

	q := NewPull(p.Bytes())
	if max := q.ULong(); max != 4 {
		t.Fatalf("max_count = %d", max)
	}
	if off := q.ULong(); off != 0 {
		t.Fatalf("offset = %d", off)
	}
	actual := q.ULong()
	if actual != 4 {
		t.Fatalf("actual_count = %d", actual)
	}
	if got := q.Bytes(int(actual)); !bytes.Equal(got, data) {
		t.Fatalf("payload = % x, want % x", got, data)
	}
	if err := q.Err(); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// TestRawDoesNotAlign guards the seam the advisor flagged: opaque payloads (a
// ROP buffer rides inside one) must not pick up NDR padding, or the embedded
// flat stream would be corrupted.
func TestRawDoesNotAlign(t *testing.T) {
	p := NewPush()
	p.Uint8(0xAA)
	p.Raw([]byte{0x01, 0x02, 0x03})
	want := []byte{0xAA, 0x01, 0x02, 0x03}
	if !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("got % x, want % x — Raw must not align", p.Bytes(), want)
	}
}

func TestTruncationLatchesError(t *testing.T) {
	q := NewPull([]byte{0x01, 0x02})
	_ = q.Uint32()
	if !errors.Is(q.Err(), ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", q.Err())
	}
	// A latched error makes subsequent reads no-ops returning zero.
	if v := q.Uint8(); v != 0 {
		t.Fatalf("post-error read = %#x, want 0", v)
	}
}
