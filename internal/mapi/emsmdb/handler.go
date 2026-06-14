package emsmdb

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// maxRequestBody caps a MAPI/HTTP request body: an Execute ROP buffer is at most
// 256 KiB plus auxiliary data, so 1 MiB leaves comfortable headroom.
const maxRequestBody = 1 << 20

// readBody reads the full (size-capped) request body.
func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
}

// ServerVersion is the Exchange build the connector reports to clients in the
// X-ServerApplication header and Autodiscover.
const ServerVersion = "15.00.0847.4040"

// Response timing hints (milliseconds) advertised in the common headers
// (MS-OXCMAPIHTTP 2.2.3.3): how long the server may hold a NotificationWait and
// how long a session stays valid.
const (
	pendingPeriodMS   = 15000
	expirationInfoMS  = 900000
	connectPollsMax   = 60000
	connectRetryCount = 6
	connectRetryDelay = 5000
)

// orgDnPrefix is the mailbox DN prefix returned in the Connect response,
// derived from the shared essdn so every surface agrees on the organization.
var orgDnPrefix = strings.TrimSuffix(wire.BuildESSDN(""), "/cn=Recipients/cn=")

type contextKey int

const emailKey contextKey = 0

// WithEmail returns a context carrying the authenticated mailbox email; the
// HTTP front end sets this after Basic authentication.
func WithEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, emailKey, email)
}

func emailFromContext(ctx context.Context) string {
	e, ok := ctx.Value(emailKey).(string)
	if !ok {
		return ""
	}
	return e
}

// Session is one MAPI/HTTP mailbox session, created by Connect and looked up by
// the sid cookie on later requests. The ROP layer attaches its per-session
// logon and handle state to Logon.
type Session struct {
	ID    string
	Email string

	mu    sync.Mutex
	Logon any // opaque per-session ROP state owned by the ROP dispatcher

	// notifyMu guards notify, the push-notification state. It is separate from mu
	// (the Execute serialization lock) so a parked NotificationWait long-poll never
	// blocks an Execute on the same session.
	notifyMu sync.Mutex
	notify   *notifyState
}

// Lock guards the session for the duration of a single Execute, so a client's
// serialized ROP requests do not race on the handle table.
func (s *Session) Lock()   { s.mu.Lock() }
func (s *Session) Unlock() { s.mu.Unlock() }

// ensureNotify subscribes the session to the change-event feed on the first
// RopRegisterNotification. Subscribe runs synchronously here (inside the Execute that
// processes the registration), so an event raised immediately afterward is captured.
func (s *Session) ensureNotify(email string, src NotificationSource) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	if s.notify != nil {
		return
	}
	events, cancel := src.Subscribe(email)
	s.notify = &notifyState{events: events, cancel: cancel}
}

// addSubscription records a registered subscription on the session's notify state.
func (s *Session) addSubscription(sub *subscriptionObject) {
	s.notifyMu.Lock()
	n := s.notify
	s.notifyMu.Unlock()
	if n != nil {
		n.add(sub)
	}
}

// getNotify returns the session's notify state, or nil when the client has not
// registered for notifications.
func (s *Session) getNotify() *notifyState {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	return s.notify
}

// closeNotify tears down the notification subscription when the session ends.
func (s *Session) closeNotify() {
	s.notifyMu.Lock()
	n := s.notify
	s.notify = nil
	s.notifyMu.Unlock()
	if n != nil && n.cancel != nil {
		n.cancel()
	}
}

// ROPDispatcher processes a decoded ROP request buffer against a session and
// returns the response ROP bytes and the output server-object handle table.
type ROPDispatcher interface {
	Dispatch(sess *Session, ropData []byte, handlesIn []uint32, maxOut int) (ropResp []byte, handlesOut []uint32)
}

// Server is the emsmdb MAPI/HTTP endpoint. It owns the session table and
// delegates ROP semantics to a dispatcher.
type Server struct {
	dispatcher ROPDispatcher
	mu         sync.Mutex
	sessions   map[string]*Session
}

// NewServer returns an emsmdb endpoint backed by the given ROP dispatcher.
func NewServer(dispatcher ROPDispatcher) *Server {
	return &Server{dispatcher: dispatcher, sessions: make(map[string]*Session)}
}

func (s *Server) putSession(sess *Session) {
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
}

