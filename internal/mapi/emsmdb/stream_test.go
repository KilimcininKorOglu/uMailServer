package emsmdb

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestStreamWriteFlow drives RopCreateMessage -> RopOpenStream -> two RopWriteStream
// calls -> RopCommitStream -> RopSaveChangesMessage and verifies the streamed HTML
// body lands in the committed message. The discriminating check is that BOTH
// written chunks survive: a single write would pass even if the server kept only
// the last chunk or overwrote the buffer on each call, so the body is delivered in
// two chunks and both must be present in the committed RFC 5322 message.
func TestStreamWriteFlow(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store) // Inbox folder bound at handle index 1
	app, blob, _ := newWriteAppender()
	p.SetAppender(app)

	// RopCreateMessage: open a new message in the Inbox at output handle index 2.
	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	// RopSetProperties: a subject, so the committed draft carries a Subject header.
	arr := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(arr, []wire.TaggedPropertyValue{
		{Tag: wire.PidTagSubject, Value: "streamed body"},
	}); err != nil {
		t.Fatalf("push property array: %v", err)
	}
	sp := wire.NewPush(wire.FlagUTF16)
	sp.Uint16(uint16(len(arr.Bytes())))
	sp.Raw(arr.Bytes())
	_, handles = p.Dispatch(sess, ropRequest(RopSetProperties, 2, sp.Bytes()), handles, 0x10000)

	// RopOpenStream on PidTagHtml (binary), binding the stream at output handle index 3.
	os := wire.NewPush(wire.FlagUTF16)
	os.Uint8(3)                        // output handle index (the stream)
	os.Uint32(uint32(wire.PidTagHtml)) // property to stream
	os.Uint8(0x01)                     // open mode flags: create/readwrite
	resp, handles := p.Dispatch(sess, ropRequest(RopOpenStream, 2, os.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopOpenStream {
		t.Fatalf("rop id = %#x, want RopOpenStream", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("OpenStream return value = %#x, want success", rv)
	}
	if sz := q.Uint32(); sz != 0 {
		t.Errorf("new stream size = %d, want 0 (a created stream is empty)", sz)
	}

	// RopWriteStream twice: the body is delivered in two chunks. The accumulation
	// gate is that BOTH chunks survive into the committed message.
	chunkA := []byte("<p>chunk-alpha</p>")
	chunkB := []byte("<p>chunk-bravo</p>")
	for _, chunk := range [][]byte{chunkA, chunkB} {
		ws := wire.NewPush(wire.FlagUTF16)
		ws.BinS(chunk) // WriteStream data: g_sbin (u16 length + bytes)
		wresp, h := p.Dispatch(sess, ropRequest(RopWriteStream, 3, ws.Bytes()), handles, 0x10000)
		handles = h
		wq := wire.NewPull(wresp, wire.FlagUTF16)
		if got := wq.Uint8(); got != RopWriteStream {
			t.Fatalf("rop id = %#x, want RopWriteStream", got)
		}
		wq.Uint8() // handle index
		if rv := wq.Uint32(); rv != ecSuccess {
			t.Fatalf("WriteStream return value = %#x, want success", rv)
		}
		if n := wq.Uint16(); int(n) != len(chunk) {
			t.Errorf("WrittenSize = %d, want %d", n, len(chunk))
		}
	}

	// RopCommitStream flushes the accumulated bytes into the message property buffer.
	resp2, handles := p.Dispatch(sess, ropRequest(RopCommitStream, 3, nil), handles, 0x10000)
	q2 := wire.NewPull(resp2, wire.FlagUTF16)
	if got := q2.Uint8(); got != RopCommitStream {
		t.Fatalf("rop id = %#x, want RopCommitStream", got)
	}
	q2.Uint8() // handle index
	if rv := q2.Uint32(); rv != ecSuccess {
		t.Fatalf("CommitStream return value = %#x, want success", rv)
	}

	// RopSaveChangesMessage commits the message through the canonical append core.
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)
	sc.Uint8(0)
	resp3, _ := p.Dispatch(sess, ropRequest(RopSaveChangesMessage, 2, sc.Bytes()), handles, 0x10000)
	q3 := wire.NewPull(resp3, wire.FlagUTF16)
	q3.Uint8() // rop id
	q3.Uint8() // handle index
	if rv := q3.Uint32(); rv != ecSuccess {
		t.Fatalf("SaveChanges return value = %#x, want success", rv)
	}

	// The committed blob must be a valid RFC 5322 HTML message carrying both chunks.
	if len(blob.msgs) != 1 {
		t.Fatalf("blob store holds %d messages, want 1", len(blob.msgs))
	}
	m, err := mail.ReadMessage(bytes.NewReader(blob.msgs[0]))
	if err != nil {
		t.Fatalf("committed blob is not a valid RFC 5322 message: %v", err)
	}
	if ct := m.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html (the streamed HTML body)", ct)
	}
	if !bytes.Contains(blob.msgs[0], chunkA) {
		t.Error("committed body is missing the first streamed chunk")
	}
	if !bytes.Contains(blob.msgs[0], chunkB) {
		t.Error("committed body is missing the second streamed chunk")
	}
}

// TestOpenStreamRejectsNonBinary verifies a stream open on a text property is
// refused with ecNotSupported. A binary stream is the property value verbatim, but
// a text stream (PtypString/PtypString8) needs codepage-aware decoding on commit
// that is deferred; refusing the open is honest rather than mis-decoding the bytes.
func TestOpenStreamRejectsNonBinary(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, _, _ := newWriteAppender()
	p.SetAppender(app)

	// RopCreateMessage at output handle index 2.
	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	// RopOpenStream on PidTagBody (Unicode): text streaming is deferred.
	os := wire.NewPush(wire.FlagUTF16)
	os.Uint8(3)
	os.Uint32(uint32(wire.PidTagBody))
	os.Uint8(0x01)
	resp, _ := p.Dispatch(sess, ropRequest(RopOpenStream, 2, os.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecNotSupported {
		t.Errorf("OpenStream on a text property = %#x, want ecNotSupported", rv)
	}
}
