package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AdminConfig holds configuration for the admin server
type AdminConfig struct {
	Addr              string            // e.g., "127.0.0.1:8443"
	JWTSecret         string            // Legacy single secret
	JWTSecretVersions map[string]string // kid -> secret, for key rotation
	DisableLegacyJWT  bool              // When true, disables fallback to legacy JWTSecret after kid rotation
	AuditLog          AuditLogConfig
}

// AdminServer is a lightweight HTTP server for admin panel access.
// It serves on a separate port bound to localhost only.
// It embeds the main Server to reuse its handlers.
type AdminServer struct {
	*Server    // Embed main server to reuse handlers
	config     AdminConfig
	httpServer *http.Server
}

// NewAdminServer creates a new admin-only HTTP server
// It shares the main Server's handlers but runs on a separate port
func NewAdminServer(server *Server, cfg AdminConfig) *AdminServer {
	s := &AdminServer{
		Server: server,
		config: cfg,
	}
	return s
}

// Start starts the admin HTTP server
func (s *AdminServer) Start() error {
	s.httpServer = &http.Server{
		Addr:         s.config.Addr,
		Handler:      s.router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Info("Admin API server starting", "addr", s.config.Addr)
	return s.httpServer.ListenAndServe()
}

// Stop gracefully stops the admin server
func (s *AdminServer) Stop() error {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// router sets up the admin-only HTTP routes
func (s *AdminServer) router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
	})

	// Admin panel static files
	mux.HandleFunc("/admin/", s.handleAdmin)

	// Health check - delegate to embedded server's handler
	mux.HandleFunc("/health", s.Server.handleHealth)

	// Metrics - delegate to embedded server's handler
	mux.Handle("/metrics", s.Server.authMiddleware(s.Server.adminMiddleware(http.HandlerFunc(s.Server.handleMetrics))))

	// Admin auth/session routes for the SPA
	mux.Handle("/api/v1/auth/login", s.Server.limitBodyMiddleware(http.HandlerFunc(s.Server.handleLogin)))
	mux.Handle("/api/v1/auth/logout", s.Server.rateLimitMiddleware(s.Server.authMiddleware(http.HandlerFunc(s.Server.handleLogout))))
	mux.Handle("/api/v1/auth/refresh", s.Server.rateLimitMiddleware(s.Server.authMiddleware(http.HandlerFunc(s.Server.handleRefresh))))
	mux.Handle("/api/v1/events", s.Server.authMiddleware(s.Server.sseServer.Handler()))

	// Admin API routes (all require admin auth)
	api := http.NewServeMux()
	s.Server.registerAdminAPIRoutes(api)
	apiHandler := s.Server.rateLimitMiddleware(s.Server.limitBodyMiddleware(s.Server.securityHeadersMiddleware(s.Server.csrfMiddleware(s.Server.corsMiddleware(s.Server.authMiddleware(api))))))
	mux.Handle("/api/v1/", apiHandler)

	return mux
}

// withAuth middleware requires valid JWT authentication
func (s *AdminServer) withAuth(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || len(authHeader) < 7 {
			writeError(w, "unauthorized", "Missing authorization header", http.StatusUnauthorized)
			return
		}
		tokenStr := authHeader[7:]

		// Parse JWT - use versioned secrets from embedded Server
		parsed, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			// Try kid-based secret lookup first
			if kid, ok := t.Header["kid"].(string); ok && kid != "" {
				if kidSecret, ok := s.Server.jwtSecrets[kid]; ok {
					return []byte(kidSecret), nil
				}
			}
			// Fall back to current kid
			if secret, ok := s.Server.jwtSecrets[s.Server.currentKid]; ok {
				return []byte(secret), nil
			}
			// Last resort: try legacy JWTSecret only if not disabled
			if !s.config.DisableLegacyJWT {
				return []byte(s.config.JWTSecret), nil
			}
			return nil, fmt.Errorf("unknown signing key")
		})
		if err != nil || !parsed.Valid {
			writeError(w, "unauthorized", "Invalid token", http.StatusUnauthorized)
			return
		}

		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			writeError(w, "unauthorized", "Invalid claims", http.StatusUnauthorized)
			return
		}

		user, _ := claims["sub"].(string)
		isAdmin, _ := claims["admin"].(bool)
		mustChangePasswordClaim, _ := claims[passwordChangeRequiredClaim].(bool)

		// Validate that we got valid values
		if user == "" {
			writeError(w, "unauthorized", "Invalid token: missing subject", http.StatusUnauthorized)
			return
		}
		mustChangePassword, err := enforceAuthenticatedAccount(s.Server.db, user, mustChangePasswordClaim)
		if err != nil {
			writeError(w, "unauthorized", "Invalid token", http.StatusUnauthorized)
			return
		}
		if mustChangePassword {
			writeError(w, "password_change_required", "Password change required", http.StatusForbidden)
			return
		}

		ctx := context.WithValue(r.Context(), "user", user)
		ctx = context.WithValue(ctx, "isAdmin", isAdmin)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// adminMiddleware ensures user is an admin
func (s *AdminServer) adminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isAdmin, ok := r.Context().Value("isAdmin").(bool)
		if !ok || !isAdmin {
			writeError(w, "forbidden", "Admin access required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, errCode, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(map[string]interface{}{
		"error":   errCode,
		"message": message,
		"code":    status,
	})
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// handleAdmin serves the admin panel static files
func (s *AdminServer) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if s.Server.adminFS == nil {
		http.Error(w, "Admin filesystem not configured", http.StatusInternalServerError)
		return
	}

	path := r.URL.Path
	if path == "/admin" || path == "/admin/" {
		path = "/admin/"
	}

	// Remove /admin prefix to get file path
	filePath := strings.TrimPrefix(path, "/admin/")
	if filePath == "" {
		filePath = "index.html"
	}

	// Try to serve the file
	data, err := s.Server.adminFS.Open(filePath)
	if err != nil {
		// Try index.html for SPA routing
		data, err = s.Server.adminFS.Open("index.html")
		if err != nil {
			http.Error(w, "Admin panel not found", http.StatusNotFound)
			return
		}
	}
	defer data.Close()

	contentType := getContentType(filePath)
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	io.Copy(w, data)
}

// getContentType returns MIME type for static files
func getContentType(path string) string {
	switch {
	case strings.HasSuffix(path, ".html"):
		return "text/html"
	case strings.HasSuffix(path, ".js"):
		return "application/javascript"
	case strings.HasSuffix(path, ".css"):
		return "text/css"
	case strings.HasSuffix(path, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(path, ".png"):
		return "image/png"
	case strings.HasSuffix(path, ".ico"):
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}
