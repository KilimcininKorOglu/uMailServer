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

// extractMessageBody returns the plain-text body of a raw RFC 822 message
// (PidTagBody). A message with no text/plain part yields an empty string.
func extractMessageBody(raw []byte) string {
	return strings.TrimSpace(string(extractPart(raw, "text/plain")))
}

// extractHTMLBody returns the HTML body bytes of a raw RFC 822 message
// (PidTagHtml), or nil when the message has no text/html part.
func extractHTMLBody(raw []byte) []byte {
	return extractPart(raw, "text/html")
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
