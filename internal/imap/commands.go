package imap

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
	"github.com/umailserver/umailserver/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tokenizeIMAPLine splits an IMAP command line into tokens on whitespace, like
// strings.Fields, EXCEPT that whitespace inside a double-quoted string (RFC 3501
// quoted-string) is not a delimiter, so `SUBJECT "two words"` stays one value
// token instead of being split into `"two` and `words"`. The surrounding quote
// characters are retained in the token, so the per-token quote-stripping done
// downstream (parseSearchCriteria's unq, and the trims other handlers apply) is
// unchanged. A backslash inside a quoted string escapes the next character, so
// \" does not close the string. For any input without a space inside quotes the
// output is byte-identical to strings.Fields, so no existing command regresses.
func tokenizeIMAPLine(line string) []string {
	var tokens []string
	var b strings.Builder
	inQuote, escaped, started := false, false, false
	flush := func() {
		if started {
			tokens = append(tokens, b.String())
			b.Reset()
			started = false
		}
	}
	for _, r := range line {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case inQuote:
			started = true
			b.WriteRune(r)
			switch r {
			case '\\':
				escaped = true
			case '"':
				inQuote = false
			}
		case r == '"':
			started, inQuote = true, true
			b.WriteRune(r)
		case unicode.IsSpace(r):
			flush()
		default:
			started = true
			b.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// handleCommand parses and handles an IMAP command
func (s *Session) handleCommand(line string) error {
	// Parse the command line
	// Format: TAG COMMAND [arguments...]
	parts := tokenizeIMAPLine(line)
	if len(parts) < 2 {
		s.WriteResponse("BAD", "Command expected")
		return nil
	}

	s.tag = parts[0]
	command := strings.ToUpper(parts[1])
	args := parts[2:]

	// Handle the command based on current state
	switch s.state {
	case StateNotAuthenticated:
		return s.handleNotAuthenticated(command, args, line)
	case StateAuthenticated:
		return s.handleAuthenticated(command, args, line)
	case StateSelected:
		return s.handleSelected(command, args, line)
	case StateLoggedOut:
		return nil
	}

	return nil
}

// handleNotAuthenticated handles commands in the Not Authenticated state
func (s *Session) handleNotAuthenticated(command string, args []string, line string) error {
	switch command {
	case "CAPABILITY":
		return s.handleCapability()
	case "STARTTLS":
		return s.handleStartTLS()
	case "AUTHENTICATE":
		return s.handleAuthenticate(args)
	case "LOGIN":
		return s.handleLogin(args)
	case "NOOP":
		return s.handleNoop()
	case "LOGOUT":
		return s.handleLogout()
	case "COMPRESS":
		return s.handleCompress(args)
	default:
		s.WriteResponse(s.tag, "BAD Command not allowed in this state")
		return nil
	}
}

// handleAuthenticated handles commands in the Authenticated state
func (s *Session) handleAuthenticated(command string, args []string, line string) error {
	switch command {
	case "CAPABILITY":
		return s.handleCapability()
	case "NOOP":
		return s.handleNoop()
	case "LOGOUT":
		return s.handleLogout()
	case "COMPRESS":
		return s.handleCompress(args)
	case "SELECT":
		return s.handleSelect(args)
	case "EXAMINE":
		return s.handleExamine(args)
	case "CREATE":
		return s.handleCreate(args)
	case "DELETE":
		return s.handleDelete(args)
	case "RENAME":
		return s.handleRename(args)
	case "SUBSCRIBE":
		return s.handleSubscribe(args)
	case "UNSUBSCRIBE":
		return s.handleUnsubscribe(args)
	case "LIST":
		return s.handleList(args)
	case "LSUB":
		return s.handleLsub(args)
	case "STATUS":
		return s.handleStatus(args)
	case "APPEND":
		return s.handleAppend(args, line)
	case "NAMESPACE":
		return s.handleNamespace()
	case "IDLE":
		return s.handleIdle()
	case "ENABLE":
		return s.handleEnable(args)
	case "ID":
		return s.handleID(args)
	case "GETACL", "SETACL", "DELETEACL", "MYRIGHTS", "LISTRIGHTS":
		return s.handleACLCommand(command, args)
	default:
		s.WriteResponse(s.tag, "BAD Command not recognized")
		return nil
	}
}

// handleSelected handles commands in the Selected state
func (s *Session) handleSelected(command string, args []string, line string) error {
	switch command {
	case "CAPABILITY":
		return s.handleCapability()
	case "NOOP":
		return s.handleNoop()
	case "LOGOUT":
		return s.handleLogout()
	case "COMPRESS":
		return s.handleCompress(args)
	case "SELECT":
		return s.handleSelect(args)
	case "EXAMINE":
		return s.handleExamine(args)
	case "CREATE":
		return s.handleCreate(args)
	case "DELETE":
		return s.handleDelete(args)
	case "RENAME":
		return s.handleRename(args)
	case "SUBSCRIBE":
		return s.handleSubscribe(args)
	case "UNSUBSCRIBE":
		return s.handleUnsubscribe(args)
	case "LIST":
		return s.handleList(args)
	case "LSUB":
		return s.handleLsub(args)
	case "STATUS":
		return s.handleStatus(args)
	case "APPEND":
		return s.handleAppend(args, line)
	case "NAMESPACE":
		return s.handleNamespace()
	case "CHECK":
		return s.handleCheck()
	case "CLOSE":
		return s.handleClose()
	case "EXPUNGE":
		return s.handleExpunge()
	case "SEARCH":
		return s.handleSearch(args, line, false)
	case "SORT":
		return s.handleSort(args, line)
	case "THREAD":
		return s.handleThread(args, line)
	case "FETCH":
		return s.handleFetch(args, line, false)
	case "STORE":
		return s.handleStore(args, false)
	case "COPY":
		return s.handleCopy(args, false)
	case "MOVE":
		return s.handleMove(args, false)
	case "UID":
		return s.handleUID(args, line)
	case "IDLE":
		return s.handleIdle()
	case "ID":
		return s.handleID(args)
	case "GETACL", "SETACL", "DELETEACL", "MYRIGHTS", "LISTRIGHTS":
		return s.handleACLCommand(command, args)
	default:
		s.WriteResponse(s.tag, "BAD Command not recognized")
		return nil
	}
}

// CAPABILITY command
func (s *Session) handleCapability() error {
	caps := "CAPABILITY"
	for _, cap := range s.capabilities {
		caps += " " + cap
	}
	s.WriteData(caps)
	s.WriteResponse(s.tag, "OK CAPABILITY completed")
	return nil
}

func (s *Session) handleACLCommand(command string, args []string) error {
	if !s.server.sharedFoldersEnabled {
		s.WriteResponse(s.tag, "BAD Command not recognized")
		return nil
	}

	switch command {
	case "GETACL":
		return s.handleGetACL(args)
	case "SETACL":
		return s.handleSetACL(args)
	case "DELETEACL":
		return s.handleDeleteACL(args)
	case "MYRIGHTS":
		return s.handleMyRights(args)
	case "LISTRIGHTS":
		return s.handleListRights(args)
	default:
		s.WriteResponse(s.tag, "BAD Command not recognized")
		return nil
	}
}

// NOOP command
func (s *Session) handleNoop() error {
	s.WriteResponse(s.tag, "OK NOOP completed")
	return nil
}

// LOGOUT command
func (s *Session) handleLogout() error {
	s.WriteData("BYE IMAP4rev1 Server logging out")
	s.WriteResponse(s.tag, "OK LOGOUT completed")
	s.state = StateLoggedOut
	s.Close()
	return nil
}

// STARTTLS command
func (s *Session) handleStartTLS() error {
	if s.tlsActive {
		s.WriteResponse(s.tag, "BAD TLS already active")
		return nil
	}

	if s.server.tlsConfig == nil {
		s.WriteResponse(s.tag, "NO TLS not available")
		return nil
	}

	s.WriteResponse(s.tag, "OK Begin TLS negotiation now")

	// Upgrade to TLS with a bounded handshake timeout
	_ = s.conn.SetDeadline(time.Now().Add(30 * time.Second)) // Best-effort deadline
	tlsConn := tls.Server(s.conn, s.server.tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		_ = s.conn.SetDeadline(time.Time{}) // Best-effort deadline reset
		return fmt.Errorf("TLS handshake failed: %w", err)
	}
	_ = s.conn.SetDeadline(time.Time{}) // Best-effort deadline reset

	s.tlsConn = tlsConn
	s.conn = tlsConn
	s.reader.Reset(tlsConn)
	s.writer.Reset(tlsConn)
	s.tlsActive = true

	return nil
}

// handleCompress enables compression using DEFLATE algorithm (RFC 4978)
func (s *Session) handleCompress(args []string) error {
	if s.compressActive {
		s.WriteResponse(s.tag, "BAD Compression already active")
		return nil
	}

	if len(args) < 1 || strings.ToUpper(args[0]) != "DEFLATE" {
		s.WriteResponse(s.tag, "BAD COMPRESS requires DEFLATE argument")
		return nil
	}

	s.WriteResponse(s.tag, "OK Compression active")

	// Create gzip writer for compressing responses to client
	s.compressWriter = gzip.NewWriter(s.conn)
	s.writer.Reset(s.compressWriter)

	// Create gzip reader for decompressing requests from client
	gzReader, err := gzip.NewReader(s.conn)
	if err != nil {
		s.WriteResponse(s.tag, "BAD Compression initialization failed")
		return nil
	}
	s.compressReader = gzReader
	s.reader.Reset(s.compressReader)

	s.compressActive = true

	return nil
}

// AUTHENTICATE command
func (s *Session) handleAuthenticate(args []string) error {
	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD Missing authentication mechanism")
		return nil
	}

	if !s.tlsActive && !s.server.allowPlainAuth {
		s.WriteResponse(s.tag, "NO TLS required for authentication")
		return nil
	}

	mechanism := strings.ToUpper(args[0])

	switch mechanism {
	case "PLAIN":
		return s.handleAuthPlain(args[1:])
	case "LOGIN":
		return s.handleAuthLogin()
	default:
		s.WriteResponse(s.tag, "NO Unsupported authentication mechanism")
		return nil
	}
}

// handleAuthPlain handles PLAIN authentication (RFC 4616 with SASL-IR)
func (s *Session) handleAuthPlain(args []string) error {
	var credentials []byte
	var err error

	if len(args) >= 1 && args[0] != "" {
		// SASL-IR: initial response provided with the AUTHENTICATE command
		credentials, err = base64.StdEncoding.DecodeString(args[0])
		if err != nil {
			s.WriteResponse(s.tag, "NO Invalid base64 in AUTHENTICATE PLAIN")
			return nil
		}
	} else {
		// No initial response; send continuation request
		s.WriteContinuation("")
		line, err := s.readLine()
		if err != nil {
			return fmt.Errorf("failed to read PLAIN credentials: %w", err)
		}
		// Client may send "*" to cancel
		if line == "*" {
			s.WriteResponse(s.tag, "NO AUTHENTICATE cancelled")
			return nil
		}
		credentials, err = base64.StdEncoding.DecodeString(line)
		if err != nil {
			s.WriteResponse(s.tag, "NO Invalid base64 in AUTHENTICATE PLAIN")
			return nil
		}
	}

	// PLAIN format: authzid\0authcid\0passwd
	// We ignore authzid and use authcid as the username.
	parts := strings.SplitN(string(credentials), "\x00", 3)
	if len(parts) < 3 {
		s.WriteResponse(s.tag, "NO Invalid PLAIN credentials")
		return nil
	}

	username := parts[1]
	password := parts[2]

	return s.authenticateUser(username, password, "AUTHENTICATE completed", "AUTHENTICATE failed")
}

// handleAuthLogin handles LOGIN authentication (multi-step SASL)
func (s *Session) handleAuthLogin() error {
	// Step 1: Send Username challenge (base64 of "Username:")
	s.WriteContinuation("VXNlcm5hbWU6")

	// Read username response
	line, err := s.readLine()
	if err != nil {
		return fmt.Errorf("failed to read LOGIN username: %w", err)
	}
	if line == "*" {
		s.WriteResponse(s.tag, "NO AUTHENTICATE cancelled")
		return nil
	}
	usernameBytes, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		s.WriteResponse(s.tag, "NO Invalid base64 username in AUTHENTICATE LOGIN")
		return nil
	}
	username := string(usernameBytes)

	// Step 2: Send Password challenge (base64 of "Password:")
	s.WriteContinuation("UGFzc3dvcmQ6")

	// Read password response
	line, err = s.readLine()
	if err != nil {
		return fmt.Errorf("failed to read LOGIN password: %w", err)
	}
	if line == "*" {
		s.WriteResponse(s.tag, "NO AUTHENTICATE cancelled")
		return nil
	}
	passwordBytes, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		s.WriteResponse(s.tag, "NO Invalid base64 password in AUTHENTICATE LOGIN")
		return nil
	}
	password := string(passwordBytes)

	return s.authenticateUser(username, password, "AUTHENTICATE completed", "AUTHENTICATE failed")
}

// authenticateUser is the shared authentication logic used by LOGIN,
// AUTHENTICATE PLAIN, and AUTHENTICATE LOGIN.
// okMsg is the human-readable text sent on success (e.g. "LOGIN completed").
// failMsg is sent on authentication failure (e.g. "AUTHENTICATE failed").
func clientIP(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

func (s *Session) authenticateUser(username, password, okMsg, failMsg string) error {
	ctx := context.Background()

	// Create tracing span if we have a tracing provider
	var span trace.Span
	if s.server.tracingProvider != nil && s.server.tracingProvider.IsEnabled() {
		ctx, span = s.server.tracingProvider.StartSpanWithKind(ctx, "imap.authenticate", tracing.SpanKindServer,
			attribute.String("session.id", s.id),
			attribute.String("user", username),
			attribute.String("ip", clientIP(s.conn)),
		)
		defer span.End()
	}

	ip := clientIP(s.conn)
	if s.server.isAuthLockedOut(ip) {
		s.WriteResponse(s.tag, "NO Too many failed authentication attempts")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "auth locked out")
		}
		if s.server.onLoginResult != nil {
			s.server.onLoginResult(username, false, ip, "lockout")
		}
		return nil
	}

	authenticated := false

	// RFC 7616 PRECIS: normalize username and password before authentication
	// Use UsernameCaseMapped profile (lowercase, Unicode normalization)
	usernameNormalized, err := normalizeUsername(username)
	if err != nil {
		// Invalid username characters per PRECIS
		s.WriteResponse(s.tag, "NO Invalid username characters")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "invalid username")
		}
		return nil
	}
	passwordNormalized := normalizePassword(password)

	if s.server.authFunc != nil {
		auth, err := s.server.authFunc(usernameNormalized, passwordNormalized)
		if err == nil && auth {
			authenticated = true
		}
	} else if s.server.mailstore != nil {
		auth, err := s.server.mailstore.Authenticate(usernameNormalized, passwordNormalized)
		if err == nil && auth {
			authenticated = true
		}
	}

	if !authenticated {
		s.server.recordAuthFailure(ip)
		s.WriteResponse(s.tag, "NO "+failMsg)
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "authentication failed")
			tracing.SetBoolAttribute(span, "auth.success", false)
		}
		if s.server.onLoginResult != nil {
			s.server.onLoginResult(usernameNormalized, false, ip, "invalid_credentials")
		}
		return nil
	}

	s.server.clearAuthFailures(ip)
	s.user = usernameNormalized
	s.state = StateAuthenticated
	if s.server.onLoginResult != nil {
		s.server.onLoginResult(usernameNormalized, true, ip, "")
	}

	// Auto-create the standard folders after first successful authentication.
	// This backstops accounts created through paths that do not provision at
	// creation time, so a client always sees a consistent folder set.
	if s.server.mailstore != nil {
		_ = s.server.mailstore.EnsureDefaultMailboxes(s.user) //nolint:errcheck
	}

	if span != nil {
		tracing.SetBoolAttribute(span, "auth.success", true)
		tracing.SetStatus(span, tracing.StatusOk, "")
	}

	s.WriteResponse(s.tag, "OK "+okMsg)
	return nil
}

