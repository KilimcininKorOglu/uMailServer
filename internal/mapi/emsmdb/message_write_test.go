package emsmdb

import (
	"bytes"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/mailappend"
	"github.com/umailserver/umailserver/internal/mapi/wire"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// The write ROPs commit through the shared mailappend.Appender. These fakes
// stand in for the canonical stores so the create→set→save flow can be exercised
// without a real bbolt/Maildir stack: the blob fake captures the committed RFC
// 5322 bytes, and the index fake assigns the uid the returned MID is derived from.

type fakeAppendIdent struct{}

func (fakeAppendIdent) EnsureMailboxId(string) (semcore.MailboxId, error) {
	return semcore.MailboxId{}, nil
}

func (fakeAppendIdent) EnsureFolderId(string, string, string) (semcore.FolderId, error) {
	return semcore.FolderId{}, nil
}

func (fakeAppendIdent) GetItemIdentity(semcore.ItemId) (*semcore.StoredItemIdentity, error) {
	return nil, nil
}

func (fakeAppendIdent) PutItemIdentity(string, string, semcore.ItemId, semcore.MailboxId, semcore.FolderId, semcore.ChangeKey, semcore.ConversationId, bool) error {
	return nil
}

func (fakeAppendIdent) PutItemIdentityWithKey(string, string, string, semcore.ItemId, semcore.MailboxId, semcore.FolderId, semcore.ChangeKey, semcore.ConversationId, bool) error {
	return nil
}

func (fakeAppendIdent) PutChangeKey(semcore.ItemId, semcore.ChangeKey, semcore.ChangeKey) error {
	return nil
}

func (fakeAppendIdent) PutConversationIdentity(semcore.ConversationId, semcore.MailboxId) error {
	return nil
}

type fakeAppendPipe struct{}

func (fakeAppendPipe) Identity() semcore.PipelineIdentityStore { return fakeAppendIdent{} }

func (fakeAppendPipe) MutateItem(*semcore.MutationInput) (*semcore.MutationResult, error) {
	return &semcore.MutationResult{}, nil
}

type fakeAppendBlob struct{ msgs [][]byte }

func (f *fakeAppendBlob) StoreMessage(_ string, data []byte) (string, error) {
	f.msgs = append(f.msgs, append([]byte(nil), data...))
	return "blob-" + strconv.Itoa(len(f.msgs)), nil
}

type fakeAppendIndex struct {
	next  uint32
	saved map[string]*storage.MessageMetadata
}

func (f *fakeAppendIndex) GetNextUID(_, _ string) (uint32, error) {
	f.next++
	return f.next, nil
}

func (f *fakeAppendIndex) GetOrCreateThreadID(_, _, _, _, _ string, _ []string) (string, error) {
	return "thread-1", nil
}

func (f *fakeAppendIndex) StoreMessageMetadata(_, mailbox string, _ uint32, meta *storage.MessageMetadata) error {
	if f.saved == nil {
		f.saved = map[string]*storage.MessageMetadata{}
	}
	f.saved[mailbox] = meta
	return nil
}

// newWriteAppender builds an Appender over the fakes, using the same role
// resolver every surface uses so a created message resolves to the same folder
// identity SMTP and EWS would.
func newWriteAppender() (*mailappend.Appender, *fakeAppendBlob, *fakeAppendIndex) {
	blob := &fakeAppendBlob{}
	idx := &fakeAppendIndex{}
	return mailappend.NewAppender(fakeAppendPipe{}, blob, idx, semcore.RoleForCanonicalFolderName), blob, idx
}

// TestCreateSetPropertiesSaveFlow drives the full RopCreateMessage →
// RopSetProperties → RopSaveChangesMessage chain and verifies the committed
// message is a valid RFC 5322 draft, the returned MID round-trips to the index
// uid so the client can reopen it, and the index entry carries the draft flags.
func TestCreateSetPropertiesSaveFlow(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store) // folder bound at handle index 1
	app, blob, idx := newWriteAppender()
	p.SetAppender(app)

	// RopCreateMessage: open a new message in the Inbox at output handle index 2.
	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)                         // output handle index
	cm.Uint16(1252)                     // code page id
	cm.Uint64(makeFID(fidReplID, 0x0d)) // Inbox folder id
	cm.Uint8(0)                         // associated flag: a regular message
	resp, handles := p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopCreateMessage {
		t.Fatalf("rop id = %#x, want RopCreateMessage", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("create return value = %#x, want success", rv)
	}
	if has := q.Uint8(); has != 0 {
		t.Errorf("HasMessageId = %d, want 0 (a created message has no id until saved)", has)
	}
	mo, ok := stateFor(sess).objects[handles[2]].(*messageObject)
	if !ok || mo.write == nil {
		t.Fatal("no in-flight write message object bound at the output handle")
	}

	// RopSetProperties: set the subject and a plain-text body on the message.
	arr := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(arr, []wire.TaggedPropertyValue{
		{Tag: wire.PidTagSubject, Value: "draft subject"},
		{Tag: wire.PidTagBody, Value: "draft body text"},
	}); err != nil {
		t.Fatalf("push property array: %v", err)
	}
	sp := wire.NewPush(wire.FlagUTF16)
	sp.Uint16(uint16(len(arr.Bytes()))) // PropertyValueSize: byte count of the array
	sp.Raw(arr.Bytes())
	resp2, handles := p.Dispatch(sess, ropRequest(RopSetProperties, 2, sp.Bytes()), handles, 0x10000)

	q2 := wire.NewPull(resp2, wire.FlagUTF16)
	if got := q2.Uint8(); got != RopSetProperties {
		t.Fatalf("rop id = %#x, want RopSetProperties", got)
	}
	q2.Uint8() // handle index
	if rv := q2.Uint32(); rv != ecSuccess {
		t.Fatalf("set properties return value = %#x, want success", rv)
	}
	if pc := q2.Uint16(); pc != 0 {
		t.Errorf("property problem count = %d, want 0", pc)
	}

	// RopSaveChangesMessage: commit the message and read back its MID.
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2) // response handle index (ihindex2)
	sc.Uint8(0) // save flags
	resp3, _ := p.Dispatch(sess, ropRequest(RopSaveChangesMessage, 2, sc.Bytes()), handles, 0x10000)

	q3 := wire.NewPull(resp3, wire.FlagUTF16)
	if got := q3.Uint8(); got != RopSaveChangesMessage {
		t.Fatalf("rop id = %#x, want RopSaveChangesMessage", got)
	}
	q3.Uint8() // input handle index
	if rv := q3.Uint32(); rv != ecSuccess {
		t.Fatalf("save return value = %#x, want success", rv)
	}
	if ih := q3.Uint8(); ih != 2 {
		t.Errorf("response handle index = %d, want 2", ih)
	}
	mid := q3.Uint64()
	if q3.Err() != nil {
		t.Fatalf("save response parse error: %v", q3.Err())
	}
	// The MID must invert to the assigned index uid, the only lookup key
	// RopOpenMessage has — otherwise the client could never reopen the message.
	if got := messageUID(mid); got != idx.next {
		t.Errorf("messageUID(mid) = %d, want assigned uid %d", got, idx.next)
	}

	// The committed blob must be a valid RFC 5322 message carrying the properties.
	if len(blob.msgs) != 1 {
		t.Fatalf("blob store holds %d messages, want 1", len(blob.msgs))
	}
	m, err := mail.ReadMessage(bytes.NewReader(blob.msgs[0]))
	if err != nil {
		t.Fatalf("committed blob is not a valid RFC 5322 message: %v", err)
	}
	if subj := m.Header.Get("Subject"); subj != "draft subject" {
		t.Errorf("Subject = %q, want %q", subj, "draft subject")
	}
	if !bytes.Contains(blob.msgs[0], []byte("draft body text")) {
		t.Errorf("committed blob does not carry the body text")
	}

	// The index entry must mark the message a read draft so IMAP/JMAP/webmail
	// agree with what MAPI created.
	meta := idx.saved["INBOX"]
	if meta == nil {
		t.Fatal("no index metadata stored for INBOX")
	}
	if !slices.Contains(meta.Flags, "\\Draft") || !slices.Contains(meta.Flags, "\\Seen") {
		t.Errorf("index flags = %v, want both \\Draft and \\Seen", meta.Flags)
	}
}

