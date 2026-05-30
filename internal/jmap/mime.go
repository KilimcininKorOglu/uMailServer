package jmap

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"time"
)

// parseJMAPAddressList parses an RFC 5322 address-list header into JMAP
// addresses, preserving display names. Returns nil on an empty or invalid value.
func parseJMAPAddressList(v string) []EmailAddress {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(v)
	if err != nil {
		return nil
	}
	out := make([]EmailAddress, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, EmailAddress{Name: a.Name, Email: a.Address})
	}
	return out
}

// refsFromHeader splits a References header into bare Message-ID values.
func refsFromHeader(v string) []string {
	var out []string
	for _, r := range strings.Fields(v) {
		if id := strings.Trim(r, "<>"); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// enrichEmailFromMIME parses raw RFC 5322 data and fills the JMAP Email fields
// that storage metadata does not carry: sender/from/to/cc/bcc/replyTo (with
// display names), messageId/inReplyTo/references, sentAt, and hasAttachment.
// It is resilient — on any parse error the email is left as-is.
func enrichEmailFromMIME(email *Email, data []byte) {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return
	}
	h := msg.Header

	if v := parseJMAPAddressList(h.Get("From")); len(v) > 0 {
		email.From = v
	}
	if v := parseJMAPAddressList(h.Get("To")); len(v) > 0 {
		email.To = v
	}
	if v := parseJMAPAddressList(h.Get("Cc")); len(v) > 0 {
		email.CC = v
	}
	if v := parseJMAPAddressList(h.Get("Bcc")); len(v) > 0 {
		email.BCC = v
	}
	if v := parseJMAPAddressList(h.Get("Sender")); len(v) > 0 {
		email.Sender = v
	}
	if v := parseJMAPAddressList(h.Get("Reply-To")); len(v) > 0 {
		email.ReplyTo = v
	}

	if id := strings.Trim(strings.TrimSpace(h.Get("Message-ID")), "<>"); id != "" {
		email.MessageID = []string{id}
	}
	if irt := strings.Trim(strings.TrimSpace(h.Get("In-Reply-To")), "<>"); irt != "" {
		email.InReplyTo = []string{irt}
	}
	if refs := refsFromHeader(h.Get("References")); len(refs) > 0 {
		email.References = refs
	}
	if d, derr := h.Date(); derr == nil {
		email.SentAt = d.UTC().Format(time.RFC3339)
	}

	email.HasAttachment = mimeHasAttachment(h.Get("Content-Type"), msg.Body)
}

// mimeHasAttachment reports whether a multipart message carries an attachment
// (a part with an "attachment" Content-Disposition or a filename). Non-multipart
// messages have no attachments.
func mimeHasAttachment(contentType string, body io.Reader) bool {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return false
	}
	boundary := params["boundary"]
	if boundary == "" {
		return false
	}
	mr := multipart.NewReader(body, boundary)
	for {
		p, perr := mr.NextPart()
		if perr != nil {
			return false
		}
		disp := strings.ToLower(p.Header.Get("Content-Disposition"))
		if strings.Contains(disp, "attachment") || p.FileName() != "" {
			_ = p.Close() //nolint:errcheck
			return true
		}
		_ = p.Close() //nolint:errcheck
	}
}