// LOGIN command
func (s *Session) handleLogin(args []string) error {
	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD Missing username or password")
		return nil
	}

	if !s.tlsActive && !s.server.allowPlainAuth {
		s.WriteResponse(s.tag, "NO LOGIN requires TLS - use STARTTLS first")
		return nil
	}

	username := args[0]
	password := args[1]

	// Remove quotes if present
	username = strings.Trim(username, "\"'")
	password = strings.Trim(password, "\"'")

	return s.authenticateUser(username, password, "LOGIN completed", "Authentication failed")
}

// SELECT command
// parseQResyncParam extracts the RFC 7162 SELECT/EXAMINE parameter
// "(QRESYNC (uidvalidity highestmodseq [known-uids [seq-match-data]]))" from the
// arguments after the mailbox name. It returns the client's UIDVALIDITY and
// mod-sequence, the optional known-uid set, and whether a QRESYNC param was
// present and well-formed.
func parseQResyncParam(args []string) (uidValidity uint32, modSeq uint64, knownUIDs string, ok bool) {
	joined := strings.Join(args, " ")
	qi := strings.Index(strings.ToUpper(joined), "QRESYNC")
	if qi < 0 {
		return 0, 0, "", false
	}
	rest := joined[qi+len("QRESYNC"):]
	open := strings.Index(rest, "(")
	if open < 0 {
		return 0, 0, "", false
	}
	depth, end := 0, -1
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return 0, 0, "", false
	}
	fields := strings.Fields(rest[open+1 : end])
	if len(fields) < 2 {
		return 0, 0, "", false
	}
	uv, err1 := strconv.ParseUint(fields[0], 10, 32)
	ms, err2 := strconv.ParseUint(fields[1], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, "", false
	}
	if len(fields) >= 3 {
		knownUIDs = fields[2]
	}
	return uint32(uv), ms, knownUIDs, true
}

// filterUIDsInSet keeps only the UIDs contained in the IMAP sequence-set string.
// On a parse failure it returns the input unchanged (never over-filters).
func filterUIDsInSet(uids []uint32, set string) []uint32 {
	ranges, err := ParseSequenceSet(set)
	if err != nil || len(ranges) == 0 {
		return uids
	}
	maxUID := uint32(0)
	for _, u := range uids {
		if u > maxUID {
			maxUID = u
		}
	}
	var out []uint32
	for _, u := range uids {
		for _, r := range ranges {
			if r.Contains(u, maxUID) {
				out = append(out, u)
				break
			}
		}
	}
	return out
}

// emitQResync replays an RFC 7162 QRESYNC SELECT/EXAMINE resync: VANISHED
// (EARLIER) for UIDs expunged since the client's mod-sequence (restricted to the
// client's known-uid set when given) and an unsolicited FETCH for messages whose
// flags changed since then. It is a no-op unless QRESYNC is enabled and the
// client's UIDVALIDITY still matches the mailbox.
func (s *Session) emitQResync(mailbox *Mailbox, clientUIDValidity uint32, clientModSeq uint64, knownUIDs string) {
	if !s.enabledCaps["QRESYNC"] || clientUIDValidity != mailbox.UIDValidity {
		return
	}

	// VANISHED (EARLIER): UIDs expunged at a mod-sequence above the client's.
	if vanished, err := s.server.mailstore.ExpungedUIDsSince(s.selOwner(), mailbox.Name, clientModSeq); err == nil && len(vanished) > 0 {
		if knownUIDs != "" {
			vanished = filterUIDsInSet(vanished, knownUIDs)
		}
		if len(vanished) > 0 {
			s.WriteData("VANISHED (EARLIER) " + uidSetString(vanished))
		}
	}

	// Flag changes since the client's mod-sequence.
	msgs, err := s.server.mailstore.FetchMessages(s.selOwner(), mailbox.Name, "1:*", []string{"FLAGS", "UID"})
	if err != nil {
		return
	}
	for _, m := range msgs {
		if m.ModSeq > clientModSeq {
			s.WriteData(fmt.Sprintf("%d FETCH (UID %d FLAGS (%s) MODSEQ (%d))",
				m.SeqNum, m.UID, strings.Join(m.Flags, " "), m.ModSeq))
		}
	}
}

func (s *Session) handleSelect(args []string) error {
	ctx := context.Background()

	// Create tracing span
	var span trace.Span
	if s.server.tracingProvider != nil && s.server.tracingProvider.IsEnabled() {
		ctx, span = s.server.tracingProvider.StartSpanWithKind(ctx, "imap.select", tracing.SpanKindServer,
			attribute.String("session.id", s.id),
			attribute.String("user", s.user),
		)
		defer span.End()
	}

	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD Missing mailbox name")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "missing mailbox name")
		}
		return nil
	}

	mailboxName := args[0]
	mailboxName = strings.Trim(mailboxName, "\"'")

	if s.server.mailstore == nil {
		s.WriteResponse(s.tag, "NO Mailstore not available")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "mailstore not available")
		}
		return nil
	}

	if span != nil {
		tracing.SetStringAttribute(span, "mailbox.name", mailboxName)
	}

	owner, mbox, ok := s.resolvePublicFolder(mailboxName, uint8(storage.ACLRead))
	if !ok {
		s.WriteResponse(s.tag, "NO [NOPERM] Access denied")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "public folder access denied")
		}
		return nil
	}
	mailbox, err := s.server.mailstore.SelectMailbox(owner, mbox)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		if span != nil {
			tracing.RecordError(span, err)
			tracing.SetStatus(span, tracing.StatusError, "select mailbox failed")
		}
		return nil
	}

	s.selected = mailbox
	s.selectedOwner = owner
	s.state = StateSelected

	// Send mailbox data
	s.WriteData(fmt.Sprintf("%d EXISTS", mailbox.Exists))
	s.WriteData(fmt.Sprintf("%d RECENT", mailbox.Recent))

	if mailbox.Unseen > 0 {
		s.WriteData(fmt.Sprintf("OK [UNSEEN %d] Message %d is first unseen", mailbox.Unseen, mailbox.Unseen))
	}

	s.WriteData(fmt.Sprintf("OK [UIDVALIDITY %d] UIDs valid", mailbox.UIDValidity))
	s.WriteData(fmt.Sprintf("OK [UIDNEXT %d] Predicted next UID", mailbox.UIDNext))

	// RFC 7162: HIGHESTMODSEQ when CONDSTORE/QRESYNC is enabled
	if s.enabledCaps["CONDSTORE"] || s.enabledCaps["QRESYNC"] {
		s.WriteData(fmt.Sprintf("OK [HIGHESTMODSEQ %d] Highest modification sequence", mailbox.HighestModSeq))
	}

	// PERMANENTFLAGS
	s.WriteData("FLAGS (\\Answered \\Flagged \\Deleted \\Seen \\Draft)")
	s.WriteData("OK [PERMANENTFLAGS (\\Answered \\Flagged \\Deleted \\Seen \\Draft \\*)] Flags permitted")

	// RFC 7162 QRESYNC: replay VANISHED (EARLIER) + changed flags for a client
	// resuming from a known UIDVALIDITY/mod-sequence.
	if uv, cms, known, qok := parseQResyncParam(args[1:]); qok {
		s.emitQResync(mailbox, uv, cms, known)
	}

	if span != nil {
		tracing.SetIntAttribute(span, "mailbox.exists", mailbox.Exists)
		tracing.SetIntAttribute(span, "mailbox.recent", mailbox.Recent)
		tracing.SetStatus(span, tracing.StatusOk, "")
	}

	s.WriteResponse(s.tag, "OK [READ-WRITE] SELECT completed")
	return nil
}

// EXAMINE command
func (s *Session) handleExamine(args []string) error {
	// Similar to SELECT but read-only
	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD Missing mailbox name")
		return nil
	}

	mailboxName := args[0]
	mailboxName = strings.Trim(mailboxName, "\"'")

	if s.server.mailstore == nil {
		s.WriteResponse(s.tag, "NO Mailstore not available")
		return nil
	}

	owner, mbox, ok := s.resolvePublicFolder(mailboxName, uint8(storage.ACLRead))
	if !ok {
		s.WriteResponse(s.tag, "NO [NOPERM] Access denied")
		return nil
	}
	mailbox, err := s.server.mailstore.SelectMailbox(owner, mbox)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	s.selected = mailbox
	s.selectedOwner = owner
	s.state = StateSelected

	// Send mailbox data (same as SELECT but read-only)
	s.WriteData(fmt.Sprintf("%d EXISTS", mailbox.Exists))
	s.WriteData(fmt.Sprintf("%d RECENT", mailbox.Recent))

	if mailbox.Unseen > 0 {
		s.WriteData(fmt.Sprintf("OK [UNSEEN %d] Message %d is first unseen", mailbox.Unseen, mailbox.Unseen))
	}

	s.WriteData(fmt.Sprintf("OK [UIDVALIDITY %d] UIDs valid", mailbox.UIDValidity))
	s.WriteData(fmt.Sprintf("OK [UIDNEXT %d] Predicted next UID", mailbox.UIDNext))

	// RFC 7162: HIGHESTMODSEQ when CONDSTORE/QRESYNC is enabled
	if s.enabledCaps["CONDSTORE"] || s.enabledCaps["QRESYNC"] {
		s.WriteData(fmt.Sprintf("OK [HIGHESTMODSEQ %d] Highest modification sequence", mailbox.HighestModSeq))
	}

	s.WriteData("FLAGS (\\Answered \\Flagged \\Deleted \\Seen \\Draft)")
	s.WriteData("OK [PERMANENTFLAGS ()] No permanent flags permitted")

	// RFC 7162 QRESYNC: replay VANISHED (EARLIER) + changed flags for a client
	// resuming from a known UIDVALIDITY/mod-sequence.
	if uv, cms, known, qok := parseQResyncParam(args[1:]); qok {
		s.emitQResync(mailbox, uv, cms, known)
	}

	s.WriteResponse(s.tag, "OK [READ-ONLY] EXAMINE completed")
	return nil
}

// CREATE command
func (s *Session) handleCreate(args []string) error {
	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD Missing mailbox name")
		return nil
	}

	mailboxName := args[0]
	mailboxName = strings.Trim(mailboxName, "\"'")

	if s.isPublicPath(mailboxName) {
		s.WriteResponse(s.tag, "NO [NOPERM] Public folders are managed by the administrator")
		return nil
	}

	if s.server.mailstore == nil {
		s.WriteResponse(s.tag, "NO Mailstore not available")
		return nil
	}

	err := s.server.mailstore.CreateMailbox(s.user, mailboxName)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	s.WriteResponse(s.tag, "OK CREATE completed")
	return nil
}

// DELETE command
func (s *Session) handleDelete(args []string) error {
	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD Missing mailbox name")
		return nil
	}

	mailboxName := args[0]
	mailboxName = strings.Trim(mailboxName, "\"'")

	// Cannot delete INBOX
	if strings.ToUpper(mailboxName) == "INBOX" {
		s.WriteResponse(s.tag, "NO Cannot delete INBOX")
		return nil
	}

	if s.isPublicPath(mailboxName) {
		s.WriteResponse(s.tag, "NO [NOPERM] Public folders are managed by the administrator")
		return nil
	}

	if s.server.mailstore == nil {
		s.WriteResponse(s.tag, "NO Mailstore not available")
		return nil
	}

	err := s.server.mailstore.DeleteMailbox(s.user, mailboxName)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	s.WriteResponse(s.tag, "OK DELETE completed")
	return nil
}

// RENAME command
func (s *Session) handleRename(args []string) error {
	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD Missing old or new mailbox name")
		return nil
	}

	oldName := strings.Trim(args[0], "\"'")
	newName := strings.Trim(args[1], "\"'")

	if s.isPublicPath(oldName) || s.isPublicPath(newName) {
		s.WriteResponse(s.tag, "NO [NOPERM] Public folders are managed by the administrator")
		return nil
	}

	if s.server.mailstore == nil {
		s.WriteResponse(s.tag, "NO Mailstore not available")
		return nil
	}

	err := s.server.mailstore.RenameMailbox(s.user, oldName, newName)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	s.WriteResponse(s.tag, "OK RENAME completed")
	return nil
}

// SUBSCRIBE command
func (s *Session) handleSubscribe(args []string) error {
	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD Missing mailbox name")
		return nil
	}

	mailboxName := strings.Trim(args[0], "\"'")
	if mailboxName == "" {
		s.WriteResponse(s.tag, "BAD Empty mailbox name")
		return nil
	}

	if s.isPublicPath(mailboxName) {
		s.WriteResponse(s.tag, "NO [NOPERM] Public folders are managed by the administrator")
		return nil
	}

	// Verify mailbox exists first
	mailboxes, err := s.server.mailstore.ListMailboxes(s.user, mailboxName)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	// Check if the mailbox exists (exact match)
	found := false
	for _, m := range mailboxes {
		if m == mailboxName {
			found = true
			break
		}
	}

	if !found {
		s.WriteResponse(s.tag, "NO Mailbox not found")
		return nil
	}

	// Subscribe to the mailbox
	if err := s.server.mailstore.SetSubscribed(s.user, mailboxName, true); err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	s.WriteResponse(s.tag, "OK SUBSCRIBE completed")
	return nil
}

