package jmap

import (
	"bytes"
	"fmt"
	"net/mail"
	"strings"
	"sync/atomic"
	"time"

	"github.com/umailserver/umailserver/internal/storage"
)

// createEmail builds an RFC 5322 message from a JMAP Email creation object,
// stores it in the message store, and files it (in Drafts by default). The
// returned Email's id is the content-addressed blob key.
func (s *Server) createEmail(user string, create map[string]interface{}) (Email, error) {
	if s.msgStore == nil {
		return Email{}, fmt.Errorf("message store unavailable")
	}

	data := buildMIMEFromJMAP(user, create)

	msgKey, err := s.msgStore.StoreMessage(user, data)
	if err != nil {
		return Email{}, err
	}

	// Target mailbox: first true mailboxId, defaulting to Drafts.
	targetMbox := "Drafts"
	if mids := asMap(create["mailboxIds"]); mids != nil {
		for id, v := range mids {
			if asBool(v) {
				targetMbox = getMailboxNameFromID(id)
				break
			}
		}
	}

	meta := parseEmailMetadata(data, msgKey)
	applyKeywordsToFlags(meta, create["keywords"])
	if targetMbox == "Drafts" && !storage.HasFlag(meta.Flags, "\\Draft") {
		meta.Flags = append(meta.Flags, "\\Draft")
	}

	// Thread id consistent with the rest of the system (Phase 1 rooting).
	msgID, inReplyTo, refs := threadingFromData(data)
	if tid, terr := s.db.GetOrCreateThreadID(user, targetMbox, meta.Subject, msgID, inReplyTo, refs); terr == nil {
		meta.ThreadID = tid
	}

	uid, err := s.db.GetNextUID(user, targetMbox)
	if err != nil {
		return Email{}, err
	}
	meta.UID = uid
	if err := s.db.StoreMessageMetadata(user, targetMbox, uid, meta); err != nil {
		return Email{}, err
	}

	return s.storageToJMAPEmail(user, meta, nil, targetMbox), nil
}

// handleEmailSubmissionSet submits a previously created Email through the shared
// delivery path (Sieve/OOF/conversation-id/relay), mirroring EWS submission.
func (s *Server) handleEmailSubmissionSet(user string, call MethodCall, createdIDs map[string]string) Response {
	args := call.Args
	accountID := asString(args["accountId"])
	if valid, resp := validateAccountId(accountID, user, "EmailSubmission/set", call.ID); !valid {
		return resp
	}

	if s.submitMessage == nil {
		return Response{
			Name: "error",
			Args: map[string]interface{}{"type": "notSupported", "description": "JMAP sending is not enabled"},
			ID:   call.ID,
		}
	}

	create := asMap(args["create"])
	onSuccessUpdate := asMap(args["onSuccessUpdateEmail"])

	created := make(map[string]interface{})
	notCreated := make(map[string]interface{})
	// submission creationId -> emailId that was submitted, for onSuccessUpdateEmail.
	submitted := make(map[string]string)

	for key, val := range create {
		sub := asMap(val)
		if sub == nil {
			notCreated[key] = submissionError("invalidProperties", "submission must be an object")
			continue
		}
		emailID := resolveCreationRef(asString(sub["emailId"]), createdIDs)
		if emailID == "" {
			notCreated[key] = submissionError("invalidProperties", "emailId is required")
			continue
		}

		data, err := s.msgStore.ReadMessage(user, emailID)
		if err != nil {
			notCreated[key] = submissionError("invalidEmail", "email blob not found")
			continue
		}

		from, rcpt := resolveEnvelope(user, sub["envelope"], data)
		if from == "" {
			notCreated[key] = submissionError("invalidProperties", "could not determine sender")
			continue
		}
		if len(rcpt) == 0 {
			notCreated[key] = submissionError("noRecipients", "no recipients in envelope or message")
			continue
		}

		// Recipients must never see Bcc; relay a copy with the header removed.
		if err := s.submitMessage(from, rcpt, stripBccHeader(data)); err != nil {
			notCreated[key] = submissionError("forbiddenToSend", s.safeError("submitMessage", err))
			continue
		}

		subID := generateSubmissionID()
		created[key] = map[string]interface{}{
			"id":         subID,
			"emailId":    emailID,
			"identityId": "default",
			"sendAt":     time.Now().UTC().Format(time.RFC3339),
			"undoStatus": "final",
		}
		if createdIDs != nil {
			createdIDs[key] = subID
		}
		submitted[key] = emailID
	}

	respArgs := map[string]interface{}{
		"accountId":  accountID,
		"oldState":   nil,
		"newState":   fmt.Sprintf("state-%d", time.Now().Unix()),
		"created":    created,
		"notCreated": notCreated,
	}

	// onSuccessUpdateEmail moves the submitted draft (clears $draft, files it in
	// Sent). RFC 8621 §7.5 models this as an implicit Email/set; we apply it
	// inline and report the affected ids under updatedEmails.
	if len(onSuccessUpdate) > 0 {
		updatedEmails := make(map[string]interface{})
		for refKey, patchVal := range onSuccessUpdate {
			emailID := resolveSuccessTarget(refKey, submitted)
			patch := asMap(patchVal)
			if emailID == "" || patch == nil {
				continue
			}
			if err := s.applyEmailPatch(user, emailID, patch); err == nil {
				updatedEmails[emailID] = nil
			}
		}
		if len(updatedEmails) > 0 {
			respArgs["updatedEmails"] = updatedEmails
		}
	}

	return Response{Name: "EmailSubmission/set", Args: respArgs, ID: call.ID}
}

