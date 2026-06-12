package wire

import (
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf16"
)

// Flag selects per-buffer serialization variants. The flags are not on the
// wire; they mirror the EXT_FLAG_* behavior the MS-OXCMAPIHTTP and ROP
// transports require so the same codec serves every surface.
type Flag uint32

const (
	// FlagUTF16 encodes wide strings as NUL-terminated UTF-16LE; without it
	// wide strings fall back to NUL-terminated UTF-8 (the 8-bit form).
	FlagUTF16 Flag = 1 << 0
	// FlagWCount prefixes counted binary with a 32-bit length; without it the
	// length is 16-bit.
	FlagWCount Flag = 1 << 1
	// FlagABK selects the address-book (NSPI) property-value encoding, where a
	// string, binary, or multivalue value is preceded by a one-byte presence
	// marker (0x00 absent, 0xFF present).
	FlagABK Flag = 1 << 2
)

// Codec errors. Pull uses a sticky error: the first failing read latches the
// error and every subsequent read is a no-op, so a caller can decode a whole
// structure and check Err once.
var (
	// ErrTruncated is set when a read would run past the end of the buffer.
	ErrTruncated = errors.New("mapi/wire: unexpected end of data")
	// ErrFormat is set when bytes are structurally invalid (e.g. a non-0/1
	// boolean or an unterminated string).
	ErrFormat = errors.New("mapi/wire: malformed data")
)

// GUID is a MAPI/COM GUID serialized as time_low (LE uint32), time_mid (LE
// uint16), time_hi_and_version (LE uint16), then 2 + 6 raw bytes.
type GUID struct {
	TimeLow          uint32
	TimeMid          uint16
	TimeHiAndVersion uint16
	ClockSeq         [2]byte
	Node             [6]byte
}

// Push accumulates little-endian MAPI bytes.
type Push struct {
	buf   []byte
	flags Flag
}

// NewPush returns an encoder with the given serialization flags.
func NewPush(flags Flag) *Push { return &Push{flags: flags} }

// Bytes returns the accumulated buffer.
func (p *Push) Bytes() []byte { return p.buf }

// Len returns the number of bytes written so far.
func (p *Push) Len() int { return len(p.buf) }

// Flags returns the encoder's serialization flags.
func (p *Push) Flags() Flag { return p.flags }

// Raw appends bytes verbatim.
func (p *Push) Raw(b []byte) { p.buf = append(p.buf, b...) }

// Uint8 appends a byte.
func (p *Push) Uint8(v uint8) { p.buf = append(p.buf, v) }

// Uint16 appends a little-endian uint16.
func (p *Push) Uint16(v uint16) { p.buf = binary.LittleEndian.AppendUint16(p.buf, v) }

// Uint32 appends a little-endian uint32.
func (p *Push) Uint32(v uint32) { p.buf = binary.LittleEndian.AppendUint32(p.buf, v) }

// Uint64 appends a little-endian uint64.
func (p *Push) Uint64(v uint64) { p.buf = binary.LittleEndian.AppendUint64(p.buf, v) }

// Float32 appends an IEEE-754 single as a little-endian uint32.
func (p *Push) Float32(v float32) { p.Uint32(math.Float32bits(v)) }

// Float64 appends an IEEE-754 double as a little-endian uint64.
func (p *Push) Float64(v float64) { p.Uint64(math.Float64bits(v)) }

// Bool appends a 1-byte boolean (0 or 1).
func (p *Push) Bool(v bool) {
	if v {
		p.Uint8(1)
	} else {
		p.Uint8(0)
	}
}

// GUID appends a 16-byte GUID.
func (p *Push) GUID(g GUID) {
	p.Uint32(g.TimeLow)
	p.Uint16(g.TimeMid)
	p.Uint16(g.TimeHiAndVersion)
	p.Raw(g.ClockSeq[:])
	p.Raw(g.Node[:])
}

