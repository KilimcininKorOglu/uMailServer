package wire

import (
	"bytes"
	"reflect"
	"testing"
)

// TestPushFastTransferPropval pins the FastTransfer propValue encoding against
// hand-computed spec vectors (MS-OXCFXICS 2.2.4.1.1.1), NOT round-trip: a propdef
// (proptype u16 LE + propid u16 LE) then the value, where strings carry a u32 count
// that includes the terminating NUL, binaries a u32 count, and booleans are a u16.
func TestPushFastTransferPropval(t *testing.T) {
	cases := []struct {
		name string
		tag  PropTag
		val  any
		want []byte
	}{
		{
			"PtLong", PropTag(0x10000003), uint32(0x12345678),
			[]byte{0x03, 0x00, 0x00, 0x10, 0x78, 0x56, 0x34, 0x12},
		},
		{
			"PtBoolean true", PropTag(0x1001000B), true,
			[]byte{0x0B, 0x00, 0x01, 0x10, 0x01, 0x00},
		},
		{
			// String8 "Hi": u32 count 3 (incl NUL) + "Hi" + 0x00.
			"PtString8", PropTag(0x1002001E), "Hi",
			[]byte{0x1E, 0x00, 0x02, 0x10, 0x03, 0x00, 0x00, 0x00, 0x48, 0x69, 0x00},
		},
		{
			// Unicode "Hi": u32 count 6 (UTF-16LE "Hi" = 4 bytes + 2-byte NUL).
			"PtUnicode", PropTag(0x1003001F), "Hi",
			[]byte{0x1F, 0x00, 0x03, 0x10, 0x06, 0x00, 0x00, 0x00, 0x48, 0x00, 0x69, 0x00, 0x00, 0x00},
		},
		{
			// Empty Unicode: count 2, just the UTF-16 NUL terminator.
			"PtUnicode empty", PropTag(0x1003001F), "",
			[]byte{0x1F, 0x00, 0x03, 0x10, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00},
		},
		{
			"PtBinary", PropTag(0x10040102), []byte{0xAA, 0xBB},
			[]byte{0x02, 0x01, 0x04, 0x10, 0x02, 0x00, 0x00, 0x00, 0xAA, 0xBB},
		},
		{
			"PtI8", PropTag(0x10050014), uint64(0x1122334455667788),
			[]byte{0x14, 0x00, 0x05, 0x10, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11},
		},
	}
	for _, c := range cases {
		p := NewPush(0)
		if err := PushFastTransferPropval(p, c.tag, c.val); err != nil {
			t.Errorf("%s: PushFastTransferPropval: %v", c.name, err)
			continue
		}
		if got := p.Bytes(); !bytes.Equal(got, c.want) {
			t.Errorf("%s: = % x, want % x", c.name, got, c.want)
		}
	}
}

// TestPushFastTransferPropvalNamedRejected verifies a named property (id >= 0x8000)
// is rejected, since its propdef GUID+kind block is not yet emitted.
func TestPushFastTransferPropvalNamedRejected(t *testing.T) {
	p := NewPush(0)
	if err := PushFastTransferPropval(p, PropTag(0x80100003), uint32(0)); err == nil {
		t.Error("named-property id was accepted, want an error")
	}
}

// TestPushFastTransferPropvalTypeMismatch verifies a value whose Go type does not
// match the property type is rejected rather than mis-serialized.
func TestPushFastTransferPropvalTypeMismatch(t *testing.T) {
	p := NewPush(0)
	if err := PushFastTransferPropval(p, PropTag(0x10000003), "not a uint32"); err == nil {
		t.Error("type mismatch was accepted, want an error")
	}
}

