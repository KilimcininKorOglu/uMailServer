package server

import "time"

// push_dispatcher delivers EWS PushSubscription notifications. EWS push is a
// server-initiated POST: for each push subscription, the server polls the
// canonical lifecycle/watermark stream and POSTs a SendNotification envelope to
// the client's callback URL. The loop lives here (not in internal/ews) because
// it needs the subscription store, the lifecycle cursor, and the cluster leader
// gate the server owns; the EWS-shaped delivery (envelope + SSRF guard + POST)
// is ews.Server.DeliverPushNotification.

// startEWSPushDispatcher launches the background push-delivery loop. It is a
// no-op when EWS or the semantic store is not wired. The loop stops on ctx
// cancellation (graceful shutdown), like startAlertChecker.
func (s *Server) startEWSPushDispatcher() {
	if s.ewsServer == nil || s.semcoreStore == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.dispatchPushNotifications()
			}
		}
	}()
}

// dispatchPushNotifications delivers any pending events to each push
// subscription's callback URL. It is leader-gated so a cluster delivers each
// notification once; a single node is always leader. A delivery error leaves
// the subscription's cursor unadvanced so the next tick retries; an explicit
// client Unsubscribe (or a no-longer-permitted URL) drops the subscription.
func (s *Server) dispatchPushNotifications() {
	if !s.IsClusterLeader() {
		return
	}
	subsStore := s.semcoreStore.Subscriptions()
	lifecycle := s.semcoreStore.Lifecycle()

	subs, err := subsStore.ListPushSubscriptions()
	if err != nil {
		s.logger.Warn("ews push: list subscriptions failed", "error", err)
		return
	}
	for i := range subs {
		sub := subs[i]
		events, highestSeq, perr := lifecycle.PollEvents(sub.MailboxID, sub.LastSeq, 100)
		if perr != nil || len(events) == 0 {
			continue
		}
		remove, derr := s.ewsServer.DeliverPushNotification(&sub, events)
		if remove {
			//nolint:errcheck // best-effort drop; a re-list next tick is harmless
			_ = subsStore.RemoveSubscription(sub.ID)
			continue
		}
		if derr != nil {
			// Transient (network / non-2xx): keep the cursor so we retry.
			s.logger.Debug("ews push: delivery failed, will retry", "subscription", sub.ID.ID, "error", derr)
			continue
		}
		if uerr := subsStore.UpdateSubscriptionSeq(sub.ID, highestSeq); uerr != nil {
			s.logger.Warn("ews push: advance cursor failed", "subscription", sub.ID.ID, "error", uerr)
		}
	}
}
