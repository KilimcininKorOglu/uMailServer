package emsmdb

import (
	"errors"
	"fmt"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// FastTransfer destination source operations (MS-OXCFXICS 2.2.3.1.2.1.1, the
// RopFastTransferDestinationConfigure SourceOperation byte). Only the two that upload
// a single object's content (the message-content path) are named.
const (
	fastSourceOpCopyTo         uint8 = 0x01
	fastSourceOpCopyProperties uint8 = 0x02
)

// errUploadTargetClosed reports that a FastTransfer upload's target object is no longer
// writable (it was saved or released mid-upload), so its property bag cannot receive
// the streamed values.
var errUploadTargetClosed = errors.New("emsmdb: fasttransfer upload target is not writable")

// fastUploadContext is a server object bound by RopFastTransferDestinationConfigure: it
// receives a FastTransfer stream uploaded in RopFastTransferDestinationPutBuffer chunks,
// parses each complete element, and applies the message-content property values to the
// bound target's in-flight property bag — the same bag RopSetProperties fills, so an
// upload converges on the existing message write/save path. The buffer holds bytes that
// do not yet form a complete element (a value split across two PutBuffer chunks) until
// the next chunk completes it.
type fastUploadContext struct {
	target propWriter
	buf    []byte
}

// write appends a PutBuffer chunk and applies every complete FastTransfer element it now
// forms. A property element sets its value in the target's property bag; a structural
// marker (a recipient or attachment sub-stream) is not yet applied and is rejected
// rather than silently dropped, which would land a partial message. A value split across
// chunks (a truncated read) is left buffered for the next chunk.
func (uc *fastUploadContext) write(chunk []byte) error {
	uc.buf = append(uc.buf, chunk...)
	for len(uc.buf) > 0 {
		p := wire.NewPull(uc.buf, 0)
		el, err := wire.PullFastTransferElement(p)
		if errors.Is(err, wire.ErrTruncated) {
			return nil // a partial element at the tail; wait for the next chunk
		}
		if err != nil {
			return err
		}
		uc.buf = uc.buf[p.Offset():]
		if el.Marker != 0 {
			// MESSAGECONTENT is a bare property list; the recipient (STARTRECIP) and
			// attachment (NEWATTACH) sub-streams are a later refinement.
			return fmt.Errorf("emsmdb: fasttransfer upload marker %#x not supported", el.Marker)
		}
		props := uc.target.writeProps()
		if props == nil {
			return errUploadTargetClosed
		}
		props[el.Tag] = el.Value
	}
	return nil
}

// ropFastTransferDestConfigure handles RopFastTransferDestinationConfigure (MS-OXCFXICS
// 2.2.3.1.2.1; MS-OXCROPS 2.2.12.1.1): it binds a FastTransfer upload context to the
// output handle so RopFastTransferDestinationPutBuffer can stream content into the object
// at the input handle. Only the COPYTO/COPYPROPERTIES operations on a writable object
// (a message or attachment opened for creation, whose content is a property list) are
// supported; folder-level uploads (a message list or a whole folder) are a later
// refinement. The response carries no body; the upload context lives at the output
// handle.
func ropFastTransferDestConfigure(c *ropCtx, _ uint8, hindex uint8) {
	ohindex := c.in.Uint8()
	sourceOp := c.in.Uint8()
	_ = c.in.Uint8() // copy flags: only FAST_DEST_CONFIG_FLAG_MOVE is defined; not used here
	if c.in.Err() != nil {
		writeRopError(c.out, RopFastTransferDestConfigure, ohindex, ecError)
		return
	}
	if sourceOp != fastSourceOpCopyTo && sourceOp != fastSourceOpCopyProperties {
		writeRopError(c.out, RopFastTransferDestConfigure, ohindex, ecNotSupported)
		return
	}
	pw, ok := c.objectAt(hindex).(propWriter)
	if !ok || pw.writeProps() == nil {
		writeRopError(c.out, RopFastTransferDestConfigure, ohindex, ecNullObject)
		return
	}
	c.setHandle(ohindex, c.state.alloc(&fastUploadContext{target: pw}))

	out := c.out
	out.Uint8(RopFastTransferDestConfigure)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
}

// ropFastTransferDestPutBuffer handles RopFastTransferDestinationPutBuffer (MS-OXCFXICS
// 2.2.3.1.2.2; MS-OXCROPS 2.2.12.2): it feeds one chunk of an uploaded FastTransfer
// stream (a u16-counted binary) into the upload context, which parses and applies it.
// The response reports the transfer status, the progress counters, and the number of
// bytes consumed from the chunk.
func ropFastTransferDestPutBuffer(c *ropCtx, _ uint8, hindex uint8) {
	n := int(c.in.Uint16()) // transfer_data: a u16-counted binary
	data := c.in.Bytes(n)
	if c.in.Err() != nil {
		writeRopError(c.out, RopFastTransferDestPutBuffer, hindex, ecError)
		return
	}
	uc, ok := c.objectAt(hindex).(*fastUploadContext)
	if !ok {
		writeRopError(c.out, RopFastTransferDestPutBuffer, hindex, ecNullObject)
		return
	}
	if err := uc.write(data); err != nil {
		writeRopError(c.out, RopFastTransferDestPutBuffer, hindex, ecError)
		return
	}

	out := c.out
	out.Uint8(RopFastTransferDestPutBuffer)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	out.Uint16(0)         // transfer_status: a successful put reports no error and no remaining step
	out.Uint16(0)         // in_progress_count
	out.Uint16(1)         // total_step_count
	out.Uint8(0)          // reserved
	out.Uint16(uint16(n)) // used_size: the whole chunk was consumed
}
