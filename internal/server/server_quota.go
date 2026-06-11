package server

import (
	"context"
	"time"
)

// quotaReconcileInterval is how often the background reconciler drains the set
// of accounts whose mailbox size changed and resets their QuotaUsed counter.
const quotaReconcileInterval = 5 * time.Second

// markQuotaDirty flags an account for quota reconciliation. It is the callback
// the metadata store fires whenever a message is added to or removed from a
// mailbox (the canonical stored size changed). Cheap by design — it only
// records the email under a mutex, so it never slows a delivery/append/expunge.
func (s *Server) markQuotaDirty(email string) {
	if email == "" {
		return
	}
	s.quotaDirtyMu.Lock()
	if s.quotaDirty == nil {
		s.quotaDirty = make(map[string]struct{})
	}
	s.quotaDirty[email] = struct{}{}
	s.quotaDirtyMu.Unlock()
}

// startQuotaReconciler runs a background loop that periodically reconciles the
// QuotaUsed counter of every account whose mailbox size changed since the last
// tick. QuotaUsed is kept exact only at inbound delivery (the IncrementQuota
// reserve); IMAP APPEND / EWS / JMAP writes and every delete path change the
// canonical store without touching it, so the counter would otherwise drift
// (under-counting added mail, over-counting deleted mail). Reconciling from the
// canonical index — the authoritative sum of stored message sizes, the same
// figure mbsize reports — keeps it correct on every surface, coalescing a burst
// of changes into one recompute per account per tick. Self-healing: any path
// that ever mutates the index is covered, since the hook fires at the single
// StoreMessageMetadata/DeleteMessage chokepoint.
func (s *Server) startQuotaReconciler() {
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(quotaReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-parent.Done():
				return
			case <-ticker.C:
				s.drainQuotaDirty()
			}
		}
	}()
}

// drainQuotaDirty reconciles every account marked dirty since the last drain.
func (s *Server) drainQuotaDirty() {
	s.quotaDirtyMu.Lock()
	if len(s.quotaDirty) == 0 {
		s.quotaDirtyMu.Unlock()
		return
	}
	dirty := s.quotaDirty
	s.quotaDirty = make(map[string]struct{})
	s.quotaDirtyMu.Unlock()
	for email := range dirty {
		s.reconcileQuota(email)
	}
}

// reconcileQuota sets an account's QuotaUsed to the true size of its mailbox
// (the sum of stored message sizes across all folders). Fail-open: a scan or
// write error is logged and never blocks mail flow.
func (s *Server) reconcileQuota(email string) {
	if s.storageDB == nil || s.database == nil {
		return
	}
	user, domain := parseEmail(email)
	if user == "" || domain == "" {
		return
	}
	used, err := s.storageDB.MailboxUsedBytes(email)
	if err != nil {
		s.logger.Debug("quota reconcile: size scan failed", "email", email, "error", err)
		return
	}
	if err := s.database.SetQuotaUsed(domain, user, used); err != nil {
		s.logger.Debug("quota reconcile: set failed", "email", email, "error", err)
	}
}
