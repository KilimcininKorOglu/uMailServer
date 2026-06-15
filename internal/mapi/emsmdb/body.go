package emsmdb

import (
	"strings"

	"github.com/umailserver/umailserver/internal/mailbody"
)

// BodyStore reads a raw RFC 822 message by its storage id. It is the Maildir
// message store, kept separate from the metadata Store; message bodies are not
// part of the canonical metadata.
type BodyStore interface {
	ReadMessage(user, messageID string) ([]byte, error)
}

// extractMessageBody returns the plain-text body of a raw RFC 822 message
// (PidTagBody). A message with no text/plain part yields an empty string. The
// MIME walk and content-transfer-encoding decoding are shared with every other
// surface through internal/mailbody, so cached-mode Outlook reads the same
// decoded text as webmail, search, EAS and JMAP.
func extractMessageBody(raw []byte) string {
	return strings.TrimSpace(string(mailbody.ExtractPart(raw, "text/plain")))
}

// extractHTMLBody returns the HTML body bytes of a raw RFC 822 message
// (PidTagHtml), or nil when the message has no text/html part.
func extractHTMLBody(raw []byte) []byte {
	return mailbody.ExtractPart(raw, "text/html")
}