// TestBuildMIMEFromProps verifies the property-bag-to-MIME conversion produces a
// valid RFC 5322 message for the sparse draft, HTML-only, and dual-body cases —
// the cross-surface gate that another surface (EWS FindItem) can parse the result.
func TestBuildMIMEFromProps(t *testing.T) {
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name    string
		props   map[wire.PropTag]any
		ctPart  string
		bodyHas string
	}{
		{
			"sparse plain draft",
			map[wire.PropTag]any{wire.PidTagSubject: "s", wire.PidTagBody: "plain only"},
			"text/plain",
			"plain only",
		},
		{
			"html only",
			map[wire.PropTag]any{wire.PidTagSubject: "s", wire.PidTagHtml: []byte("<p>rich</p>")},
			"text/html",
			"<p>rich</p>",
		},
		{
			"plain and html alternative",
			map[wire.PropTag]any{wire.PidTagBody: "txt", wire.PidTagHtml: []byte("<p>h</p>")},
			"multipart/alternative",
			"",
		},
	}
	for _, c := range cases {
		raw, err := buildMIMEFromProps(c.props, nil, "owner@local.test", now)
		if err != nil {
			t.Fatalf("%s: buildMIMEFromProps: %v", c.name, err)
		}
		m, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: not a valid RFC 5322 message: %v", c.name, err)
		}
		if from := m.Header.Get("From"); from != "owner@local.test" {
			t.Errorf("%s: From = %q, want owner@local.test", c.name, from)
		}
		if ct := m.Header.Get("Content-Type"); !strings.Contains(ct, c.ctPart) {
			t.Errorf("%s: Content-Type = %q, want substring %q", c.name, ct, c.ctPart)
		}
		if c.bodyHas != "" && !bytes.Contains(raw, []byte(c.bodyHas)) {
			t.Errorf("%s: body missing %q", c.name, c.bodyHas)
		}
	}
}

