// Package ndr implements the minimal subset of the DCE/RPC Network Data
// Representation transfer syntax (C706, NDR32) required to marshal the EMSMDB
// RPC interface (EcDoConnectEx, EcDoRpcExt2, EcDoDisconnect) carried over
// RPC-over-HTTP (MS-RPCH).
//
// Only NDR32, little-endian is supported: that is the transfer syntax the
// EMSMDB endpoint negotiates over MS-RPCH. The codec implements primitive
// alignment (a value of size N advances the stream offset to a multiple of N,
// padding with zero bytes, per C706 §14.2.2), opaque octet copies, GUIDs, RPC
// context handles, and unique (referent-id) pointers — exactly the features the
// three EMSMDB operations use. Counted arrays (conformant and conformant
// varying) are composed by callers from these primitives so the asymmetric
// wire layout of [in] versus [out] byte buffers stays explicit at the call
// site. This is deliberately not a general NDR engine.
package ndr

import (
	"encoding/binary"
	"errors"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// ErrTruncated is latched when a read runs past the end of the buffer.
var ErrTruncated = errors.New("ndr: buffer truncated")

// ErrFormat is latched when decoded data violates an NDR structural invariant.
var ErrFormat = errors.New("ndr: malformed data")

// ContextHandle is a DCE/RPC context handle (C706 §6.2.2): a 4-byte handle
// type followed by a 16-byte GUID. EMSMDB returns one from EcDoConnectEx; the
// client echoes it on EcDoRpcExt2 and EcDoDisconnect.
type ContextHandle struct {
	HandleType uint32
	GUID       wire.GUID
}

// Push is an NDR32 little-endian encoder. Scalar writes self-align the stream
// offset to the value size (2, 4 or 8), padding with zero bytes.
type Push struct {
	buf      []byte
	ptrCount uint32
}

// NewPush returns an empty encoder.
func NewPush() *Push { return &Push{} }

// Bytes returns the accumulated buffer.
func (p *Push) Bytes() []byte { return p.buf }

// Len returns the current stream offset.
func (p *Push) Len() int { return len(p.buf) }

// align pads the stream with zero bytes until the offset is a multiple of n.
func (p *Push) align(n int) {
	for len(p.buf)%n != 0 {
		p.buf = append(p.buf, 0)
	}
}

// Uint8 writes an unaligned byte.
func (p *Push) Uint8(v uint8) { p.buf = append(p.buf, v) }

// Uint16 writes a 2-aligned little-endian uint16.
func (p *Push) Uint16(v uint16) {
	p.align(2)
	p.buf = binary.LittleEndian.AppendUint16(p.buf, v)
}

// Uint32 writes a 4-aligned little-endian uint32.
func (p *Push) Uint32(v uint32) {
	p.align(4)
	p.buf = binary.LittleEndian.AppendUint32(p.buf, v)
}

// Uint64 writes an 8-aligned little-endian uint64.
func (p *Push) Uint64(v uint64) {
	p.align(8)
	p.buf = binary.LittleEndian.AppendUint64(p.buf, v)
}

// ULong writes an NDR `unsigned long`, encoded as a uint32 under NDR32.
func (p *Push) ULong(v uint32) { p.Uint32(v) }

// Raw appends opaque octets verbatim, with no alignment. This is how counted
// array payloads and embedded flat buffers (for example a ROP buffer) are
// written without NDR alignment leaking into their interior.
func (p *Push) Raw(b []byte) { p.buf = append(p.buf, b...) }

// GUID writes a 4-aligned GUID in NDR field order.
func (p *Push) GUID(g wire.GUID) {
	p.align(4)
	p.Uint32(g.TimeLow)
	p.Uint16(g.TimeMid)
	p.Uint16(g.TimeHiAndVersion)
	p.Raw(g.ClockSeq[:])
	p.Raw(g.Node[:])
}

// CtxHandle writes a context handle (4-byte type then GUID).
func (p *Push) CtxHandle(h ContextHandle) {
	p.align(4)
	p.Uint32(h.HandleType)
	p.GUID(h.GUID)
}

// UniquePtr writes a unique-pointer referent id: a non-zero token when present,
// zero when absent. The token form mirrors C706 referent-id assignment so a
// captured trace lines up with reference encoders.
func (p *Push) UniquePtr(present bool) {
	if !present {
		p.ULong(0)
		return
	}
	ptr := p.ptrCount*4 | 0x00020000
	p.ptrCount++
	p.ULong(ptr)
}

// Align pads the stream with zero bytes to the next n-byte boundary. DCERPC
// payloads use it explicitly to place 8-aligned request/response stub data and
// 4-aligned result lists relative to the start of the PDU.
func (p *Push) Align(n int) { p.align(n) }

// Pull is an NDR32 little-endian decoder. Scalar reads self-align the stream
// offset to the value size before reading, mirroring Push.
type Pull struct {
	b   []byte
	off int
	err error
}

// NewPull returns a decoder over b.
func NewPull(b []byte) *Pull { return &Pull{b: b} }

// Err returns the first error encountered, or nil.
func (p *Pull) Err() error { return p.err }

// Offset returns the current read offset.
func (p *Pull) Offset() int { return p.off }

// Remaining returns the number of unread bytes.
func (p *Pull) Remaining() int { return len(p.b) - p.off }

// Align advances the offset to the next n-byte boundary, skipping padding.
func (p *Pull) Align(n int) { p.align(n) }

// Fault latches ErrFormat so a higher-level parser can reject structurally
// invalid input (for example a fragment length smaller than its header) and
// stop consuming.
func (p *Pull) Fault() {
	if p.err == nil {
		p.err = ErrFormat
	}
}

// align advances the offset to the next multiple of n, latching ErrTruncated if
// that runs past the end of the buffer.
func (p *Pull) align(n int) {
	if p.err != nil {
		return
	}
	if rem := p.off % n; rem != 0 {
		p.off += n - rem
	}
	if p.off > len(p.b) {
		p.err = ErrTruncated
	}
}

// ensure reports whether n more bytes are available, latching ErrTruncated
// otherwise.
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

// Uint8 reads an unaligned byte.
func (p *Pull) Uint8() uint8 {
	if !p.ensure(1) {
		return 0
	}
	v := p.b[p.off]
	p.off++
	return v
}

// Uint16 reads a 2-aligned little-endian uint16.
func (p *Pull) Uint16() uint16 {
	p.align(2)
	if !p.ensure(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(p.b[p.off:])
	p.off += 2
	return v
}

// Uint32 reads a 4-aligned little-endian uint32.
func (p *Pull) Uint32() uint32 {
	p.align(4)
	if !p.ensure(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(p.b[p.off:])
	p.off += 4
	return v
}

// Uint64 reads an 8-aligned little-endian uint64.
func (p *Pull) Uint64() uint64 {
	p.align(8)
	if !p.ensure(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(p.b[p.off:])
	p.off += 8
	return v
}

// ULong reads an NDR `unsigned long`, decoded as a uint32 under NDR32.
func (p *Pull) ULong() uint32 { return p.Uint32() }

// Bytes reads n opaque octets verbatim, with no alignment.
func (p *Pull) Bytes(n int) []byte {
	if !p.ensure(n) {
		return nil
	}
	b := make([]byte, n)
	copy(b, p.b[p.off:p.off+n])
	p.off += n
	return b
}

// Str reads n opaque octets and returns them as a string.
func (p *Pull) Str(n int) string {
	b := p.Bytes(n)
	if b == nil {
		return ""
	}
	return string(b)
}

// GUID reads a 4-aligned GUID in NDR field order.
func (p *Pull) GUID() wire.GUID {
	p.align(4)
	var g wire.GUID
	g.TimeLow = p.Uint32()
	g.TimeMid = p.Uint16()
	g.TimeHiAndVersion = p.Uint16()
	copy(g.ClockSeq[:], p.Bytes(2))
	copy(g.Node[:], p.Bytes(6))
	return g
}

// CtxHandle reads a context handle (4-byte type then GUID).
func (p *Pull) CtxHandle() ContextHandle {
	p.align(4)
	var h ContextHandle
	h.HandleType = p.Uint32()
	h.GUID = p.GUID()
	return h
}
