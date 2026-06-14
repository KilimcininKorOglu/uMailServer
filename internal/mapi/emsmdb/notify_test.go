package emsmdb

import (
	"bytes"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// fakeNotifySource is a controllable NotificationSource for tests: push feeds events
// onto the channel a subscription drains, and cancel records teardown.
type fakeNotifySource struct {
	ch       chan MailboxEvent
	canceled bool
}

func newFakeNotifySource() *fakeNotifySource {
	return &fakeNotifySource{ch: make(chan MailboxEvent, 16)}
}

func (f *fakeNotifySource) Subscribe(string) (<-chan MailboxEvent, func()) {
	return f.ch, func() { f.canceled = true }
}

func (f *fakeNotifySource) push(ev MailboxEvent) { f.ch <- ev }

// notifyLogon builds a logged-on session whose processor has the notification source
// wired, returning the handle table with the logon bound at index 0.
func notifyLogon(t *testing.T, src NotificationSource) (*Processor, *Session, []uint32) {
	t.Helper()
	p := NewProcessor(newFakeStore())
	p.SetNotificationSource(src)
	sess := &Session{ID: "s", Email: "qa.bob@local.test"}
	rop := append([]byte{RopLogon, 0x00, 0x00}, encodeLogonRequest()...)
	_, handles := p.Dispatch(sess, rop, []uint32{0xFFFFFFFF}, 0x10000)
	if len(handles) == 0 || handles[0] == 0xFFFFFFFF {
		t.Fatal("logon did not bind a handle at index 0")
	}
	return p, sess, handles
}

// registerNotificationRequest builds a RopRegisterNotification body: the output handle
// index, the 2-byte NotificationTypes/Reserved field, WantWholeStore, and the scoped
// folder/message ids only when not whole-store.
func registerNotificationRequest(ohindex uint8, wholeStore bool) []byte {
	body := wire.NewPush(wire.FlagUTF16)
	body.Uint8(ohindex)
	body.Uint16(0) // NotificationTypes/Reserved
	if wholeStore {
		body.Uint8(1)
	} else {
		body.Uint8(0)
		body.Uint64(0) // folder id
		body.Uint64(0) // message id
	}
	return body.Bytes()
}

// TestNewMailNotifySerialization pins the exact RopNotify wire layout for a new mail by
// message (MS-OXCROPS 2.2.14.2.1): op id, handle, logon id, flags, folder id, message
// id, message flags, unicode flag, and the 8-bit message class.
func TestNewMailNotifySerialization(t *testing.T) {
	p := wire.NewPush(wire.FlagUTF16)
	writeNewMailNotify(p, 0x11223344, 0x05, 0x0D00000000000001, 0x0700000000000001)
	want := []byte{
		RopNotify,
		0x44, 0x33, 0x22, 0x11, // handle u32 LE
		0x05,       // logon id
		0x02, 0x80, // nflags u16 LE = fnevNewMail|NF_BY_MESSAGE
		0x01, 0, 0, 0, 0, 0, 0, 0x0D, // folder id u64 LE
		0x01, 0, 0, 0, 0, 0, 0, 0x07, // message id u64 LE
		0, 0, 0, 0, // message flags u32
		0x00,                                   // unicode flag
		'I', 'P', 'M', '.', 'N', 'o', 't', 'e', // message class
		0x00, // NUL terminator
	}
	if !bytes.Equal(p.Bytes(), want) {
		t.Errorf("RopNotify bytes =\n% x\nwant\n% x", p.Bytes(), want)
	}
}

// TestRegisterNotificationCreatesSubscription verifies RopRegisterNotification binds a
// subscription object, subscribes the session to the feed, and returns success.
func TestRegisterNotificationCreatesSubscription(t *testing.T) {
	src := newFakeNotifySource()
	p, sess, handles := notifyLogon(t, src)

	resp, handles := p.Dispatch(sess, ropRequest(RopRegisterNotification, 0, registerNotificationRequest(1, true)), handles, 0x10000)
	if code := ropResult(t, resp); code != ecSuccess {
		t.Fatalf("RegisterNotification result = %#x, want success", code)
	}
	sub, ok := stateFor(sess).objects[handles[1]].(*subscriptionObject)
	if !ok {
		t.Fatalf("handle index 1 is not a subscription: %T", stateFor(sess).objects[handles[1]])
	}
	if !sub.wholeStore {
		t.Error("subscription should be whole-store")
	}
	if sub.handle != handles[1] {
		t.Errorf("subscription handle = %#x, want %#x", sub.handle, handles[1])
	}
	if sess.getNotify() == nil {
		t.Error("session was not subscribed to the notification feed")
	}
}

// TestRegisterNotificationWithoutSourceUnsupported verifies that without a wired
// notification source the ROP reports the operation unsupported rather than panicking.
func TestRegisterNotificationWithoutSourceUnsupported(t *testing.T) {
	p := NewProcessor(newFakeStore())
	sess := &Session{ID: "s", Email: "qa.bob@local.test"}
	rop := append([]byte{RopLogon, 0x00, 0x00}, encodeLogonRequest()...)
	_, handles := p.Dispatch(sess, rop, []uint32{0xFFFFFFFF}, 0x10000)

	resp, _ := p.Dispatch(sess, ropRequest(RopRegisterNotification, 0, registerNotificationRequest(1, true)), handles, 0x10000)
	if code := ropResult(t, resp); code != ecNotImplemented {
		t.Fatalf("result = %#x, want ecNotImplemented", code)
	}
}

// TestExecuteDrainEmitsRopNotify is the heart of the push path: after registering for
// notifications, a new-mail event raised on the feed must surface as a RopNotify in the
// next Execute, carrying the Inbox folder id and the message's id. A drain that lost the
// event or mis-mapped the ids would fail here.
func TestExecuteDrainEmitsRopNotify(t *testing.T) {
	src := newFakeNotifySource()
	p, sess, handles := notifyLogon(t, src)
	_, handles = p.Dispatch(sess, ropRequest(RopRegisterNotification, 0, registerNotificationRequest(1, true)), handles, 0x10000)

	src.push(MailboxEvent{Mailbox: "INBOX", UID: 7})

	// An Execute with no requested ROPs still drains queued notifications.
	resp, _ := p.Dispatch(sess, nil, handles, 0x10000)

	pull := wire.NewPull(resp, wire.FlagUTF16)
	if op := pull.Uint8(); op != RopNotify {
		t.Fatalf("first response op = %#x, want RopNotify", op)
	}
	if h := pull.Uint32(); h != handles[1] {
		t.Errorf("notify handle = %#x, want %#x", h, handles[1])
	}
	pull.Uint8() // logon id
	if nf := pull.Uint16(); nf != fnevNewMail|nfByMessage {
		t.Errorf("nflags = %#x, want %#x", nf, fnevNewMail|nfByMessage)
	}
	wantFolder := makeFID(fidReplID, specialFolderGC[sfInbox])
	if fid := pull.Uint64(); fid != wantFolder {
		t.Errorf("folder id = %#x, want %#x (Inbox)", fid, wantFolder)
	}
	if mid := pull.Uint64(); mid != messageID(7) {
		t.Errorf("message id = %#x, want %#x", mid, messageID(7))
	}
}

// TestReleasedSubscriptionNotNotified verifies that once a client releases a
// subscription handle, a later event produces no RopNotify for it — the drain skips a
// subscription whose handle no longer resolves.
func TestReleasedSubscriptionNotNotified(t *testing.T) {
	src := newFakeNotifySource()
	p, sess, handles := notifyLogon(t, src)
	_, handles = p.Dispatch(sess, ropRequest(RopRegisterNotification, 0, registerNotificationRequest(1, true)), handles, 0x10000)

	// Release the subscription bound at handle index 1.
	_, handles = p.Dispatch(sess, ropRequest(RopRelease, 1, nil), handles, 0x10000)

	src.push(MailboxEvent{Mailbox: "INBOX", UID: 7})
	resp, _ := p.Dispatch(sess, nil, handles, 0x10000)
	if len(resp) != 0 {
		t.Errorf("released subscription should produce no RopNotify, got % x", resp)
	}
}

// TestNoSubscriptionNoNotify verifies a session that never registered drains nothing,
// so an ordinary Execute is unaffected by the notification path.
func TestNoSubscriptionNoNotify(t *testing.T) {
	src := newFakeNotifySource()
	p, sess, handles := notifyLogon(t, src)
	src.push(MailboxEvent{Mailbox: "INBOX", UID: 7}) // nobody is subscribed

	resp, _ := p.Dispatch(sess, nil, handles, 0x10000)
	if len(resp) != 0 {
		t.Errorf("unsubscribed Execute produced % x, want empty", resp)
	}
}

// TestCloseNotifyCancelsSubscription verifies that ending the session tears down the
// notification subscription, so an abandoned session does not leak its feed.
func TestCloseNotifyCancelsSubscription(t *testing.T) {
	src := newFakeNotifySource()
	p, sess, handles := notifyLogon(t, src)
	_, _ = p.Dispatch(sess, ropRequest(RopRegisterNotification, 0, registerNotificationRequest(1, true)), handles, 0x10000)

	sess.closeNotify()
	if !src.canceled {
		t.Error("closeNotify should cancel the subscription")
	}
	if sess.getNotify() != nil {
		t.Error("closeNotify should clear the notify state")
	}
}

// TestNotifyStateWait covers the long-poll: wait reports an event that is already
// buffered, and times out (returning false) when none arrives.
func TestNotifyStateWait(t *testing.T) {
	ch := make(chan MailboxEvent, 4)
	n := &notifyState{events: ch}

	if n.wait(10 * time.Millisecond) {
		t.Error("wait should time out with an empty feed")
	}
	ch <- MailboxEvent{Mailbox: "INBOX", UID: 1}
	if !n.wait(time.Second) {
		t.Error("wait should report a buffered event")
	}
	if got := n.drain(); len(got) != 1 || got[0].UID != 1 {
		t.Errorf("drain = %+v, want one event uid 1", got)
	}
}

// ropResult reads the result code from a generic ROP response (op id, handle index,
// result u32).
func ropResult(t *testing.T, resp []byte) uint32 {
	t.Helper()
	pull := wire.NewPull(resp, wire.FlagUTF16)
	pull.Uint8() // op id
	pull.Uint8() // handle index
	return pull.Uint32()
}
