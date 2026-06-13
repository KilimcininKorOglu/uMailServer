package emsmdb

import (
	"bytes"
	"fmt"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// fakeSubmitter records a submission so a test can assert the delivery envelope
// (including the Bcc recipients) and the exact RFC 5322 body handed to the shared
// canonical submission path RopSubmitMessage routes through.
type fakeSubmitter struct {
	from   string
	to     []string
	raw    []byte
	err    error
	called int
}

func (f *fakeSubmitter) submit(from string, to []string, raw []byte) error {
	f.called++
	f.from = from
	f.to = append([]string(nil), to...)
	f.raw = append([]byte(nil), raw...)
	return f.err
}

// fakeBodyStore serves the bytes the append blob captured back under the key the
// blob assigned, so RopSubmitMessage reads the exact stored (Bcc-free) MIME the
// save committed — the same blob/key indirection msgStore provides in production.
type fakeBodyStore struct{ blob *fakeAppendBlob }

func (f fakeBodyStore) ReadMessage(_ string, id string) ([]byte, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "blob-"))
	if err != nil || n < 1 || n > len(f.blob.msgs) {
		return nil, fmt.Errorf("blob %q not found", id)
	}
	return f.blob.msgs[n-1], nil
}

// testRecipient is one recipient to compose a draft with in the submit tests.
type testRecipient struct {
	kind  uint8
	email string
	name  string
}

// fixedRecipientRow encodes a RECIPIENT_ROW carrying the address and display name
// in the fixed EMAIL/DISPLAY fields (no property columns), the simplest shape
// RopModifyRecipients accepts.
func fixedRecipientRow(email, name string) []byte {
	row := wire.NewPush(wire.FlagUTF16)
	row.Uint16(recipFlagUnicode | recipFlagEmail | recipFlagDisplay)
	row.WStr(email)
	row.WStr(name)
	row.Uint16(0)   // RecipientColumnCount: the address is in the fixed fields
	row.Uint8(0x00) // empty property row
	return row.Bytes()
}

// composeAndSaveDraft drives CreateMessage -> SetProperties -> ModifyRecipients ->
// SaveChangesMessage for a message in the Inbox at handle index 2 with the given
// recipients, returning the updated handle table. It is the common prelude that
// leaves a saved message bound at handle 2 ready for RopSubmitMessage.
func composeAndSaveDraft(t *testing.T, p *Processor, sess *Session, handles []uint32, recipients []testRecipient) []uint32 {
	t.Helper()

	// CreateMessage at output handle index 2.
	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	// SetProperties: a subject and a plain-text body so the message is non-empty.
	arr := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(arr, []wire.TaggedPropertyValue{
		{Tag: wire.PidTagSubject, Value: "submit subject"},
		{Tag: wire.PidTagBody, Value: "submit body text"},
	}); err != nil {
		t.Fatalf("push property array: %v", err)
	}
	sp := wire.NewPush(wire.FlagUTF16)
	sp.Uint16(uint16(len(arr.Bytes())))
	sp.Raw(arr.Bytes())
	_, handles = p.Dispatch(sess, ropRequest(RopSetProperties, 2, sp.Bytes()), handles, 0x10000)

	// ModifyRecipients, when the draft has any.
	if len(recipients) > 0 {
		cols := []wire.PropTag{wire.PidTagSmtpAddress, wire.PidTagDisplayName}
		mr := wire.NewPush(wire.FlagUTF16)
		wire.PushPropertyTagArray(mr, cols)
		mr.Uint16(uint16(len(recipients)))
		for i, r := range recipients {
			rb := fixedRecipientRow(r.email, r.name)
			mr.Uint32(uint32(i + 1))
			mr.Uint8(r.kind)
			mr.Uint16(uint16(len(rb)))
			mr.Raw(rb)
		}
		_, handles = p.Dispatch(sess, ropRequest(RopModifyRecipients, 2, mr.Bytes()), handles, 0x10000)
	}

	// SaveChangesMessage commits the draft and assigns its uid, blob key, and envelope.
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)
	sc.Uint8(0)
	resp, handles := p.Dispatch(sess, ropRequest(RopSaveChangesMessage, 2, sc.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("SaveChangesMessage in submit prelude = %#x, want success", rv)
	}
	return handles
}

