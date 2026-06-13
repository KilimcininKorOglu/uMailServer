package wire

import (
	"bytes"
	"testing"
)

// TestSerializeGlobset asserts the GLOBSET encoder against hand-computed spec
// vectors (MS-OXCFXICS 2.2.2.6, mirroring the reference encoder), NOT round-trip:
// the bytes a real ICS client must accept are fixed, so the test pins them rather
// than only checking the encoder agrees with itself. Each GLOBCNT is the low 48
// bits of the value, most-significant byte first.
func TestSerializeGlobset(t *testing.T) {
	cases := []struct {
		name   string
		ranges []GlobcntRange
		want   []byte
	}{
		{
			// No ids: just the end command.
			"empty",
			nil,
			[]byte{0x00},
		},
		{
			// Single id 1 → push 6 bytes of GLOBCNT(1), then end.
			"single id",
			[]GlobcntRange{{Lo: 1, Hi: 1}},
			[]byte{0x06, 0, 0, 0, 0, 0, 1, 0x00},
		},
		{
			// Single contiguous range [1,5] → range command, 6 low + 6 high, then end.
			"single range",
			[]GlobcntRange{{Lo: 1, Hi: 5}},
			[]byte{0x52, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 5, 0x00},
		},
		{
			// Two non-contiguous single ids 1 and 3: common 5-byte prefix [0,0,0,0,0]
			// pushed once, each id's last byte pushed, pop, end.
			"two single ids share a prefix",
			[]GlobcntRange{{Lo: 1, Hi: 1}, {Lo: 3, Hi: 3}},
			[]byte{0x05, 0, 0, 0, 0, 0, 0x01, 0x01, 0x01, 0x03, 0x50, 0x00},
		},
		{
			// A single id 1 then a range [5,7]: shared 5-byte prefix, id pushes its
			// last byte, the range emits its 1-byte low/high suffix, pop, end.
			"single id then a range",
			[]GlobcntRange{{Lo: 1, Hi: 1}, {Lo: 5, Hi: 7}},
			[]byte{0x05, 0, 0, 0, 0, 0, 0x01, 0x01, 0x52, 0x05, 0x07, 0x50, 0x00},
		},
		{
			// Two ids with a non-zero common prefix (0x010205, 0x010208): the shared
			// 5-byte prefix [00 00 00 01 02] is pushed, then each id's last byte.
			"two ids with a non-zero common prefix",
			[]GlobcntRange{{Lo: 0x010205, Hi: 0x010205}, {Lo: 0x010208, Hi: 0x010208}},
			[]byte{0x05, 0, 0, 0, 0x01, 0x02, 0x01, 0x05, 0x01, 0x08, 0x50, 0x00},
		},
	}
	for _, c := range cases {
		got := SerializeGlobset(c.ranges)
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s: SerializeGlobset = % x, want % x", c.name, got, c.want)
		}
	}
}

// rangesEqual compares two range slices, treating nil and empty as equal.
func rangesEqual(a, b []GlobcntRange) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestParseGlobsetRoundTrip verifies ParseGlobset inverts SerializeGlobset across
// single ids, single ranges, multi-range sets sharing a prefix, and a sparse set —
// the encoder and decoder agree on every shape.
func TestParseGlobsetRoundTrip(t *testing.T) {
	cases := [][]GlobcntRange{
		nil,
		{{Lo: 1, Hi: 1}},
		{{Lo: 1, Hi: 5}},
		{{Lo: 1, Hi: 1}, {Lo: 3, Hi: 3}},
		{{Lo: 1, Hi: 1}, {Lo: 5, Hi: 7}},
		{{Lo: 0x010205, Hi: 0x010205}, {Lo: 0x010208, Hi: 0x010208}},
		{{Lo: 10, Hi: 20}, {Lo: 100, Hi: 200}, {Lo: 1000, Hi: 1000}},
	}
	for _, ranges := range cases {
		dec, err := ParseGlobset(SerializeGlobset(ranges))
		if err != nil {
			t.Errorf("%v: ParseGlobset: %v", ranges, err)
			continue
		}
		if !rangesEqual(dec, ranges) {
			t.Errorf("round trip %v -> %v", ranges, dec)
		}
	}
}

// TestParseGlobsetBitmask decodes the 0x42 bitmask command — which the encoder never
// emits but a conformant reader must accept — from hand-crafted bytes, so the test is
// independent of SerializeGlobset rather than a round trip.
func TestParseGlobsetBitmask(t *testing.T) {
	// push 5 common bytes [0,0,0,0,0], then bitmask with start 0x10 and no bits set:
	// just the single id 0x10.
	none, err := ParseGlobset([]byte{0x05, 0, 0, 0, 0, 0, 0x42, 0x10, 0x00, 0x00})
	if err != nil {
		t.Fatalf("bitmask (no bits): %v", err)
	}
	if !rangesEqual(none, []GlobcntRange{{Lo: 0x10, Hi: 0x10}}) {
		t.Errorf("bitmask (no bits) = %v, want [{0x10,0x10}]", none)
	}
	// bit 0 set extends the range to include 0x11: range [0x10, 0x11].
	one, err := ParseGlobset([]byte{0x05, 0, 0, 0, 0, 0, 0x42, 0x10, 0x01, 0x00})
	if err != nil {
		t.Fatalf("bitmask (bit 0): %v", err)
	}
	if !rangesEqual(one, []GlobcntRange{{Lo: 0x10, Hi: 0x11}}) {
		t.Errorf("bitmask (bit 0) = %v, want [{0x10,0x11}]", one)
	}
}