// Str appends a NUL-terminated 8-bit string (UTF-8 bytes plus a 0 terminator).
func (p *Push) Str(s string) {
	p.buf = append(p.buf, s...)
	p.buf = append(p.buf, 0)
}

// WStr appends a NUL-terminated wide string: UTF-16LE plus a 0x0000 terminator
// when FlagUTF16 is set, otherwise the 8-bit form (Str).
func (p *Push) WStr(s string) {
	if p.flags&FlagUTF16 == 0 {
		p.Str(s)
		return
	}
	for _, u := range utf16.Encode([]rune(s)) {
		p.Uint16(u)
	}
	p.Uint16(0)
}

// Bin appends counted binary: a 32-bit length when FlagWCount is set else a
// 16-bit length, followed by the bytes.
func (p *Push) Bin(b []byte) {
	if p.flags&FlagWCount != 0 {
		p.Uint32(uint32(len(b)))
	} else {
		p.Uint16(uint16(len(b)))
	}
	p.Raw(b)
}

// BinS appends counted binary with an always-16-bit length (the "small" form).
func (p *Push) BinS(b []byte) {
	p.Uint16(uint16(len(b)))
	p.Raw(b)
}

// BinEx appends counted binary with an always-32-bit length (the "extended" form).
func (p *Push) BinEx(b []byte) {
	p.Uint32(uint32(len(b)))
	p.Raw(b)
}

// Pull is a little-endian cursor over a MAPI byte slice with a sticky error.
type Pull struct {
	b     []byte
	off   int
	flags Flag
	err   error
}

// NewPull returns a decoder over b with the given serialization flags.
func NewPull(b []byte, flags Flag) *Pull { return &Pull{b: b, flags: flags} }

// Err returns the first error encountered, or nil.
func (p *Pull) Err() error { return p.err }

// Fault latches a format error so a higher-level parser can reject semantically
// invalid input (for example an unknown discriminator) and stop consuming.
func (p *Pull) Fault() {
	if p.err == nil {
		p.err = ErrFormat
	}
}

// Offset returns the current read offset.
func (p *Pull) Offset() int { return p.off }

// Remaining returns the number of unread bytes.
func (p *Pull) Remaining() int { return len(p.b) - p.off }

// Flags returns the decoder's serialization flags.
func (p *Pull) Flags() Flag { return p.flags }

// ensure reports whether n more bytes are available, latching ErrTruncated otherwise.
func (p *Pull) ensure(n int) bool {
	if p.err != nil {
		return false
	}
	if n < 0 || p.off+n > len(p.b) {
		p.err = ErrTruncated
		return false
	}
	return true
}

// Uint8 reads a byte.
func (p *Pull) Uint8() uint8 {
	if !p.ensure(1) {
		return 0
	}
	v := p.b[p.off]
	p.off++
	return v
}

