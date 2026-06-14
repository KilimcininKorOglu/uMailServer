// Package activesync implements the Exchange ActiveSync (EAS) endpoint at
// /Microsoft-Server-ActiveSync, the mobile-sync surface spoken by iOS Mail,
// Android and Outlook mobile. EAS is a command protocol layered on HTTP
// (MS-ASHTTP): the client selects a command with the Cmd query parameter and
// sends a WBXML-encoded request body (internal/activesync/wbxml); the server
// replies with a WBXML body.
//
// This file is the transport: it answers OPTIONS with the advertised protocol
// versions and command set, authenticates POSTs through an injected Basic-auth
// gate, and dispatches each command to its handler. The command handlers
// (Provision, FolderSync, Sync, ...) are registered as their phases land; an
// unregistered command is reported as not implemented rather than guessed.
package activesync

import (
	"io"
	"log/slog"
	"net/http"
)

// maxRequestBody caps a WBXML request body, a guard against a hostile
// Content-Length; ActiveSync requests (including ItemOperations uploads) stay
// well under this.
const maxRequestBody = 30 << 20 // 30 MiB

// Advertised protocol surface (MS-ASHTTP 2.2.1 OPTIONS response). Versions are
// newest-last per the MS-ASProtocolVersions header convention; the command list
// is what an EAS client may issue against this server.
const (
	protocolVersions = "14.1,16.0,16.1"
	protocolCommands = "Provision,FolderSync,FolderCreate,FolderUpdate,FolderDelete,Sync,GetItemEstimate,Ping,ItemOperations,SendMail,SmartForward,SmartReply,MoveItems,MeetingResponse,Settings,Search"
	serverVersion    = "16.1"
)

// CommandFunc handles one decoded EAS command request and returns the response
// body to send (WBXML), or an error. A nil body with no error means the command
// produced an empty 200 (e.g. a SendMail that succeeded with no content).
type CommandFunc func(ctx *Context) ([]byte, error)

// Context carries the per-request state a command handler needs: the
// authenticated mailbox, the raw request, and the decoded request body.
type Context struct {
	Email   string
	Request *http.Request
	Body    []byte
}

// Server is the EAS endpoint handler. authenticate is the injected Basic-auth
// gate (it also enforces the FeatureEAS opt-in and account-state policy); it
// returns the mailbox email and ok=false when the request must be rejected.
type Server struct {
	authenticate func(*http.Request) (email string, ok bool)
	logger       *slog.Logger
	commands     map[string]CommandFunc
}

// NewServer builds an EAS endpoint that authenticates through authenticate.
func NewServer(authenticate func(*http.Request) (string, bool)) *Server {
	return &Server{
		authenticate: authenticate,
		logger:       slog.Default(),
		commands:     make(map[string]CommandFunc),
	}
}

// SetLogger overrides the default logger.
func (s *Server) SetLogger(l *slog.Logger) {
	if l != nil {
		s.logger = l
	}
}

// Handle registers a handler for an EAS command (the Cmd query value). It is
// called during wiring as each command phase lands.
func (s *Server) Handle(cmd string, fn CommandFunc) {
	s.commands[cmd] = fn
}

// ServeHTTP implements the EAS transport: OPTIONS advertises the protocol,
// POST authenticates then dispatches on the Cmd query parameter.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		s.writeOptions(w)
		return
	case http.MethodPost:
		s.servePost(w, r)
		return
	default:
		w.Header().Set("Allow", "OPTIONS, POST")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// writeOptions advertises the supported EAS protocol versions and commands
// (MS-ASHTTP), the headers a client reads to decide which version to speak.
func (s *Server) writeOptions(w http.ResponseWriter) {
	h := w.Header()
	h.Set("MS-ASProtocolVersions", protocolVersions)
	h.Set("MS-ASProtocolCommands", protocolCommands)
	h.Set("MS-Server-ActiveSync", serverVersion)
	h.Set("Allow", "OPTIONS, POST")
	h.Set("Public", "OPTIONS, POST")
	w.WriteHeader(http.StatusOK)
}

// servePost authenticates the request, then reads the WBXML body and dispatches
// the command named by the Cmd query parameter.
func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	email, ok := s.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="ActiveSync"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	cmd := r.URL.Query().Get("Cmd")
	if cmd == "" {
		http.Error(w, "Bad Request: missing Cmd", http.StatusBadRequest)
		return
	}
	handler, known := s.commands[cmd]
	if !known {
		// A command this server does not implement: report it rather than
		// pretending success, so a client falls back instead of hanging.
		http.Error(w, "Not Implemented: "+cmd, http.StatusNotImplemented)
		return
	}

	body, err := readBody(r)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	resp, err := handler(&Context{Email: email, Request: r, Body: body})
	if err != nil {
		s.logger.Warn("activesync command failed", "cmd", cmd, "email", email, "error", err)
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	if len(resp) > 0 {
		if _, err := w.Write(resp); err != nil {
			s.logger.Debug("activesync response write failed", "cmd", cmd, "error", err)
		}
	}
}

// readBody reads the WBXML request body, bounded by maxRequestBody.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
}
