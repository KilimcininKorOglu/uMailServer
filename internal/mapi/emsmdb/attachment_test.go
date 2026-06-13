package emsmdb

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// TestCreateAttachmentFlow drives RopCreateMessage -> RopSetProperties (message) ->
// RopCreateAttachment -> RopSetProperties (attachment) -> RopOpenStream +
// 2x RopWriteStream + RopCommitStream (attachment data) -> RopSaveChangesAttachment
// -> RopSaveChangesMessage and verifies the committed message is multipart/mixed
// carrying the attachment. The discriminating check is the attachment's DECODED
// content: both streamed chunks must reassemble into the part's bytes, not just the
// filename — a filename-only assertion passes with empty or wrong data, and a single
// write would pass even if the server kept only the last chunk.
func TestCreateAttachmentFlow(t *testing.T) {
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

	// RopSetProperties on the message: a subject and a plain body.
	marr := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(marr, []wire.TaggedPropertyValue{
		{Tag: wire.PidTagSubject, Value: "with attachment"},
		{Tag: wire.PidTagBody, Value: "see the attached file"},
	}); err != nil {
		t.Fatalf("push message props: %v", err)
	}
	msp := wire.NewPush(wire.FlagUTF16)
	msp.Uint16(uint16(len(marr.Bytes())))
	msp.Raw(marr.Bytes())
	_, handles = p.Dispatch(sess, ropRequest(RopSetProperties, 2, msp.Bytes()), handles, 0x10000)

	// RopCreateAttachment on the message, binding the attachment at output handle 3.
	ca := wire.NewPush(wire.FlagUTF16)
	ca.Uint8(3) // output handle index
	resp, handles := p.Dispatch(sess, ropRequest(RopCreateAttachment, 2, ca.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopCreateAttachment {
		t.Fatalf("rop id = %#x, want RopCreateAttachment", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("CreateAttachment return value = %#x, want success", rv)
	}
	if id := q.Uint32(); id != 0 {
		t.Errorf("first attachment id = %d, want 0", id)
	}

	// RopSetProperties on the attachment: filename, MIME type, by-value method.
	aarr := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(aarr, []wire.TaggedPropertyValue{
		{Tag: wire.PidTagAttachLongFilename, Value: "report.txt"},
		{Tag: wire.PidTagAttachMimeTag, Value: "text/plain"},
		{Tag: wire.PidTagAttachMethod, Value: uint32(1)}, // ATTACH_BY_VALUE
	}); err != nil {
		t.Fatalf("push attachment props: %v", err)
	}
	asp := wire.NewPush(wire.FlagUTF16)
	asp.Uint16(uint16(len(aarr.Bytes())))
	asp.Raw(aarr.Bytes())
	_, handles = p.Dispatch(sess, ropRequest(RopSetProperties, 3, asp.Bytes()), handles, 0x10000)

	// RopOpenStream on the attachment's PidTagAttachDataBinary at output handle 4.
	os := wire.NewPush(wire.FlagUTF16)
	os.Uint8(4)
	os.Uint32(uint32(wire.PidTagAttachDataBinary))
	os.Uint8(0x01)
	resp2, handles := p.Dispatch(sess, ropRequest(RopOpenStream, 3, os.Bytes()), handles, 0x10000)
	q2 := wire.NewPull(resp2, wire.FlagUTF16)
	if got := q2.Uint8(); got != RopOpenStream {
		t.Fatalf("rop id = %#x, want RopOpenStream", got)
	}
	q2.Uint8() // handle index
	if rv := q2.Uint32(); rv != ecSuccess {
		t.Fatalf("OpenStream(attachment data) return value = %#x, want success", rv)
	}

	// RopWriteStream twice: the attachment content delivered in two chunks.
	chunkA := []byte("ATTACH-DATA-PART-A-")
	chunkB := []byte("PART-B-END")
	for _, chunk := range [][]byte{chunkA, chunkB} {
		ws := wire.NewPush(wire.FlagUTF16)
		ws.BinS(chunk)
		wresp, h := p.Dispatch(sess, ropRequest(RopWriteStream, 4, ws.Bytes()), handles, 0x10000)
		handles = h
		wq := wire.NewPull(wresp, wire.FlagUTF16)
		wq.Uint8() // rop id
		wq.Uint8() // handle index
		if rv := wq.Uint32(); rv != ecSuccess {
			t.Fatalf("WriteStream return value = %#x, want success", rv)
		}
	}

	// RopCommitStream flushes the data into the attachment's property bag.
	resp3, handles := p.Dispatch(sess, ropRequest(RopCommitStream, 4, nil), handles, 0x10000)
	q3 := wire.NewPull(resp3, wire.FlagUTF16)
	q3.Uint8() // rop id
	q3.Uint8() // handle index
	if rv := q3.Uint32(); rv != ecSuccess {
		t.Fatalf("CommitStream return value = %#x, want success", rv)
	}

	// RopSaveChangesAttachment records the attachment on the in-flight message.
	sa := wire.NewPush(wire.FlagUTF16)
	sa.Uint8(3) // response handle index
	sa.Uint8(0) // save flags
	resp4, handles := p.Dispatch(sess, ropRequest(RopSaveChangesAttachment, 3, sa.Bytes()), handles, 0x10000)
	q4 := wire.NewPull(resp4, wire.FlagUTF16)
	if got := q4.Uint8(); got != RopSaveChangesAttachment {
		t.Fatalf("rop id = %#x, want RopSaveChangesAttachment", got)
	}
	q4.Uint8() // handle index
	if rv := q4.Uint32(); rv != ecSuccess {
		t.Fatalf("SaveChangesAttachment return value = %#x, want success", rv)
	}

	// RopSaveChangesMessage commits the message together with its attachment.
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)
	sc.Uint8(0)
	resp5, _ := p.Dispatch(sess, ropRequest(RopSaveChangesMessage, 2, sc.Bytes()), handles, 0x10000)
	q5 := wire.NewPull(resp5, wire.FlagUTF16)
	q5.Uint8() // rop id
	q5.Uint8() // handle index
	if rv := q5.Uint32(); rv != ecSuccess {
		t.Fatalf("SaveChangesMessage return value = %#x, want success", rv)
	}

	// The committed message must be multipart/mixed carrying the attachment, whose
	// decoded content is BOTH streamed chunks reassembled in order.
	if len(blob.msgs) != 1 {
		t.Fatalf("blob store holds %d messages, want 1", len(blob.msgs))
	}
	gotName, gotData := extractSingleAttachment(t, blob.msgs[0])
	if gotName != "report.txt" {
		t.Errorf("attachment filename = %q, want report.txt", gotName)
	}
	want := append(append([]byte(nil), chunkA...), chunkB...)
	if !bytes.Equal(gotData, want) {
		t.Errorf("attachment data = %q, want both streamed chunks %q", gotData, want)
	}
}

// TestCreateAttachmentDistinctIds verifies two attachments created before either is
// saved get distinct ids. A len(attachments)-based id would collide here (the list
// is still empty until the first save), so distinct ids prove the per-message
// monotonic counter.
func TestCreateAttachmentDistinctIds(t *testing.T) {
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

	ids := make([]uint32, 0, 2)
	for _, ohindex := range []uint8{3, 4} {
		ca := wire.NewPush(wire.FlagUTF16)
		ca.Uint8(ohindex)
		resp, h := p.Dispatch(sess, ropRequest(RopCreateAttachment, 2, ca.Bytes()), handles, 0x10000)
		handles = h
		q := wire.NewPull(resp, wire.FlagUTF16)
		q.Uint8() // rop id
		q.Uint8() // handle index
		if rv := q.Uint32(); rv != ecSuccess {
			t.Fatalf("CreateAttachment return value = %#x, want success", rv)
		}
		ids = append(ids, q.Uint32())
	}
	if ids[0] == ids[1] {
		t.Errorf("two attachments share id %d; ids must be distinct (monotonic, not len-based)", ids[0])
	}
}

// extractSingleAttachment parses a committed RFC 5322 message, asserts it is a
// multipart message, and returns the filename and DECODED content of its single
// attachment part (the part carrying a filename), base64-decoding the body.
func extractSingleAttachment(t *testing.T, raw []byte) (string, []byte) {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("committed blob is not a valid RFC 5322 message: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse Content-Type: %v", err)
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		t.Fatalf("Content-Type = %q, want multipart/* for a message with an attachment", mediaType)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			t.Fatalf("read multipart: %v", perr)
		}
		if part.FileName() == "" {
			continue // the body part, not the attachment
		}
		body, rerr := io.ReadAll(part)
		if rerr != nil {
			t.Fatalf("read attachment part: %v", rerr)
		}
		if part.Header.Get("Content-Transfer-Encoding") != "base64" {
			return part.FileName(), body
		}
		stripped := strings.NewReplacer("\r", "", "\n", "").Replace(string(body))
		decoded, derr := base64.StdEncoding.DecodeString(stripped)
		if derr != nil {
			t.Fatalf("decode base64 attachment: %v", derr)
		}
		return part.FileName(), decoded
	}
	t.Fatal("no attachment part found in the committed message")
	return "", nil
}
