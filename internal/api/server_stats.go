package api

import (
	"net/http"

	"github.com/umailserver/umailserver/internal/db"
)

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Per-tenant observability: a tenant-admin sees only its own tenant's
	// domains/accounts; a super-admin sees the whole instance.
	ts := s.callerTenantScope(r)
	var domains []*db.DomainData
	var err error
	if ts.isTenantAdmin && !ts.isSuperAdmin {
		domains, err = s.db.ListDomainsByTenant(ts.tenantID)
	} else {
		domains, err = s.db.ListDomains()
	}
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to get stats")
		return
	}

	// Count accounts across the in-scope domains.
	accounts := 0
	for _, d := range domains {
		accts, _ := s.db.ListAccountsByDomain(d.Name)
		accounts += len(accts)
	}

	// The delivery queue is not yet tenant-partitioned, so only a super-admin
	// sees the global queue depth; a tenant-admin gets 0 (avoids leaking other
	// tenants' load) until per-tenant queue accounting lands.
	queueSize := 0
	if !ts.isTenantAdmin || ts.isSuperAdmin {
		if s.queueMgr != nil {
			if stats, err := s.queueMgr.GetStats(); err == nil {
				queueSize = stats.Pending + stats.Sending + stats.Failed
			}
		}
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"domains":    len(domains),
		"accounts":   accounts,
		"messages":   0, // Would need to scan maildirs
		"queue_size": queueSize,
	})
}