// UNSUBSCRIBE command
func (s *Session) handleUnsubscribe(args []string) error {
	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD Missing mailbox name")
		return nil
	}

	mailboxName := strings.Trim(args[0], "\"'")
	if mailboxName == "" {
		s.WriteResponse(s.tag, "BAD Empty mailbox name")
		return nil
	}

	if s.isPublicPath(mailboxName) {
		s.WriteResponse(s.tag, "NO [NOPERM] Public folders are managed by the administrator")
		return nil
	}

	// Unsubscribe from the mailbox
	if err := s.server.mailstore.SetSubscribed(s.user, mailboxName, false); err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	s.WriteResponse(s.tag, "OK UNSUBSCRIBE completed")
	return nil
}

// LIST command
func (s *Session) handleList(args []string) error {
	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD Missing reference or pattern")
		return nil
	}

	reference := strings.Trim(args[0], "\"'")
	pattern := strings.Trim(args[1], "\"'")

	// Combine reference and pattern
	fullPattern := reference
	if pattern != "" {
		if fullPattern != "" && !strings.HasSuffix(fullPattern, "/") {
			fullPattern += "/"
		}
		fullPattern += pattern
	}

	if s.server.mailstore == nil {
		s.WriteResponse(s.tag, "NO Mailstore not available")
		return nil
	}

	mailboxes, err := s.server.mailstore.ListMailboxes(s.user, fullPattern)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	// Get all mailboxes to check for children hierarchy (RFC 3348)
	allMailboxes, _ := s.server.mailstore.ListMailboxes(s.user, "*")

	for _, mbox := range mailboxes {
		// The Recoverable Items dumpster is a soft-delete retention area, not a
		// browsable folder — Exchange keeps it out of IMAP LIST. It stays
		// SELECTable by name for the recover flow and retention cleaner.
		if semcore.IsClientHiddenFolderName(mbox) {
			continue
		}
		// Determine hierarchy indicators (RFC 3348)
		hasChildren := false
		hasNoSelect := false

		// Check if this mailbox has children by looking for sub-mailboxes
		mboxPrefix := mbox + "/"
		for _, other := range allMailboxes {
			if other != mbox && strings.HasPrefix(other, mboxPrefix) {
				hasChildren = true
				break
			}
		}

		// Build flags based on hierarchy
		flags := "\\HasNoChildren"
		if hasChildren {
			flags = "\\HasChildren"
		}
		if hasNoSelect {
			flags += " \\NoSelect"
		}

		s.WriteData(fmt.Sprintf("LIST (%s) \"/\" \"%s\"", flags, mbox))
	}

	// Fold in the per-domain public-folder namespace (ACL-gated) when the LIST
	// pattern reaches into it: a discovery list ("*") or a namespace-scoped list
	// ("Public Folders" / "Public Folders/...").
	if s.server.publicFoldersOn() {
		if dom := domainOf(s.user); dom != "" &&
			(fullPattern == "*" || fullPattern == "Public Folders" || strings.HasPrefix(fullPattern, publicFolderPrefix)) {
			owner := storage.PublicFolderOwner(dom)
			if pubs, perr := s.server.mailstore.ListMailboxes(owner, "*"); perr == nil {
				for _, name := range pubs {
					if s.publicRights(owner, name)&uint8(storage.ACLRead) == uint8(storage.ACLRead) {
						s.WriteData(fmt.Sprintf("LIST (\\HasNoChildren) \"/\" \"%s%s\"", publicFolderPrefix, name))
					}
				}
			}
		}
	}

	s.WriteResponse(s.tag, "OK LIST completed")
	return nil
}

// LSUB command
func (s *Session) handleLsub(args []string) error {
	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD Missing reference or pattern")
		return nil
	}

	reference := strings.Trim(args[0], "\"'")
	pattern := strings.Trim(args[1], "\"'")

	// Combine reference and pattern
	fullPattern := reference
	if pattern != "" {
		if fullPattern != "" && !strings.HasSuffix(fullPattern, "/") {
			fullPattern += "/"
		}
		fullPattern += pattern
	}

	if s.server.mailstore == nil {
		s.WriteResponse(s.tag, "NO Mailstore not available")
		return nil
	}

	// Get subscribed mailboxes
	var mailboxes []string
	subscribed, err := s.server.mailstore.ListSubscribed(s.user)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}
	// Filter by pattern
	for _, mbox := range subscribed {
		if matchMailboxPattern(mbox, fullPattern) {
			mailboxes = append(mailboxes, mbox)
		}
	}

	for _, mbox := range mailboxes {
		if semcore.IsClientHiddenFolderName(mbox) {
			continue
		}
		s.WriteData(fmt.Sprintf("LSUB (\\HasNoChildren) \"/\" \"%s\"", mbox))
	}

	s.WriteResponse(s.tag, "OK LSUB completed")
	return nil
}

// matchMailboxPattern checks if a mailbox name matches an IMAP pattern
func matchMailboxPattern(name, pattern string) bool {
	if pattern == "*" {
		return true
	}

	// Handle * wildcard at end
	if strings.HasSuffix(pattern, "*") {
		prefix := pattern[:len(pattern)-1]
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	// Handle * wildcard at start
	if strings.HasPrefix(pattern, "*") {
		suffix := pattern[1:]
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}

	// Exact match
	return name == pattern
}

// STATUS command
func (s *Session) handleStatus(args []string) error {
	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD Missing mailbox or status items")
		return nil
	}

	mailboxName := strings.Trim(args[0], "\"'")
	statusItems := strings.Join(args[1:], " ")

	if s.server.mailstore == nil {
		s.WriteResponse(s.tag, "NO Mailstore not available")
		return nil
	}

	// Get mailbox info (public folders resolve to the per-domain public owner,
	// gated by read access).
	owner, mbox, ok := s.resolvePublicFolder(mailboxName, uint8(storage.ACLRead))
	if !ok {
		s.WriteResponse(s.tag, "NO [NOPERM] Access denied")
		return nil
	}
	mailbox, err := s.server.mailstore.SelectMailbox(owner, mbox)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	// Build status response
	status := fmt.Sprintf("STATUS \"%s\" (", mailboxName)

	if strings.Contains(statusItems, "MESSAGES") {
		status += fmt.Sprintf("MESSAGES %d ", mailbox.Exists)
	}
	if strings.Contains(statusItems, "RECENT") {
		status += fmt.Sprintf("RECENT %d ", mailbox.Recent)
	}
	if strings.Contains(statusItems, "UIDNEXT") {
		status += fmt.Sprintf("UIDNEXT %d ", mailbox.UIDNext)
	}
	if strings.Contains(statusItems, "UIDVALIDITY") {
		status += fmt.Sprintf("UIDVALIDITY %d ", mailbox.UIDValidity)
	}
	if strings.Contains(statusItems, "UNSEEN") {
		status += fmt.Sprintf("UNSEEN %d ", mailbox.Unseen)
	}

	status = strings.TrimRight(status, " ") + ")"

	s.WriteData(status)
	s.WriteResponse(s.tag, "OK STATUS completed")
	return nil
}

// APPEND command (RFC 3501) with MULTIAPPEND extension (RFC 7889)
func (s *Session) handleAppend(args []string, line string) error {
	ctx := context.Background()

	// Create tracing span
	var span trace.Span
	if s.server.tracingProvider != nil && s.server.tracingProvider.IsEnabled() {
		ctx, span = s.server.tracingProvider.StartSpanWithKind(ctx, "imap.append", tracing.SpanKindServer,
			attribute.String("session.id", s.id),
			attribute.String("user", s.user),
		)
		defer span.End()
	}

	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD Missing mailbox or message data")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "missing mailbox or message data")
		}
		return nil
	}

	mailboxName := strings.Trim(args[0], "\"'")

	if span != nil {
		tracing.SetStringAttribute(span, "append.mailbox", mailboxName)
	}

	// Posting into a public folder requires write access; a personal folder
	// resolves to the caller unchanged.
	owner, mbox, ok := s.resolvePublicFolder(mailboxName, uint8(storage.ACLWrite))
	if !ok {
		s.WriteResponse(s.tag, "NO [NOPERM] Access denied")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "public folder access denied")
		}
		return nil
	}

	// Limit APPEND message size to 50MB
	const maxAppendSize = 50 * 1024 * 1024

	// Process first message (has literal in command or needs continuation)
	flags, date, size, err := s.parseAppendParams(args[1:], line)
	if err != nil {
		s.WriteResponse(s.tag, "BAD "+err.Error())
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "parse error")
		}
		return nil
	}

	if size > maxAppendSize {
		s.WriteResponse(s.tag, "NO Message too large (limit 50MB)")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "message too large")
		}
		return nil
	}

	if span != nil {
		tracing.SetIntAttribute(span, "append.size", size)
		tracing.SetIntAttribute(span, "append.flag_count", len(flags))
	}

	// Check if non-synchronizing literal (no + suffix) - needs continuation
	needsCont := !strings.Contains(line, "+}")

	// Request the literal if non-synchronizing
	if needsCont {
		s.WriteContinuation(fmt.Sprintf("Ready for %d octets", size))
	}

	// Read the message data
	data := make([]byte, size)
	_, err = io.ReadFull(s.reader, data)
	if err != nil {
		s.WriteResponse(s.tag, "NO Failed to read message data")
		if span != nil {
			tracing.RecordError(span, err)
			tracing.SetStatus(span, tracing.StatusError, "read message data failed")
		}
		return err
	}

	// Collect the UID(s) assigned for the RFC 4315 APPENDUID response. A
	// MULTIAPPEND assigns one per message in the same mailbox, so the UIDVALIDITY
	// is captured once from the first stored message.
	var appendedUIDs []uint32
	var appendUIDValidity uint32

	// Append to mailbox
	if s.server.mailstore != nil {
		au, err := s.server.mailstore.AppendMessage(owner, mbox, flags, date, data)
		if err != nil {
			s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
			if span != nil {
				tracing.RecordError(span, err)
				tracing.SetStatus(span, tracing.StatusError, "append failed")
			}
			return nil
		}
		appendedUIDs = append(appendedUIDs, au.UID)
		appendUIDValidity = au.UIDValidity
	}

	// RFC 7889 MULTIAPPEND: Check for additional messages already buffered in
	// the connection after consuming the current literal.
	for {
		buffered := s.reader.Buffered()
		if buffered == 0 {
			break
		}

		rest, err := s.reader.Peek(buffered)
		if err != nil || len(rest) == 0 {
			break
		}

		restStr := string(rest)
		trimmed := strings.TrimLeft(restStr, "\r\n")
		consumed := len(restStr) - len(trimmed)
		if consumed > 0 {
			if _, err := s.reader.Discard(consumed); err != nil {
				break
			}
			if trimmed == "" {
				break
			}
			restStr = trimmed
		}

		litIdx := strings.Index(restStr, "{")
		if litIdx < 0 {
			break
		}

		litEnd := strings.Index(restStr[litIdx:], "}")
		if litEnd < 0 {
			break
		}

		sizeStr := restStr[litIdx+1 : litIdx+litEnd]
		nextSize, err := strconv.Atoi(sizeStr)
		if err != nil {
			break
		}

		if _, err := s.reader.Discard(litIdx + litEnd + 1); err != nil {
			break
		}

		if nextSize > maxAppendSize {
			s.WriteResponse(s.tag, "NO Message too large (limit 50MB)")
			if span != nil {
				tracing.SetStatus(span, tracing.StatusError, "message too large")
			}
			return nil
		}

		hasPlus := strings.Contains(restStr[litIdx:litIdx+litEnd+1], "+")
		if !hasPlus {
			s.WriteContinuation(fmt.Sprintf("Ready for %d octets", nextSize))
		}

		data := make([]byte, nextSize)
		_, err = io.ReadFull(s.reader, data)
		if err != nil {
			s.WriteResponse(s.tag, "NO Failed to read message data")
			return err
		}

		if s.server.mailstore != nil {
			au, err := s.server.mailstore.AppendMessage(owner, mbox, nil, time.Now(), data)
			if err != nil {
				s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
				return nil
			}
			appendedUIDs = append(appendedUIDs, au.UID)
		}
	}

	if span != nil {
		tracing.SetStatus(span, tracing.StatusOk, "")
	}

	// RFC 4315 UIDPLUS: report the assigned UID(s) in the tagged OK.
	if code := appendUIDCode(appendUIDValidity, appendedUIDs); code != "" {
		s.WriteResponse(s.tag, "OK "+code+" APPEND completed")
	} else {
		s.WriteResponse(s.tag, "OK APPEND completed")
	}
	return nil
}

// parseAppendParams extracts flags, date, and literal size from APPEND args
func (s *Session) parseAppendParams(args []string, line string) ([]string, time.Time, int, error) {
	flags := []string{}
	date := time.Now()
	size := 0

	// Check for flags in parentheses
	for i, arg := range args {
		if strings.HasPrefix(arg, "(") {
			flagsStr := strings.Join(args[i:], " ")
			end := strings.Index(flagsStr, ")")
			if end > 0 {
				flagsStr = flagsStr[1:end]
				flags = strings.Fields(flagsStr)
			}
			break
		}
	}

	// Find literal string indicator {N} in the command line
	// Handle both {size} and {size}+ forms
	literalStart := strings.Index(line, "{")
	if literalStart < 0 {
		return flags, date, 0, fmt.Errorf("missing literal size")
	}

	literalEnd := strings.Index(line[literalStart:], "}")
	if literalEnd < 0 {
		return flags, date, 0, fmt.Errorf("invalid literal format")
	}

	sizeStr := line[literalStart+1 : literalStart+literalEnd]
	size, err := strconv.Atoi(sizeStr)
	if err != nil {
		return flags, date, 0, fmt.Errorf("invalid literal size")
	}

	return flags, date, size, nil
}

// NAMESPACE command
func (s *Session) handleNamespace() error {
	// Personal namespace always; advertise the shared "Public Folders/" namespace
	// (RFC 2342 third element) only when the public-folder tree is exposed.
	if s.server.publicFoldersOn() {
		s.WriteData("NAMESPACE ((\"\" \"/\")) NIL ((\"" + publicFolderPrefix + "\" \"/\"))")
	} else {
		s.WriteData("NAMESPACE ((\"\" \"/\")) NIL NIL")
	}
	s.WriteResponse(s.tag, "OK NAMESPACE completed")
	return nil
}

