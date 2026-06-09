package imap

import (
	"sync"
	"time"
)

// NotificationType represents the type of mailbox notification
type NotificationType int

const (
	// NotificationNewMessage indicates a new message was delivered
	NotificationNewMessage NotificationType = 1
	// NotificationExpunge indicates a message was expunged
	NotificationExpunge NotificationType = 2
	// NotificationFlagsChanged indicates flags were changed
	NotificationFlagsChanged NotificationType = 3
	// NotificationMailboxUpdate indicates general mailbox update
	NotificationMailboxUpdate NotificationType = 4
)

// MailboxNotification represents a notification about mailbox changes
type MailboxNotification struct {
	Type       NotificationType
	User       string
	Mailbox    string
	MessageUID uint32
	SeqNum     uint32
	Flags      []string
	Timestamp  time.Time
}

// NotificationHub manages notifications for IMAP IDLE and other real-time features
type NotificationHub struct {
	subscribers map[string][]chan MailboxNotification // user -> channels
	mu          sync.RWMutex

	// publisher, when set, mirrors every locally originated notification onto the
	// cluster so other nodes can wake their own IDLE/SSE subscribers. It is set
	// once at startup and read on every Notify, so it is guarded separately.
	pubMu     sync.RWMutex
	publisher func(MailboxNotification)
}

// NewNotificationHub creates a new notification hub
func NewNotificationHub() *NotificationHub {
	return &NotificationHub{
		subscribers: make(map[string][]chan MailboxNotification),
	}
}

// SetPublisher installs a cluster fan-out sink. After this, every notification
// raised on this node (via Notify) is also handed to fn for cross-node
// propagation. Pass nil to detach. Notifications arriving FROM the cluster must
// be delivered with DeliverLocal, never Notify, to avoid a re-publish loop.
func (h *NotificationHub) SetPublisher(fn func(MailboxNotification)) {
	h.pubMu.Lock()
	h.publisher = fn
	h.pubMu.Unlock()
}

func (h *NotificationHub) currentPublisher() func(MailboxNotification) {
	h.pubMu.RLock()
	defer h.pubMu.RUnlock()
	return h.publisher
}

// Subscribe subscribes a session to notifications for a user
func (h *NotificationHub) Subscribe(user string) chan MailboxNotification {
	ch := make(chan MailboxNotification, 100) // Buffer to prevent blocking

	h.mu.Lock()
	h.subscribers[user] = append(h.subscribers[user], ch)
	h.mu.Unlock()

	return ch
}

// Unsubscribe removes a subscription
func (h *NotificationHub) Unsubscribe(user string, ch chan MailboxNotification) {
	h.mu.Lock()
	defer h.mu.Unlock()

	channels := h.subscribers[user]
	for i, c := range channels {
		if c == ch {
			// Close and remove the channel
			close(c)
			h.subscribers[user] = append(channels[:i], channels[i+1:]...)
			break
		}
	}
}

// Notify sends a notification to all subscribers for a user on this node and,
// when a cluster publisher is set, mirrors it to the other nodes.
func (h *NotificationHub) Notify(user string, notification MailboxNotification) {
	notification.User = user
	notification.Timestamp = time.Now()

	h.deliverLocal(notification)

	if pub := h.currentPublisher(); pub != nil {
		pub(notification)
	}
}

// DeliverLocal fans a notification out to this node's subscribers WITHOUT
// re-publishing it to the cluster. It is the entry point for notifications
// relayed in from other nodes, breaking the cross-node propagation loop.
func (h *NotificationHub) DeliverLocal(notification MailboxNotification) {
	h.deliverLocal(notification)
}

// deliverLocal performs the local subscriber fan-out for notification.User.
func (h *NotificationHub) deliverLocal(notification MailboxNotification) {
	h.mu.RLock()
	channels := h.subscribers[notification.User]
	h.mu.RUnlock()

	for _, ch := range channels {
		// Non-blocking send; drop if subscriber is slow
		select {
		case ch <- notification:
		default:
			// Channel is full or blocked, skip this notification
		}
	}
}

// NotifyNewMessage notifies subscribers about a new message
func (h *NotificationHub) NotifyNewMessage(user, mailbox string, uid, seqNum uint32) {
	h.Notify(user, MailboxNotification{
		Type:       NotificationNewMessage,
		User:       user,
		Mailbox:    mailbox,
		MessageUID: uid,
		SeqNum:     seqNum,
	})
}

// NotifyExpunge notifies subscribers about an expunged message
func (h *NotificationHub) NotifyExpunge(user, mailbox string, uid, seqNum uint32) {
	h.Notify(user, MailboxNotification{
		Type:       NotificationExpunge,
		User:       user,
		Mailbox:    mailbox,
		MessageUID: uid,
		SeqNum:     seqNum,
	})
}

// NotifyFlagsChanged notifies subscribers about flag changes
func (h *NotificationHub) NotifyFlagsChanged(user, mailbox string, uid, seqNum uint32, flags []string) {
	h.Notify(user, MailboxNotification{
		Type:       NotificationFlagsChanged,
		User:       user,
		Mailbox:    mailbox,
		MessageUID: uid,
		SeqNum:     seqNum,
		Flags:      flags,
	})
}

// NotifyMailboxUpdate notifies subscribers about general mailbox updates
func (h *NotificationHub) NotifyMailboxUpdate(user, mailbox string) {
	h.Notify(user, MailboxNotification{
		Type:    NotificationMailboxUpdate,
		User:    user,
		Mailbox: mailbox,
	})
}

// Global notification hub instance
var globalHub = NewNotificationHub()

// GetNotificationHub returns the global notification hub
func GetNotificationHub() *NotificationHub {
	return globalHub
}