// TestParseGlobsetMalformed verifies truncated or unterminated sets are rejected.
func TestParseGlobsetMalformed(t *testing.T) {
	if _, err := ParseGlobset([]byte{0x06, 0, 0, 0}); err == nil {
		t.Error("truncated push accepted, want an error")
	}
	if _, err := ParseGlobset([]byte{0x06, 0, 0, 0, 0, 0, 1}); err == nil {
		t.Error("missing end command accepted, want an error")
	}
}

// TestParseIDSETRoundTrip verifies ParseIDSET recovers the replica GUID and ranges
// SerializeIDSET wrote.
func TestParseIDSETRoundTrip(t *testing.T) {
	guid := GUID{TimeLow: 0xDEADBEEF, TimeMid: 0x1234, TimeHiAndVersion: 0x5678, ClockSeq: [2]byte{0x9A, 0xBC}, Node: [6]byte{1, 2, 3, 4, 5, 6}}
	ranges := []GlobcntRange{{Lo: 1, Hi: 9}, {Lo: 100, Hi: 100}}
	g, dec, err := ParseIDSET(SerializeIDSET(guid, ranges))
	if err != nil {
		t.Fatalf("ParseIDSET: %v", err)
	}
	if g != guid {
		t.Errorf("replica GUID = %+v, want %+v", g, guid)
	}
	if !rangesEqual(dec, ranges) {
		t.Errorf("ranges = %v, want %v", dec, ranges)
	}
}

// TestSerializeIDSET pins the single-replica IDSET layout: the replica GUID in
// standard wire order (TimeLow u32 LE, TimeMid u16 LE, TimeHiAndVersion u16 LE,
// ClockSeq, Node) followed by the GLOBSET, against hand-computed bytes.
func TestSerializeIDSET(t *testing.T) {
	guid := GUID{
		TimeLow:          0x01020304,
		TimeMid:          0x0506,
		TimeHiAndVersion: 0x0708,
		ClockSeq:         [2]byte{0x09, 0x0A},
		Node:             [6]byte{0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10},
	}
	got := SerializeIDSET(guid, []GlobcntRange{{Lo: 1, Hi: 1}})
	want := []byte{
		0x04, 0x03, 0x02, 0x01, 0x06, 0x05, 0x08, 0x07, // GUID: TimeLow LE, TimeMid LE, TimeHi LE
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, // GUID: ClockSeq, Node
		0x06, 0, 0, 0, 0, 0, 1, 0x00, // GLOBSET: push 6 of GLOBCNT(1), end
	}
	if !bytes.Equal(got, want) {
		t.Errorf("SerializeIDSET = % x, want % x", got, want)
	}
}

// TestSerializeXID pins the 22-byte XID layout: the GUID in standard wire order
// followed by the 6-byte big-endian GLOBCNT, against hand-computed bytes.
func TestSerializeXID(t *testing.T) {
	guid := GUID{
		TimeLow:          0x01020304,
		TimeMid:          0x0506,
		TimeHiAndVersion: 0x0708,
		ClockSeq:         [2]byte{0x09, 0x0A},
		Node:             [6]byte{0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10},
	}
	got := SerializeXID(guid, 0x665544)
	want := []byte{
		0x04, 0x03, 0x02, 0x01, 0x06, 0x05, 0x08, 0x07, // GUID first half
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, // GUID second half
		0x00, 0x00, 0x00, 0x66, 0x55, 0x44, // GLOBCNT(0x665544): low 48 bits, MSB first
	}
	if !bytes.Equal(got, want) {
		t.Errorf("SerializeXID = % x, want % x", got, want)
	}
	if len(got) != 22 {
		t.Errorf("XID length = %d, want 22", len(got))
	}
}

// TestValueToGlobcnt pins the value→GLOBCNT conversion: the low 48 bits, MSB first.
func TestValueToGlobcnt(t *testing.T) {
	got := valueToGlobcnt(0x0000_1122_3344_5566)
	want := [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
	if got != want {
		t.Errorf("valueToGlobcnt = % x, want % x (low 48 bits, MSB first)", got, want)
	}
	// The high 16 bits of the value are NOT part of the GLOBCNT.
	if valueToGlobcnt(0xFFFF_000000000001) != [6]byte{0, 0, 0, 0, 0, 1} {
		t.Error("valueToGlobcnt must drop the high 16 bits (the replica id portion)")
	}
}
