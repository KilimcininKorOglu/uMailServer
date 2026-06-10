package rwz

import (
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf16"
)

// errTruncated is the sticky error set when a read would run past the buffer.
var errTruncated = errors.New("rwz: unexpected end of data")

// reader is a little-endian cursor over an .rwz byte slice. It follows the
// read semantics of the Outlook rule stream: every integer is little-endian,
// strings are length-prefixed UTF-16LE. A sticky
// err is set on the first out-of-bounds read so callers can read a whole element
// and check the error once; no read ever panics or indexes out of range.
type reader struct {
	b   []byte
	off int
	err error
}

func newReader(b []byte) *reader { return &reader{b: b} }

// ensure reports whether n more bytes are available, setting the sticky error otherwise.
func (r *reader) ensure(n int) bool {
	if r.err != nil {
		return false
	}
	if n < 0 || r.off+n > len(r.b) {
		r.err = errTruncated
		return false
	}
	return true
}

func (r *reader) u8() uint8 {
	if !r.ensure(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u16() uint16 {
	if !r.ensure(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if !r.ensure(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) u64() uint64 {
	if !r.ensure(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v
}

func (r *reader) f64() float64 {
	return math.Float64frombits(r.u64())
}

// skip advances the cursor by n bytes (bounds-checked).
func (r *reader) skip(n int) {
	if !r.ensure(n) {
		return
	}
	r.off += n
}

// stringObject reads a length-prefixed UTF-16LE string: a u8 length in UTF-16
// code units, escaped to a u16 length plus a 2-byte pad when the u8 is 0xff.
// No NUL terminator is stored.
func (r *reader) stringObject() string {
	n := int(r.u8())
	if n == 0xff {
		n = int(r.u16())
		r.skip(2)
	}
	if !r.ensure(n * 2) {
		return ""
	}
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		u[i] = r.u16()
	}
	return string(utf16.Decode(u))
}

// asciiString reads exactly n bytes as ASCII, trimming trailing NULs.
func (r *reader) asciiString(n int) string {
	if !r.ensure(n) {
		return ""
	}
	b := r.b[r.off : r.off+n]
	r.off += n
	end := len(b)
	for end > 0 && b[end-1] == 0 {
		end--
	}
	return string(b[:end])
}

// stringUTF16UntilNUL reads UTF-16LE code units until a U+0000 terminator,
// used for the values inside a PropertyValueArray data block (the
// MS-OXCDATA PropertyValueArray value encoding).
func (r *reader) stringUTF16UntilNUL() string {
	var u []uint16
	for r.err == nil {
		c := r.u16()
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// stringASCIIUntilNUL reads ASCII bytes until a 0 terminator.
func (r *reader) stringASCIIUntilNUL() string {
	var b []byte
	for r.err == nil {
		c := r.u8()
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// PropertyValueArray (the MS-OXCDATA PropertyValueArray)
// ---------------------------------------------------------------------------

// propVal is one decoded property: the kind plus either an inline integer or a
// string read from the value block. Only the property ids we care about
// (display name / address) are interpreted by callers.
type propVal struct {
	typ uint16
	id  uint16
	str string
	num uint32
}

// propValArray reads a single property-value array. Layout: u32 magic(0),
// u32 nProps, u32 propDataSize, then nProps fixed 16-byte headers, then the
// value block. String/binary values are referenced by an offset from the start
// of the header region; the cursor is left just past the whole data block.
func (r *reader) propValArray() []propVal {
	_ = r.u32() // magic, always 0
	nProps := r.u32()
	dataSize := r.u32()
	start := r.off
	end := start + int(dataSize)
	if dataSize > uint32(len(r.b)) || end > len(r.b) {
		r.err = errTruncated
		return nil
	}
	out := make([]propVal, 0, nProps)
	for i := uint32(0); i < nProps && r.err == nil; i++ {
		typ := r.u16()
		id := r.u16()
		d0 := r.u32()
		d1 := r.u32()
		d2 := r.u32()
		_ = d0
		_ = d2
		pv := propVal{typ: typ, id: id}
		switch typ {
		case ptypString:
			save := r.off
			if int(d1) >= 0 && start+int(d1) <= end {
				r.off = start + int(d1)
				pv.str = r.stringUTF16UntilNUL()
			}
			r.off = save
		case ptypString8:
			save := r.off
			if int(d1) >= 0 && start+int(d1) <= end {
				r.off = start + int(d1)
				pv.str = r.stringASCIIUntilNUL()
			}
			r.off = save
		case ptypInteger32, ptypErrorCode, ptypBoolean:
			pv.num = d1
		default:
			// PtypBinary / PtypTime / others: value lives in the block but we
			// do not need it; the offset reset below skips it.
		}
		out = append(out, pv)
	}
	if r.err == nil {
		r.off = end
	}
	return out
}

// skipEntryID consumes a flat entry id: a u32 size followed by size bytes
// (the folder/store entry ids, which always advance to pos+size+4 regardless
// of inner content).
func (r *reader) skipEntryID() {
	size := r.u32()
	r.skip(int(size))
}
