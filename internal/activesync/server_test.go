package activesync

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// allowAuth is an auth stub that accepts every request as a fixed mailbox.
func allowAuth(_ *http.Request) (string, bool) { return "bob@x.test", true }

// denyAuth is an auth stub that rejects every request.
func denyAuth(_ *http.Request) (string, bool) { return "", false }

// TestOptionsAdvertisesProtocol checks the OPTIONS response carries the protocol
// version and command advertisement an EAS client reads to negotiate — without
// it, clients refuse to provision.
func TestOptionsAdvertisesProtocol(t *testing.T) {
	s := NewServer(allowAuth)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/Microsoft-Server-ActiveSync", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("OPTIONS status = %d, want 200", rec.Code)
	}
	if v := rec.Header().Get("MS-ASProtocolVersions"); !strings.Contains(v, "16.1") {
		t.Errorf("MS-ASProtocolVersions = %q, want it to include 16.1", v)
	}
	if c := rec.Header().Get("MS-ASProtocolCommands"); !strings.Contains(c, "Provision") || !strings.Contains(c, "FolderSync") {
		t.Errorf("MS-ASProtocolCommands = %q, want Provision and FolderSync", c)
	}
	if rec.Header().Get("MS-Server-ActiveSync") == "" {
		t.Errorf("MS-Server-ActiveSync header missing")
	}
}

// TestPostRequiresAuth checks an unauthenticated POST is challenged, not served.
func TestPostRequiresAuth(t *testing.T) {
	s := NewServer(denyAuth)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=FolderSync", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth POST status = %d, want 401", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic") {
		t.Errorf("WWW-Authenticate = %q, want a Basic challenge", rec.Header().Get("WWW-Authenticate"))
	}
}

// TestPostDispatch checks an authenticated POST reaches the registered handler
// and that the handler's body and the WBXML content type are returned.
func TestPostDispatch(t *testing.T) {
	s := NewServer(allowAuth)
	var gotEmail, gotBody string
	s.Handle("FolderSync", func(ctx *Context) ([]byte, error) {
		gotEmail = ctx.Email
		gotBody = string(ctx.Body)
		return []byte{0x03, 0x01, 0x6A, 0x00}, nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=FolderSync", strings.NewReader("REQ"))
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch status = %d, want 200", rec.Code)
	}
	if gotEmail != "bob@x.test" || gotBody != "REQ" {
		t.Errorf("handler saw email=%q body=%q, want bob@x.test/REQ", gotEmail, gotBody)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.ms-sync.wbxml" {
		t.Errorf("Content-Type = %q, want application/vnd.ms-sync.wbxml", ct)
	}
}

// TestUnknownCommand checks an unimplemented command is reported (501), not
// silently accepted, so a client falls back rather than hanging.
func TestUnknownCommand(t *testing.T) {
	s := NewServer(allowAuth)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=Nonsense", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("unknown Cmd status = %d, want 501", rec.Code)
	}
}

// TestMissingCommand checks a POST with no Cmd is a bad request.
func TestMissingCommand(t *testing.T) {
	s := NewServer(allowAuth)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Cmd status = %d, want 400", rec.Code)
	}
}