// handleEmailSubmissionGet returns an empty result; submissions are not
// persisted as queryable objects in this implementation.
func (s *Server) handleEmailSubmissionGet(user string, call MethodCall) Response {
	accountID := asString(call.Args["accountId"])
	if valid, resp := validateAccountId(accountID, user, "EmailSubmission/get", call.ID); !valid {
		return resp
	}
	return Response{
		Name: "EmailSubmission/get",
		Args: map[string]interface{}{
			"accountId": accountID,
			"state":     fmt.Sprintf("state-%d", time.Now().Unix()),
			"list":      []interface{}{},
			"notFound":  call.Args["ids"],
		},
		ID: call.ID,
	}
}

// handleEmailSubmissionQuery returns an empty id set.
func (s *Server) handleEmailSubmissionQuery(user string, call MethodCall) Response {
	accountID := asString(call.Args["accountId"])
	if valid, resp := validateAccountId(accountID, user, "EmailSubmission/query", call.ID); !valid {
		return resp
	}
	return Response{
		Name: "EmailSubmission/query",
		Args: map[string]interface{}{
			"accountId":           accountID,
			"queryState":          fmt.Sprintf("state-%d", time.Now().Unix()),
			"canCalculateChanges": false,
			"position":            0,
			"total":               0,
			"ids":                 []string{},
		},
		ID: call.ID,
	}
}

// handleEmailSubmissionChanges reports no changes.
func (s *Server) handleEmailSubmissionChanges(user string, call MethodCall) Response {
	accountID := asString(call.Args["accountId"])
	if valid, resp := validateAccountId(accountID, user, "EmailSubmission/changes", call.ID); !valid {
		return resp
	}
	state := fmt.Sprintf("state-%d", time.Now().Unix())
	return Response{
		Name: "EmailSubmission/changes",
		Args: map[string]interface{}{
			"accountId":      accountID,
			"oldState":       state,
			"newState":       state,
			"hasMoreChanges": false,
			"created":        []string{},
			"updated":        []string{},
			"destroyed":      []string{},
		},
		ID: call.ID,
	}
}

// applyEmailPatch applies a JMAP Email PatchObject (keyword and mailbox changes)
// to a stored message, supporting both full-object and patch-path forms.
func (s *Server) applyEmailPatch(user, emailID string, patch map[string]interface{}) error {
	mbox, uid, meta, ok := s.findMessageByID(user, emailID)
	if !ok {
		return fmt.Errorf("email %s not found", emailID)
	}

	flags := flagSet(meta.Flags)
	targetMbox := mbox

	for path, val := range patch {
		switch {
		case path == "keywords":
			if kw := asMap(val); kw != nil {
				flags = map[string]bool{}
				for k, v := range kw {
					if asBool(v) {
						if f := keywordToFlag(k); f != "" {
							flags[f] = true
						}
					}
				}
			}
		case strings.HasPrefix(path, "keywords/"):
			f := keywordToFlag(strings.TrimPrefix(path, "keywords/"))
			if f == "" {
				continue
			}
			if asBool(val) {
				flags[f] = true
			} else {
				delete(flags, f)
			}
		case path == "mailboxIds":
			if mids := asMap(val); mids != nil {
				for id, v := range mids {
					if asBool(v) {
						targetMbox = getMailboxNameFromID(id)
						break
					}
				}
			}
		case strings.HasPrefix(path, "mailboxIds/"):
			if asBool(val) {
				targetMbox = getMailboxNameFromID(strings.TrimPrefix(path, "mailboxIds/"))
			}
		}
	}

	meta.Flags = flagSlice(flags)

	if targetMbox != "" && targetMbox != mbox {
		newUID, err := s.db.GetNextUID(user, targetMbox)
		if err != nil {
			return err
		}
		meta.UID = newUID
		if err := s.db.StoreMessageMetadata(user, targetMbox, newUID, meta); err != nil {
			return err
		}
		_ = s.db.DeleteMessage(user, mbox, uid) //nolint:errcheck
		return nil
	}

	return s.db.StoreMessageMetadata(user, mbox, uid, meta)
}

