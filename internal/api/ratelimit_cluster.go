package api

import (
	"context"
	"time"

	"github.com/umailserver/umailserver/internal/cluster"
)

// This file routes the ephemeral auth rate-limit and lockout counters through a
// shared Redis store when clustering is active, so brute-force and lockout
// protection holds across nodes behind a round-robin load balancer instead of
// being reset by hitting a different node. The counters are loss-safe (not a
// source of truth), so on any Redis error the caller falls back to allowing the
// request: a coordination-layer blip must not lock every node's users out. When
// this node runs standalone (clusterCounters returns nil) the original
// in-memory maps remain authoritative and unchanged.

// rlOpTimeout bounds each shared-counter Redis round trip on the auth hot path.
const rlOpTimeout = 2 * time.Second

// clusterCounters returns the shared counter store when clustering is active,
// or nil when this node runs standalone.
func (s *Server) clusterCounters() cluster.CounterStore {
	if s.clusterMgr == nil {
		return nil
	}
	return s.clusterMgr.Counters()
}

func rlCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), rlOpTimeout)
}

// Namespaced keys for each counter. The caller already validates/derives the IP
// or email, so they are used verbatim under a per-counter prefix.
func rlLoginCountKey(ip string) string { return "rl:login:cnt:" + ip }
func rlLoginLockKey(ip string) string  { return "rl:login:lock:" + ip }
func rlAccountKey(email string) string { return "rl:acct:" + email }
func rlTOTPKey(email string) string    { return "rl:totp:" + email }
func rlAPIKey(ip string) string        { return "rl:api:" + ip }
