package wire

import (
	"bytes"
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
