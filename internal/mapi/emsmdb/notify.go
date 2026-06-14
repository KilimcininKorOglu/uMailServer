package emsmdb

import (
	"sync"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// Notification flags carried in a RopNotify's NotificationFlags field (MS-OXCNOTIF
// 2.2.1.1; MS-OXCROPS 2.2.14.2.1). Exactly one event-type bit (the low 12 bits) is
// set per notification; the high bits are modifiers. Only the values this connector
// emits are named — new mail by message.
const (
	fnevNewMail       uint16 = 0x0002
	fnevObjectDeleted uint16 = 0x0008
	nfByMessage       uint16 = 0x8000
)

// NotifyKind classifies a mailbox change so the push path can serialize the matching
// RopNotify form. The server bridge sets it when translating a hub event.
type NotifyKind int

const (
	// NotifyNewMail is a message delivered to a folder (fnevNewMail).
	NotifyNewMail NotifyKind = iota
	// NotifyDeleted is a message removed from a folder (fnevObjectDeleted).
	NotifyDeleted
)

// flagNotificationPending is the NotificationWait response FlagsOut value that tells
// the client an event is queued, so it should issue an Execute to drain the RopNotify
// ROPs (MS-OXCMAPIHTTP 2.2.4.4.2).
const flagNotificationPending uint32 = 0x00000001

// MailboxEvent is a single mailbox change delivered by a NotificationSource. It is a
// surface-neutral shape so the emsmdb package takes no dependency on the IMAP (or any
// other) notification type.
type MailboxEvent struct {
	Kind    NotifyKind // new mail or deletion
	Mailbox string     // IMAP-canonical folder name (e.g. "INBOX")
	UID     uint32     // message uid within that mailbox
}

// NotificationSource feeds mailbox change events to the emsmdb push path. It is
// implemented outside this package (the server bridges the shared IMAP notification
// hub that also drives IMAP IDLE and webmail SSE), so the protocol layer stays free
// of any delivery-surface import and every surface sees the one event stream.
type NotificationSource interface {
	// Subscribe begins delivering events for the mailbox and returns the event channel
	// plus a cancel func that stops delivery. The underlying subscription MUST be
	// registered synchronously before Subscribe returns: a client that registers and
	// then immediately causes an event (the probe's register-then-deliver) would
	// otherwise lose it, since the source buffers only from the moment it is wired in.
	Subscribe(email string) (events <-chan MailboxEvent, cancel func())
}

// subscriptionObject is the server object RopRegisterNotification binds: a client's
// standing request to be notified of mailbox changes. handle is the subscription's own
// handle value (the value echoed in every RopNotify it produces, not the table index);
// logonID is the logon it belongs to; wholeStore distinguishes a store-wide
// subscription (the common cached-mode case) from a folder-scoped one.
type subscriptionObject struct {
	handle     uint32
	logonID    uint8
	wholeStore bool
}

// notifyState is a session's push-notification state: the subscriptions the client
// registered, the event channel feeding them, and the events staged for the next
// Execute to drain. It is created lazily on the first RopRegisterNotification and torn
// down on Disconnect. Its mutex guards subs/staged and is independent of the session's
// Execute lock, so the long-poll NotificationWait never blocks an Execute.
type notifyState struct {
	mu     sync.Mutex
	subs   []*subscriptionObject
	staged []MailboxEvent
	events <-chan MailboxEvent
	cancel func()
}

// add records a new subscription.
func (n *notifyState) add(sub *subscriptionObject) {
	n.mu.Lock()
	n.subs = append(n.subs, sub)
	n.mu.Unlock()
}

// snapshotSubs returns a copy of the current subscriptions for lock-free iteration.
func (n *notifyState) snapshotSubs() []*subscriptionObject {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]*subscriptionObject(nil), n.subs...)
}

// pending reports whether an event is staged or buffered without consuming it.
func (n *notifyState) pending() bool {
	n.mu.Lock()
	staged := len(n.staged)
	n.mu.Unlock()
	return staged > 0 || len(n.events) > 0
}

// wait blocks up to timeout for an event, returning true if one is (or becomes)
// available. An event received while waiting is staged for the next drain so the
// NotificationWait-detects/Execute-delivers split never loses it.
func (n *notifyState) wait(timeout time.Duration) bool {
	if n.pending() {
		return true
	}
	select {
	case ev, ok := <-n.events:
		if !ok {
			return false
		}
		n.mu.Lock()
		n.staged = append(n.staged, ev)
		n.mu.Unlock()
		return true
	case <-time.After(timeout):
		return false
	}
}

