package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handleSessions returns all active sessions for the authenticated user.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userVal := r.Context().Value("user")
	if userVal == nil {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	email, ok := userVal.(string)
	if !ok {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	sessions, err := s.db.ListClientSessionsByEmail(email)
	if err != nil {
		s.logger.Error("failed to list sessions", "error", err, "email", email)
		s.sendError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}

	// Build response matching frontend expectations
	type SessionInfo struct {
		ID         string `json:"id"`
		DeviceType string `json:"device_type"`
		ClientIP   string `json:"client_ip"`
		CreatedAt  string `json:"created_at"`
		LastActive string `json:"last_active"`
		UserAgent  string `json:"user_agent"`
	}

	result := make([]SessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		result = append(result, SessionInfo{
			ID:         sess.ID,
			DeviceType: sess.DeviceType,
			ClientIP:   sess.ClientIP,
			CreatedAt:  sess.CreatedAt.Format(time.RFC3339),
			LastActive: sess.LastActive.Format(time.RFC3339),
			UserAgent:  sess.UserAgent,
		})
	}

	s.sendJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": result,
	})
}

// handleSessionRevoke handles DELETE /api/v1/sessions/{sessionId}
func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userVal := r.Context().Value("user")
	if userVal == nil {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	email, ok := userVal.(string)
	if !ok {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Extract session ID from path: /api/v1/sessions/{id}
	sessionID := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	if sessionID == "" || sessionID == r.URL.Path {
		s.sendError(w, http.StatusBadRequest, "missing session ID")
		return
	}

	// Get the session to verify ownership
	session, err := s.db.GetClientSession(sessionID)
	if err != nil {
		s.sendError(w, http.StatusNotFound, "session not found")
		return
	}

	// Verify the session belongs to this user
	if session.Email != email {
		s.sendError(w, http.StatusNotFound, "session not found")
		return
	}

	// Check if this is the current session (matched by token hash)
	isCurrentSession := false
	currentTokenHashVal := r.Context().Value(contextKeyTokenHash)
	if currentTokenHashVal != nil {
		if currentTokenHash, ok := currentTokenHashVal.(string); ok {
			isCurrentSession = currentTokenHash != "" && session.TokenHash == currentTokenHash
		}
	}

	// Mark session as revoked
	session.Revoked = true
	if err := s.db.UpdateClientSession(session); err != nil {
		s.logger.Error("failed to revoke session", "error", err, "sessionID", sessionID)
		s.sendError(w, http.StatusInternalServerError, "failed to revoke session")
		return
	}

	// If revoking the current session, also revoke the token to force re-login
	if isCurrentSession {
		s.RevokeToken(session.TokenHash, time.Now().Add(s.config.TokenExpiry))
	}

	s.sendJSON(w, http.StatusOK, map[string]string{
		"message": "session revoked",
	})
}

// generateSessionID generates a unique session ID using crypto/rand.
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if crypto/rand fails (should not happen)
		return fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	return "sess-" + hex.EncodeToString(b)
}

// detectDeviceType parses a User-Agent string to determine the device type.
func detectDeviceType(userAgent string) string {
	ua := strings.ToLower(userAgent)
	if strings.Contains(ua, "mobile") || strings.Contains(ua, "android") ||
		strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad") ||
		strings.Contains(ua, "ipod") || strings.Contains(ua, "windows phone") ||
		strings.Contains(ua, "blackberry") {
		return "mobile"
	}
	if strings.Contains(ua, "tablet") || strings.Contains(ua, "ipad") ||
		strings.Contains(ua, "kindle") || strings.Contains(ua, "silk") {
		return "tablet"
	}
	if strings.Contains(ua, "mozilla/5.0") || strings.Contains(ua, "chrome/") ||
		strings.Contains(ua, "safari/") || strings.Contains(ua, "firefox/") ||
		strings.Contains(ua, "edge/") {
		return "desktop"
	}
	return "unknown"
}
