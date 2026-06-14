package server

import (
	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/mapi/emsmdb"
)

// emsmdbNotifier bridges the shared IMAP notification hub to the emsmdb push path,
// satisfying emsmdb.NotificationSource. RopRegisterNotification subscribes through it,
// so a MAPI/HTTP client receives the same mailbox-change events that drive IMAP IDLE
// and webmail SSE — one canonical event stream, not a MAPI-local mechanism. It keeps
// the emsmdb package free of any imap import (the adapter does the type translation).
type emsmdbNotifier struct{}

// Subscribe registers with the global notification hub and returns a channel of
// surface-neutral events plus a cancel func. The hub subscription is registered
// synchronously before returning (the hub's Subscribe appends the channel under its
// lock), so a change raised immediately after registration is captured by the hub's
// buffered channel rather than lost. A pump goroutine translates the hub's
// notifications into emsmdb events and exits when cancel unsubscribes (which closes the
// hub channel); a non-blocking forward drops events when the consumer is behind,
// matching the hub's own back-pressure policy.
func (emsmdbNotifier) Subscribe(email string) (<-chan emsmdb.MailboxEvent, func()) {
	hub := imap.GetNotificationHub()
	in := hub.Subscribe(email)
	out := make(chan emsmdb.MailboxEvent, 100)
	go func() {
		defer close(out)
		for n := range in {
			if n.Type != imap.NotificationNewMessage {
				continue // the connector currently pushes new-mail notifications only
			}
			select {
			case out <- emsmdb.MailboxEvent{Mailbox: n.Mailbox, UID: n.MessageUID}:
			default:
			}
		}
	}()
	return out, func() { hub.Unsubscribe(email, in) }
}
