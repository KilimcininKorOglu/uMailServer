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
