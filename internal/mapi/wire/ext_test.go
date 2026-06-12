package wire

import (
	"bytes"
	"errors"
	"testing"
)

// TestPrimitiveRoundTrip verifies every integer/float/bool primitive survives a
// Push→Pull cycle unchanged, which is the codec's core correctness guarantee:
// the encoder and decoder agree on the little-endian layout.
func TestPrimitiveRoundTrip(t *testing.T) {
	p := NewPush(0)
	p.Uint8(0xAB)
	p.Uint16(0x1234)
	p.Uint32(0x89ABCDEF)
	p.Uint64(0x0102030405060708)
	p.Float32(3.5)
	p.Float64(-12.25)
	p.Bool(true)
	p.Bool(false)

	q := NewPull(p.Bytes(), 0)
	if v := q.Uint8(); v != 0xAB {
		t.Errorf("Uint8 = %#x, want 0xAB", v)
	}
	if v := q.Uint16(); v != 0x1234 {
		t.Errorf("Uint16 = %#x, want 0x1234", v)
	}
	if v := q.Uint32(); v != 0x89ABCDEF {
		t.Errorf("Uint32 = %#x, want 0x89ABCDEF", v)
	}
	if v := q.Uint64(); v != 0x0102030405060708 {
		t.Errorf("Uint64 = %#x, want 0x0102030405060708", v)
	}
	if v := q.Float32(); v != 3.5 {
		t.Errorf("Float32 = %v, want 3.5", v)
	}
	if v := q.Float64(); v != -12.25 {
		t.Errorf("Float64 = %v, want -12.25", v)
	}
	if v := q.Bool(); v != true {
		t.Errorf("Bool#1 = %v, want true", v)
	}
	if v := q.Bool(); v != false {
		t.Errorf("Bool#2 = %v, want false", v)
	}
	if q.Err() != nil {
		t.Fatalf("Err = %v, want nil", q.Err())
	}
	if q.Remaining() != 0 {
		t.Errorf("Remaining = %d, want 0 (every byte consumed)", q.Remaining())
	}
}

// TestLittleEndianLayout pins the exact bytes so a regression in byte order is
// caught even if Push and Pull drift together.
func TestLittleEndianLayout(t *testing.T) {
	p := NewPush(0)
	p.Uint32(0x01020304)
	want := []byte{0x04, 0x03, 0x02, 0x01}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("Uint32 layout = % x, want % x", p.Bytes(), want)
	}
}

// TestGUIDRoundTrip checks the mixed-endian GUID layout (LE uint32/uint16 head,
// raw byte tail) round-trips and matches the documented 16-byte wire form.
func TestGUIDRoundTrip(t *testing.T) {
	g := GUID{
		TimeLow:          0xDEADBEEF,
		TimeMid:          0x1122,
		TimeHiAndVersion: 0x3344,
		ClockSeq:         [2]byte{0x55, 0x66},
		Node:             [6]byte{0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC},
	}
	p := NewPush(0)
	p.GUID(g)
	if p.Len() != 16 {
		t.Fatalf("GUID encoded to %d bytes, want 16", p.Len())
	}
	want := []byte{0xEF, 0xBE, 0xAD, 0xDE, 0x22, 0x11, 0x44, 0x33, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("GUID layout = % x, want % x", p.Bytes(), want)
	}
	if got := NewPull(p.Bytes(), 0).GUID(); got != g {
		t.Errorf("GUID round-trip = %+v, want %+v", got, g)
	}
}

// TestStrRoundTrip covers the 8-bit NUL-terminated string and confirms the
// terminator is written and consumed.
func TestStrRoundTrip(t *testing.T) {
	p := NewPush(0)
	p.Str("hi")
	if want := []byte{'h', 'i', 0}; !bytes.Equal(p.Bytes(), want) {
		t.Errorf("Str layout = % x, want % x", p.Bytes(), want)
	}
	q := NewPull(p.Bytes(), 0)
	if s := q.Str(); s != "hi" {
		t.Errorf("Str = %q, want %q", s, "hi")
	}
	if q.Remaining() != 0 {
		t.Errorf("Remaining = %d, want 0", q.Remaining())
	}
}

