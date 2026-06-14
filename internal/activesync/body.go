package activesync

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// MessageFromRaw projects a raw RFC 822 message into a SyncMessage, the shape the
// Sync encoder renders into an EAS Add/Change. It decodes the headers a client
// shows in a message list (Subject, From, To, received date, importance) and the
// body, preferring text/plain (EAS body type "1") and falling back to text/html
// (type "2"). serverID is the caller's stable EAS item id. Read defaults false
// (correct for newly-arrived mail); callers that know the seen state — the folder
// snapshot, which carries IMAP flags — set it afterwards.
//
// The EAS surface owns this projection rather than sharing one with EWS/MAPI:
// each protocol surface maps the canonical raw message into its own wire shape.
func MessageFromRaw(serverID string, raw []byte) SyncMessage {
	m := SyncMessage{ServerID: serverID, Importance: "1", BodyType: "1"}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return m
	}
	dec := &mime.WordDecoder{}
	if s, derr := dec.DecodeHeader(msg.Header.Get("Subject")); derr == nil {
		m.Subject = s
	} else {
		m.Subject = msg.Header.Get("Subject")
	}
	m.From = decodeHeader(dec, msg.Header.Get("From"))
	m.To = decodeHeader(dec, msg.Header.Get("To"))
	if t, derr := mail.ParseDate(msg.Header.Get("Date")); derr == nil {
		m.DateReceived = t.UTC().Format(easDateLayout)
	}
	m.Importance = importanceOf(msg.Header.Get("Importance"))
	m.BodyType, m.Body = BodyForSync(raw)
	return m
}

// easDateLayout is the EAS DateReceived format (MS-ASCMD): ISO 8601 UTC with
// millisecond precision and a trailing Z.
const easDateLayout = "2006-01-02T15:04:05.000Z"

// decodeHeader RFC 2047-decodes an encoded-word header value, returning the raw
// value when it is not encoded or cannot be decoded.
func decodeHeader(dec *mime.WordDecoder, v string) string {
	if v == "" {
		return ""
	}
	if d, err := dec.DecodeHeader(v); err == nil {
		return d
	}
	return v
}

// importanceOf maps the RFC 4021 Importance header to an EAS importance value
// ("0" low, "1" normal, "2" high); anything else is normal.
func importanceOf(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "high":
		return "2"
	case "low":
		return "0"
	default:
		return "1"
	}
}

// BodyForSync returns the body to project and its EAS type: text/plain as "1",
// else text/html as "2", else an empty plain body. The folder snapshot calls it
// directly (its headers and read state come from the canonical metadata index),
// while MessageFromRaw uses it for the journal change-feed path.
func BodyForSync(raw []byte) (bodyType, body string) {
	if text := extractPart(raw, "text/plain"); text != nil {
		return "1", strings.TrimSpace(string(text))
	}
	if html := extractPart(raw, "text/html"); html != nil {
		return "2", string(html)
	}
	return "1", ""
}

// extractPart returns the decoded content of the first MIME part whose media type
// has the given prefix, descending through multipart containers and decoding the
// common content-transfer-encodings. It returns nil when no part matches.
func extractPart(raw []byte, want string) []byte {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	return partOfType(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, want)
}

// partOfType resolves the content of the wanted media type from a MIME entity
// given its headers and body reader, recursing into multipart containers.
func partOfType(contentType, encoding string, body io.Reader, want string) []byte {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, perr := mr.NextPart()
			if perr != nil {
				break
			}
			if data := partOfType(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part, want); data != nil {
				return data
			}
		}
		return nil
	case strings.HasPrefix(mediaType, want):
		data, derr := io.ReadAll(decodeReader(encoding, body))
		if derr != nil && len(data) == 0 {
			return nil
		}
		return data
	default:
		return nil
	}
}

// decodeReader wraps body with the decoder for the content-transfer-encoding.
func decodeReader(encoding string, body io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body)
	default:
		return body
	}
}