func (s *Server) getSession(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) dropSession(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// ServeHTTP dispatches a MAPI/HTTP request by its X-RequestType header.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := readBody(r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch r.Header.Get("X-RequestType") {
	case "Connect":
		s.handleConnect(w, r, body)
	case "Execute":
		s.handleExecute(w, r, body)
	case "Disconnect":
		s.handleDisconnect(w, r, body)
	case "NotificationWait":
		s.handleNotificationWait(w, r, body)
	case "PING":
		s.writeResponse(w, r, "PING", "", nil)
	default:
		http.Error(w, "unknown request type", http.StatusBadRequest)
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	if _, err := DecodeConnectRequest(body); err != nil || email == "" {
		s.writeResponse(w, r, "Connect", "", ConnectResponse{StatusCode: 0, ErrorCode: ecAccessDenied}.Encode())
		return
	}
	id, err := newSessionID()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.putSession(&Session{ID: id, Email: email})
	resp := ConnectResponse{
		PollsMax:    connectPollsMax,
		RetryCount:  connectRetryCount,
		RetryDelay:  connectRetryDelay,
		DnPrefix:    orgDnPrefix,
		DisplayName: email,
	}
	s.writeResponse(w, r, "Connect", id, resp.Encode())
}

func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request, body []byte) {
	sess := s.getSession(sidCookie(r))
	req, err := DecodeExecuteRequest(body)
	if sess == nil || err != nil {
		s.writeResponse(w, r, "Execute", "", ExecuteResponse{ErrorCode: ecError}.Encode())
		return
	}
	version, ropData, handlesIn, derr := DecodeROPBuffer(req.RopBuffer)
	if derr != nil {
		s.writeResponse(w, r, "Execute", "", ExecuteResponse{ErrorCode: ecError}.Encode())
		return
	}
	var ropResp []byte
	var handlesOut []uint32
	if s.dispatcher != nil {
		sess.Lock()
		ropResp, handlesOut = s.dispatcher.Dispatch(sess, ropData, handlesIn, int(req.MaxRopOut))
		sess.Unlock()
	}
	out := EncodeROPBuffer(version, ropResp, handlesOut, false)
	s.writeResponse(w, r, "Execute", "", ExecuteResponse{RopBuffer: out}.Encode())
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request, _ []byte) {
	id := sidCookie(r)
	if sess := s.getSession(id); sess != nil {
		sess.closeNotify()
	}
	s.dropSession(id)
	s.writeResponse(w, r, "Disconnect", "", DisconnectResponse{}.Encode())
}

func (s *Server) handleNotificationWait(w http.ResponseWriter, r *http.Request, _ []byte) {
	// Long-poll: hold the request up to the advertised pending period waiting for a
	// queued change, then report whether one is pending so the client issues an
	// Execute to drain the RopNotify ROPs (MS-OXCMAPIHTTP 2.2.4.4).
	var flagsOut uint32
	if sess := s.getSession(sidCookie(r)); sess != nil {
		if n := sess.getNotify(); n != nil && n.wait(pendingPeriodMS*time.Millisecond) {
			flagsOut = flagNotificationPending
		}
	}
	s.writeResponse(w, r, "NotificationWait", "", NotificationWaitResponse{FlagsOut: flagsOut}.Encode())
}

// writeResponse emits a MAPI/HTTP success response: the common headers, then the
// DONE meta-tag block, then the binary response body (Go applies chunked
// transfer encoding). A non-empty sid sets the session cookie.
func (s *Server) writeResponse(w http.ResponseWriter, r *http.Request, reqType, sid string, body []byte) {
	h := w.Header()
	h.Set("Cache-Control", "private")
	h.Set("Content-Type", "application/mapi-http")
	h.Set("X-RequestType", reqType)
	if v := r.Header.Get("X-RequestId"); v != "" {
		h.Set("X-RequestId", v)
	}
	if v := r.Header.Get("X-ClientInfo"); v != "" {
		h.Set("X-ClientInfo", v)
	}
	h.Set("X-ResponseCode", "0")
	h.Set("X-PendingPeriod", fmt.Sprintf("%d", pendingPeriodMS))
	h.Set("X-ExpirationInfo", fmt.Sprintf("%d", expirationInfoMS))
	h.Set("X-ServerApplication", "Exchange/"+ServerVersion)
	if sid != "" {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: sid, Path: "/mapi"})
	}
	w.WriteHeader(http.StatusOK)

	meta := "PROCESSING\r\nDONE\r\nX-ElapsedTime: 0\r\nX-StartTime: " +
		time.Now().UTC().Format(http.TimeFormat) + "\r\n\r\n"
	if _, err := w.Write([]byte(meta)); err != nil {
		return
	}
	if len(body) > 0 {
		if _, err := w.Write(body); err != nil {
			return
		}
	}
}

func sidCookie(r *http.Request) string {
	c, err := r.Cookie("sid")
	if err != nil {
		return ""
	}
	return c.Value
}

// newSessionID returns a fresh random GUID-formatted session identifier.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15]), nil
}