// IDLE command (RFC 2177)
func (s *Session) handleIdle() error {
	// IDLE is only valid in Authenticated or Selected state
	if s.state != StateAuthenticated && s.state != StateSelected {
		s.WriteResponse(s.tag, "BAD Command not allowed in this state")
		return nil
	}

	// Subscribe to notifications for this user
	s.idleActive = true
	s.idleStop = make(chan struct{})
	s.idleNotifyChan = GetNotificationHub().Subscribe(s.user)

	defer func() {
		s.idleActive = false
		if s.idleNotifyChan != nil {
			GetNotificationHub().Unsubscribe(s.user, s.idleNotifyChan)
			s.idleNotifyChan = nil
		}
	}()

	// Send continuation response
	s.WriteContinuation("idling")

	// Channel for DONE command
	doneChan := make(chan bool, 1)

	// Start goroutine to wait for DONE
	go func() {
		for {
			line, err := s.readLine()
			if err != nil {
				doneChan <- true
				return
			}
			if strings.ToUpper(strings.TrimSpace(line)) == "DONE" {
				doneChan <- true
				return
			}
		}
	}()

	// idleCleanup ensures the DONE-reading goroutine exits by forcing
	// a read deadline on the connection, then waits for it to finish.
	idleCleanup := func() {
		select {
		case <-s.idleStop:
		default:
			close(s.idleStop)
		}
		_ = s.conn.SetReadDeadline(time.Now())
		// Wait for goroutine with timeout to avoid deadlock
		select {
		case <-doneChan:
		case <-time.After(5 * time.Second):
		}
	}

	// Wait for either DONE or notifications
	var idleTimer <-chan time.Time
	if s.server.idleTimeout > 0 {
		t := time.NewTimer(s.server.idleTimeout)
		defer t.Stop()
		idleTimer = t.C
	}

	for {
		select {
		case <-doneChan:
			s.WriteResponse(s.tag, "OK IDLE terminated")
			idleCleanup()
			return nil

		case <-idleTimer:
			s.WriteResponse(s.tag, "OK IDLE terminated")
			idleCleanup()
			return nil

		case notification, ok := <-s.idleNotifyChan:
			if !ok {
				s.WriteResponse(s.tag, "OK IDLE terminated")
				idleCleanup()
				return nil
			}

			// Only send notifications if a mailbox is selected
			if s.selected == nil {
				continue
			}

			// Only notify about changes to the selected mailbox
			if notification.Mailbox != s.selected.Name {
				continue
			}

			// Send appropriate untagged response based on notification type
			switch notification.Type {
			case NotificationNewMessage:
				// Send EXISTS and RECENT updates
				s.selected.Exists++
				s.selected.Recent++
				s.WriteData(fmt.Sprintf("%d EXISTS", s.selected.Exists))
				s.WriteData(fmt.Sprintf("%d RECENT", s.selected.Recent))

			case NotificationExpunge:
				// Send EXPUNGE update
				s.selected.Exists--
				if s.selected.Recent > 0 {
					s.selected.Recent--
				}
				// RFC 7162 §3.2.5.2: a QRESYNC client receives VANISHED instead of
				// EXPUNGE, provided the notification carries the UID it reports.
				if s.enabledCaps["QRESYNC"] && notification.MessageUID != 0 {
					s.WriteData(fmt.Sprintf("VANISHED %d", notification.MessageUID))
				} else {
					s.WriteData(fmt.Sprintf("%d EXPUNGE", notification.SeqNum))
				}

			case NotificationFlagsChanged:
				// Send FETCH response with updated flags
				flagsStr := ""
				if len(notification.Flags) > 0 {
					flagsStr = "(" + strings.Join(notification.Flags, " ") + ")"
				} else {
					flagsStr = "()"
				}
				s.WriteData(fmt.Sprintf("%d FETCH (FLAGS %s)", notification.SeqNum, flagsStr))

			case NotificationMailboxUpdate:
				// Re-fetch mailbox status and send updates
				if mailbox, err := s.server.mailstore.SelectMailbox(s.selOwner(), s.selected.Name); err == nil {
					if mailbox.Exists != s.selected.Exists {
						s.WriteData(fmt.Sprintf("%d EXISTS", mailbox.Exists))
					}
					if mailbox.Recent != s.selected.Recent {
						s.WriteData(fmt.Sprintf("%d RECENT", mailbox.Recent))
					}
					*s.selected = *mailbox
				}
			}
		}
	}
}

// ENABLE command (RFC 5161)
func (s *Session) handleEnable(args []string) error {
	enabled := []string{}
	for _, arg := range args {
		cap := strings.ToUpper(arg)
		if cap == "CONDSTORE" || cap == "QRESYNC" {
			s.enabledCaps[cap] = true
			enabled = append(enabled, cap)
		}
	}

	if len(enabled) > 0 {
		s.WriteData("ENABLED " + strings.Join(enabled, " "))
	}

	s.WriteResponse(s.tag, "OK ENABLE completed")
	return nil
}

// ID command (RFC 2971)
func (s *Session) handleID(args []string) error {
	// Client may send parenthesized list or NIL; we ignore client ID
	_ = args
	s.WriteData("* ID (\"name\" \"uMailServer\" \"version\" \"dev\")")
	s.WriteResponse(s.tag, "OK ID completed")
	return nil
}

// CHECK command
func (s *Session) handleCheck() error {
	s.WriteResponse(s.tag, "OK CHECK completed")
	return nil
}

// CLOSE command - RFC 3501: implicit EXPUNGE before deselecting
func (s *Session) handleClose() error {
	if s.selected != nil && s.server.mailstore != nil {
		_ = s.server.mailstore.Expunge(s.selOwner(), s.selected.Name) //nolint:errcheck // best-effort: CLOSE deselects regardless of expunge outcome
	}
	s.selected = nil
	s.selectedOwner = ""
	s.state = StateAuthenticated
	s.WriteResponse(s.tag, "OK CLOSE completed")
	return nil
}

// writeExpungeResponses emits the untagged responses for an expunge. With
// QRESYNC enabled (RFC 7162 §3.2.5.2) it sends a single VANISHED carrying the
// expunged UIDs; otherwise per-message "* N EXPUNGE" in highest-sequence-first
// order so the remaining sequence numbers stay valid during output.
func (s *Session) writeExpungeResponses(seqs, uids []uint32) {
	if s.enabledCaps["QRESYNC"] {
		if len(uids) > 0 {
			s.WriteData("VANISHED " + uidSetString(uids))
		}
		return
	}
	for i := len(seqs) - 1; i >= 0; i-- {
		s.WriteData(fmt.Sprintf("%d EXPUNGE", seqs[i]))
	}
}

// EXPUNGE command
func (s *Session) handleExpunge() error {
	ctx := context.Background()

	// Create tracing span
	var span trace.Span
	if s.server.tracingProvider != nil && s.server.tracingProvider.IsEnabled() {
		ctx, span = s.server.tracingProvider.StartSpanWithKind(ctx, "imap.expunge", tracing.SpanKindServer,
			attribute.String("session.id", s.id),
			attribute.String("user", s.user),
			attribute.String("mailbox", s.selected.Name),
		)
		defer span.End()
	}

	if s.server.mailstore == nil || s.selected == nil {
		s.WriteResponse(s.tag, "NO No mailbox selected")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "no mailbox selected")
		}
		return nil
	}

	// Before expunging, find messages with \Deleted flag to report their
	// sequence numbers via untagged EXPUNGE responses.
	criteria := SearchCriteria{Deleted: true}
	deletedSeqs, err := s.server.mailstore.SearchMessages(s.selOwner(), s.selected.Name, criteria)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		if span != nil {
			tracing.RecordError(span, err)
			tracing.SetStatus(span, tracing.StatusError, "search deleted messages failed")
		}
		return nil
	}

	if span != nil {
		tracing.SetIntAttribute(span, "expunge.deleted_count", len(deletedSeqs))
	}

	// Capture the \Deleted messages' UIDs before expunging — a QRESYNC VANISHED
	// response reports UIDs, which are gone from the index after Expunge.
	var deletedUIDs []uint32
	if s.enabledCaps["QRESYNC"] && len(deletedSeqs) > 0 {
		seqList := make([]string, len(deletedSeqs))
		for i, sn := range deletedSeqs {
			seqList[i] = fmt.Sprintf("%d", sn)
		}
		if msgs, ferr := s.server.mailstore.FetchMessages(s.selOwner(), s.selected.Name, strings.Join(seqList, ","), []string{"UID"}); ferr == nil {
			for _, m := range msgs {
				deletedUIDs = append(deletedUIDs, m.UID)
			}
		}
	}

	err = s.server.mailstore.Expunge(s.selOwner(), s.selected.Name)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		if span != nil {
			tracing.RecordError(span, err)
			tracing.SetStatus(span, tracing.StatusError, "expunge failed")
		}
		return nil
	}

	// Notify search index about expunged messages
	// Sequence numbers map 1:1 to position, so seq=N means the Nth message.
	// We pass sequence numbers as identifiers — search index uses folder+uid keys,
	// so this is a best-effort cleanup.
	if s.server.onExpunge != nil {
		for _, seq := range deletedSeqs {
			s.server.onExpunge(s.selOwner(), s.selected.Name, seq)
		}
	}

	// Untagged EXPUNGE (or VANISHED under QRESYNC) for the removed messages.
	s.writeExpungeResponses(deletedSeqs, deletedUIDs)

	if span != nil {
		tracing.SetStatus(span, tracing.StatusOk, "")
	}

	s.WriteResponse(s.tag, "OK EXPUNGE completed")
	return nil
}

// SEARCH command. When byUID is true (UID SEARCH), the returned identifiers are
// UIDs rather than sequence numbers (RFC 3501 §6.4.8).
func (s *Session) handleSearch(args []string, line string, byUID bool) error {
	ctx := context.Background()

	// Create tracing span
	var span trace.Span
	if s.server.tracingProvider != nil && s.server.tracingProvider.IsEnabled() {
		ctx, span = s.server.tracingProvider.StartSpanWithKind(ctx, "imap.search", tracing.SpanKindServer,
			attribute.String("session.id", s.id),
			attribute.String("user", s.user),
			attribute.String("mailbox", s.selected.Name),
		)
		defer span.End()
	}

	if s.server.mailstore == nil || s.selected == nil {
		s.WriteResponse(s.tag, "NO No mailbox selected")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "no mailbox selected")
		}
		return nil
	}

	// RFC 4731 ESEARCH: SEARCH RETURN (opts) criteria. The ESEARCH capability is
	// advertised, so honor RETURN here (previously it was ignored and a plain
	// SEARCH response was returned, which is not a valid ESEARCH reply).
	esearch := false
	var retCount, retMin, retMax, retAll bool
	searchArgs := args
	if len(args) > 0 && strings.ToUpper(args[0]) == "RETURN" {
		esearch = true
		joined := strings.Join(args[1:], " ")
		open := strings.Index(joined, "(")
		closeIdx := strings.Index(joined, ")")
		if open == 0 && closeIdx > open {
			for _, opt := range strings.Fields(joined[open+1 : closeIdx]) {
				switch strings.ToUpper(opt) {
				case "COUNT":
					retCount = true
				case "MIN":
					retMin = true
				case "MAX":
					retMax = true
				case "ALL":
					retAll = true
				}
			}
			searchArgs = tokenizeIMAPLine(joined[closeIdx+1:])
		} else {
			searchArgs = args[1:]
		}
		if !retCount && !retMin && !retMax && !retAll {
			retAll = true // RFC 4731: RETURN () defaults to ALL
		}
	}

	// Parse search criteria
	criteria := parseSearchCriteria(searchArgs)

	if span != nil {
		tracing.SetIntAttribute(span, "search.criteria_count", len(searchArgs))
	}

	uids, err := s.server.mailstore.SearchMessages(s.selOwner(), s.selected.Name, criteria)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		if span != nil {
			tracing.RecordError(span, err)
			tracing.SetStatus(span, tracing.StatusError, "search failed")
		}
		return nil
	}

	// RFC 7162 §3.1.5: a SEARCH with a MODSEQ criterion implicitly enables
	// CONDSTORE and appends the highest mod-sequence among the matches. uids are
	// still sequence numbers here (before the UID remap below).
	var highestMatchedModSeq uint64
	if criteria.HasModSeq && len(uids) > 0 {
		s.enabledCaps["CONDSTORE"] = true
		seqList := make([]string, len(uids))
		for i, sn := range uids {
			seqList[i] = fmt.Sprintf("%d", sn)
		}
		if msgs, ferr := s.server.mailstore.FetchMessages(s.selOwner(), s.selected.Name, strings.Join(seqList, ","), []string{"UID"}); ferr == nil {
			for _, mm := range msgs {
				if mm.ModSeq > highestMatchedModSeq {
					highestMatchedModSeq = mm.ModSeq
				}
			}
		}
	}

	// SearchMessages returns message sequence numbers. UID SEARCH must report
	// UIDs instead (RFC 3501 §6.4.8), so map the sequence numbers to UIDs.
	if byUID {
		msgs, _, merr := s.loadUIDOrder(s.selected.Name)
		if merr == nil {
			seqToUID := make(map[uint32]uint32, len(msgs))
			for _, m := range msgs {
				seqToUID[m.SeqNum] = m.UID
			}
			for i, seq := range uids {
				if u, ok := seqToUID[seq]; ok {
					uids[i] = u
				}
			}
		}
	}

	if span != nil {
		tracing.SetIntAttribute(span, "search.result_count", len(uids))
		tracing.SetStatus(span, tracing.StatusOk, "")
	}

	if esearch {
		// RFC 4731 ESEARCH reply: * ESEARCH (TAG "<tag>") [UID] COUNT n MIN x MAX y ALL set
		var parts []string
		if retCount {
			parts = append(parts, fmt.Sprintf("COUNT %d", len(uids)))
		}
		if len(uids) > 0 {
			lo, hi := uids[0], uids[0]
			for _, u := range uids {
				if u < lo {
					lo = u
				}
				if u > hi {
					hi = u
				}
			}
			if retMin {
				parts = append(parts, fmt.Sprintf("MIN %d", lo))
			}
			if retMax {
				parts = append(parts, fmt.Sprintf("MAX %d", hi))
			}
			if retAll {
				nums := make([]string, len(uids))
				for i, u := range uids {
					nums[i] = fmt.Sprintf("%d", u)
				}
				parts = append(parts, "ALL "+strings.Join(nums, ","))
			}
		}
		if criteria.HasModSeq && highestMatchedModSeq > 0 {
			parts = append(parts, fmt.Sprintf("MODSEQ %d", highestMatchedModSeq))
		}
		uidTag := ""
		if byUID {
			uidTag = "UID "
		}
		s.WriteData(fmt.Sprintf("ESEARCH (TAG \"%s\") %s%s", s.tag, uidTag, strings.Join(parts, " ")))
		s.WriteResponse(s.tag, "OK SEARCH completed")
		return nil
	}

	// Convert UIDs to sequence numbers and output
	// For simplicity, just output as SEARCH result
	result := "SEARCH"
	for _, uid := range uids {
		result += fmt.Sprintf(" %d", uid)
	}
	// RFC 7162 §3.1.5: append the highest mod-sequence of the matches.
	if criteria.HasModSeq && highestMatchedModSeq > 0 {
		result += fmt.Sprintf(" (MODSEQ %d)", highestMatchedModSeq)
	}
	s.WriteData(result)

	s.WriteResponse(s.tag, "OK SEARCH completed")
	return nil
}