// TestPullFastTransferElement decodes hand-computed spec vectors (the same byte
// layouts TestPushFastTransferPropval pins), NOT a round-trip against the producer: a
// propdef (proptype u16 LE + propid u16 LE) then the FastTransfer-framed value. This
// catches a systematic decode error (a string length off by the NUL, a boolean read as
// the wrong width) that a Push-then-Pull test would mask.
func TestPullFastTransferElement(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		tag  PropTag
		want any
	}{
		{"PtLong", []byte{0x03, 0x00, 0x00, 0x10, 0x78, 0x56, 0x34, 0x12}, PropTag(0x10000003), uint32(0x12345678)},
		{"PtBoolean true", []byte{0x0B, 0x00, 0x01, 0x10, 0x01, 0x00}, PropTag(0x1001000B), true},
		{"PtBoolean false", []byte{0x0B, 0x00, 0x01, 0x10, 0x00, 0x00}, PropTag(0x1001000B), false},
		{"PtString8", []byte{0x1E, 0x00, 0x02, 0x10, 0x03, 0x00, 0x00, 0x00, 0x48, 0x69, 0x00}, PropTag(0x1002001E), "Hi"},
		{"PtUnicode", []byte{0x1F, 0x00, 0x03, 0x10, 0x06, 0x00, 0x00, 0x00, 0x48, 0x00, 0x69, 0x00, 0x00, 0x00}, PropTag(0x1003001F), "Hi"},
		{"PtUnicode empty", []byte{0x1F, 0x00, 0x03, 0x10, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00}, PropTag(0x1003001F), ""},
		{"PtBinary", []byte{0x02, 0x01, 0x04, 0x10, 0x02, 0x00, 0x00, 0x00, 0xAA, 0xBB}, PropTag(0x10040102), []byte{0xAA, 0xBB}},
		{"PtI8", []byte{0x14, 0x00, 0x05, 0x10, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}, PropTag(0x10050014), uint64(0x1122334455667788)},
	}
	for _, c := range cases {
		el, err := PullFastTransferElement(NewPull(c.in, 0))
		if err != nil {
			t.Errorf("%s: PullFastTransferElement: %v", c.name, err)
			continue
		}
		if el.Marker != 0 {
			t.Errorf("%s: decoded a marker %#x, want a property value", c.name, el.Marker)
			continue
		}
		if el.Tag != c.tag {
			t.Errorf("%s: tag = %#08x, want %#08x", c.name, uint32(el.Tag), uint32(c.tag))
		}
		if !reflect.DeepEqual(el.Value, c.want) {
			t.Errorf("%s: value = %#v, want %#v", c.name, el.Value, c.want)
		}
	}
}

// TestPullFastTransferElementMarker verifies a marker atom decodes as a marker (not a
// property): INCRSYNCCHG's little-endian bytes 03 00 12 40 -> FXIncrSyncChg.
func TestPullFastTransferElementMarker(t *testing.T) {
	el, err := PullFastTransferElement(NewPull([]byte{0x03, 0x00, 0x12, 0x40}, 0))
	if err != nil {
		t.Fatalf("PullFastTransferElement: %v", err)
	}
	if el.Marker != FXIncrSyncChg {
		t.Errorf("marker = %#x, want FXIncrSyncChg %#x", el.Marker, FXIncrSyncChg)
	}
}

// TestPullFastTransferElementRoundTrip confirms the parser is the exact inverse of the
// producer across the supported scalar/string/binary types (a secondary check on top
// of the spec-vector decode).
func TestPullFastTransferElementRoundTrip(t *testing.T) {
	cases := []struct {
		tag PropTag
		val any
	}{
		{PropTag(0x30070040), uint64(0x01D9A1B2C3D4E5F6)}, // PtSysTime
		{PropTag(0x0037001F), "Subject ünïcödé"},          // PtUnicode with non-ASCII
		{PropTag(0x0E1D001E), "ascii subject"},            // PtString8
		{PropTag(0x10000102), []byte{1, 2, 3, 4, 5}},      // PtBinary
		{PropTag(0x36010003), uint32(42)},                 // PtLong
	}
	for _, c := range cases {
		p := NewPush(0)
		if err := PushFastTransferPropval(p, c.tag, c.val); err != nil {
			t.Fatalf("push %#08x: %v", uint32(c.tag), err)
		}
		el, err := PullFastTransferElement(NewPull(p.Bytes(), 0))
		if err != nil {
			t.Fatalf("pull %#08x: %v", uint32(c.tag), err)
		}
		if el.Tag != c.tag || !reflect.DeepEqual(el.Value, c.val) {
			t.Errorf("round-trip %#08x: got tag %#08x value %#v", uint32(c.tag), uint32(el.Tag), el.Value)
		}
	}
}

// TestPullFastTransferElementErrors verifies the parser hard-errors instead of
// desyncing: an unsupported property type, a named-property id, and a truncated value
// must each return an error (and not panic).
func TestPullFastTransferElementErrors(t *testing.T) {
	// PtObject (0x000D) is not a FastTransfer value type; propid 0x1000 is not a marker.
	if _, err := PullFastTransferElement(NewPull([]byte{0x0D, 0x00, 0x00, 0x10}, 0)); err == nil {
		t.Error("unsupported property type was accepted, want an error")
	}
	// Named-property id (>= 0x8000) in the high half.
	if _, err := PullFastTransferElement(NewPull([]byte{0x03, 0x00, 0x10, 0x80, 0, 0, 0, 0}, 0)); err == nil {
		t.Error("named-property id was accepted, want an error")
	}
	// PtBinary claiming 0x10 bytes with only 2 present: truncated, must error not panic.
	if _, err := PullFastTransferElement(NewPull([]byte{0x02, 0x01, 0x00, 0x10, 0x10, 0x00, 0x00, 0x00, 0xAA, 0xBB}, 0)); err == nil {
		t.Error("truncated binary was accepted, want an error")
	}
}