// findMessageByID locates a stored message by its blob/message id across the
// user's mailboxes.
func (s *Server) findMessageByID(user, emailID string) (mbox string, uid uint32, meta *storage.MessageMetadata, ok bool) {
	mailboxes, _ := s.db.ListMailboxes(user) //nolint:errcheck
	for _, mb := range mailboxes {
		uids, _ := s.db.GetMessageUIDs(user, mb) //nolint:errcheck
		for _, u := range uids {
			m, err := s.db.GetMessageMetadata(user, mb, u)
			if err != nil || m == nil {
				continue
			}
			if m.MessageID == emailID {
				return mb, u, m, true
			}
		}
	}
	return "", 0, nil, false
}

// resolveEnvelope determines the SMTP envelope sender and recipients. An
// explicit JMAP envelope wins; otherwise the sender is the account user and the
// recipients are taken from the message's To/Cc/Bcc headers.
func resolveEnvelope(user string, envelope interface{}, data []byte) (from string, rcpt []string) {
	if env := asMap(envelope); env != nil {
		if mf := asMap(env["mailFrom"]); mf != nil {
			from = asString(mf["email"])
		}
		if rs := asSlice(env["rcptTo"]); rs != nil {
			for _, r := range rs {
				if rm := asMap(r); rm != nil {
					if e := asString(rm["email"]); e != "" {
						rcpt = append(rcpt, e)
					}
				}
			}
		}
	}
	if from == "" {
		from = user
	}
	if len(rcpt) == 0 {
		rcpt = recipientsFromMessage(data)
	}
	return from, rcpt
}

// recipientsFromMessage extracts To/Cc/Bcc addresses from a raw message.
func recipientsFromMessage(data []byte) []string {
	m, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, hdr := range []string{"To", "Cc", "Bcc"} {
		addrs, err := m.Header.AddressList(hdr)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if a.Address != "" && !seen[a.Address] {
				seen[a.Address] = true
				out = append(out, a.Address)
			}
		}
	}
	return out
}

// stripBccHeader removes the Bcc header (and its continuation lines) from a
// message so relayed recipients never see blind copies.
func stripBccHeader(data []byte) []byte {
	idx := bytes.Index(data, []byte("\r\n\r\n"))
	if idx < 0 {
		return data
	}
	header := string(data[:idx])
	body := data[idx:]

	var kept []string
	skipping := false
	for _, line := range strings.Split(header, "\r\n") {
		if skipping {
			// Continuation lines start with whitespace.
			if line != "" && (line[0] == ' ' || line[0] == '\t') {
				continue
			}
			skipping = false
		}
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			skipping = true
			continue
		}
		kept = append(kept, line)
	}
	return append([]byte(strings.Join(kept, "\r\n")), body...)
}

