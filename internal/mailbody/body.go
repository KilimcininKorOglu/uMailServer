// Package mailbody is the single home for decoding a raw RFC 822 message into the
// text every surface reads. Webmail display, full-text search, and the EAS Sync
// projection all parse a message through here, so a phone, a browser, and a
// search query see the same body content rather than three subtly different
// extractions of it. It owns MIME part selection and content-transfer-encoding
// decoding; callers map the structured result into their own wire shape.
package mailbody

import (
	"bytes"
	"encoding/base64"
	stdhtml "html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
)

// Body is the decoded body of a message, split by representation so each consumer
// reads the part it needs from one parse: Text is the message's text/plain part
// (empty when the message carries only HTML); HTML is the message's text/html
// part (empty when the message is plain only). Display prefers HTML for rich
// rendering; SearchText prefers Text and falls back to HTML reduced to text, so
// search always matches visible words rather than markup.
type Body struct {
	Text string
	HTML string
}

// Parse decodes a raw RFC 822 message into its structured Body, descending
// through multipart containers and decoding base64 / quoted-printable parts. A
// message that cannot be parsed yields a zero Body.
func Parse(raw []byte) Body {
	var b Body
	if t := ExtractPart(raw, "text/plain"); t != nil {
		b.Text = strings.TrimSpace(string(t))
	}
	if h := ExtractPart(raw, "text/html"); h != nil {
		b.HTML = string(h)
	}
	if b.Text == "" && b.HTML == "" {
		// No recognizable MIME text part (a non-MIME or malformed message that
		// mail.ReadMessage could not parse). Fall back to the header-stripped raw
		// body so a degenerate message still shows its content rather than nothing.
		b.Text = headerStrip(raw)
	}
	return b
}

// headerStrip returns everything after the first header/body separator, the
// defensive fallback for a message with no recognizable MIME text part. A message
// with no separator at all is returned whole.
func headerStrip(raw []byte) string {
	s := string(raw)
	if _, body, ok := strings.Cut(s, "\r\n\r\n"); ok {
		return strings.TrimSpace(body)
	}
	if _, body, ok := strings.Cut(s, "\n\n"); ok {
		return strings.TrimSpace(body)
	}
	return s
}

// Display returns the body to render and whether it is HTML: the text/html part
// when present (so rich formatting survives), otherwise the plain text. The
// caller is responsible for sanitizing HTML before injecting it into a document.
func (b Body) Display() (content string, isHTML bool) {
	if b.HTML != "" {
		return b.HTML, true
	}
	return b.Text, false
}

// SearchText returns a plain-text view of the body for full-text search and
// indexing: the text/plain part when present, otherwise the HTML part reduced to
// text. It never returns markup, so a query matches visible words rather than tag
// or attribute names.
func (b Body) SearchText() string {
	if b.Text != "" {
		return b.Text
	}
	return HTMLToText(b.HTML)
}

// ExtractPart returns the decoded content of the first MIME part whose media type
// has the given prefix (e.g. "text/plain", "text/calendar"), descending through
// multipart containers and decoding the common content-transfer-encodings. It
// returns nil when no part matches.
func ExtractPart(raw []byte, want string) []byte {
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

var (
	// Go's regexp (RE2) has no backreferences, so script and style blocks are
	// matched by their own patterns rather than one with a \1 close-tag tie.
	htmlScript     = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	htmlStyle      = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	htmlTag = regexp.MustCompile(`(?s)<[^>]*>`)
	// \p{Zs} folds non-breaking and other Unicode spaces (e.g. &nbsp; -> U+00A0)
	// that ASCII \s misses, so a search query matches across them.
	htmlWhitespace = regexp.MustCompile(`[\s\p{Zs}]+`)
)

// HTMLToText reduces an HTML body to a plain-text approximation for full-text
// search and indexing: it drops script and style blocks, strips tags, unescapes
// HTML entities, and collapses runs of whitespace. It is a search aid, not a
// renderer — the structure of the original markup is intentionally discarded.
func HTMLToText(html string) string {
	if html == "" {
		return ""
	}
	s := htmlScript.ReplaceAllString(html, " ")
	s = htmlStyle.ReplaceAllString(s, " ")
	s = htmlTag.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	s = htmlWhitespace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
