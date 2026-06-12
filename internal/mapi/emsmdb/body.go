package emsmdb

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

// BodyStore reads a raw RFC 822 message by its storage id. It is the Maildir
// message store, kept separate from the metadata Store; message bodies are not
// part of the canonical metadata.
type BodyStore interface {
	ReadMessage(user, messageID string) ([]byte, error)
}

// extractMessageBody returns the plain-text body of a raw RFC 822 message. It
// prefers a text/plain part, descending through multipart containers, and decodes
// the common content-transfer-encodings. A message with no text/plain part yields
// an empty string (its content lives only in an HTML part this path does not
// serve).
func extractMessageBody(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	text := partText(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	return strings.TrimSpace(text)
}

// partText resolves the text/plain content of a MIME entity from its headers and
// body reader, recursing into multipart containers and skipping HTML parts and
// attachments so the plain body wins.
func partText(contentType, encoding string, body io.Reader) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			pt, _, perr := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if perr == nil && pt != "" && !strings.HasPrefix(pt, "text/plain") && !strings.HasPrefix(pt, "multipart/") {
				continue
			}
			if text := partText(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part); text != "" {
				return text
			}
		}
		return ""
	case strings.HasPrefix(mediaType, "text/"):
		data, err := io.ReadAll(decodeReader(encoding, body))
		if err != nil && len(data) == 0 {
			return ""
		}
		return string(data)
	default:
		return ""
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
