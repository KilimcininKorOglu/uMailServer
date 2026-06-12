package nspi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// payloadAfterMeta returns the binary response body that follows the MAPI/HTTP
// meta-tag block.
func payloadAfterMeta(t *testing.T, body []byte) []byte {
	t.Helper()
	_, payload, found := bytes.Cut(body, []byte("\r\n\r\n"))
	if !found {
		t.Fatalf("response has no meta block: %q", body)
	}
	return payload
}

// bindRequestBody builds a minimal Bind request (no state block).
func bindRequestBody() []byte {
	p := wire.NewPush(0)
	p.Uint32(0) // bind flags
	p.Uint8(0)  // no state block
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// doBind runs Bind for the given email and returns the sid cookie.
func doBind(t *testing.T, srv *Server, email string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(bindRequestBody()))
	req.Header.Set("X-RequestType", "Bind")
	req = req.WithContext(WithEmail(req.Context(), email))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bind status = %d, want 200", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "sid" {
			return c.Value
		}
	}
	t.Fatal("bind set no sid cookie")
	return ""
}

// TestBindCreatesSession verifies an authenticated Bind succeeds, returns the
// server GUID, and registers a session.
func TestBindCreatesSession(t *testing.T) {
	srv := NewServer()
	sid := doBind(t, srv, "qa.bob@local.test")

	if srv.getSession(sid) == nil {
		t.Fatal("bind did not register a session")
	}
	// The server GUID is stable across binds.
	sid2 := doBind(t, srv, "qa.bob@local.test")
	if srv.getSession(sid).guid != srv.getSession(sid2).guid {
		t.Error("server GUID is not stable across sessions")
	}
}

// TestBindWithoutEmailFails verifies a Bind with no authenticated email returns
// an error result and no session.
func TestBindWithoutEmailFails(t *testing.T) {
	srv := NewServer()
	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(bindRequestBody()))
	req.Header.Set("X-RequestType", "Bind")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	q := wire.NewPull(payloadAfterMeta(t, rec.Body.Bytes()), 0)
	q.Uint32() // status
	if result := q.Uint32(); result != ecError {
		t.Errorf("unauthenticated bind result = %#x, want ecError", result)
	}
}

// TestUnbindDropsSession verifies Unbind reports the NSPI success code and drops
// the session.
func TestUnbindDropsSession(t *testing.T) {
	srv := NewServer()
	sid := doBind(t, srv, "qa.bob@local.test")

	ub := wire.NewPush(0)
	ub.Uint32(0) // reserved
	ub.Uint32(0) // cb_auxin
	req := httptest.NewRequest(http.MethodPost, "/mapi/nspi", bytes.NewReader(ub.Bytes()))
	req.Header.Set("X-RequestType", "Unbind")
	req.AddCookie(&http.Cookie{Name: "sid", Value: sid})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	q := wire.NewPull(payloadAfterMeta(t, rec.Body.Bytes()), 0)
	q.Uint32() // status
	if result := q.Uint32(); result != ecUnbindSuccess {
		t.Errorf("unbind result = %#x, want ecUnbindSuccess", result)
	}
	if srv.getSession(sid) != nil {
		t.Error("unbind did not drop the session")
	}
}
