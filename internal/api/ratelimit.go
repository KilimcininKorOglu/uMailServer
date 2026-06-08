package api

import (
	"net/http"
	"path"
)

// handleRateLimitIPStats handles GET /api/v1/admin/ratelimits/ip/:ip
func (s *Server) handleRateLimitIPStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if s.rateLimitMgr == nil {
		s.sendError(w, http.StatusServiceUnavailable, "rate limiting not available")
		return
	}

	ip := path.Base(r.URL.Path)
	if ip == "" || ip == "ip" {
		s.sendError(w, http.StatusBadRequest, "IP address is required")
		return
	}

	stats := s.rateLimitMgr.GetIPStats(ip)
	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"ip":    ip,
		"stats": stats,
	})
}

// handleRateLimitUserStats handles GET /api/v1/admin/ratelimits/user/:user
func (s *Server) handleRateLimitUserStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if s.rateLimitMgr == nil {
		s.sendError(w, http.StatusServiceUnavailable, "rate limiting not available")
		return
	}

	user := path.Base(r.URL.Path)
	if user == "" || user == "user" {
		s.sendError(w, http.StatusBadRequest, "username is required")
		return
	}

	stats := s.rateLimitMgr.GetUserStats(user)
	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"user":  user,
		"stats": stats,
	})
}