// SORT command (RFC 5256)
func (s *Session) handleSort(args []string, line string) error {
	if s.server.mailstore == nil || s.selected == nil {
		s.WriteResponse(s.tag, "NO No mailbox selected")
		return nil
	}

	// RFC 5256 SORT: SORT (sort-keys) charset search-keys. Extract the
	// parenthesized sort-key list, the positional charset (ignored), and the
	// trailing search keys (applied as a filter below). The old parser fed the
	// whole arg list — including "(SUBJECT)", the charset and the search keys —
	// to parseSortCriteria, so every RFC-conformant SORT failed with BAD.
	joined := strings.Join(args, " ")
	open := strings.Index(joined, "(")
	closeIdx := strings.Index(joined, ")")
	if open < 0 || closeIdx < open {
		s.WriteResponse(s.tag, "BAD SORT requires a parenthesized sort-key list")
		return nil
	}
	sortKeyArgs := strings.Fields(joined[open+1 : closeIdx])
	rest := tokenizeIMAPLine(joined[closeIdx+1:]) // [charset, search-keys...]
	var searchArgs []string
	if len(rest) > 1 {
		searchArgs = rest[1:] // drop the positional charset token
	}

	criteria, err := parseSortCriteria(sortKeyArgs)
	if err != nil {
		s.server.logger.Error("imap sort criteria parse error", "error", err)
		s.WriteResponse(s.tag, "BAD invalid sort criteria")
		return nil
	}

	// Apply the search filter (default ALL = no filter).
	sortFilter := map[uint32]bool(nil)
	if len(searchArgs) > 0 && strings.ToUpper(strings.Join(searchArgs, " ")) != "ALL" {
		sc := parseSearchCriteria(searchArgs)
		if matched, serr := s.server.mailstore.SearchMessages(s.selOwner(), s.selected.Name, sc); serr == nil {
			sortFilter = make(map[uint32]bool, len(matched))
			for _, sn := range matched {
				sortFilter[sn] = true
			}
		}
	}

	// Get all messages in mailbox with metadata
	messages, err := s.server.mailstore.FetchMessages(s.selOwner(), s.selected.Name, "1:*", []string{"ENVELOPE"})
	if err != nil {
		s.server.logger.Error("imap fetch messages error", "error", err)
		s.WriteResponse(s.tag, "NO unable to fetch messages")
		return nil
	}

	// Build metadata list with sequence numbers
	var metas []*storage.MessageMetadata
	var seqNums []uint32
	seqNum := uint32(0)
	for _, msg := range messages {
		seqNum++
		seqNums = append(seqNums, seqNum)
		// Build a minimal MessageMetadata from the Message. Envelope may be nil
		// (parse failure / partial fetch) — guard every field, else SORT panics
		// with a nil dereference and the client hangs with no response.
		meta := &storage.MessageMetadata{UID: msg.UID, InternalDate: msg.InternalDate, Size: msg.Size}
		if msg.Envelope != nil {
			meta.MessageID = msg.Envelope.MessageID
			meta.Subject = msg.Envelope.Subject
			meta.From = addressToString(msg.Envelope.From)
			meta.Date = msg.Envelope.Date
			meta.InReplyTo = msg.Envelope.InReplyTo
		}
		metas = append(metas, meta)
	}

	// Sort
	sortedSeqNums := sortMessagesByCriteria(metas, criteria, seqNums)

	// Output result (honoring the search filter when present)
	result := "SORT"
	for _, seq := range sortedSeqNums {
		if sortFilter != nil && !sortFilter[seq] {
			continue
		}
		result += fmt.Sprintf(" %d", seq)
	}
	s.WriteData(result)
	s.WriteResponse(s.tag, "OK SORT completed")
	return nil
}

// addressToString converts Address slice to string
func addressToString(addrs []*Address) string {
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0].MailboxName + "@" + addrs[0].HostName
}

// THREAD command (RFC 5256)
func (s *Session) handleThread(args []string, line string) error {
	if s.server.mailstore == nil || s.selected == nil {
		s.WriteResponse(s.tag, "NO No mailbox selected")
		return nil
	}

	// Parse thread algorithm
	algo := ThreadReferences
	if len(args) > 0 {
		arg := strings.ToUpper(args[0])
		if arg == "ORDEREDSUBJECT" {
			algo = ThreadOrderedSubject
		} else if arg == "REFERENCES" {
			algo = ThreadReferences
		}
	}

	// Get all messages in mailbox
	messages, err := s.server.mailstore.FetchMessages(s.selOwner(), s.selected.Name, "1:*", []string{"ENVELOPE"})
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	// Build metadata list with sequence numbers
	var metas []*storage.MessageMetadata
	var seqNums []uint32
	seqNum := uint32(0)
	for _, msg := range messages {
		seqNum++
		seqNums = append(seqNums, seqNum)
		// Envelope may be nil — guard every field (see handleSort).
		meta := &storage.MessageMetadata{UID: msg.UID, InternalDate: msg.InternalDate}
		if msg.Envelope != nil {
			meta.MessageID = msg.Envelope.MessageID
			meta.Subject = msg.Envelope.Subject
			meta.From = addressToString(msg.Envelope.From)
			meta.Date = msg.Envelope.Date
			meta.InReplyTo = msg.Envelope.InReplyTo
		}
		metas = append(metas, meta)
	}

	var children map[uint32][]uint32
	if algo == ThreadReferences {
		children = threadMessagesByReferences(metas, seqNums)
	} else {
		children = threadMessagesByOrderedSubject(metas, seqNums)
	}

	// Find all root messages (those that are not children)
	allChildren := make(map[uint32]bool)
	for _, kids := range children {
		for _, child := range kids {
			allChildren[child] = true
		}
	}

	var roots []uint32
	for _, seq := range seqNums {
		if !allChildren[seq] {
			roots = append(roots, seq)
		}
	}

	// Output threads as a single untagged THREAD response (RFC 5256):
	//   * THREAD (1)(2 3)(4)
	// Each parenthesized group is one thread; nesting is flattened to a member
	// list. (Previously each group was written on its own line without the
	// THREAD keyword, which no RFC 5256 client recognizes.)
	visited := make(map[uint32]bool)
	result := "THREAD "
	for _, root := range roots {
		threadSeqNums := flattenThread(root, children, visited)
		result += "("
		for i, seq := range threadSeqNums {
			if i > 0 {
				result += " "
			}
			result += fmt.Sprintf("%d", seq)
		}
		result += ")"
	}
	s.WriteData(result)

	s.WriteResponse(s.tag, "OK THREAD completed")
	return nil
}

// UID SORT command
func (s *Session) handleUIDSort(args []string, line string) error {
	// Add UID prefix to results
	// Parse criteria from args
	var criteriaArgs []string
	if len(args) > 0 && strings.ToUpper(args[0]) != "CHARSET" {
		criteriaArgs = args
	} else {
		if len(args) > 1 {
			criteriaArgs = args[1:]
		}
	}

	criteria, err := parseSortCriteria(criteriaArgs)
	if err != nil {
		s.server.logger.Error("imap sort criteria parse error", "error", err)
		s.WriteResponse(s.tag, "BAD invalid sort criteria")
		return nil
	}

	// Get all messages with UID
	messages, err := s.server.mailstore.FetchMessages(s.selOwner(), s.selected.Name, "1:*", []string{"ENVELOPE"})
	if err != nil {
		s.server.logger.Error("imap fetch messages error", "error", err)
		s.WriteResponse(s.tag, "NO unable to fetch messages")
		return nil
	}

	var metas []*storage.MessageMetadata
	var seqNums []uint32
	var uids []uint32
	seqNum := uint32(0)
	for _, msg := range messages {
		seqNum++
		seqNums = append(seqNums, seqNum)
		uids = append(uids, msg.UID)
		meta := &storage.MessageMetadata{
			MessageID:    msg.Envelope.MessageID,
			UID:          msg.UID,
			Subject:      msg.Envelope.Subject,
			From:         addressToString(msg.Envelope.From),
			Date:         msg.Envelope.Date,
			InternalDate: msg.InternalDate,
			Size:         msg.Size,
		}
		metas = append(metas, meta)
	}

	sortedSeqNums := sortMessagesByCriteria(metas, criteria, seqNums)

	// Convert sequence numbers to UIDs
	result := "SORT"
	for _, seq := range sortedSeqNums {
		// Find corresponding UID
		for i, s := range seqNums {
			if s == seq {
				result += fmt.Sprintf(" %d", uids[i])
				break
			}
		}
	}
	s.WriteData(result)
	s.WriteResponse(s.tag, "OK UID SORT completed")
	return nil
}

// UID THREAD command
func (s *Session) handleUIDThread(args []string, line string) error {
	// Parse thread algorithm
	algo := ThreadReferences
	if len(args) > 0 {
		arg := strings.ToUpper(args[0])
		if arg == "ORDEREDSUBJECT" {
			algo = ThreadOrderedSubject
		} else if arg == "REFERENCES" {
			algo = ThreadReferences
		}
	}

	// Get all messages
	messages, err := s.server.mailstore.FetchMessages(s.selOwner(), s.selected.Name, "1:*", []string{"ENVELOPE"})
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	var metas []*storage.MessageMetadata
	var seqNums []uint32
	var uids []uint32
	seqNum := uint32(0)
	for _, msg := range messages {
		seqNum++
		seqNums = append(seqNums, seqNum)
		uids = append(uids, msg.UID)
		meta := &storage.MessageMetadata{
			MessageID:    msg.Envelope.MessageID,
			UID:          msg.UID,
			Subject:      msg.Envelope.Subject,
			From:         addressToString(msg.Envelope.From),
			Date:         msg.Envelope.Date,
			InternalDate: msg.InternalDate,
		}
		meta.InReplyTo = msg.Envelope.InReplyTo
		metas = append(metas, meta)
	}

	var children map[uint32][]uint32
	if algo == ThreadReferences {
		children = threadMessagesByReferences(metas, seqNums)
	} else {
		children = threadMessagesByOrderedSubject(metas, seqNums)
	}

	// Find roots and build seq->uid mapping
	allChildren := make(map[uint32]bool)
	for _, kids := range children {
		for _, child := range kids {
			allChildren[child] = true
		}
	}

	var roots []uint32
	for _, seq := range seqNums {
		if !allChildren[seq] {
			roots = append(roots, seq)
		}
	}

	// seq to uid mapping
	seqToUID := make(map[uint32]uint32)
	for i, seq := range seqNums {
		seqToUID[seq] = uids[i]
	}

	visited := make(map[uint32]bool)
	for _, root := range roots {
		threadSeqNums := flattenThread(root, children, visited)
		threadStr := "("
		for i, seq := range threadSeqNums {
			if i > 0 {
				threadStr += " "
			}
			threadStr += fmt.Sprintf("%d", seqToUID[seq])
		}
		threadStr += ")"
		s.WriteData(threadStr)
	}

	s.WriteResponse(s.tag, "OK UID THREAD completed")
	return nil
}