// buildMIMEFromJMAP renders a minimal RFC 5322 text/plain message from a JMAP
// Email creation object.
func buildMIMEFromJMAP(user string, create map[string]interface{}) []byte {
	var b strings.Builder

	from := jmapAddressHeader(create["from"])
	if from == "" {
		from = user
	}
	to := jmapAddressHeader(create["to"])
	cc := jmapAddressHeader(create["cc"])
	bcc := jmapAddressHeader(create["bcc"])
	subject := asString(create["subject"])

	fmt.Fprintf(&b, "From: %s\r\n", from)
	if to != "" {
		fmt.Fprintf(&b, "To: %s\r\n", to)
	}
	if cc != "" {
		fmt.Fprintf(&b, "Cc: %s\r\n", cc)
	}
	if bcc != "" {
		fmt.Fprintf(&b, "Bcc: %s\r\n", bcc)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", generateRFCMessageID(user))
	if irt := jmapFirstString(create["inReplyTo"]); irt != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\n", strings.Trim(irt, "<> "))
	}
	if refs := jmapStringList(create["references"]); len(refs) > 0 {
		for i := range refs {
			refs[i] = "<" + strings.Trim(refs[i], "<> ") + ">"
		}
		fmt.Fprintf(&b, "References: %s\r\n", strings.Join(refs, " "))
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(extractJMAPBody(create))
	return []byte(b.String())
}

// extractJMAPBody resolves the text body from textBody/bodyValues, falling back
// to htmlBody.
func extractJMAPBody(create map[string]interface{}) string {
	bodyValues := asMap(create["bodyValues"])
	for _, listKey := range []string{"textBody", "htmlBody"} {
		for _, p := range asSlice(create[listKey]) {
			pm := asMap(p)
			if pm == nil {
				continue
			}
			partID := asString(pm["partId"])
			if bv := asMap(bodyValues[partID]); bv != nil {
				if v := asString(bv["value"]); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// jmapAddressHeader renders a JMAP address array ([]{name,email}) as an RFC 5322
// address header value.
func jmapAddressHeader(v interface{}) string {
	list := asSlice(v)
	if list == nil {
		return ""
	}
	var parts []string
	for _, item := range list {
		m := asMap(item)
		if m == nil {
			continue
		}
		email := asString(m["email"])
		if email == "" {
			continue
		}
		if name := asString(m["name"]); name != "" {
			parts = append(parts, fmt.Sprintf("%s <%s>", name, email))
		} else {
			parts = append(parts, email)
		}
	}
	return strings.Join(parts, ", ")
}

func jmapStringList(v interface{}) []string {
	list := asSlice(v)
	if list == nil {
		return nil
	}
	var out []string
	for _, item := range list {
		if s := asString(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func jmapFirstString(v interface{}) string {
	if l := jmapStringList(v); len(l) > 0 {
		return l[0]
	}
	return ""
}

// threadingFromData extracts the RFC 5322 Message-ID, In-Reply-To and
// References needed for thread rooting.
func threadingFromData(data []byte) (msgID, inReplyTo string, refs []string) {
	m, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return "", "", nil
	}
	return strings.Trim(m.Header.Get("Message-ID"), "<> "),
		m.Header.Get("In-Reply-To"),
		refsFromHeader(m.Header.Get("References"))
}

// applyKeywordsToFlags converts a JMAP keywords map to IMAP flags on meta.
func applyKeywordsToFlags(meta *storage.MessageMetadata, v interface{}) {
	for k, val := range asMap(v) {
		if asBool(val) {
			if f := keywordToFlag(k); f != "" {
				meta.Flags = append(meta.Flags, f)
			}
		}
	}
}

func keywordToFlag(kw string) string {
	switch kw {
	case "$seen":
		return "\\Seen"
	case "$answered":
		return "\\Answered"
	case "$flagged":
		return "\\Flagged"
	case "$draft":
		return "\\Draft"
	default:
		return ""
	}
}

func flagSet(flags []string) map[string]bool {
	out := make(map[string]bool, len(flags))
	for _, f := range flags {
		out[f] = true
	}
	return out
}

func flagSlice(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	return out
}

// resolveCreationRef resolves a "#creationId" back-reference against the
// request's created-id map; literal ids pass through unchanged.
func resolveCreationRef(id string, createdIDs map[string]string) string {
	if strings.HasPrefix(id, "#") {
		if createdIDs != nil {
			return createdIDs[strings.TrimPrefix(id, "#")]
		}
		return ""
	}
	return id
}

// resolveSuccessTarget maps an onSuccessUpdateEmail key (a submission id, or a
// "#submissionCreationId") to the emailId that was submitted under it.
func resolveSuccessTarget(refKey string, submitted map[string]string) string {
	if strings.HasPrefix(refKey, "#") {
		return submitted[strings.TrimPrefix(refKey, "#")]
	}
	// Literal submission ids are not tracked; nothing to update.
	return ""
}

func submissionError(typ, desc string) map[string]interface{} {
	return map[string]interface{}{"type": typ, "description": desc}
}

var submissionCounter uint64

func generateRFCMessageID(user string) string {
	domain := "umailserver.local"
	if at := strings.LastIndex(user, "@"); at >= 0 && at < len(user)-1 {
		domain = user[at+1:]
	}
	n := atomic.AddUint64(&idCounter, 1)
	return fmt.Sprintf("jmap.%d.%d@%s", time.Now().UnixNano(), n, domain)
}

func generateSubmissionID() string {
	n := atomic.AddUint64(&submissionCounter, 1)
	return fmt.Sprintf("sub-%d-%d", time.Now().UnixNano(), n)
}

// Type-assertion helpers that consume the comma-ok result (satisfying the
// errcheck check-type-assertions/check-blank lint) and return zero values on
// mismatch.
func asString(v interface{}) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func asBool(v interface{}) bool {
	b, ok := v.(bool)
	return ok && b
}

func asMap(v interface{}) map[string]interface{} {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	return m
}

func asSlice(v interface{}) []interface{} {
	s, ok := v.([]interface{})
	if !ok {
		return nil
	}
	return s
}
