package msg

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"time"
)

// MIME reconstructs an RFC 5322 message from the decoded OXMSG fields. The body
// structure mirrors what the message carried: a text/plain and/or text/html
// alternative, wrapped in multipart/mixed when there are attachments.
func (m *Message) MIME() ([]byte, error) {
	var hdr bytes.Buffer
	if m.From != "" {
		fmt.Fprintf(&hdr, "From: %s\r\n", m.From)
	}
	if len(m.To) > 0 {
		fmt.Fprintf(&hdr, "To: %s\r\n", strings.Join(m.To, ", "))
	}
	if len(m.Cc) > 0 {
		fmt.Fprintf(&hdr, "Cc: %s\r\n", strings.Join(m.Cc, ", "))
	}
	if m.Subject != "" {
		fmt.Fprintf(&hdr, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	}
	if !m.Date.IsZero() {
		fmt.Fprintf(&hdr, "Date: %s\r\n", m.Date.Format(time.RFC1123Z))
	}
	if id := strings.TrimSpace(m.MessageID); id != "" {
		if !strings.HasPrefix(id, "<") {
			id = "<" + id + ">"
		}
		fmt.Fprintf(&hdr, "Message-ID: %s\r\n", id)
	}
	hdr.WriteString("MIME-Version: 1.0\r\n")

	topHeaders, body, err := m.buildBody()
	if err != nil {
		return nil, err
	}
	for _, h := range topHeaders {
		hdr.WriteString(h)
		hdr.WriteString("\r\n")
	}
	hdr.WriteString("\r\n")
	return append(hdr.Bytes(), body...), nil
}

// buildBody returns the content headers (Content-Type and, for single-part
// messages, Content-Transfer-Encoding) plus the encoded body bytes.
func (m *Message) buildBody() (headers []string, body []byte, err error) {
	hasText := strings.TrimSpace(m.BodyText) != ""
	hasHTML := len(bytes.TrimSpace(m.BodyHTML)) > 0

	switch {
	case len(m.Attachments) == 0 && hasText && hasHTML:
		// multipart/alternative of the two body forms.
		alt, boundary, aerr := m.alternativeBody()
		if aerr != nil {
			return nil, nil, aerr
		}
		return []string{"Content-Type: multipart/alternative; boundary=" + boundary}, alt, nil

	case len(m.Attachments) == 0:
		// Single part (text, html, or an empty text/plain).
		ct, content := m.singleBody(hasText, hasHTML)
		return []string{"Content-Type: " + ct, "Content-Transfer-Encoding: quoted-printable"}, qp(content), nil

	default:
		// multipart/mixed: the body (single or alternative) then the attachments.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if berr := mw.SetBoundary(m.boundary("mixed")); berr != nil {
			return nil, nil, berr
		}
		if hasText && hasHTML {
			alt, altBoundary, aerr := m.alternativeBody()
			if aerr != nil {
				return nil, nil, aerr
			}
			part, perr := mw.CreatePart(textproto.MIMEHeader{
				"Content-Type": {"multipart/alternative; boundary=" + altBoundary},
			})
			if perr != nil {
				return nil, nil, perr
			}
			if _, werr := part.Write(alt); werr != nil {
				return nil, nil, werr
			}
		} else {
			ct, content := m.singleBody(hasText, hasHTML)
			if werr := writeQPPart(mw, ct, content); werr != nil {
				return nil, nil, werr
			}
		}
		for _, att := range m.Attachments {
			if werr := writeAttachmentPart(mw, att); werr != nil {
				return nil, nil, werr
			}
		}
		if cerr := mw.Close(); cerr != nil {
			return nil, nil, cerr
		}
		return []string{"Content-Type: multipart/mixed; boundary=" + mw.Boundary()}, buf.Bytes(), nil
	}
}

// singleBody picks the one body form to emit and its content type. An HTML body
// is preferred when present; with neither, an empty text/plain is emitted.
func (m *Message) singleBody(hasText, hasHTML bool) (contentType string, content []byte) {
	switch {
	case hasHTML:
		return "text/html; charset=utf-8", m.BodyHTML
	case hasText:
		return "text/plain; charset=utf-8", []byte(m.BodyText)
	default:
		return "text/plain; charset=utf-8", nil
	}
}

// alternativeBody builds a multipart/alternative body (text/plain then
// text/html) and returns it with its boundary.
func (m *Message) alternativeBody() (body []byte, boundary string, err error) {
	var buf bytes.Buffer
	aw := multipart.NewWriter(&buf)
	if berr := aw.SetBoundary(m.boundary("alt")); berr != nil {
		return nil, "", berr
	}
	if werr := writeQPPart(aw, "text/plain; charset=utf-8", []byte(m.BodyText)); werr != nil {
		return nil, "", werr
	}
	if werr := writeQPPart(aw, "text/html; charset=utf-8", m.BodyHTML); werr != nil {
		return nil, "", werr
	}
	if cerr := aw.Close(); cerr != nil {
		return nil, "", cerr
	}
	return buf.Bytes(), aw.Boundary(), nil
}

// writeQPPart writes a quoted-printable text part with the given content type.
func writeQPPart(w *multipart.Writer, contentType string, content []byte) error {
	part, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	_, err = part.Write(qp(content))
	return err
}

// writeAttachmentPart writes one base64 attachment part.
func writeAttachmentPart(w *multipart.Writer, att Attachment) error {
	h := textproto.MIMEHeader{
		"Content-Type":              {att.ContentType + "; name=" + quoteParam(att.Filename)},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {"attachment; filename=" + quoteParam(att.Filename)},
	}
	if att.ContentID != "" {
		id := att.ContentID
		if !strings.HasPrefix(id, "<") {
			id = "<" + id + ">"
		}
		h.Set("Content-ID", id)
	}
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(base64Wrap(att.Data))
	return err
}

// boundary derives a stable MIME multipart boundary from the message body and
// attachments, so reconstructing the same message twice yields byte-identical
// output. That keeps re-importing the same .msg idempotent under the importer's
// content-hash deduplication (a random boundary would defeat it). The kind
// prefix keeps the nested mixed/alternative boundaries distinct.
func (m *Message) boundary(kind string) string {
	h := sha256.New()
	h.Write([]byte(m.BodyText))
	h.Write(m.BodyHTML)
	for _, att := range m.Attachments {
		h.Write([]byte(att.Filename))
		h.Write(att.Data)
	}
	return "umail_" + kind + "_" + hex.EncodeToString(h.Sum(nil))[:32]
}

// qp quoted-printable-encodes content for a text part. Writes target a
// bytes.Buffer, which never fails; the error checks satisfy the linter.
func qp(content []byte) []byte {
	var buf bytes.Buffer
	qw := quotedprintable.NewWriter(&buf)
	if _, err := qw.Write(content); err != nil {
		return content
	}
	if err := qw.Close(); err != nil {
		return content
	}
	return buf.Bytes()
}

// base64Wrap base64-encodes data wrapped at 76 columns per RFC 2045.
func base64Wrap(data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var buf bytes.Buffer
	for len(enc) > 76 {
		buf.WriteString(enc[:76])
		buf.WriteString("\r\n")
		enc = enc[76:]
	}
	buf.WriteString(enc)
	return buf.Bytes()
}

// quoteParam quotes a MIME parameter value (a filename) when it is not a token.
func quoteParam(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"\\;()<>@,:/[]?=") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
