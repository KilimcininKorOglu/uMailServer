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

	"github.com/umailserver/umailserver/internal/db"
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
	// W is the response writer. Most handlers return their body as bytes and
	// ignore it; the Ping handler needs it to lift the write deadline for a
	// long heartbeat (http.NewResponseController).
	W http.ResponseWriter
}

// Server is the EAS endpoint handler. authenticate is the injected Basic-auth
// gate (it also enforces the FeatureEAS opt-in and account-state policy); it
// returns the mailbox email and ok=false when the request must be rejected.
// DeviceStore persists EAS device partnerships (the provisioning policy key the
// device echoes on each request); it is the subset of db.Store that the
// ActiveSync surface needs.
type DeviceStore interface {
	GetEASDevice(email, deviceID string) (*db.EASDevice, error)
	PutEASDevice(dev *db.EASDevice) error
	DeleteEASDevice(email, deviceID string) error
}

type Server struct {
	authenticate func(*http.Request) (email string, ok bool)
	logger       *slog.Logger
	commands     map[string]CommandFunc
	devices      DeviceStore
	folders      FolderSource
	sync         SyncState
	mail         MailSource
	calendar     CalendarSource
	calMutator   CalendarMutator
	meetings     MeetingResponder
	contacts     ContactSource
	conMutator   ContactMutator
	tasks        TaskSource
	taskMutator  TaskMutator
	mutator      Mutator
	submitter    Submitter
	notifier     MailboxNotifier
	aliases      AliasSource
	oof          OOFStore
	pings        *pingCache
}

// NewServer builds an EAS endpoint that authenticates through authenticate.
func NewServer(authenticate func(*http.Request) (string, bool)) *Server {
	s := &Server{
		authenticate: authenticate,
		logger:       slog.Default(),
		commands:     make(map[string]CommandFunc),
		pings:        newPingCache(),
	}
	s.Handle("Provision", s.handleProvision)
	s.Handle("FolderSync", s.handleFolderSync)
	s.Handle("Sync", s.handleSync)
	s.Handle("MoveItems", s.handleMoveItems)
	s.Handle("SendMail", s.handleSendMail)
	s.Handle("SmartForward", s.handleSendMail)
	s.Handle("SmartReply", s.handleSendMail)
	s.Handle("ItemOperations", s.handleItemOperations)
	s.Handle("MeetingResponse", s.handleMeetingResponse)
	s.Handle("Ping", s.handlePing)
	s.Handle("Settings", s.handleSettings)
	return s
}

// SetDeviceStore wires the EAS device-partnership store used by Provision.
func (s *Server) SetDeviceStore(d DeviceStore) {
	s.devices = d
}

// SetFolderSource wires the mailbox folder source used by FolderSync.
func (s *Server) SetFolderSource(f FolderSource) {
	s.folders = f
}

// SetSyncState wires the per-(email, collection, device) sync-watermark store.
func (s *Server) SetSyncState(st SyncState) {
	s.sync = st
}

// SetMailSource wires the mail source used by the Sync command.
func (s *Server) SetMailSource(m MailSource) {
	s.mail = m
}

// SetCalendarSource wires the calendar source the Sync command uses for calendar
// collections. When nil, calendar collections fall through to the mail path,
// which a mail-only deployment never reaches (it advertises no calendar folder).
func (s *Server) SetCalendarSource(c CalendarSource) {
	s.calendar = c
}

// SetCalendarMutator wires the canonical-store mutator that applies a client's
// calendar up-sync changes (Add/Change/Delete). When nil, those commands are
// accepted but not applied.
func (s *Server) SetCalendarMutator(m CalendarMutator) {
	s.calMutator = m
}

// SetMeetingResponder wires the responder that applies a client's MeetingResponse
// (accept/tentative/decline) to the canonical calendar.
func (s *Server) SetMeetingResponder(m MeetingResponder) {
	s.meetings = m
}

// SetContactSource wires the contacts source the Sync command uses for contacts
// collections. When nil, contacts collections fall through to the mail path,
// which a mail-only deployment never reaches (it advertises no contacts folder).
func (s *Server) SetContactSource(c ContactSource) {
	s.contacts = c
}

// SetContactMutator wires the canonical-store mutator that applies a client's
// contacts up-sync changes (Add/Change/Delete). When nil, those commands are
// accepted but not applied.
func (s *Server) SetContactMutator(m ContactMutator) {
	s.conMutator = m
}

// SetTaskSource wires the tasks source the Sync command uses for tasks
// collections. When nil, tasks collections fall through to the mail path, which
// a mail-only deployment never reaches (it advertises no tasks folder).
func (s *Server) SetTaskSource(t TaskSource) {
	s.tasks = t
}

// SetTaskMutator wires the canonical-store mutator that applies a client's tasks
// up-sync changes (Add/Change/Delete). When nil, those commands are accepted but
// not applied.
func (s *Server) SetTaskMutator(m TaskMutator) {
	s.taskMutator = m
}

// SetMutator wires the canonical-mailstore mutator used to apply the client's
// Sync up-sync changes (read-flag Changes, Deletes).
func (s *Server) SetMutator(m Mutator) {
	s.mutator = m
}

// SetSubmitter wires the outbound submitter used by SendMail/SmartForward/
// SmartReply.
func (s *Server) SetSubmitter(sub Submitter) {
	s.submitter = sub
}

// SetMailNotifier wires the mailbox change notifier the Ping command long-polls
// for mail-folder changes. Without it, Ping still honors the heartbeat but only
// returns on expiry (Status 1) for mail folders.
func (s *Server) SetMailNotifier(n MailboxNotifier) {
	s.notifier = n
}

// SetAliasSource wires the canonical alias source the Settings UserInformation
// sub-command reads. When nil, UserInformation returns just the primary address.
func (s *Server) SetAliasSource(a AliasSource) {
	s.aliases = a
}

// SetOOFStore wires the canonical out-of-office accessor the Settings Oof
// sub-command reads and writes. When nil, Oof Get reports disabled and Oof Set is
// accepted without persisting.
func (s *Server) SetOOFStore(o OOFStore) {
	s.oof = o
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

	// Provisioning gate (MS-ASHTTP): every command except Provision requires the
	// device's current policy key. An unprovisioned, stale-key or wipe-flagged
	// device gets 449, the signal to (re)run Provision — which is also how an
	// admin-requested remote wipe reaches the device.
	if cmd != "Provision" && !s.deviceProvisioned(r, email) {
		http.Error(w, "Provisioning required", statusProvisioningRequired)
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
	resp, err := handler(&Context{Email: email, Request: r, Body: body, W: w})
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