// drain returns all staged and buffered events, clearing them. It is called by an
// Execute to turn queued changes into RopNotify ROPs.
func (n *notifyState) drain() []MailboxEvent {
	n.mu.Lock()
	out := n.staged
	n.staged = nil
	n.mu.Unlock()
	for {
		select {
		case ev, ok := <-n.events:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// ropRegisterNotification handles RopRegisterNotification (MS-OXCNOTIF 2.2.1.2.1;
// MS-OXCROPS 2.2.14.1): the client asks to be notified of changes to the object named
// by the input handle (the logon for a whole-store subscription). The request's fixed
// prefix is a 4-byte field after the input handle — OutputHandleIndex (1), a 2-byte
// NotificationTypes/Reserved field this connector does not act on, and WantWholeStore
// (1) — followed by a folder id and message id only when the subscription is scoped to
// one message. The subscription is bound to the output handle; the session lazily
// subscribes to the shared notification feed on the first registration so a change
// raised on any surface reaches this client. The response carries no body.
func ropRegisterNotification(c *ropCtx, logonID, hindex uint8) {
	ohindex := c.in.Uint8()
	_ = c.in.Uint16() // NotificationTypes/Reserved: a 2-byte field not acted on here
	wantWholeStore := c.in.Uint8()
	if wantWholeStore == 0 {
		_ = c.in.Uint64() // folder id: folder/message-scoped subscriptions are a refinement
		_ = c.in.Uint64() // message id
	}
	if c.in.Err() != nil {
		writeRopError(c.out, RopRegisterNotification, ohindex, ecError)
		return
	}
	if c.objectAt(hindex) == nil {
		writeRopError(c.out, RopRegisterNotification, ohindex, ecNullObject)
		return
	}
	if c.notifier == nil || c.sess == nil {
		writeRopError(c.out, RopRegisterNotification, ohindex, ecNotImplemented)
		return
	}
	sub := &subscriptionObject{logonID: logonID, wholeStore: wantWholeStore != 0}
	handle := c.state.alloc(sub)
	sub.handle = handle
	c.setHandle(ohindex, handle)
	c.sess.ensureNotify(c.email, c.notifier)
	c.sess.addSubscription(sub)

	out := c.out
	out.Uint8(RopRegisterNotification)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
}

// emitNotifications drains the session's queued mailbox changes and appends a RopNotify
// ROP for each one to a whole-store subscription, after the requested ROPs' responses
// (MS-OXCROPS 2.2.14.2). A change to a special folder maps to that folder's well-known
// id with no logon-state lookup, so the notify path never races the Execute-locked
// custom-folder registry; a change to a custom folder is skipped (a later refinement).
func emitNotifications(sess *Session, out *wire.Push) {
	if sess == nil {
		return
	}
	n := sess.getNotify()
	if n == nil {
		return
	}
	events := n.drain()
	if len(events) == 0 {
		return
	}
	st := stateFor(sess)
	subs := n.snapshotSubs()
	for _, ev := range events {
		slot := specialSlotForName(ev.Mailbox)
		if slot < 0 {
			continue // custom-folder target: notify mapping deferred
		}
		folderID := makeFID(fidReplID, specialFolderGC[slot])
		mid := messageID(ev.UID)
		for _, sub := range subs {
			if !sub.wholeStore {
				continue // folder-scoped subscriptions are a refinement
			}
			if st.objects[sub.handle] != sub {
				continue // the subscription handle was released (RopRelease)
			}
			if ev.Kind == NotifyDeleted {
				writeDeletedNotify(out, sub.handle, sub.logonID, folderID, mid)
			} else {
				writeNewMailNotify(out, sub.handle, sub.logonID, folderID, mid)
			}
		}
	}
}

// writeNewMailNotify serializes a new-mail RopNotify (MS-OXCROPS 2.2.14.2.1): the op id,
// the subscription handle, the logon id, the notification flags, then — for a new mail
// by message — the folder id, message id, message flags, and the message class as an
// 8-bit string. The field presence follows the NotificationData layout for
// fnevNewMail|NF_BY_MESSAGE: folder id and message id are present, no parent/old ids or
// property tags.
func writeNewMailNotify(out *wire.Push, handle uint32, logonID uint8, folderID, messageID uint64) {
	out.Uint8(RopNotify)
	out.Uint32(handle)
	out.Uint8(logonID)
	out.Uint16(fnevNewMail | nfByMessage)
	out.Uint64(folderID)
	out.Uint64(messageID)
	out.Uint32(0)       // message flags: a freshly delivered message is unread
	out.Uint8(0)        // unicode flag: the message class is an 8-bit string
	out.Str("IPM.Note") // message class
}

// writeDeletedNotify serializes an object-deleted RopNotify for a message
// (MS-OXCROPS 2.2.14.2.1). For fnevObjectDeleted|NF_BY_MESSAGE the NotificationData
// carries only the folder id and the message id: no parent id (the by-search/by-message
// presence rule excludes it), no property tags, and no new-mail block.
func writeDeletedNotify(out *wire.Push, handle uint32, logonID uint8, folderID, messageID uint64) {
	out.Uint8(RopNotify)
	out.Uint32(handle)
	out.Uint8(logonID)
	out.Uint16(fnevObjectDeleted | nfByMessage)
	out.Uint64(folderID)
	out.Uint64(messageID)
}
