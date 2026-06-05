package server

import (
	"context"
	"time"

	"github.com/umailserver/umailserver/internal/cluster"
)

// startClusterHeartbeat runs the per-node cluster maintenance loop while
// clustering is enabled: every few seconds it acquires or refreshes cluster
// leadership and records a heartbeat tagged with the current leadership status,
// so every node sees this one as alive and so exactly one node reports as
// leader. No-op when un-clustered. The goroutine exits when s.ctx is canceled.
func (s *Server) startClusterHeartbeat() {
	if s.clusterManager == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		s.clusterTick() // immediate first pass so leadership is live at once
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.clusterTick()
			}
		}
	}()
}

// clusterTick performs one leadership + heartbeat cycle.
func (s *Server) clusterTick() {
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()

	le := s.clusterManager.LeaderElection()
	hm := s.clusterManager.HealthMonitor()

	// Acquire leadership if it is free, or refresh it if we already hold it.
	isLeader, err := le.IsLeader(ctx, cluster.LeaderElectionKey)
	switch {
	case err != nil:
		s.logger.Warn("cluster: leadership check failed", "error", err)
	case isLeader:
		if rerr := le.Refresh(ctx, cluster.LeaderElectionKey); rerr != nil {
			s.logger.Warn("cluster: leader refresh failed", "error", rerr)
			isLeader = false
		}
	default:
		if ok, aerr := le.TryAcquire(ctx, cluster.LeaderElectionKey); aerr != nil {
			s.logger.Warn("cluster: leader acquire failed", "error", aerr)
		} else if ok {
			isLeader = true
			s.logger.Info("cluster: acquired leadership")
		}
	}

	// Record the heartbeat carrying the current leadership status.
	if herr := hm.RecordHeartbeat(ctx, isLeader); herr != nil {
		s.logger.Warn("cluster: heartbeat failed", "error", herr)
	}
}

// IsClusterLeader reports whether this node may run the singleton side-effect
// loops (queue delivery, ACME renewal, scheduler, alerts). An un-clustered
// single node is always its own leader; a clustered node defers to the Redis
// leader election. Used to leader-gate those loops so they fire exactly once.
func (s *Server) IsClusterLeader() bool {
	if s.clusterManager == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	leader, err := s.clusterManager.LeaderElection().IsLeader(ctx, cluster.LeaderElectionKey)
	if err != nil {
		// Fail safe: a node that cannot confirm leadership does NOT run the
		// singleton loops, so a partition cannot produce double actuation.
		s.logger.Warn("cluster: leadership check failed; treating as non-leader", "error", err)
		return false
	}
	return leader
}
