package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// deadlineWriter is a minimal ResponseWriter that also supports a write
// deadline — the capability http.ResponseController looks for, and the one the
// real *http.response provides. It records the last deadline set through it.
type deadlineWriter struct {
	http.ResponseWriter
	deadline    time.Time
	deadlineSet bool
}

func (w *deadlineWriter) SetWriteDeadline(t time.Time) error {
	w.deadline, w.deadlineSet = t, true
	return nil
}

// TestStatusCaptureWriter_UnwrapReachesDeadline proves the tracing wrapper is
// transparent to http.ResponseController: a handler wrapped by the enabled
// middleware can still lift the write deadline. This is load-bearing for the
// ActiveSync Ping, which clears the deadline to hold the connection past the
// listener's WriteTimeout; without Unwrap the controller stops at the wrapper
// and SetWriteDeadline returns ErrNotSupported, silently capping the heartbeat.
func TestStatusCaptureWriter_UnwrapReachesDeadline(t *testing.T) {
	underlying := &deadlineWriter{ResponseWriter: httptest.NewRecorder()}
	wrapped := &statusCaptureWriter{ResponseWriter: underlying, status: http.StatusOK}

	if err := http.NewResponseController(wrapped).SetWriteDeadline(time.Time{}); err != nil {
		t.Fatalf("SetWriteDeadline through wrapper: %v (Ping would degrade to a sub-WriteTimeout heartbeat)", err)
	}
	if !underlying.deadlineSet {
		t.Fatal("deadline did not reach the underlying writer through Unwrap")
	}
}

func TestHTTPMiddleware_NilProvider_PassesThrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	HTTPMiddleware(nil, "http", next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler should have been called")
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status passthrough broken: got %d", rec.Code)
	}
}

func TestHTTPMiddleware_DisabledProvider_PassesThrough(t *testing.T) {
	provider, err := NewProvider(Config{Enabled: false})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/y", nil)
	HTTPMiddleware(provider, "caldav", next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler should run when provider disabled")
	}
}

func TestHTTPMiddleware_EnabledProvider_RecordsAttributes(t *testing.T) {
	provider, err := NewProvider(Config{
		Enabled: true, ServiceName: "test", Exporter: "noop", SampleRate: 1.0,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Stop(context.Background()) })

	body := []byte("hi")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(body)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/cal/foo.ics", nil)
	req.Header.Set("User-Agent", "ua-test")
	HTTPMiddleware(provider, "caldav", next).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != "hi" {
		t.Errorf("body passthrough broken: got %q", got)
	}
}

func TestHTTPMiddleware_EmptyComponentDefaultsToHTTP(t *testing.T) {
	provider, err := NewProvider(Config{
		Enabled: true, ServiceName: "test", Exporter: "noop", SampleRate: 1.0,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Stop(context.Background()) })

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	HTTPMiddleware(provider, "", next).ServeHTTP(rec, req)
	// Span name internal — we just verify no panic and request flows.
}

func TestHTTPMiddleware_ServerError(t *testing.T) {
	provider, err := NewProvider(Config{
		Enabled: true, ServiceName: "test", Exporter: "noop", SampleRate: 1.0,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Stop(context.Background()) })

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/x", nil)
	HTTPMiddleware(provider, "http", next).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status passthrough broken: got %d", rec.Code)
	}
}

func TestHTTPMiddleware_ClientError(t *testing.T) {
	provider, err := NewProvider(Config{
		Enabled: true, ServiceName: "test", Exporter: "noop", SampleRate: 1.0,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Stop(context.Background()) })

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/missing", nil)
	HTTPMiddleware(provider, "http", next).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status passthrough broken: got %d", rec.Code)
	}
}