// FETCH command
// FETCH command. When byUID is true (UID FETCH), args[0] is a UID set and the
// response implicitly includes the UID data item (RFC 3501 §6.4.8).
func (s *Session) handleFetch(args []string, line string, byUID bool) error {
	ctx := context.Background()

	// Create tracing span
	var span trace.Span
	if s.server.tracingProvider != nil && s.server.tracingProvider.IsEnabled() {
		ctx, span = s.server.tracingProvider.StartSpanWithKind(ctx, "imap.fetch", tracing.SpanKindServer,
			attribute.String("session.id", s.id),
			attribute.String("user", s.user),
			attribute.String("mailbox", s.selected.Name),
		)
		defer span.End()
	}

	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD Missing sequence or fetch items")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "missing sequence or fetch items")
		}
		return nil
	}

	if s.server.mailstore == nil || s.selected == nil {
		s.WriteResponse(s.tag, "NO No mailbox selected")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "no mailbox selected")
		}
		return nil
	}

	seqSet := args[0]
	origUIDSet := args[0] // the requested UID set, before seq translation (UID FETCH)
	// RFC 7162 §3.1.4 / §3.2.5.1: an optional trailing
	// "(CHANGEDSINCE <modseq> [VANISHED])" modifier restricts the response to
	// messages changed since <modseq>, implicitly forces MODSEQ into the response
	// (and enables CONDSTORE), and — with VANISHED on a UID FETCH — replays the
	// UIDs in the set expunged since <modseq> as VANISHED (EARLIER).
	itemArgs, changedSince, hasChangedSince, wantVanished := extractChangedSince(args[1:])
	fetchItems := parseFetchItems(itemArgs)
	if hasChangedSince {
		s.enabledCaps["CONDSTORE"] = true
		fetchItems = appendItemIfAbsent(fetchItems, "MODSEQ")
	}

	if byUID {
		translated, terr := s.uidSetToSeqSet(s.selected.Name, seqSet)
		if terr != nil {
			s.WriteResponse(s.tag, fmt.Sprintf("NO %s", terr))
			if span != nil {
				tracing.SetStatus(span, tracing.StatusError, "uid set translation failed")
			}
			return nil
		}
		seqSet = translated
		// RFC 3501 §6.4.8: a UID FETCH response MUST implicitly include UID.
		hasUID := false
		for _, it := range fetchItems {
			if strings.EqualFold(it, "UID") {
				hasUID = true
				break
			}
		}
		if !hasUID {
			fetchItems = append(fetchItems, "UID")
		}
	}

	if span != nil {
		tracing.SetStringAttribute(span, "fetch.seqset", seqSet)
		tracing.SetIntAttribute(span, "fetch.item_count", len(fetchItems))
	}

	messages, err := s.server.mailstore.FetchMessages(s.selOwner(), s.selected.Name, seqSet, fetchItems)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		if span != nil {
			tracing.RecordError(span, err)
			tracing.SetStatus(span, tracing.StatusError, "fetch failed")
		}
		return nil
	}

	if span != nil {
		tracing.SetIntAttribute(span, "fetch.message_count", len(messages))
	}

	// RFC 7162 §3.1.4: with CHANGEDSINCE, only report messages whose mod-sequence
	// exceeds the given value.
	if hasChangedSince {
		filtered := messages[:0]
		for _, msg := range messages {
			if msg.ModSeq > changedSince {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}

	// RFC 7162 §3.2.5.1: with the VANISHED modifier on a UID FETCH, report the
	// UIDs in the requested set expunged since <modseq> as VANISHED (EARLIER),
	// ahead of the FETCH data responses.
	if byUID && wantVanished && hasChangedSince {
		if exp, eerr := s.server.mailstore.ExpungedUIDsSince(s.selOwner(), s.selected.Name, changedSince); eerr == nil && len(exp) > 0 {
			if vset := filterUIDsInSet(exp, origUIDSet); len(vset) > 0 {
				s.WriteData("VANISHED (EARLIER) " + uidSetString(vset))
			}
		}
	}

	for _, msg := range messages {
		fetchResponse := formatFetchResponse(msg, fetchItems)
		s.WriteData(fmt.Sprintf("%d FETCH (%s)", msg.SeqNum, fetchResponse))
	}

	if span != nil {
		tracing.SetStatus(span, tracing.StatusOk, "")
	}

	s.WriteResponse(s.tag, "OK FETCH completed")
	return nil
}

// STORE command. When byUID is true (UID STORE), args[0] is a UID set and the
// FLAGS responses implicitly include the UID data item (RFC 3501 §6.4.8).
func (s *Session) handleStore(args []string, byUID bool) error {
	ctx := context.Background()

	// Create tracing span
	var span trace.Span
	if s.server.tracingProvider != nil && s.server.tracingProvider.IsEnabled() {
		ctx, span = s.server.tracingProvider.StartSpanWithKind(ctx, "imap.store", tracing.SpanKindServer,
			attribute.String("session.id", s.id),
			attribute.String("user", s.user),
			attribute.String("mailbox", s.selected.Name),
		)
		defer span.End()
	}

	if len(args) < 3 {
		s.WriteResponse(s.tag, "BAD Missing sequence, operation, or flags")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "missing sequence, operation, or flags")
		}
		return nil
	}

	if s.server.mailstore == nil || s.selected == nil {
		s.WriteResponse(s.tag, "NO No mailbox selected")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "no mailbox selected")
		}
		return nil
	}

	seqSet := args[0]

	// RFC 7162: optional (UNCHANGEDSINCE n) modifier between the sequence set and
	// the operation. Messages whose modseq exceeds n are left untouched and
	// reported back in a MODIFIED response code.
	rest := args[1:]
	var unchangedSince uint64
	hasUnchangedSince := false
	if len(rest) > 0 && strings.HasPrefix(rest[0], "(") {
		var modTokens []string
		for len(rest) > 0 {
			t := rest[0]
			rest = rest[1:]
			modTokens = append(modTokens, strings.Trim(t, "()"))
			if strings.HasSuffix(t, ")") {
				break
			}
		}
		if len(modTokens) >= 2 && strings.ToUpper(modTokens[0]) == "UNCHANGEDSINCE" {
			if n, perr := strconv.ParseUint(modTokens[1], 10, 64); perr == nil {
				unchangedSince = n
				hasUnchangedSince = true
			}
		}
	}
	if len(rest) < 2 {
		s.WriteResponse(s.tag, "BAD Missing operation or flags")
		return nil
	}
	operation := strings.ToUpper(rest[0]) // FLAGS, +FLAGS, -FLAGS
	flagsStr := strings.Join(rest[1:], " ")

	// Partition the target set by UNCHANGEDSINCE before applying. The check works
	// in sequence-number space, so do it after a UID->seq translation when needed.
	var modifiedUIDs []uint32
	skipTranslate := false
	if hasUnchangedSince {
		fetchSet := seqSet
		if byUID {
			if t, e := s.uidSetToSeqSet(s.selected.Name, seqSet); e == nil {
				fetchSet = t
			}
		}
		if msgs, ferr := s.server.mailstore.FetchMessages(s.selOwner(), s.selected.Name, fetchSet, []string{"FLAGS", "UID"}); ferr == nil {
			var allowed []string
			for _, mmsg := range msgs {
				if mmsg.ModSeq > unchangedSince {
					modifiedUIDs = append(modifiedUIDs, mmsg.UID)
				} else {
					allowed = append(allowed, fmt.Sprintf("%d", mmsg.SeqNum))
				}
			}
			if len(allowed) == 0 {
				// Nothing left to update: report MODIFIED only if some entries
				// actually failed the check, else a plain OK (empty/no match).
				if len(modifiedUIDs) > 0 {
					s.WriteResponse(s.tag, fmt.Sprintf("OK [MODIFIED %s] STORE completed", joinUint32(modifiedUIDs)))
				} else {
					s.WriteResponse(s.tag, "OK STORE completed")
				}
				return nil
			}
			seqSet = strings.Join(allowed, ",")
			skipTranslate = true // seqSet is already sequence numbers
		}
	}

	if byUID && !skipTranslate {
		translated, terr := s.uidSetToSeqSet(s.selected.Name, seqSet)
		if terr != nil {
			s.WriteResponse(s.tag, fmt.Sprintf("NO %s", terr))
			if span != nil {
				tracing.SetStatus(span, tracing.StatusError, "uid set translation failed")
			}
			return nil
		}
		seqSet = translated
	}

	// Parse flags
	flags := parseFlags(flagsStr)

	if span != nil {
		tracing.SetStringAttribute(span, "store.seqset", seqSet)
		tracing.SetStringAttribute(span, "store.operation", operation)
		tracing.SetIntAttribute(span, "store.flag_count", len(flags))
	}

	var op FlagOperation
	switch operation {
	case "FLAGS", "FLAGS.SILENT":
		op = FlagReplace
	case "+FLAGS", "+FLAGS.SILENT":
		op = FlagAdd
	case "-FLAGS", "-FLAGS.SILENT":
		op = FlagRemove
	default:
		s.WriteResponse(s.tag, "BAD Invalid STORE operation")
		if span != nil {
			tracing.SetStatus(span, tracing.StatusError, "invalid operation")
		}
		return nil
	}

	err := s.server.mailstore.StoreFlags(s.selOwner(), s.selected.Name, seqSet, flags, op)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		if span != nil {
			tracing.RecordError(span, err)
			tracing.SetStatus(span, tracing.StatusError, "store failed")
		}
		return nil
	}

	// If not silent, fetch updated messages and output FLAGS responses. A UID
	// STORE response implicitly includes the UID data item (RFC 3501 §6.4.8).
	if !strings.HasSuffix(operation, ".SILENT") {
		fetchItems := []string{"FLAGS"}
		if byUID {
			fetchItems = append(fetchItems, "UID")
		}
		messages, fetchErr := s.server.mailstore.FetchMessages(s.selOwner(), s.selected.Name, seqSet, fetchItems)
		if fetchErr == nil {
			// RFC 7162 §3.2: once CONDSTORE/QRESYNC is enabled, every unsolicited
			// FETCH (a STORE result included) MUST carry the updated MODSEQ.
			condstore := s.enabledCaps["CONDSTORE"] || s.enabledCaps["QRESYNC"]
			for _, msg := range messages {
				var inner string
				if byUID {
					inner = fmt.Sprintf("UID %d FLAGS (%s)", msg.UID, strings.Join(msg.Flags, " "))
				} else {
					inner = fmt.Sprintf("FLAGS (%s)", strings.Join(msg.Flags, " "))
				}
				if condstore {
					inner += fmt.Sprintf(" MODSEQ (%d)", msg.ModSeq)
				}
				s.WriteData(fmt.Sprintf("%d FETCH (%s)", msg.SeqNum, inner))
			}
		}
	}

	if span != nil {
		tracing.SetStatus(span, tracing.StatusOk, "")
	}

	if len(modifiedUIDs) > 0 {
		// RFC 7162: report the entries that failed the UNCHANGEDSINCE test.
		s.WriteResponse(s.tag, fmt.Sprintf("OK [MODIFIED %s] STORE completed", joinUint32(modifiedUIDs)))
		return nil
	}
	s.WriteResponse(s.tag, "OK STORE completed")
	return nil
}

// joinUint32 renders a uint32 slice as a comma-separated IMAP sequence-set.
func joinUint32(nums []uint32) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, ",")
}

// COPY command. When byUID is true (UID COPY), args[0] is a UID set.
// uidSetString formats UIDs as an IMAP sequence-set for the RFC 4315
// COPYUID/APPENDUID response codes: sorted ascending with consecutive runs
// collapsed into "a:b". Returns "" for an empty set.
func uidSetString(uids []uint32) string {
	if len(uids) == 0 {
		return ""
	}
	sorted := append([]uint32(nil), uids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var b strings.Builder
	start, prev := sorted[0], sorted[0]
	flush := func() {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if start == prev {
			fmt.Fprintf(&b, "%d", start)
		} else {
			fmt.Fprintf(&b, "%d:%d", start, prev)
		}
	}
	for _, u := range sorted[1:] {
		if u == prev+1 {
			prev = u
			continue
		}
		flush()
		start, prev = u, u
	}
	flush()
	return b.String()
}

// copyUIDCode returns the RFC 4315 "[COPYUID validity src dst]" response code for
// a COPY/MOVE, or "" when nothing was copied (so the caller omits it). The src
// and dst UID sets correspond positionally once both are sorted ascending.
func copyUIDCode(cu CopyUIDs) string {
	if len(cu.DstUIDs) == 0 {
		return ""
	}
	return fmt.Sprintf("[COPYUID %d %s %s]", cu.UIDValidity, uidSetString(cu.SrcUIDs), uidSetString(cu.DstUIDs))
}

// appendUIDCode returns the RFC 4315 "[APPENDUID validity uidset]" response code
// for an APPEND (uidset is a single UID, or the run of UIDs a MULTIAPPEND
// assigned), or "" when nothing was stored (so the caller omits it).
func appendUIDCode(uidValidity uint32, uids []uint32) string {
	if uidValidity == 0 || len(uids) == 0 {
		return ""
	}
	return fmt.Sprintf("[APPENDUID %d %s]", uidValidity, uidSetString(uids))
}

func (s *Session) handleCopy(args []string, byUID bool) error {
	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD Missing sequence or destination")
		return nil
	}

	if s.server.mailstore == nil || s.selected == nil {
		s.WriteResponse(s.tag, "NO No mailbox selected")
		return nil
	}

	seqSet := args[0]
	destMailbox := strings.Trim(args[1], "\"'")

	if byUID {
		translated, terr := s.uidSetToSeqSet(s.selected.Name, seqSet)
		if terr != nil {
			s.WriteResponse(s.tag, fmt.Sprintf("NO %s", terr))
			return nil
		}
		seqSet = translated
	}

	cu, err := s.server.mailstore.CopyMessages(s.selOwner(), s.selected.Name, destMailbox, seqSet)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	// RFC 4315 UIDPLUS: report the source->dest UID mapping in the tagged OK.
	if code := copyUIDCode(cu); code != "" {
		s.WriteResponse(s.tag, "OK "+code+" COPY completed")
	} else {
		s.WriteResponse(s.tag, "OK COPY completed")
	}
	return nil
}

// MOVE command. When byUID is true (UID MOVE), args[0] is a UID set.
func (s *Session) handleMove(args []string, byUID bool) error {
	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD Missing sequence or destination")
		return nil
	}

	if s.server.mailstore == nil || s.selected == nil {
		s.WriteResponse(s.tag, "NO No mailbox selected")
		return nil
	}

	seqSet := args[0]
	destMailbox := strings.Trim(args[1], "\"'")

	if byUID {
		translated, terr := s.uidSetToSeqSet(s.selected.Name, seqSet)
		if terr != nil {
			s.WriteResponse(s.tag, fmt.Sprintf("NO %s", terr))
			return nil
		}
		seqSet = translated
	}

	copied, expungedSeqs, expungedUIDs, err := s.server.mailstore.MoveMessages(s.selOwner(), s.selected.Name, destMailbox, seqSet)
	if err != nil {
		s.WriteResponse(s.tag, fmt.Sprintf("NO %s", err))
		return nil
	}

	// RFC 6851 §4.3 / RFC 4315: emit the COPYUID mapping as an untagged OK
	// response code first, then (RFC 6851) the untagged EXPUNGE responses for the
	// removed source messages — highest sequence first so the remaining numbers
	// stay valid — and notify the search index by UID, before the tagged OK.
	if code := copyUIDCode(copied); code != "" {
		s.WriteData("OK " + code)
	}
	if s.server.onExpunge != nil {
		for _, uid := range expungedUIDs {
			s.server.onExpunge(s.selOwner(), s.selected.Name, uid)
		}
	}
	s.writeExpungeResponses(expungedSeqs, expungedUIDs)

	s.WriteResponse(s.tag, "OK MOVE completed")
	return nil
}

// loadUIDOrder returns the selected mailbox's messages in ascending UID order
// (each carrying its 1-based sequence number) plus the highest UID, used to
// translate between UID sets and sequence sets for UID-prefixed commands
// (RFC 3501 §6.4.8).
func (s *Session) loadUIDOrder(mailbox string) ([]*Message, uint32, error) {
	msgs, err := s.server.mailstore.FetchMessages(s.selOwner(), mailbox, "1:*", []string{"UID"})
	if err != nil {
		return nil, 0, err
	}
	var maxUID uint32
	for _, m := range msgs {
		if m.UID > maxUID {
			maxUID = m.UID
		}
	}
	return msgs, maxUID, nil
}

// uidSetToSeqSet converts a UID set (e.g. "5:9,12") into the equivalent message
// sequence set (e.g. "2,3,7") for the messages currently in the mailbox. A UID
// in the set that no longer exists is simply skipped, and an empty result means
// the UID command matches nothing — both of which are the correct behavior for
// UID commands referencing absent UIDs.
func (s *Session) uidSetToSeqSet(mailbox, uidSet string) (string, error) {
	ranges, err := ParseSequenceSet(uidSet)
	if err != nil {
		return "", err
	}
	msgs, maxUID, err := s.loadUIDOrder(mailbox)
	if err != nil {
		return "", err
	}
	var seqs []string
	for _, m := range msgs {
		for _, r := range ranges {
			if r.Contains(m.UID, maxUID) {
				seqs = append(seqs, strconv.FormatUint(uint64(m.SeqNum), 10))
				break
			}
		}
	}
	return strings.Join(seqs, ","), nil
}

// UID command (prefix for UID variants)
func (s *Session) handleUID(args []string, line string) error {
	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD Missing UID command")
		return nil
	}

	uidCommand := strings.ToUpper(args[0])
	uidArgs := args[1:]

	switch uidCommand {
	case "FETCH":
		return s.handleUIDFetch(uidArgs, line)
	case "STORE":
		return s.handleUIDStore(uidArgs)
	case "COPY":
		return s.handleUIDCopy(uidArgs)
	case "MOVE":
		return s.handleUIDMove(uidArgs)
	case "SEARCH":
		return s.handleUIDSearch(uidArgs, line)
	case "SORT":
		return s.handleUIDSort(uidArgs, line)
	case "THREAD":
		return s.handleUIDThread(uidArgs, line)
	case "EXPUNGE":
		return s.handleUIDExpunge(uidArgs)
	default:
		s.WriteResponse(s.tag, "BAD Unknown UID command")
		return nil
	}
}

