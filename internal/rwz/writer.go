package rwz

import (
	"bytes"
	"encoding/binary"
	"math"
	"unicode/utf16"
)

// writer accumulates little-endian .rwz bytes. It is the exact inverse of
// reader and mirrors the documented Outlook rule byte layout, so its output
// round-trips through reader and parses with an independent reference parser.
type writer struct {
	buf bytes.Buffer
}

func (w *writer) bytesOut() []byte { return w.buf.Bytes() }
func (w *writer) len() int         { return w.buf.Len() }

func (w *writer) u8(v uint8) { w.buf.WriteByte(v) }

func (w *writer) u16(v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	w.buf.Write(b[:])
}

func (w *writer) u32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.buf.Write(b[:])
}

func (w *writer) u64(v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	w.buf.Write(b[:])
}

func (w *writer) f64(v float64) { w.u64(math.Float64bits(v)) }

func (w *writer) raw(b []byte) { w.buf.Write(b) }

// stringObject writes a length-prefixed UTF-16LE string. Lengths below 0xff use
// the single-byte form (rule names/values are short), avoiding the 0xff escape.
func (w *writer) stringObject(s string) {
	u := utf16.Encode([]rune(s))
	if len(u) < 0xff {
		w.u8(uint8(len(u)))
	} else {
		w.u8(0xff)
		w.u16(uint16(len(u)))
		w.u16(0)
	}
	for _, c := range u {
		w.u16(c)
	}
}

// ascii writes raw ASCII bytes (no length prefix, no terminator).
func (w *writer) ascii(s string) { w.buf.WriteString(s) }

// ---------------------------------------------------------------------------
// PropertyValueArray + recipient list builders
// ---------------------------------------------------------------------------

// wprop is one property to emit: a string value (PtypString) or an inline
// integer (PtypInteger32).
type wprop struct {
	typ uint16
	id  uint16
	str string
	num uint32
}

// propValArray writes a property-value array that reader.propValArray (and any
// MS-OXCDATA PropertyValueArray reader) can parse: u32 magic(0), u32 nProps, u32 dataSize, the
// fixed 16-byte headers, then the NUL-terminated UTF-16 value block. String
// values are referenced by a byte offset from the start of the header region.
func (w *writer) propValArray(props []wprop) {
	header := len(props) * 16
	var blob bytes.Buffer
	offs := make([]uint32, len(props))
	for i, p := range props {
		if p.typ == ptypString {
			offs[i] = uint32(header + blob.Len())
			for _, c := range utf16.Encode([]rune(p.str)) {
				var b [2]byte
				binary.LittleEndian.PutUint16(b[:], c)
				blob.Write(b[:])
			}
			blob.Write([]byte{0, 0}) // U+0000 terminator
		}
	}
	w.u32(0)
	w.u32(uint32(len(props)))
	w.u32(uint32(header + blob.Len()))
	for i, p := range props {
		w.u16(p.typ)
		w.u16(p.id)
		switch p.typ {
		case ptypString:
			w.u32(0)
			w.u32(offs[i])
			w.u32(0)
		default: // PtypInteger32 inline value
			w.u32(0)
			w.u32(p.num)
			w.u32(0)
		}
	}
	w.raw(blob.Bytes())
}

// recipientProps builds the minimal property set that identifies an SMTP
// recipient. Only unambiguous PtypString/PtypInteger32 properties are emitted
// (no PR_ENTRYID binary blob, whose layout the two reverse-engineered specs
// disagree on); the SMTP address is what reader.addressFromProps recovers.
func recipientProps(addr, name string) []wprop {
	if name == "" {
		name = addr
	}
	return []wprop{
		{typ: ptypString, id: pidDisplayName, str: name},
		{typ: ptypString, id: pidAddressType, str: "SMTP"},
		{typ: ptypString, id: pidEmailAddress, str: addr},
		{typ: ptypString, id: pidSmtpAddress, str: addr},
		{typ: ptypInteger32, id: pidRecipientType, num: recipientTypeTo},
	}
}

// peopleList writes a People-or-public-group list element: u32 ext(1),
// u32 reserved(0), u32 nValues, the recipient arrays, then trailing
// u32 1, u32 0.
func (w *writer) peopleList(addrs []string) {
	w.u32(1)
	w.u32(0)
	w.u32(uint32(len(addrs)))
	for _, a := range addrs {
		w.propValArray(recipientProps(a, ""))
	}
	w.u32(1)
	w.u32(0)
}

// moveToFolder writes a Move/Copy payload with empty folder and store entry
// ids — a real Outlook store entry id cannot be synthesized, so only the
// folder name is carried (and recovered on import).
func (w *writer) moveToFolder(folder string) {
	w.u32(1) // extended
	w.u32(0) // reserved
	w.u32(0) // folder entry id: size 0
	w.u32(0) // store entry id: size 0
	w.stringObject(folder)
	w.u32(0) // secondary user store = false
}