// TestWStrUTF16 verifies wide strings encode as UTF-16LE with a 0x0000
// terminator under FlagUTF16 and decode back, including a non-ASCII rune to
// catch a byte-order or surrogate mistake.
func TestWStrUTF16(t *testing.T) {
	const s = "Aü"
	p := NewPush(FlagUTF16)
	p.WStr(s)
	// 'A' = 0x0041, 'ü' = 0x00FC, terminator 0x0000 — all little-endian.
	want := []byte{0x41, 0x00, 0xFC, 0x00, 0x00, 0x00}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("WStr layout = % x, want % x", p.Bytes(), want)
	}
	if got := NewPull(p.Bytes(), FlagUTF16).WStr(); got != s {
		t.Errorf("WStr round-trip = %q, want %q", got, s)
	}
}

// TestWStrFallsBackToASCII confirms that without FlagUTF16 a wide string uses
// the 8-bit form, so the same call site works on both transports.
func TestWStrFallsBackToASCII(t *testing.T) {
	p := NewPush(0)
	p.WStr("ok")
	if want := []byte{'o', 'k', 0}; !bytes.Equal(p.Bytes(), want) {
		t.Errorf("WStr (no UTF16) layout = % x, want % x", p.Bytes(), want)
	}
}

// TestBinCountWidth verifies the count prefix is 16-bit by default and 32-bit
// under FlagWCount, since the two MAPI transports disagree on the width.
func TestBinCountWidth(t *testing.T) {
	payload := []byte{0xDE, 0xAD}

	p16 := NewPush(0)
	p16.Bin(payload)
	if want := []byte{0x02, 0x00, 0xDE, 0xAD}; !bytes.Equal(p16.Bytes(), want) {
		t.Errorf("Bin (16-bit count) = % x, want % x", p16.Bytes(), want)
	}
	if got := NewPull(p16.Bytes(), 0).Bin(); !bytes.Equal(got, payload) {
		t.Errorf("Bin 16-bit round-trip = % x, want % x", got, payload)
	}

	p32 := NewPush(FlagWCount)
	p32.Bin(payload)
	if want := []byte{0x02, 0x00, 0x00, 0x00, 0xDE, 0xAD}; !bytes.Equal(p32.Bytes(), want) {
		t.Errorf("Bin (32-bit count) = % x, want % x", p32.Bytes(), want)
	}
	if got := NewPull(p32.Bytes(), FlagWCount).Bin(); !bytes.Equal(got, payload) {
		t.Errorf("Bin 32-bit round-trip = % x, want % x", got, payload)
	}
}

// TestRPCHeaderExtRoundTrip pins the 8-byte RPC_HEADER_EXT frame.
func TestRPCHeaderExtRoundTrip(t *testing.T) {
	h := RPCHeaderExt{Version: 0x0000, Flags: RHEFlagLast | RHEFlagCompressed, Size: 0x40, SizeActual: 0x80}
	p := NewPush(0)
	h.Push(p)
	if p.Len() != 8 {
		t.Fatalf("RPC_HEADER_EXT encoded to %d bytes, want 8", p.Len())
	}
	if got := PullRPCHeaderExt(NewPull(p.Bytes(), 0)); got != h {
		t.Errorf("RPC_HEADER_EXT round-trip = %+v, want %+v", got, h)
	}
}

// TestTruncationIsSticky verifies that reading past the end latches ErrTruncated
// and subsequent reads stay zero rather than panicking, so a malformed request
// degrades safely.
func TestTruncationIsSticky(t *testing.T) {
	q := NewPull([]byte{0x01}, 0)
	_ = q.Uint8()
	_ = q.Uint32() // past the end
	if !errors.Is(q.Err(), ErrTruncated) {
		t.Fatalf("Err = %v, want ErrTruncated", q.Err())
	}
	if v := q.Uint16(); v != 0 {
		t.Errorf("post-error read = %#x, want 0", v)
	}
}

// TestPropTagPacking checks the id/type packing used everywhere a property is
// referenced.
func TestPropTagPacking(t *testing.T) {
	tag := MakeTag(0x3001, PtUnicode)
	if tag != PidTagDisplayName {
		t.Errorf("MakeTag(0x3001, PtUnicode) = %#x, want PidTagDisplayName %#x", uint32(tag), uint32(PidTagDisplayName))
	}
	if tag.ID() != 0x3001 {
		t.Errorf("ID = %#x, want 0x3001", tag.ID())
	}
	if tag.Type() != PtUnicode {
		t.Errorf("Type = %#x, want PtUnicode", tag.Type())
	}
	if !MakeTag(0, PtMvBinary).IsMultivalue() {
		t.Error("PtMvBinary should be multivalue")
	}
	if MakeTag(0, PtUnicode).IsMultivalue() {
		t.Error("PtUnicode should not be multivalue")
	}
}