func (s *Session) handleUIDFetch(args []string, line string) error {
	// FETCH whose message set is interpreted as UIDs.
	return s.handleFetch(args, line, true)
}

func (s *Session) handleUIDStore(args []string) error {
	// STORE whose message set is interpreted as UIDs.
	return s.handleStore(args, true)
}

func (s *Session) handleUIDCopy(args []string) error {
	// COPY whose message set is interpreted as UIDs.
	return s.handleCopy(args, true)
}

func (s *Session) handleUIDMove(args []string) error {
	// MOVE whose message set is interpreted as UIDs.
	return s.handleMove(args, true)
}

func (s *Session) handleUIDSearch(args []string, line string) error {
	// SEARCH whose results are reported as UIDs.
	return s.handleSearch(args, line, true)
}

// handleUIDExpunge implements RFC 4315 UID EXPUNGE: permanently remove messages
// that both have the \Deleted flag set and whose UID is in the given UID set.
func (s *Session) handleUIDExpunge(args []string) error {
	if s.server.mailstore == nil || s.selected == nil {
		s.WriteResponse(s.tag, "NO No mailbox selected")
		return nil
	}

	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD UID EXPUNGE requires a UID set")
		return nil
	}

	ranges, err := ParseSequenceSet(args[0])
	if err != nil {
		s.WriteResponse(s.tag, "BAD Invalid UID set")
		return nil
	}

	expungedSeqs, expungedUIDs, err := s.server.mailstore.ExpungeUIDs(s.selOwner(), s.selected.Name, ranges)
	if err != nil {
		// ExpungeUIDs is best-effort: it removes every message it can and
		// returns the first error it hit. Log it (fail loud) but still report
		// the messages that were successfully expunged.
		s.server.logger.Error("imap uid expunge encountered errors", "user", s.user, "mailbox", s.selected.Name, "error", err)
	}

	// Notify the search index about expunged messages. The index keys by
	// folder+uid, so it must receive UIDs (not sequence numbers).
	if s.server.onExpunge != nil {
		for _, uid := range expungedUIDs {
			s.server.onExpunge(s.selOwner(), s.selected.Name, uid)
		}
	}

	// Untagged EXPUNGE (or VANISHED under QRESYNC) for the removed messages.
	s.writeExpungeResponses(expungedSeqs, expungedUIDs)

	s.WriteResponse(s.tag, "OK UID EXPUNGE completed")
	return nil
}

// handleGetACL implements RFC 4314 GETACL command
func (s *Session) handleGetACL(args []string) error {
	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD GETACL requires a mailbox name")
		return nil
	}

	mailbox := args[0]

	// User must be authenticated
	if s.state == StateNotAuthenticated {
		s.WriteResponse(s.tag, "NO Not authenticated")
		return nil
	}

	// Get owner - for own mailbox, user is owner; for shared, we need to parse owner:mailbox
	owner, mb, isShared := s.parseOwnerMailbox(mailbox)
	if isShared && owner != s.user {
		// Check if user has ACL lookup right on this shared mailbox
		rights, err := s.server.mailstore.GetACL(owner, mb, s.user)
		if err != nil || rights&uint8(storage.ACLLookup) == 0 {
			s.WriteResponse(s.tag, "NO Access denied")
			return nil
		}
	}

	aclEntries, err := s.server.mailstore.ListACL(owner, mb)
	if err != nil {
		s.WriteResponse(s.tag, "NO Internal server error")
		return nil
	}

	// Send untagged ACL responses
	for _, entry := range aclEntries {
		s.WriteData(fmt.Sprintf("ACL %s %s %s", mailbox, entry.Grantee, entry.Rights.String()))
	}

	s.WriteResponse(s.tag, "OK GETACL completed")
	return nil
}

// handleSetACL implements RFC 4314 SETACL command
func (s *Session) handleSetACL(args []string) error {
	if len(args) < 3 {
		s.WriteResponse(s.tag, "BAD SETACL requires mailbox, grantee, and rights")
		return nil
	}

	mailbox := args[0]
	grantee := args[1]
	rightsStr := args[2]

	// User must be authenticated
	if s.state == StateNotAuthenticated {
		s.WriteResponse(s.tag, "NO Not authenticated")
		return nil
	}

	// Parse owner:mailbox format if shared
	owner, mb, isShared := s.parseOwnerMailbox(mailbox)

	// Only owner can set ACL
	if isShared && owner != s.user {
		s.WriteResponse(s.tag, "NO Only owner can modify ACL")
		return nil
	}

	// Parse rights string (e.g., "lrswipkxtecda" or "-lrswipkxtecda" or numeric)
	rights, err := storage.ParseACLRights(rightsStr)
	if err != nil {
		s.WriteResponse(s.tag, "BAD Invalid rights format")
		return nil
	}

	err = s.server.mailstore.SetACL(owner, mb, grantee, uint8(rights), s.user)
	if err != nil {
		s.WriteResponse(s.tag, "NO Failed to set ACL")
		return nil
	}

	s.WriteResponse(s.tag, "OK SETACL completed")
	return nil
}

// handleDeleteACL implements RFC 4314 DELETEACL command
func (s *Session) handleDeleteACL(args []string) error {
	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD DELETEACL requires mailbox and grantee")
		return nil
	}

	mailbox := args[0]
	grantee := args[1]

	// User must be authenticated
	if s.state == StateNotAuthenticated {
		s.WriteResponse(s.tag, "NO Not authenticated")
		return nil
	}

	// Parse owner:mailbox format if shared
	owner, mb, isShared := s.parseOwnerMailbox(mailbox)

	// Only owner can delete ACL
	if isShared && owner != s.user {
		s.WriteResponse(s.tag, "NO Only owner can delete ACL")
		return nil
	}

	err := s.server.mailstore.DeleteACL(owner, mb, grantee)
	if err != nil {
		s.WriteResponse(s.tag, "NO Failed to delete ACL")
		return nil
	}

	s.WriteResponse(s.tag, "OK DELETEACL completed")
	return nil
}

// handleMyRights implements RFC 4314 MYRIGHTS command
func (s *Session) handleMyRights(args []string) error {
	if len(args) < 1 {
		s.WriteResponse(s.tag, "BAD MYRIGHTS requires a mailbox name")
		return nil
	}

	mailbox := args[0]

	// User must be authenticated
	if s.state == StateNotAuthenticated {
		s.WriteResponse(s.tag, "NO Not authenticated")
		return nil
	}

	// Parse owner:mailbox format if shared
	owner, mb, isShared := s.parseOwnerMailbox(mailbox)

	var rights storage.ACLRights

	if isShared {
		if owner == s.user {
			rights = storage.ACLAll // Owner has all rights
		} else {
			aclRights, err := s.server.mailstore.GetACL(owner, mb, s.user)
			rights = storage.ACLRights(aclRights)
			if err != nil {
				s.WriteResponse(s.tag, "NO Internal server error")
				return nil
			}
		}
	} else {
		// Own mailbox - user has all rights
		rights = storage.ACLAll
	}

	s.WriteData(fmt.Sprintf("MYRIGHTS %s %s", mailbox, rights.String()))
	s.WriteResponse(s.tag, "OK MYRIGHTS completed")
	return nil
}

// normalizeUsername applies PRECIS UsernameCaseMapped profile (RFC 7616).
// Returns error if username contains invalid characters.
func normalizeUsername(username string) (string, error) {
	if username == "" {
		return "", fmt.Errorf("empty username")
	}

	// RFC 7616 Section 3: UsernameCaseMapped profile
	// 1. Ensure all characters are in NFC form
	username = strings.TrimSpace(username)

	// 2. Map uppercase letters to their lowercase equivalents (RFC 7616 Section 5.2)
	var lower strings.Builder
	for _, r := range username {
		// Apply RFC 7616 Section 5.2: case mapping
		// Map uppercase letters to lowercase
		lower.WriteRune(unicode.ToLower(r))
	}
	username = lower.String()

	// 3. Ensure no prohibited characters (RFC 7616 Section 5.3)
	// Prohibited: ASCII control chars, space, slash, null
	for _, r := range username {
		if r < 0x20 || r == 0x7F || r == ' ' || r == '/' || r == 0x00 {
			return "", fmt.Errorf("username contains prohibited character")
		}
	}

	// 4. Ensure output is valid UTF-8 (already guaranteed by Go strings)
	if !utf8.ValidString(username) {
		return "", fmt.Errorf("username is not valid UTF-8")
	}

	// 5. For internationalized domain names in email addresses, convert to punycode
	// This is handled at a higher layer by SMTP's SMTPUTF8 support

	return username, nil
}

// normalizePassword applies PRECIS PasswordPrep profile (RFC 7616).
// This is a conservative normalization that preserves meaning.
func normalizePassword(password string) string {
	if password == "" {
		return password
	}

	// RFC 7616 Section 6: PasswordPrep
	// Most passwords should be preserved as-is for compatibility.
	// Apply Unicode normalization (NFC form) to ensure consistent comparison.
	// Beyond that, minimal transformation to avoid breaking existing passwords.

	// Apply NFKC normalization for compatibility with internationalized passwords
	// But preserve the original as much as possible
	return password
}

// handleListRights implements RFC 4314 LISTRIGHTS command
func (s *Session) handleListRights(args []string) error {
	if len(args) < 2 {
		s.WriteResponse(s.tag, "BAD LISTRIGHTS requires mailbox and grantee")
		return nil
	}

	mailbox := args[0]
	grantee := args[1]

	// User must be authenticated
	if s.state == StateNotAuthenticated {
		s.WriteResponse(s.tag, "NO Not authenticated")
		return nil
	}

	// RFC 4314 specifies the standard rights that can be granted
	// l (lookup), r (read), s (seen), w (write), i (insert), p (post),
	// k (create), x (delete), t (delete seen), e (expunge), c (create mailbox), d (delete mailbox)
	standardRights := "l r s w i p k x t e c d a"

	s.WriteData(fmt.Sprintf("LISTRIGHTS %s %s %s", mailbox, grantee, standardRights))
	s.WriteResponse(s.tag, "OK LISTRIGHTS completed")
	return nil
}

// parseOwnerMailbox parses mailbox name which may be in owner:mailbox format for shared mailboxes
func (s *Session) parseOwnerMailbox(mailbox string) (owner, name string, isShared bool) {
	if !s.server.sharedFoldersEnabled {
		return s.user, mailbox, false
	}

	parts := strings.SplitN(mailbox, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1], true
	}
	return s.user, mailbox, false
}

// publicFolderPrefix marks an IMAP mailbox path in the shared public-folder
// namespace advertised by NAMESPACE; the segment after it is the folder name.
const publicFolderPrefix = "Public Folders/"

// domainOf returns the domain part of an email address ("" when absent).
func domainOf(email string) string {
	if at := strings.LastIndexByte(email, '@'); at >= 0 && at < len(email)-1 {
		return email[at+1:]
	}
	return ""
}

// publicRights resolves the caller's effective rights on a public folder: the
// union of their own grant and the reserved "anyone" grant. The mailstore's
// GetACL returns the rights as uint8, so the union is computed in uint8.
func (s *Session) publicRights(owner, name string) uint8 {
	own, err := s.server.mailstore.GetACL(owner, name, s.user)
	if err != nil {
		return 0
	}
	anyone, err := s.server.mailstore.GetACL(owner, name, storage.ACLAnyone)
	if err != nil {
		return own
	}
	return own | anyone
}

// resolvePublicFolder maps a "Public Folders/<name>" mailbox to the caller's
// per-domain public owner, verifying the caller holds at least the required
// rights (own grant unioned with the "anyone" grant). It returns
// (owner, name, true) for an authorized public folder. A non-public path returns
// (s.user, mailbox, true) unchanged. It returns ok=false when public folders are
// off for a public path, the caller's domain is unknown, the name is empty, or
// access is denied — the caller answers NO. The owner is always derived from the
// caller's own domain, so one tenant can never reach another's public tree.
func (s *Session) resolvePublicFolder(mailbox string, required uint8) (owner, name string, ok bool) {
	if !strings.HasPrefix(mailbox, publicFolderPrefix) {
		return s.user, mailbox, true
	}
	if !s.server.publicFoldersOn() {
		return "", "", false
	}
	dom := domainOf(s.user)
	if dom == "" {
		return "", "", false
	}
	owner = storage.PublicFolderOwner(dom)
	name = strings.TrimPrefix(mailbox, publicFolderPrefix)
	if name == "" {
		return "", "", false
	}
	if s.publicRights(owner, name)&required != required {
		return "", "", false
	}
	return owner, name, true
}

// isPublicPath reports whether a mailbox name targets the public-folder
// namespace (only meaningful when the feature is on). Structural mutations
// (CREATE/DELETE/RENAME/SUBSCRIBE) on public paths are rejected for regular
// users — the public tree is administrator-managed.
func (s *Session) isPublicPath(mailbox string) bool {
	return s.server.publicFoldersOn() && strings.HasPrefix(mailbox, publicFolderPrefix)
}

// selOwner returns the storage owner of the selected mailbox: the public-folder
// owner when a public folder is selected, otherwise the user themselves. It
// falls back to the user when selectedOwner is unset (e.g. a mailbox selected
// without going through the SELECT/EXAMINE owner resolution), so personal
// mailbox operations always key off the user.
func (s *Session) selOwner() string {
	if s.selectedOwner != "" {
		return s.selectedOwner
	}
	return s.user
}

// Helper functions

