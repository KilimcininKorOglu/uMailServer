package oab

import "encoding/binary"

// writer accumulates the bytes of an OAB binary file. OAB has its own
// serialization rules distinct from the NSPI/emsmdb wire codec: strings are
// UTF-8 with a single NUL terminator regardless of property type, integers in
// records use a variable-length encoding, and records are length-prefixed with
// a self-inclusive size that must be back-patched once the body is known.
type writer struct {
	buf []byte
}

// u8 appends a byte.
func (w *writer) u8(v uint8) { w.buf = append(w.buf, v) }

// u32 appends a little-endian uint32.
func (w *writer) u32(v uint32) { w.buf = binary.LittleEndian.AppendUint32(w.buf, v) }

// varui appends a variable-length unsigned integer (MS-OXOAB §2.9.6.1): values
// up to 0x7F are a single byte; larger values are a 0x81-0x84 byte-count marker
// followed by that many little-endian value bytes.
func (w *writer) varui(v uint32) {
	switch {
	case v <= 0x7F:
		w.u8(uint8(v))
	case v <= 0xFF:
		w.u8(0x81)
		w.u8(uint8(v))
	case v <= 0xFFFF:
		w.u8(0x82)
		w.u8(uint8(v))
		w.u8(uint8(v >> 8))
	case v <= 0xFFFFFF:
		w.u8(0x83)
		w.u8(uint8(v))
		w.u8(uint8(v >> 8))
		w.u8(uint8(v >> 16))
	default:
		w.u8(0x84)
		w.u32(v)
	}
}

// str appends a NUL-terminated UTF-8 string (MS-OXOAB §2.9.6.3). Callers must
// only encode non-empty values; an absent value is marked in the record's
// presence bit array instead of being written here.
func (w *writer) str(s string) {
	w.buf = append(w.buf, s...)
	w.buf = append(w.buf, 0)
}

// beginRecord reserves a 4-byte little-endian size field and returns its offset
// so endRecord can back-patch it.
func (w *writer) beginRecord() int {
	off := len(w.buf)
	w.u32(0)
	return off
}

// endRecord patches the size field reserved by beginRecord with the byte count
// from the field itself to the current end; the size is self-inclusive
// (MS-OXOAB §2.9.5).
func (w *writer) endRecord(off int) {
	binary.LittleEndian.PutUint32(w.buf[off:], uint32(len(w.buf)-off))
}

// patch overwrites a 4-byte little-endian value at the given offset, used to
// fill in the header's serial field once the body CRC is known.
func (w *writer) patch(off int, v uint32) {
	binary.LittleEndian.PutUint32(w.buf[off:], v)
}

// bytes returns the accumulated buffer.
func (w *writer) bytes() []byte { return w.buf }

// size returns the number of bytes written so far.
func (w *writer) size() int { return len(w.buf) }