// TestWriteRopsWithoutAppender verifies the write ROPs report the operation as
// unsupported when no append core is wired, rather than panicking or silently
// dropping the message.
func TestWriteRopsWithoutAppender(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store) // no SetAppender

	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	resp, _ := p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecNotImplemented {
		t.Errorf("create without appender = %#x, want ecNotImplemented", rv)
	}
}

// TestCreateAssociatedMessageRejected verifies a folder-associated (FAI) create
// is refused: those hidden configuration items are not mail and have no place in
// the canonical mail store.
func TestCreateAssociatedMessageRejected(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, _, _ := newWriteAppender()
	p.SetAppender(app)

	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(1) // associated (FAI) message
	resp, _ := p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	q := wire.NewPull(resp, wire.FlagUTF16)
	q.Uint8() // rop id
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecNotImplemented {
		t.Errorf("associated-message create = %#x, want ecNotImplemented", rv)
	}
}

// TestModifyRecipientsFlow drives RopCreateMessage -> RopModifyRecipients ->
// RopSaveChangesMessage and verifies both recipient-row shapes survive into the
// committed message's To/Cc headers: a To recipient whose address is in the fixed
// RECIPIENT_ROW EMAIL/DISPLAY fields, and a Cc recipient whose address is ONLY in
// a property column (no EMAIL flag) — the fallback branch. The test encodes the
// RECIPIENT_ROWs with the same wire codec the server decodes them with, then
// reads the addresses back out of the parsed RFC 5322 message.
func TestModifyRecipientsFlow(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, blob, _ := newWriteAppender()
	p.SetAppender(app)

	// CreateMessage in the Inbox, binding the message at output handle index 2.
	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	// SetProperties (on the message at handle 2): a subject so the message is non-empty.
	arr := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(arr, []wire.TaggedPropertyValue{
		{Tag: wire.PidTagSubject, Value: "recipients"},
	}); err != nil {
		t.Fatalf("push property array: %v", err)
	}
	sp := wire.NewPush(wire.FlagUTF16)
	sp.Uint16(uint16(len(arr.Bytes())))
	sp.Raw(arr.Bytes())
	_, handles = p.Dispatch(sess, ropRequest(RopSetProperties, 2, sp.Bytes()), handles, 0x10000)

	// ModifyRecipients: columns shared by every row, then two rows.
	cols := []wire.PropTag{wire.PidTagSmtpAddress, wire.PidTagDisplayName}
	mr := wire.NewPush(wire.FlagUTF16)
	wire.PushPropertyTagArray(mr, cols)
	mr.Uint16(2) // two recipient rows

	// Row 1 (To): address in the fixed EMAIL/DISPLAY fields, empty property row.
	row1 := wire.NewPush(wire.FlagUTF16)
	row1.Uint16(recipFlagUnicode | recipFlagEmail | recipFlagDisplay)
	row1.WStr("alice@local.test")
	row1.WStr("Alice")
	row1.Uint16(0)   // RecipientColumnCount
	row1.Uint8(0x00) // empty property row (flag byte, no values)
	mr.Uint32(1)
	mr.Uint8(recipientTo)
	mr.Uint16(uint16(len(row1.Bytes())))
	mr.Raw(row1.Bytes())

	// Row 2 (Cc): no fixed address fields; address only in the property columns.
	row2 := wire.NewPush(wire.FlagUTF16)
	row2.Uint16(recipFlagUnicode)
	row2.Uint16(uint16(len(cols)))
	if err := wire.PushPropertyRow(row2, cols, wire.PropertyRow{
		Flag:   wire.RowFlagNone,
		Values: []any{"bob@local.test", "Bob"},
	}); err != nil {
		t.Fatalf("push recipient property row: %v", err)
	}
	mr.Uint32(2)
	mr.Uint8(recipientCc)
	mr.Uint16(uint16(len(row2.Bytes())))
	mr.Raw(row2.Bytes())

	resp, handles := p.Dispatch(sess, ropRequest(RopModifyRecipients, 2, mr.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopModifyRecipients {
		t.Fatalf("rop id = %#x, want RopModifyRecipients", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("ModifyRecipients return value = %#x, want success", rv)
	}

	// SaveChanges commits the message.
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)
	sc.Uint8(0)
	resp2, _ := p.Dispatch(sess, ropRequest(RopSaveChangesMessage, 2, sc.Bytes()), handles, 0x10000)
	q2 := wire.NewPull(resp2, wire.FlagUTF16)
	q2.Uint8() // rop id
	q2.Uint8() // handle index
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
	to := m.Header.Get("To")
	if !strings.Contains(to, "alice@local.test") || !strings.Contains(to, "Alice") {
		t.Errorf("To = %q, want it to carry the fixed-field recipient alice@local.test (Alice)", to)
	}
	cc := m.Header.Get("Cc")
	if !strings.Contains(cc, "bob@local.test") || !strings.Contains(cc, "Bob") {
		t.Errorf("Cc = %q, want it to carry the property-column recipient bob@local.test (Bob)", cc)
	}
}

// TestDeletePropertiesFlow verifies RopDeleteProperties removes a property from
// the in-flight message before it is committed: the body set by RopSetProperties
// is deleted, so the saved message keeps its subject but carries no body text.
func TestDeletePropertiesFlow(t *testing.T) {
	store := newFakeStore()
	p, sess, handles := openInbox(t, store)
	app, blob, _ := newWriteAppender()
	p.SetAppender(app)

	// CreateMessage at output handle index 2.
	cm := wire.NewPush(wire.FlagUTF16)
	cm.Uint8(2)
	cm.Uint16(1252)
	cm.Uint64(makeFID(fidReplID, 0x0d))
	cm.Uint8(0)
	_, handles = p.Dispatch(sess, ropRequest(RopCreateMessage, 1, cm.Bytes()), handles, 0x10000)

	// SetProperties: a subject (kept) and a body (to be deleted).
	arr := wire.NewPush(wire.FlagUTF16)
	if err := wire.PushTPropValArray(arr, []wire.TaggedPropertyValue{
		{Tag: wire.PidTagSubject, Value: "kept subject"},
		{Tag: wire.PidTagBody, Value: "doomed body text"},
	}); err != nil {
		t.Fatalf("push property array: %v", err)
	}
	sp := wire.NewPush(wire.FlagUTF16)
	sp.Uint16(uint16(len(arr.Bytes())))
	sp.Raw(arr.Bytes())
	_, handles = p.Dispatch(sess, ropRequest(RopSetProperties, 2, sp.Bytes()), handles, 0x10000)

	// DeleteProperties: remove the body, leave the subject.
	dp := wire.NewPush(wire.FlagUTF16)
	wire.PushPropertyTagArray(dp, []wire.PropTag{wire.PidTagBody})
	resp, handles := p.Dispatch(sess, ropRequest(RopDeleteProperties, 2, dp.Bytes()), handles, 0x10000)
	q := wire.NewPull(resp, wire.FlagUTF16)
	if got := q.Uint8(); got != RopDeleteProperties {
		t.Fatalf("rop id = %#x, want RopDeleteProperties", got)
	}
	q.Uint8() // handle index
	if rv := q.Uint32(); rv != ecSuccess {
		t.Fatalf("DeleteProperties return value = %#x, want success", rv)
	}
	if pc := q.Uint16(); pc != 0 {
		t.Errorf("property problem count = %d, want 0", pc)
	}

	// SaveChanges commits the message.
	sc := wire.NewPush(wire.FlagUTF16)
	sc.Uint8(2)
	sc.Uint8(0)
	resp2, _ := p.Dispatch(sess, ropRequest(RopSaveChangesMessage, 2, sc.Bytes()), handles, 0x10000)
	q2 := wire.NewPull(resp2, wire.FlagUTF16)
	q2.Uint8() // rop id
	q2.Uint8() // handle index
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
	if subj := m.Header.Get("Subject"); subj != "kept subject" {
		t.Errorf("Subject = %q, want %q (the subject must be kept)", subj, "kept subject")
	}
	if bytes.Contains(blob.msgs[0], []byte("doomed body text")) {
		t.Error("the deleted body text is still present in the committed message")
	}
}