// submitAt drives RopSubmitMessage on the message at the handle index and returns
// the parsed result code and the remaining response bytes (which must be zero: the
// ROP has no response body).
func submitAt(t *testing.T, p *Processor, sess *Session, handles []uint32, hindex uint8) (uint32, int) {
	t.Helper()
	sm := wire.NewPush(wire.FlagUTF16)
	sm.Uint8(0) // submit flags
	resp, _ := p.Dispatch(sess, ropRequest(RopSubmitMessage, hindex, sm.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopSubmitMessage {
		t.Fatalf("rop id = %#x, want RopSubmitMessage", got)
	}
	q.Uint8() // handle index
	rv := q.Uint32()
	return rv, q.Remaining()
}

// TestSubmitMessageFlow is the cross-surface submit gate: it composes a draft
// addressed To one recipient, Cc a second, and Bcc a third, saves it, then submits
// it, and verifies the shared submission path received EVERY recipient as the
// delivery envelope — including the Bcc — while the message body it carries leaks
// none of them. A Bcc recipient must be delivered to (envelope) yet never appear in
// the copy the To/Cc recipients receive (headers); this single test fails if either
// half regresses.
func TestSubmitMessageFlow(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, blob, _ := newWriteAppender()
	p.SetAppender(app)
	p.SetBodyStore(fakeBodyStore{blob: blob})
	fs := &fakeSubmitter{}
	p.SetSubmitter(fs.submit)

	handles = composeAndSaveDraft(t, p, sess, handles, []testRecipient{
		{recipientTo, "alice@local.test", "Alice"},
		{recipientCc, "carol@local.test", "Carol"},
		{recipientBcc, "dave@local.test", "Dave"},
	})

	rv, trailing := submitAt(t, p, sess, handles, 2)
	if rv != ecSuccess {
		t.Fatalf("RopSubmitMessage return value = %#x, want success", rv)
	}
	if trailing != 0 {
		t.Errorf("RopSubmitMessage response carries %d trailing bytes, want none", trailing)
	}

	// The submission path must be invoked exactly once, by the mailbox owner.
	if fs.called != 1 {
		t.Fatalf("submitter called %d times, want exactly 1", fs.called)
	}
	if fs.from != "qa.bob@local.test" {
		t.Errorf("envelope sender = %q, want the mailbox owner qa.bob@local.test", fs.from)
	}

	// Bcc-delivery gate: the envelope must list To, Cc, AND Bcc — drop the Bcc here
	// and a blind-carbon recipient would silently never receive the message.
	wantEnvelope := []string{"alice@local.test", "carol@local.test", "dave@local.test"}
	if !slices.Equal(fs.to, wantEnvelope) {
		t.Errorf("delivery envelope = %v, want %v (To+Cc+Bcc, Bcc included)", fs.to, wantEnvelope)
	}

	// No-leak gate: the submitted message keeps To and Cc but must carry no Bcc
	// header and must not mention the Bcc recipient anywhere.
	m, err := mail.ReadMessage(bytes.NewReader(fs.raw))
	if err != nil {
		t.Fatalf("submitted message is not valid RFC 5322: %v", err)
	}
	if to := m.Header.Get("To"); !strings.Contains(to, "alice@local.test") {
		t.Errorf("To = %q, want it to carry alice@local.test", to)
	}
	if cc := m.Header.Get("Cc"); !strings.Contains(cc, "carol@local.test") {
		t.Errorf("Cc = %q, want it to carry carol@local.test", cc)
	}
	if bcc := m.Header.Get("Bcc"); bcc != "" {
		t.Errorf("Bcc header = %q, want empty (a Bcc must never leak into the sent message)", bcc)
	}
	if bytes.Contains(fs.raw, []byte("dave@local.test")) {
		t.Error("the Bcc recipient dave@local.test leaked into the submitted message")
	}
}

// TestSubmitMessageWithoutSubmitter verifies RopSubmitMessage reports the operation
// as unsupported when no submission path is wired, rather than silently dropping
// the send or panicking.
func TestSubmitMessageWithoutSubmitter(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, blob, _ := newWriteAppender()
	p.SetAppender(app)
	p.SetBodyStore(fakeBodyStore{blob: blob})
	// no SetSubmitter

	handles = composeAndSaveDraft(t, p, sess, handles, []testRecipient{
		{recipientTo, "alice@local.test", "Alice"},
	})

	if rv, _ := submitAt(t, p, sess, handles, 2); rv != ecNotImplemented {
		t.Errorf("submit without submitter = %#x, want ecNotImplemented", rv)
	}
}

// TestSubmitMessageNoRecipients verifies a saved-but-recipientless message cannot
// be submitted: with an empty delivery envelope there is nobody to deliver to, so
// the ROP fails and the submission path is never invoked.
func TestSubmitMessageNoRecipients(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, blob, _ := newWriteAppender()
	p.SetAppender(app)
	p.SetBodyStore(fakeBodyStore{blob: blob})
	fs := &fakeSubmitter{}
	p.SetSubmitter(fs.submit)

	handles = composeAndSaveDraft(t, p, sess, handles, nil)

	if rv, _ := submitAt(t, p, sess, handles, 2); rv != ecError {
		t.Errorf("submit with no recipients = %#x, want ecError", rv)
	}
	if fs.called != 0 {
		t.Errorf("submitter called %d times, want 0 (a recipientless message must not be submitted)", fs.called)
	}
}

// TestSubmitMessageUnsaved verifies a created-but-unsaved message cannot be
// submitted: it carries no blob key or envelope yet, so the ROP fails rather than
// trying to send a draft that was never persisted.
func TestSubmitMessageUnsaved(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, blob, _ := newWriteAppender()
	p.SetAppender(app)
	p.SetBodyStore(fakeBodyStore{blob: blob})
	fs := &fakeSubmitter{}
	p.SetSubmitter(fs.submit)

	// CreateMessage at handle 2 but do NOT save it.
	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	if rv, _ := submitAt(t, p, sess, handles, 2); rv != ecError {
		t.Errorf("submit of an unsaved message = %#x, want ecError", rv)
	}
	if fs.called != 0 {
		t.Errorf("submitter called %d times, want 0 (an unsaved message must not be submitted)", fs.called)
	}
}