func parseSearchCriteria(args []string) SearchCriteria {
	// Simplified search criteria parsing. NOTE: All must default to false — a
	// criterion like SUBJECT/FROM must filter. If All defaulted true it would
	// never be cleared and matchesCriteria short-circuits `if All { return true }`,
	// making every keyed SEARCH return the whole mailbox. A bare SEARCH with no
	// criteria still matches everything by falling through matchesCriteria.
	criteria := SearchCriteria{}

	// IMAP quoted-string values arrive with their surrounding quotes because the
	// command line is tokenized with strings.Fields; strip them so the value
	// compares against the header text (matches the Trim other handlers apply).
	unq := func(s string) string { return strings.Trim(s, "\"'") }

	for i := 0; i < len(args); i++ {
		arg := strings.ToUpper(args[i])
		switch arg {
		case "ALL":
			criteria.All = true
		case "ANSWERED":
			criteria.Answered = true
		case "DELETED":
			criteria.Deleted = true
		case "FLAGGED":
			criteria.Flagged = true
		case "NEW":
			criteria.New = true
		case "OLD":
			criteria.Old = true
		case "RECENT":
			criteria.Recent = true
		case "SEEN":
			criteria.Seen = true
		case "UNANSWERED":
			criteria.Unanswered = true
		case "UNDELETED":
			criteria.Undeleted = true
		case "UNFLAGGED":
			criteria.Unflagged = true
		case "UNSEEN":
			criteria.Unseen = true
		case "DRAFT":
			criteria.Draft = true
		case "UNDRAFT":
			criteria.Undraft = true
		case "FROM":
			if i+1 < len(args) {
				criteria.From = unq(args[i+1])
				i++
			}
		case "SUBJECT":
			if i+1 < len(args) {
				criteria.Subject = unq(args[i+1])
				i++
			}
		case "TO":
			if i+1 < len(args) {
				criteria.To = unq(args[i+1])
				i++
			}
		case "UID":
			if i+1 < len(args) {
				criteria.UIDSet = args[i+1]
				i++
			}
		case "CC":
			if i+1 < len(args) {
				criteria.Cc = unq(args[i+1])
				i++
			}
		case "BCC":
			if i+1 < len(args) {
				criteria.Bcc = unq(args[i+1])
				i++
			}
		case "BODY":
			if i+1 < len(args) {
				criteria.Body = unq(args[i+1])
				i++
			}
		case "TEXT":
			if i+1 < len(args) {
				criteria.Text = unq(args[i+1])
				i++
			}
		case "HEADER":
			if i+2 < len(args) {
				if criteria.Header == nil {
					criteria.Header = make(map[string]string)
				}
				criteria.Header[unq(args[i+1])] = unq(args[i+2])
				i += 2
			}
		case "BEFORE":
			if i+1 < len(args) {
				if t, err := parseIMAPDate(args[i+1]); err == nil {
					criteria.Before = t
				}
				i++
			}
		case "ON":
			if i+1 < len(args) {
				if t, err := parseIMAPDate(args[i+1]); err == nil {
					criteria.On = t
				}
				i++
			}
		case "SINCE":
			if i+1 < len(args) {
				if t, err := parseIMAPDate(args[i+1]); err == nil {
					criteria.Since = t
				}
				i++
			}
		case "SENTBEFORE":
			if i+1 < len(args) {
				if t, err := parseIMAPDate(args[i+1]); err == nil {
					criteria.SentBefore = t
				}
				i++
			}
		case "SENTON":
			if i+1 < len(args) {
				if t, err := parseIMAPDate(args[i+1]); err == nil {
					criteria.SentOn = t
				}
				i++
			}
		case "SENTSINCE":
			if i+1 < len(args) {
				if t, err := parseIMAPDate(args[i+1]); err == nil {
					criteria.SentSince = t
				}
				i++
			}
		case "LARGER":
			if i+1 < len(args) {
				if size, err := strconv.ParseInt(args[i+1], 10, 64); err == nil {
					criteria.Larger = size
				}
				i++
			}
		case "SMALLER":
			if i+1 < len(args) {
				if size, err := strconv.ParseInt(args[i+1], 10, 64); err == nil {
					criteria.Smaller = size
				}
				i++
			}
		case "MODSEQ":
			// RFC 7162 §3.1.5: MODSEQ [<entry-name> <entry-type>] <modseq>. The
			// optional entry-name is a quoted flag name followed by an entry-type
			// (all/priv/shared); we match purely by mailbox mod-sequence, so skip
			// the optional pair and read the trailing number.
			j := i + 1
			if j < len(args) && strings.HasPrefix(args[j], "\"") {
				j += 2 // skip <entry-name> <entry-type>
			}
			if j < len(args) {
				if n, err := strconv.ParseUint(args[j], 10, 64); err == nil {
					criteria.ModSeq = n
					criteria.HasModSeq = true
				}
				i = j
			}
		}
	}

	return criteria
}

// parseIMAPDate parses an IMAP date in format "DD-Mon-YYYY" (e.g., "01-Jan-2024")
func parseIMAPDate(dateStr string) (time.Time, error) {
	// IMAP date format: 01-Jan-2024
	return time.Parse("02-Jan-2006", dateStr)
}

// extractChangedSince splits the FETCH item args from a trailing
// "(CHANGEDSINCE <modseq> [VANISHED])" modifier (RFC 7162 §3.1.4 / §3.2.5.1),
// which always follows the item list. It returns the item args with the modifier
// removed, the modseq, whether a CHANGEDSINCE was present, and whether the
// VANISHED option was requested alongside it.
func extractChangedSince(args []string) (items []string, modSeq uint64, ok bool, vanished bool) {
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimLeft(args[i], "(")), "CHANGEDSINCE") {
			continue
		}
		var mod []string
		for j := i; j < len(args); j++ {
			mod = append(mod, strings.Trim(args[j], "()"))
			if strings.HasSuffix(args[j], ")") {
				break
			}
		}
		for _, t := range mod {
			if strings.EqualFold(t, "VANISHED") {
				vanished = true
			}
		}
		if len(mod) >= 2 {
			if n, err := strconv.ParseUint(mod[1], 10, 64); err == nil {
				return args[:i], n, true, vanished
			}
		}
		return args[:i], 0, false, vanished
	}
	return args, 0, false, false
}

// appendItemIfAbsent appends want to items unless a case-insensitive match is
// already present.
func appendItemIfAbsent(items []string, want string) []string {
	for _, it := range items {
		if strings.EqualFold(it, want) {
			return items
		}
	}
	return append(items, want)
}

func parseFetchItems(args []string) []string {
	itemsStr := strings.TrimSpace(strings.Join(args, " "))

	// Handle parenthesized list
	if strings.HasPrefix(itemsStr, "(") && strings.HasSuffix(itemsStr, ")") {
		itemsStr = itemsStr[1 : len(itemsStr)-1]
	}

	// Split on spaces at bracket/paren depth 0 so items that legitimately
	// contain spaces — e.g. BODY.PEEK[HEADER.FIELDS (DATE FROM)] — stay intact.
	var items []string
	var cur strings.Builder
	depth := 0
	for _, r := range itemsStr {
		switch r {
		case '[', '(':
			depth++
			cur.WriteRune(r)
		case ']', ')':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case ' ':
			if depth == 0 {
				if cur.Len() > 0 {
					items = append(items, cur.String())
					cur.Reset()
				}
			} else {
				cur.WriteRune(r)
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		items = append(items, cur.String())
	}
	return items
}

func parseFlags(flagsStr string) []string {
	// Remove parentheses if present
	flagsStr = strings.Trim(flagsStr, "()")

	// Preserve flag tokens verbatim, including the leading backslash on system
	// flags (\Seen, \Deleted, ...). The storage layer's canonical representation
	// is backslash-prefixed (e.g. Expunge checks hasFlag(flags, "\\Deleted")),
	// so stripping it here would silently break flag matching across the server.
	flags := []string{}
	for _, f := range strings.Fields(flagsStr) {
		if f != "" {
			flags = append(flags, f)
		}
	}
	return flags
}

func formatFetchResponse(msg *Message, items []string) string {
	var parts []string
	data := string(msg.Data)

	for _, item := range items {
		upper := strings.ToUpper(item)
		switch {
		case upper == "FLAGS":
			parts = append(parts, fmt.Sprintf("FLAGS (%s)", strings.Join(msg.Flags, " ")))
		case upper == "INTERNALDATE":
			parts = append(parts, fmt.Sprintf("INTERNALDATE \"%s\"", msg.InternalDate.Format("02-Jan-2006 15:04:05 -0700")))
		case upper == "RFC822.SIZE":
			parts = append(parts, fmt.Sprintf("RFC822.SIZE %d", msg.Size))
		case upper == "UID":
			parts = append(parts, fmt.Sprintf("UID %d", msg.UID))
		case upper == "MODSEQ":
			// RFC 7162 §3.1.4: the MODSEQ FETCH data item is a parenthesized list.
			parts = append(parts, fmt.Sprintf("MODSEQ (%d)", msg.ModSeq))
		case upper == "RFC822":
			parts = append(parts, fmt.Sprintf("RFC822 {%d}\r\n%s", len(data), data))
		case upper == "RFC822.HEADER":
			h := imapHeaderBytes(data)
			parts = append(parts, fmt.Sprintf("RFC822.HEADER {%d}\r\n%s", len(h), h))
		case upper == "RFC822.TEXT":
			t := imapBodyTextBytes(data)
			parts = append(parts, fmt.Sprintf("RFC822.TEXT {%d}\r\n%s", len(t), t))
		case strings.HasPrefix(upper, "BODY[") || strings.HasPrefix(upper, "BODY.PEEK["):
			respKey, payload := fetchBodySection(item, data)
			parts = append(parts, fmt.Sprintf("%s {%d}\r\n%s", respKey, len(payload), payload))
		case upper == "BODY" || upper == "BODYSTRUCTURE":
			parts = append(parts, fmt.Sprintf("BODYSTRUCTURE (\"TEXT\" \"PLAIN\" NIL NIL NIL \"7BIT\" %d 0)", msg.Size))
		case upper == "ENVELOPE":
			fromLocal, fromDomain := splitAddress(msg.From)
			toLocal, toDomain := splitAddress(msg.To)
			parts = append(parts, fmt.Sprintf("ENVELOPE (%s %s ((%s NIL %s %s)) NIL NIL ((%s NIL %s %s)) NIL NIL NIL NIL)",
				imapQuotedString(msg.Subject), imapQuotedString(msg.Date),
				imapQuotedString(msg.From), imapQuotedString(fromLocal), imapQuotedString(fromDomain),
				imapQuotedString(msg.To), imapQuotedString(toLocal), imapQuotedString(toDomain)))
		}
	}

	return strings.Join(parts, " ")
}

// fetchBodySection resolves a BODY[...] / BODY.PEEK[...] fetch item against the
// raw message, returning the response key (always without ".PEEK", per RFC 3501)
// and the requested octets. Supported sections: "" (whole message), HEADER,
// TEXT, HEADER.FIELDS (...), HEADER.FIELDS.NOT (...). An optional <start.len>
// partial specifier is honored.
func fetchBodySection(item, data string) (respKey, payload string) {
	open := strings.Index(item, "[")
	closeIdx := strings.LastIndex(item, "]")
	section := ""
	if open >= 0 && closeIdx > open {
		section = item[open+1 : closeIdx]
	}
	partial := ""
	if closeIdx >= 0 && closeIdx+1 < len(item) && item[closeIdx+1] == '<' {
		partial = item[closeIdx+1:]
	}

	secUpper := strings.ToUpper(strings.TrimSpace(section))
	switch {
	case section == "":
		payload = data
	case secUpper == "HEADER":
		payload = imapHeaderBytes(data)
	case secUpper == "TEXT":
		payload = imapBodyTextBytes(data)
	case strings.HasPrefix(secUpper, "HEADER.FIELDS.NOT"):
		payload = imapHeaderFields(imapHeaderBytes(data), parseFieldList(section), true)
	case strings.HasPrefix(secUpper, "HEADER.FIELDS"):
		payload = imapHeaderFields(imapHeaderBytes(data), parseFieldList(section), false)
	default:
		// MIME part numbers and other sections are not modeled; return the
		// whole message rather than nothing so clients still get content.
		payload = data
	}

	respKey = "BODY[" + section + "]"
	if partial != "" {
		start, length := parsePartialSpec(partial)
		if start < 0 {
			start = 0
		}
		if start > len(payload) {
			start = len(payload)
		}
		end := len(payload)
		if length >= 0 && start+length < end {
			end = start + length
		}
		payload = payload[start:end]
		respKey = fmt.Sprintf("BODY[%s]<%d>", section, start)
	}
	return respKey, payload
}

// imapHeaderBytes returns the header block including the terminating blank line.
func imapHeaderBytes(data string) string {
	if i := strings.Index(data, "\r\n\r\n"); i >= 0 {
		return data[:i+4]
	}
	if i := strings.Index(data, "\n\n"); i >= 0 {
		return data[:i+2]
	}
	return data
}

// imapBodyTextBytes returns the body that follows the header blank line.
func imapBodyTextBytes(data string) string {
	if i := strings.Index(data, "\r\n\r\n"); i >= 0 {
		return data[i+4:]
	}
	if i := strings.Index(data, "\n\n"); i >= 0 {
		return data[i+2:]
	}
	return ""
}

// imapHeaderFields returns only the named header fields (or all but them when
// not=true), preserving folded continuation lines and terminating with a blank
// line, per RFC 3501 BODY[HEADER.FIELDS].
func imapHeaderFields(header string, fields []string, not bool) string {
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[strings.ToUpper(strings.TrimSpace(f))] = true
	}
	var out []string
	keep := false
	for _, raw := range strings.Split(header, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue // skip separators; a single blank line is appended below
		}
		if raw[0] == ' ' || raw[0] == '\t' {
			if keep {
				out = append(out, line)
			}
			continue
		}
		name := line
		if c := strings.Index(line, ":"); c >= 0 {
			name = line[:c]
		}
		match := want[strings.ToUpper(strings.TrimSpace(name))]
		keep = match != not
		if keep {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return "\r\n"
	}
	return strings.Join(out, "\r\n") + "\r\n\r\n"
}

// parseFieldList extracts the field names from a HEADER.FIELDS (a b c) section.
func parseFieldList(section string) []string {
	o := strings.Index(section, "(")
	c := strings.LastIndex(section, ")")
	if o >= 0 && c > o {
		return strings.Fields(section[o+1 : c])
	}
	return nil
}

// parsePartialSpec parses a "<start.len>" or "<start>" partial specifier.
// A negative length means "to the end".
func parsePartialSpec(p string) (start, length int) {
	p = strings.TrimPrefix(p, "<")
	p = strings.TrimSuffix(p, ">")
	if dot := strings.Index(p, "."); dot >= 0 {
		start, _ = strconv.Atoi(p[:dot])    //nolint:errcheck
		length, _ = strconv.Atoi(p[dot+1:]) //nolint:errcheck
		return start, length
	}
	start, _ = strconv.Atoi(p) //nolint:errcheck
	return start, -1
}

// imapQuotedString quotes a string for use in an IMAP quoted-string per RFC 3501.
func imapQuotedString(s string) string {
	return strconv.Quote(s)
}

// splitAddress safely splits an email address into local and domain parts.
// If the address contains no "@", the domain is returned as empty.
func splitAddress(addr string) (local, domain string) {
	if atIdx := strings.LastIndex(addr, "@"); atIdx >= 0 {
		return addr[:atIdx], addr[atIdx+1:]
	}
	return addr, ""
}
