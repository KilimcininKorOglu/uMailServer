package emsmdb

import (
	"bytes"
	"net/mail"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// putBuffer drives one RopFastTransferDestinationPutBuffer of the given chunk at the
// upload-context handle index and asserts success, returning the updated handle table.
func putBuffer(t *testing.T, p *Processor, sess *Session, handles []uint32, hindex uint8, chunk []byte) []uint32 {
	t.Helper()
	body := wire.NewPush(0)
	body.Uint16(uint16(len(chunk))) // transfer_data: a u16-counted binary
	body.Raw(chunk)
	resp, handles := p.Dispatch(sess, ropRequest(RopFastTransferDestPutBuffer, hindex, body.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, 0)
	if got := q.Uint8(); got != RopFastTransferDestPutBuffer {
		t.Fatalf("rop id = %#x, want RopFastTransferDestPutBuffer", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("PutBuffer return value = %#x, want success", rv)
	}
	q.Uint16()                   // transfer_status
	q.Uint16()                   // in_progress_count
	q.Uint16()                   // total_step_count
	q.Uint8()                    // reserved
	if used := q.Uint16(); int(used) != len(chunk) {
		t.Errorf("PutBuffer used_size = %d, want %d (the whole chunk)", used, len(chunk))
	}
	return handles
}

// TestFastTransferDestinationUploadFlow drives RopCreateMessage ->
// RopFastTransferDestinationConfigure -> repeated RopFastTransferDestinationPutBuffer
// (a message-content property stream pushed in deliberately tiny chunks so values
// straddle chunk boundaries) -> RopSaveChangesMessage, and verifies the uploaded
// subject and body land in the committed RFC 5322 message — the cross-surface gate that
// the parsed FastTransfer properties reach the same write/save path RopSetProperties
// fills.
func TestFastTransferDestinationUploadFlow(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store) // folder at handle index 1
	app, blob, _ := newWriteAppender()
	p.SetAppender(app)

	// CreateMessage in the Inbox, binding the message at output handle index 2.
	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	// DestinationConfigure(COPYTO) on the message (handle 2) -> upload context at handle 3.
	dc := wire.NewPush(0)
	dc.Uint8(3)                  // output handle index
	dc.Uint8(fastSourceOpCopyTo) // source operation
	dc.Uint8(0)                  // copy flags
	resp, handles := p.Dispatch(sess, ropRequest(RopFastTransferDestConfigure, 2, dc.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, 0)
	if got := q.Uint8(); got != RopFastTransferDestConfigure {
		t.Fatalf("rop id = %#x, want RopFastTransferDestConfigure", got)
	}
	q.Uint8()
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("DestConfigure return value = %#x, want success", rv)
	}

	// Build the message-content stream: a bare FastTransfer property list (subject + body).
	stream := wire.NewPush(0)
	if err := wire.PushFastTransferPropval(stream, wire.PidTagSubject, "uploaded subject"); err != nil {
		t.Fatalf("push subject: %v", err)
	}
	if err := wire.PushFastTransferPropval(stream, wire.PidTagBody, "uploaded body via fasttransfer"); err != nil {
		t.Fatalf("push body: %v", err)
	}
	streamBytes := stream.Bytes()

	// Push in 5-byte chunks so most property values straddle a PutBuffer boundary,
	// exercising the upload context's partial-element buffering.
	for off := 0; off < len(streamBytes); off += 5 {
		handles = putBuffer(t, p, sess, handles, 3, streamBytes[off:min(off+5, len(streamBytes))])
	}

	// SaveChangesMessage on the message handle commits the uploaded properties.
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)
	sc.Uint8(0)
	resp2, _ := p.Dispatch(sess, ropRequest(RopSaveChangesMessage, 2, sc.Bytes()), handles, 0x10000)
	q2 := wire.NewPull(resp2, wire.FlagUTF16)
	q2.Uint8()
	q2.Uint8()
	if rv := q2.Uint32(); rv != ecSuccess {
		t.Fatalf("SaveChanges return value = %#x, want success", rv)
	}

	if len(blob.msgs) != 1 {
		t.Fatalf("blob store holds %d messages, want 1", len(blob.msgs))
	}
	m, err := mail.ReadMessage(bytes.NewReader(blob.msgs[0]))
	if err != nil {
		t.Fatalf("committed blob is not a valid RFC 5322 message: %v", err)
	}
	if subj := m.Header.Get("Subject"); subj != "uploaded subject" {
		t.Errorf("Subject = %q, want %q (the uploaded subject must reach the message)", subj, "uploaded subject")
	}
	if !bytes.Contains(blob.msgs[0], []byte("uploaded body via fasttransfer")) {
		t.Error("the uploaded body did not reach the committed message")
	}
}

// TestFastTransferDestPutBufferRejectsMarker verifies an unsupported structural marker
// in the upload stream is reported as an error rather than silently dropped — landing a
// partial message — since recipient/attachment sub-streams are not yet applied.
func TestFastTransferDestPutBufferRejectsMarker(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, _, _ := newWriteAppender()
	p.SetAppender(app)

	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	dc := wire.NewPush(0)
	dc.Uint8(3)
	dc.Uint8(fastSourceOpCopyTo)
	dc.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopFastTransferDestConfigure, 2, dc.Bytes()), handles, 0x10000)

	// A stream that opens a recipient sub-structure (STARTRECIP), which is not supported.
	stream := wire.NewPush(0)
	stream.Uint32(wire.FXStartRecip)
	body := wire.NewPush(0)
	body.Uint16(uint16(len(stream.Bytes())))
	body.Raw(stream.Bytes())
	resp, _ := p.Dispatch(sess, ropRequest(RopFastTransferDestPutBuffer, 3, body.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, 0)
	q.Uint8()
	q.Uint8()
	if rv := q.Uint32(); rv == ecSuccess {
		t.Error("PutBuffer accepted an unsupported marker, want an error")
	}
}

// TestFastTransferDestConfigureRejectsUnsupportedOp verifies a folder-level source
// operation (a message list or whole folder) is rejected, since only single-object
// content uploads are supported.
func TestFastTransferDestConfigureRejectsUnsupportedOp(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, _, _ := newWriteAppender()
	p.SetAppender(app)

	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	dc := wire.NewPush(0)
	dc.Uint8(3)
	dc.Uint8(0x03) // FAST_SOURCE_OPERATION_COPYMESSAGES: a folder-level upload
	dc.Uint8(0)
	resp, _ := p.Dispatch(sess, ropRequest(RopFastTransferDestConfigure, 2, dc.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, 0)
	q.Uint8()
	q.Uint8()
	if rv := q.Uint32(); rv != ecNotSupported {
		t.Errorf("DestConfigure with a folder-level op = %#x, want ecNotSupported", rv)
	}
}
