package emsmdb

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// echoDispatcher returns the request ROP bytes and handles unchanged, so the
// transport can be exercised without the real ROP layer.
type echoDispatcher struct{}

func (echoDispatcher) Dispatch(_ *Session, ropData []byte, handlesIn []uint32, _ int) ([]byte, []uint32) {
	return ropData, handlesIn
}

// mapiBody strips the DONE meta-tag block and returns the binary response body.
func mapiBody(t *testing.T, raw []byte) []byte {
	t.Helper()
	meta, body, found := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !found {
		t.Fatalf("no meta-tag terminator in response: %q", raw)
	}
	if !bytes.Contains(meta, []byte("DONE")) {
		t.Errorf("meta block missing DONE: %q", meta)
	}
	return body
}

func doRequest(s *Server, reqType, sid, email string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mapi/emsmdb", bytes.NewReader(body))
	r.Header.Set("X-RequestType", reqType)
	if sid != "" {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
	}
	if email != "" {
		r = r.WithContext(WithEmail(r.Context(), email))
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// TestConnectIssuesSession verifies Connect returns a success body, the MAPI
// content type, and a session cookie.
func TestConnectIssuesSession(t *testing.T) {
	s := NewServer(echoDispatcher{})
	body := encodeConnectRequest(ConnectRequest{UserDN: "/o=x/cn=qa.bob", DefaultCodePage: 1252})
	w := doRequest(s, "Connect", "", "qa.bob@local.test", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/mapi-http" {
		t.Errorf("Content-Type = %q, want application/mapi-http", ct)
	}
	if w.Header().Get("X-ResponseCode") != "0" {
		t.Errorf("X-ResponseCode = %q, want 0", w.Header().Get("X-ResponseCode"))
	}
	sid := sidFromRecorder(t, w)
	if sid == "" {
		t.Fatal("Connect did not set a sid cookie")
	}
	resp := decodeConnectResponse(mapiBody(t, w.Body.Bytes()))
	if resp.StatusCode != 0 || resp.DisplayName != "qa.bob@local.test" {
		t.Errorf("connect response = %+v", resp)
	}
}

// TestConnectWithoutAuthDenied verifies an unauthenticated Connect is refused.
func TestConnectWithoutAuthDenied(t *testing.T) {
	s := NewServer(echoDispatcher{})
	body := encodeConnectRequest(ConnectRequest{UserDN: "/o=x/cn=nobody"})
	w := doRequest(s, "Connect", "", "", body)
	resp := decodeConnectResponse(mapiBody(t, w.Body.Bytes()))
	if resp.ErrorCode != ecAccessDenied {
		t.Errorf("ErrorCode = %#x, want ecAccessDenied", resp.ErrorCode)
	}
}

// TestExecuteRoundTripsThroughDispatcher verifies the Execute path decodes the
// ROP buffer, runs the dispatcher, and re-frames the response so the client
// gets the dispatcher's ROP bytes back.
func TestExecuteRoundTripsThroughDispatcher(t *testing.T) {
	s := NewServer(echoDispatcher{})
	connBody := encodeConnectRequest(ConnectRequest{UserDN: "/o=x/cn=qa.bob"})
	sid := sidFromRecorder(t, doRequest(s, "Connect", "", "qa.bob@local.test", connBody))

	ropIn := []byte{0x02, 0x01, 0xAA, 0xBB}
	handlesIn := []uint32{0xFFFFFFFF}
	execBody := encodeExecuteRequest(ExecuteRequest{
		RopBuffer: EncodeROPBuffer(0, ropIn, handlesIn, false),
		MaxRopOut: 0x10000,
	})
	w := doRequest(s, "Execute", sid, "", execBody)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	resp := decodeExecuteResponse(mapiBody(t, w.Body.Bytes()))
	if resp.StatusCode != 0 {
		t.Fatalf("execute status = %#x", resp.StatusCode)
	}
	_, ropOut, handlesOut, err := DecodeROPBuffer(resp.RopBuffer)
	if err != nil {
		t.Fatalf("decode response ROP buffer: %v", err)
	}
	if !bytes.Equal(ropOut, ropIn) || len(handlesOut) != 1 || handlesOut[0] != 0xFFFFFFFF {
		t.Errorf("dispatcher round-trip = % x / %v, want % x / [ffffffff]", ropOut, handlesOut, ropIn)
	}
}

// TestExecuteWithoutSession is refused with a general error.
func TestExecuteWithoutSession(t *testing.T) {
	s := NewServer(echoDispatcher{})
	execBody := encodeExecuteRequest(ExecuteRequest{RopBuffer: EncodeROPBuffer(0, []byte{0x02}, nil, false)})
	w := doRequest(s, "Execute", "no-such-sid", "", execBody)
	resp := decodeExecuteResponse(mapiBody(t, w.Body.Bytes()))
	if resp.ErrorCode != ecError {
		t.Errorf("ErrorCode = %#x, want ecError", resp.ErrorCode)
	}
}

// TestDisconnectAndUnknown covers the remaining request types.
func TestDisconnectAndUnknown(t *testing.T) {
	s := NewServer(echoDispatcher{})
	if w := doRequest(s, "Disconnect", "", "", nil); w.Code != http.StatusOK {
		t.Errorf("Disconnect status = %d, want 200", w.Code)
	}
	if w := doRequest(s, "Bogus", "", "", nil); w.Code != http.StatusBadRequest {
		t.Errorf("unknown type status = %d, want 400", w.Code)
	}
}

// encodeExecuteRequest builds an Execute request body for the tests.
func encodeExecuteRequest(r ExecuteRequest) []byte {
	p := wire.NewPush(0)
	p.Uint32(r.Flags)
	writeCounted(p, r.RopBuffer)
	p.Uint32(r.MaxRopOut)
	writeCounted(p, r.AuxIn)
	return p.Bytes()
}

// decodeExecuteResponse parses an Execute response body (inverse of Encode).
func decodeExecuteResponse(b []byte) ExecuteResponse {
	p := wire.NewPull(b, wire.FlagUTF16)
	var r ExecuteResponse
	r.StatusCode = p.Uint32()
	r.ErrorCode = p.Uint32()
	r.Flags = p.Uint32()
	r.RopBuffer = readCounted(p)
	r.AuxOut = readCounted(p)
	return r
}

func sidFromRecorder(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == "sid" {
			return c.Value
		}
	}
	// Fall back to scanning the raw header for robustness.
	if sc := w.Header().Get("Set-Cookie"); strings.HasPrefix(sc, "sid=") {
		return strings.SplitN(strings.TrimPrefix(sc, "sid="), ";", 2)[0]
	}
	return ""
}
