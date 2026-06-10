package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"github.com/umailserver/umailserver/internal/tnef"
)

// exportTNEF writes one .tnef (winmail.dat) file per message under dir, grouped
// into folder subdirectories. Each stored RFC822 message is decomposed into its
// text/HTML body and attachments and re-encoded as a TNEF stream — the inverse of
// the inbound TNEF decode, so the result imports into Exchange/Outlook-family
// tools (e.g. gromox-tnef2mt) in the native container.
func exportTNEF(dir string, msgs []exportedMessage) error {
	seq := map[string]int{}
	for _, m := range msgs {
		sub := filepath.Join(dir, filepath.FromSlash(m.folder))
		if err := os.MkdirAll(sub, 0o750); err != nil {
			return fmt.Errorf("create %q: %w", sub, err)
		}
		msg, err := mimeToTNEF(m.raw)
		if err != nil {
			return fmt.Errorf("decode message in %q: %w", m.folder, err)
		}
		stream, err := tnef.Encode(msg)
		if err != nil {
			return fmt.Errorf("encode tnef: %w", err)
		}
		seq[m.folder]++
		name := filepath.Join(sub, fmt.Sprintf("%05d.tnef", seq[m.folder]))
		if err := os.WriteFile(name, stream, 0o600); err != nil {
			return fmt.Errorf("write %q: %w", name, err)
		}
	}
	return nil
}

// mimeToTNEF decomposes a raw RFC822 message into the TNEF body carriers
// (plain-text/HTML) and attachments. Multipart trees are walked recursively;
// transfer encodings (base64, quoted-printable) are decoded. Malformed parts
// fall back to their raw bytes rather than failing the whole export.
func mimeToTNEF(raw []byte) (*tnef.Message, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	out := &tnef.Message{}
	mediaType, params, perr := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if perr != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") && params["boundary"] != "" {
		walkMIMEParts(m.Body, params["boundary"], out)
		return out, nil
	}
	body, rerr := io.ReadAll(m.Body)
	if rerr != nil {
		return out, nil
	}
	decoded := decodeTransferEncoding(body, m.Header.Get("Content-Transfer-Encoding"))
	if strings.HasPrefix(mediaType, "text/html") {
		out.BodyHTML = string(decoded)
	} else {
		out.BodyText = string(decoded)
	}
	return out, nil
}

// walkMIMEParts walks one multipart level, recursing into nested multiparts and
// appending leaf parts to out as body or attachment.
func walkMIMEParts(r io.Reader, boundary string, out *tnef.Message) {
	mr := multipart.NewReader(r, boundary)
	for {
		p, err := mr.NextPart()
		if err != nil {
			return // io.EOF or a malformed boundary ends this level
		}
		partType, partParams, perr := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if perr != nil {
			partType, partParams = "text/plain", map[string]string{} // RFC 2045 default
		}
		if strings.HasPrefix(partType, "multipart/") && partParams["boundary"] != "" {
			// A multipart body is not transfer-encoded; recurse on the raw part.
			walkMIMEParts(p, partParams["boundary"], out)
			continue
		}
		data, derr := io.ReadAll(p)
		if derr != nil {
			continue // skip a part we cannot read (best-effort)
		}
		decoded := decodeTransferEncoding(data, p.Header.Get("Content-Transfer-Encoding"))

		disposition, dispParams, derr := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
		if derr != nil {
			disposition, dispParams = "", map[string]string{}
		}
		filename := dispParams["filename"]
		if filename == "" {
			filename = partParams["name"]
		}
		isAttachment := disposition == "attachment" || filename != "" ||
			(partType != "" && !strings.HasPrefix(partType, "text/"))

		switch {
		case isAttachment:
			ct := partType
			if ct == "" {
				ct = "application/octet-stream"
			}
			out.Attachments = append(out.Attachments, tnef.Attachment{
				Filename: filename, ContentType: ct, Data: decoded,
			})
		case strings.HasPrefix(partType, "text/html"):
			if out.BodyHTML == "" {
				out.BodyHTML = string(decoded)
			}
		default: // text/plain and untyped text
			if out.BodyText == "" {
				out.BodyText = string(decoded)
			}
		}
	}
}

// decodeTransferEncoding decodes a part body per its Content-Transfer-Encoding,
// returning the raw bytes unchanged for identity encodings or on a decode error.
func decodeTransferEncoding(data []byte, encoding string) []byte {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		stripped := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(data))
		if dec, err := base64.StdEncoding.DecodeString(stripped); err == nil {
			return dec
		}
		return data
	case "quoted-printable":
		if dec, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data))); err == nil {
			return dec
		}
		return data
	default:
		return data
	}
}
