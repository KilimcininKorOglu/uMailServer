package activesync

import (
	"bytes"
	"mime"
	"net/mail"
	"strings"

	"github.com/umailserver/umailserver/internal/mailbody"
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
// while MessageFromRaw uses it for the journal change-feed path. The decode is
// the shared canonical one (internal/mailbody), so the body a phone syncs matches
// what the webmail message view and full-text search read.
func BodyForSync(raw []byte) (bodyType, body string) {
	b := mailbody.Parse(raw)
	if b.Text != "" {
		return "1", b.Text
	}
	if b.HTML != "" {
		return "2", b.HTML
	}
	return "1", ""
}
