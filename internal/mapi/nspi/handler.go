package nspi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// maxRequestBody caps an NSPI request body; address-book requests are small.
const maxRequestBody = 1 << 20

// serverVersion is the Exchange build reported in the common headers.
const serverVersion = "15.00.0847.4040"

// Response timing hints advertised in the common headers (MS-OXCMAPIHTTP 2.2.3.3).
const (
	pendingPeriodMS  = 15000
	expirationInfoMS = 900000
)

type contextKey int

const emailKey contextKey = 0

// WithEmail returns a context carrying the authenticated mailbox email; the HTTP
// front end sets this after Basic authentication.
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

// session is one NSPI address-book session, created by Bind and looked up by the
// sid cookie on later requests.
type session struct {
	id    string
	email string
	guid  wire.GUID
}

// Server is the NSPI MAPI/HTTP AddressBook endpoint. It owns the session table.
// The transport framing (the meta-tag block, the sid cookie, the common headers)
// mirrors the emsmdb endpoint; both implement the MS-OXCMAPIHTTP transport.
type Server struct {
	dir      Directory
	mu       sync.Mutex
	sessions map[string]*session
}

// NewServer returns an NSPI address-book endpoint.
func NewServer() *Server {
	return &Server{sessions: make(map[string]*session)}
}

// SetDirectory attaches the GAL source the address-book query operations read.
func (s *Server) SetDirectory(d Directory) { s.dir = d }

func (s *Server) putSession(sess *session) {
	s.mu.Lock()
	s.sessions[sess.id] = sess
	s.mu.Unlock()
}

func (s *Server) getSession(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) dropSession(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// ServeHTTP dispatches an NSPI request by its X-RequestType header.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch r.Header.Get("X-RequestType") {
	case "Bind":
		s.handleBind(w, r, body)
	case "Unbind":
		s.handleUnbind(w, r, body)
	case "QueryRows":
		s.handleQueryRows(w, r, body)
	case "GetProps":
		s.handleGetProps(w, r, body)
	case "GetSpecialTable":
		s.handleGetSpecialTable(w, r, body)
	case "ResolveNamesW":
		s.handleResolveNamesW(w, r, body)
	case "DNToMId":
		s.handleDNToMID(w, r, body)
	case "CompareMinIds":
		s.handleCompareMinIds(w, r, body)
	case "QueryColumns":
		s.handleQueryColumns(w, r, body)
	case "ModProps":
		s.handleModProps(w, r, body)
	case "ModLinkAtt":
		s.handleModLinkAtt(w, r, body)
	case "GetPropList":
		s.handleGetPropList(w, r, body)
	case "UpdateStat":
		s.handleUpdateStat(w, r, body)
	case "SeekEntries":
		s.handleSeekEntries(w, r, body)
	case "GetMatches":
		s.handleGetMatches(w, r, body)
	case "PING":
		s.writeResponse(w, r, "PING", "", nil)
	default:
		http.Error(w, "unknown request type", http.StatusBadRequest)
	}
}

// handleBind authenticates the address-book session and returns the server GUID.
func (s *Server) handleBind(w http.ResponseWriter, r *http.Request, body []byte) {
	email := emailFromContext(r.Context())
	if _, err := DecodeBindRequest(body); err != nil || email == "" {
		s.writeResponse(w, r, "Bind", "", BindResponse{Result: ecError}.Encode())
		return
	}
	id, err := newSessionID()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	guid := serverGUID()
	s.putSession(&session{id: id, email: email, guid: guid})
	s.writeResponse(w, r, "Bind", id, BindResponse{Result: ecSuccess, ServerGUID: guid}.Encode())
}

// handleUnbind tears down the address-book session.
func (s *Server) handleUnbind(w http.ResponseWriter, r *http.Request, body []byte) {
	if err := DecodeUnbindRequest(body); err != nil {
		s.writeResponse(w, r, "Unbind", "", UnbindResponse{Result: ecError}.Encode())
		return
	}
	s.dropSession(sidCookie(r))
	s.writeResponse(w, r, "Unbind", "", UnbindResponse{Result: ecUnbindSuccess}.Encode())
}

// writeResponse emits an NSPI success response: the common headers, the DONE
// meta-tag block, then the binary response body. A non-empty sid sets the session
// cookie.
func (s *Server) writeResponse(w http.ResponseWriter, r *http.Request, reqType, sid string, body []byte) {
	h := w.Header()
	h.Set("Cache-Control", "private")
	h.Set("Content-Type", "application/mapi-http")
	h.Set("X-RequestType", reqType)
	if v := r.Header.Get("X-RequestId"); v != "" {
		h.Set("X-RequestId", v)
	}
	h.Set("X-ResponseCode", "0")
	h.Set("X-PendingPeriod", fmt.Sprintf("%d", pendingPeriodMS))
	h.Set("X-ExpirationInfo", fmt.Sprintf("%d", expirationInfoMS))
	h.Set("X-ServerApplication", "Exchange/"+serverVersion)
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

// serverGUID returns the stable NSPI server GUID, derived from the organization
// so every session reports the same server identity.
func serverGUID() wire.GUID {
	h := sha256.Sum256([]byte("nspi-server:" + wire.BuildESSDN("")))
	return wire.NewPull(h[:], 0).GUID()
}
