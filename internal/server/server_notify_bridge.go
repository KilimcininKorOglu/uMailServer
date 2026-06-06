package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/umailserver/umailserver/internal/imap"
)

// imapNotifyChannel is the Redis pub/sub channel carrying mailbox-change
// notifications between nodes. Both IMAP IDLE and webmail SSE feed off the same
// in-process NotificationHub, so bridging the hub propagates both surfaces.
const imapNotifyChannel = "umailserver:imap:notify"

// notifyPublishBuffer bounds how many outbound notifications may queue before
// the bridge drops the oldest-arriving ones. Notifications are best-effort
// wake-ups, so dropping under burst is preferable to slowing local delivery.
const notifyPublishBuffer = 1024

// notifyEnvelope wraps a notification with its origin node so the inbound side
// can drop the copy Redis echoes back to the publisher — otherwise a single
// node would deliver each of its own notifications twice (once directly, once
// via the echo).
type notifyEnvelope struct {
	Origin       string                   `json:"origin"`
	Notification imap.MailboxNotification `json:"notification"`
}

// startNotificationBridge mirrors mailbox-change notifications across the
// cluster: locally raised notifications are published to Redis, and
// notifications from other nodes are delivered to this node's IDLE/SSE
// subscribers. No-op when un-clustered. Exits when s.ctx is canceled.
func (s *Server) startNotificationBridge() {
	if s.clusterManager == nil {
		return
	}
	ps := s.clusterManager.PubSub()
	if ps == nil {
		return
	}

	origin := s.clusterManager.InstanceID()
	hub := imap.GetNotificationHub()

	// Outbound: Notify hands each local notification to this non-blocking sink,
	// and a single goroutine drains it to Redis so delivery is never blocked on
	// a Redis round trip.
	outbound := make(chan imap.MailboxNotification, notifyPublishBuffer)
	hub.SetPublisher(func(n imap.MailboxNotification) {
		select {
		case outbound <- n:
		default:
			// Buffer full: drop. The client reconciles on its next poll/IDLE.
		}
	})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-s.ctx.Done():
				return
			case n := <-outbound:
				data, err := json.Marshal(notifyEnvelope{Origin: origin, Notification: n})
				if err != nil {
					s.logger.Warn("cluster notify: marshal failed", "error", err)
					continue
				}
				ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
				if perr := ps.Publish(ctx, imapNotifyChannel, data); perr != nil {
					s.logger.Warn("cluster notify: publish failed", "error", perr)
				}
				cancel()
			}
		}
	}()

	// Inbound: deliver notifications from other nodes to local subscribers only
	// (DeliverLocal, never Notify) so they are not re-published into a loop.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		sub, err := ps.Subscribe(s.ctx, imapNotifyChannel)
		if err != nil {
			s.logger.Error("cluster notify: subscribe failed; cross-node notifications disabled", "error", err)
			return
		}
		for {
			select {
			case <-s.ctx.Done():
				return
			case payload, ok := <-sub:
				if !ok {
					return
				}
				var env notifyEnvelope
				if uerr := json.Unmarshal(payload, &env); uerr != nil {
					s.logger.Warn("cluster notify: unmarshal failed", "error", uerr)
					continue
				}
				if env.Origin == origin {
					// Redis echoes our own publish back to us; we already
					// delivered it locally in Notify. Skip to avoid a double.
					continue
				}
				hub.DeliverLocal(env.Notification)
			}
		}
	}()

	s.logger.Info("cluster notification bridge started", "channel", imapNotifyChannel)
}
