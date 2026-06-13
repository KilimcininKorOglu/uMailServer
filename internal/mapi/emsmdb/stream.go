package emsmdb

import "github.com/umailserver/umailserver/internal/mapi/wire"

// streamObject is a server object opened on a single property of an in-flight
// object (MS-OXCPRPT 2.2.14) — a message being created or an attachment being
// built. A client streams a large property value across one or more RopWriteStream
// calls and flushes it with RopCommitStream, which writes the accumulated bytes
// into the target object's property buffer for the eventual save to render (an
// HTML body into the message, attachment data into the attachment). Only a binary
// property is streamed: a binary stream is the property value verbatim, with no
// codepage conversion, so the bytes round-trip unambiguously. Streaming a text
// property (PtypString/PtypString8), which needs codepage-aware decoding on commit,
// is deferred.
type streamObject struct {
	tag    wire.PropTag
	buf    []byte
	target propWriter
}

// ropOpenStream handles RopOpenStream (MS-OXCPRPT 2.2.14): it opens a stream on a
// property of the in-flight message named by the request handle and binds it to
// the output handle index. The stream is created empty for a full rewrite of the
// property; seek/partial-overwrite and streaming a stored property back to the
// client are deferred (the property ROPs already serve stored bodies, and a
// read-opened message carries no write buffer, so its stream open is rejected).
// Only a binary property can be streamed, since a binary stream is the value
// verbatim; opening a stream on any other property type reports it unsupported.
// The response reports the current stream size, which is 0 for the new empty
// stream.
func ropOpenStream(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	proptag := wire.PropTag(c.in.Uint32())
	_ = c.in.Uint8() // open mode flags: the stream is opened to create/rewrite the value
	if c.in.Err() != nil {
		writeRopError(c.out, RopOpenStream, ohindex, ecError)
		return
	}
	target, ok := c.objectAt(hindex).(propWriter)
	if !ok || target.writeProps() == nil {
		writeRopError(c.out, RopOpenStream, ohindex, ecNullObject)
		return
	}
	if proptag.Type() != wire.PtBinary {
		// A binary stream is the property value verbatim. A text stream
		// (PtypString/PtypString8) needs codepage-aware decoding on commit, which is
		// deferred; reject it rather than mis-decode the bytes.
		writeRopError(c.out, RopOpenStream, ohindex, ecNotSupported)
		return
	}
	c.setHandle(ohindex, c.state.alloc(&streamObject{tag: proptag, target: target}))

	out := c.out
	out.Uint8(RopOpenStream)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint32(0) // stream size: a newly created stream is empty
}

// ropWriteStream handles RopWriteStream (MS-OXCPRPT 2.2.16): it appends the
// request's bytes to the open stream's buffer, accumulating content across
// successive calls so a value larger than a single ROP can be delivered in chunks.
// The request binary is u16-length-prefixed, so a chunk is at most 65535 bytes and
// the written count fits the u16 response field. The response reports the number
// of bytes written.
func ropWriteStream(c *ropCtx, _ uint8, hindex uint8) {
	data := c.in.Bin()
	if c.in.Err() != nil {
		writeRopError(c.out, RopWriteStream, hindex, ecError)
		return
	}
	so, ok := c.objectAt(hindex).(*streamObject)
	if !ok {
		writeRopError(c.out, RopWriteStream, hindex, ecNullObject)
		return
	}
	so.buf = append(so.buf, data...)

	out := c.out
	out.Uint8(RopWriteStream)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(uint16(len(data))) // WrittenSize: the whole chunk was buffered
}

// ropCommitStream handles RopCommitStream (MS-OXCPRPT 2.2.20): it flushes the
// stream's accumulated bytes into its message's property buffer, so the streamed
// value is rendered into the message when RopSaveChangesMessage runs. The target
// message must still be in-flight (a saved message's write buffer is cleared). The
// response carries no body (MS-OXCROPS 2.2.9.4).
func ropCommitStream(c *ropCtx, _ uint8, hindex uint8) {
	so, ok := c.objectAt(hindex).(*streamObject)
	if !ok {
		writeRopError(c.out, RopCommitStream, hindex, ecNullObject)
		return
	}
	props := so.target.writeProps()
	if props == nil {
		writeRopError(c.out, RopCommitStream, hindex, ecNullObject)
		return
	}
	props[so.tag] = append([]byte(nil), so.buf...)

	out := c.out
	out.Uint8(RopCommitStream)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
}