// Uint16 reads a little-endian uint16.
func (p *Pull) Uint16() uint16 {
	if !p.ensure(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(p.b[p.off:])
	p.off += 2
	return v
}

// Uint32 reads a little-endian uint32.
func (p *Pull) Uint32() uint32 {
	if !p.ensure(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(p.b[p.off:])
	p.off += 4
	return v
}

// Uint64 reads a little-endian uint64.
func (p *Pull) Uint64() uint64 {
	if !p.ensure(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(p.b[p.off:])
	p.off += 8
	return v
}

// Float32 reads an IEEE-754 single.
func (p *Pull) Float32() float32 { return math.Float32frombits(p.Uint32()) }

// Float64 reads an IEEE-754 double.
func (p *Pull) Float64() float64 { return math.Float64frombits(p.Uint64()) }

// Bool reads a 1-byte boolean; a value other than 0 or 1 latches ErrFormat.
func (p *Pull) Bool() bool {
	v := p.Uint8()
	switch v {
	case 0:
		return false
	case 1:
		return true
	default:
		if p.err == nil {
			p.err = ErrFormat
		}
		return false
	}
}

// Bytes reads n raw bytes (a copy).
func (p *Pull) Bytes(n int) []byte {
	if !p.ensure(n) {
		return nil
	}
	out := make([]byte, n)
	copy(out, p.b[p.off:p.off+n])
	p.off += n
	return out
}

// Skip advances the cursor by n bytes.
func (p *Pull) Skip(n int) {
	if p.ensure(n) {
		p.off += n
	}
}

// GUID reads a 16-byte GUID.
func (p *Pull) GUID() GUID {
	var g GUID
	g.TimeLow = p.Uint32()
	g.TimeMid = p.Uint16()
	g.TimeHiAndVersion = p.Uint16()
	if cs := p.Bytes(2); cs != nil {
		copy(g.ClockSeq[:], cs)
	}
	if nd := p.Bytes(6); nd != nil {
		copy(g.Node[:], nd)
	}
	return g
}

// Str reads a NUL-terminated 8-bit string (the terminator is consumed).
func (p *Pull) Str() string {
	if p.err != nil {
		return ""
	}
	for i := p.off; i < len(p.b); i++ {
		if p.b[i] == 0 {
			s := string(p.b[p.off:i])
			p.off = i + 1
			return s
		}
	}
	p.err = ErrFormat
	return ""
}

// WStr reads a NUL-terminated wide string: UTF-16LE terminated by 0x0000 when
// FlagUTF16 is set, otherwise the 8-bit form (Str).
func (p *Pull) WStr() string {
	if p.flags&FlagUTF16 == 0 {
		return p.Str()
	}
	if p.err != nil {
		return ""
	}
	for i := p.off; i+1 < len(p.b); i += 2 {
		if p.b[i] == 0 && p.b[i+1] == 0 {
			n := (i - p.off) / 2
			u := make([]uint16, n)
			for j := range n {
				u[j] = binary.LittleEndian.Uint16(p.b[p.off+j*2:])
			}
			p.off = i + 2
			return string(utf16.Decode(u))
		}
	}
	p.err = ErrFormat
	return ""
}

// Bin reads counted binary: a 32-bit length when FlagWCount is set else 16-bit,
// then that many bytes.
func (p *Pull) Bin() []byte {
	var n int
	if p.flags&FlagWCount != 0 {
		n = int(p.Uint32())
	} else {
		n = int(p.Uint16())
	}
	if n == 0 {
		return nil
	}
	return p.Bytes(n)
}

// RPCHeaderExt is the 8-byte MS-OXCRPC RPC_HEADER_EXT that frames a compressed
// or obfuscated ROP buffer.
type RPCHeaderExt struct {
	Version    uint16
	Flags      uint16
	Size       uint16 // size of the (possibly compressed) payload that follows
	SizeActual uint16 // uncompressed payload size
}

// RPC_HEADER_EXT flags (MS-OXCRPC 2.2.2.1).
const (
	RHEFlagCompressed uint16 = 0x0001 // payload is LZXPRESS-compressed
	RHEFlagXorMagic   uint16 = 0x0002 // payload is XOR-obfuscated with 0xA5
	RHEFlagLast       uint16 = 0x0004 // last RPC_HEADER_EXT in the sequence
)

// Push appends the RPC_HEADER_EXT.
func (h RPCHeaderExt) Push(p *Push) {
	p.Uint16(h.Version)
	p.Uint16(h.Flags)
	p.Uint16(h.Size)
	p.Uint16(h.SizeActual)
}

// PullRPCHeaderExt reads an RPC_HEADER_EXT.
func PullRPCHeaderExt(p *Pull) RPCHeaderExt {
	return RPCHeaderExt{
		Version:    p.Uint16(),
		Flags:      p.Uint16(),
		Size:       p.Uint16(),
		SizeActual: p.Uint16(),
	}
}
