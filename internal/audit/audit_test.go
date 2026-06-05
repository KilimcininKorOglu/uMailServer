package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewLogger_Disabled(t *testing.T) {
	logger, err := NewLogger("", 0, 0, 0)
	if err != nil {
		t.Fatalf("NewLogger with empty path failed: %v", err)
	}
	if logger.writer != nil {
		t.Error("expected nil writer for disabled audit logging")
	}
}

func TestNewLogger_CreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "subdir", "audit.log")

	logger, err := NewLogger(logPath, 1, 1, 1)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	if logger.writer == nil {
		t.Error("expected non-nil writer")
	}
}

func TestLogger_WriteEvent(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "audit.log")

	logger, err := NewLogger(logPath, 1, 1, 1)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	event := Event{
		Type:    LoginSuccess,
		User:    "test@example.com",
		IP:      "192.168.1.1",
		Success: true,
		Service: "api",
	}

	if err := logger.Log(event); err != nil {
		t.Fatalf("Log failed: %v", err)
	}
}

func TestLogger_LogLoginSuccess(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "audit.log")

	logger, err := NewLogger(logPath, 1, 1, 1)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	logger.LogLoginSuccess("user@example.com", "10.0.0.1")
}

func TestLogger_LogLoginFailure(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "audit.log")

	logger, err := NewLogger(logPath, 1, 1, 1)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	logger.LogLoginFailure("user@example.com", "10.0.0.1", "invalid_password")
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		name     string
		remote   string
		headers  map[string]string
		expected string
	}{
		{
			name:     "direct connection",
			remote:   "192.168.1.1:8080",
			headers:  map[string]string{},
			expected: "192.168.1.1",
		},
		{
			name:   "with X-Forwarded-For",
			remote: "10.0.0.1:8080",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.1, 10.0.0.1",
			},
			expected: "203.0.113.1",
		},
		{
			name:   "with X-Real-IP",
			remote: "10.0.0.1:8080",
			headers: map[string]string{
				"X-Real-IP": "203.0.113.2",
			},
			expected: "203.0.113.2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			req.RemoteAddr = tt.remote
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			ip := ExtractIP(req)
			if ip != tt.expected {
				t.Errorf("ExtractIP() = %s, want %s", ip, tt.expected)
			}
		})
	}
}

func TestLogger_LogAccountCreate(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "audit.log")

	logger, err := NewLogger(logPath, 1, 1, 1)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	logger.LogAccountCreate("admin@example.com", "newuser@example.com", "10.0.0.1")
}

func TestLogger_LogTOTPEnable(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "audit.log")

	logger, err := NewLogger(logPath, 1, 1, 1)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	logger.LogTOTPEnable("user@example.com", "user@example.com", "10.0.0.1")
}

func TestLogger_LogTenant(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "audit.log")

	logger, err := NewLogger(logPath, 1, 1, 1)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.LogTenant(TenantSuspend, "operator@example.com", "acme", "10.0.0.1")
	if err := logger.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var evt Event
	if err := json.Unmarshal(data, &evt); err != nil {
		t.Fatalf("Unmarshal failed: %v (raw=%q)", err, data)
	}
	// The per-tenant trail is worthless if the affected tenant is not tagged,
	// so assert the tenant id and the lifecycle event type round-trip.
	if evt.Tenant != "acme" {
		t.Errorf("Tenant = %q, want %q", evt.Tenant, "acme")
	}
	if evt.Type != TenantSuspend {
		t.Errorf("Type = %q, want %q", evt.Type, TenantSuspend)
	}
	if evt.User != "operator@example.com" {
		t.Errorf("User = %q, want %q", evt.User, "operator@example.com")
	}
}

func TestLogger_Rotation(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "audit.log")

	// Very small max size to trigger rotation quickly
	logger, err := NewLogger(logPath, 1, 2, 1)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Write enough events to trigger rotation
	for i := 0; i < 100; i++ {
		_ = logger.Log(Event{
			Type:    LoginSuccess,
			User:    "test@example.com",
			IP:      "192.168.1.1",
			Success: true,
			Service: "api",
		})
	}

	// Wait a bit for rotation to complete
	time.Sleep(100 * time.Millisecond)
}
