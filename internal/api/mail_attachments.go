package api

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/umailserver/umailserver/internal/tnef"
)

// AttachmentInfo is the JSON listing of one attachment on a received message.
// It carries no content; the bytes are fetched on demand by index via
// handleMailAttachment.
type AttachmentInfo struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	Index       int    `json:"index"`
}

// attachmentPart is one decoded attachment extracted from a message.
type attachmentPart struct {
	filename    string
	contentType string
	data        []byte
}

// collectAttachments walks a raw RFC822 message (recursing into nested
// multiparts) and returns its attachment parts in document order. A part is an
// attachment when its Content-Disposition is "attachment" or it carries a
// filename parameter on either header.
func collectAttachments(raw []byte) []attachmentPart {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		return nil
	}
	// A top-level TNEF message (application/ms-tnef) carries its attachments
	// inside the winmail.dat stream rather than as MIME parts.
	if isTNEFMediaType(mediaType) {
		body, rerr := io.ReadAll(msg.Body)
		if rerr != nil {
			return nil
		}
		decoded := decodeBody(body, msg.Header.Get("Content-Transfer-Encoding"))
		var out []attachmentPart
		expandTNEFInto(&out, []byte(decoded))
		return out
	}
	if !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		return nil
	}
	var out []attachmentPart
	walkMultipart(msg.Body, params["boundary"], &out)
	return out
}

// walkMultipart appends attachment parts found under one multipart body.
func walkMultipart(body io.Reader, boundary string, out *[]attachmentPart) {
	mr := multipart.NewReader(body, boundary)
	for {
		p, err := mr.NextPart()
		if err != nil {
			return
		}
		ctype := p.Header.Get("Content-Type")
		mt, mparams, mtErr := mime.ParseMediaType(ctype)
		if mtErr != nil {
			mt, mparams = "", nil
		}
		if strings.HasPrefix(mt, "multipart/") && mparams["boundary"] != "" {
			raw, err := io.ReadAll(p)
			if err == nil {
				walkMultipart(bytes.NewReader(raw), mparams["boundary"], out)
			}
			continue
		}
		filename := attachmentFilename(p.Header, mparams)
		disposition, dparams, dispErr := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
		if dispErr != nil {
			disposition, dparams = "", nil
		}
		if filename == "" && dparams != nil {
			filename = dparams["filename"]
		}
		isAttachment := strings.EqualFold(disposition, "attachment") || filename != ""
		if !isAttachment {
			continue
		}
		raw, err := io.ReadAll(p)
		if err != nil {
			continue
		}
		decoded := decodeBody(raw, p.Header.Get("Content-Transfer-Encoding"))
		// A winmail.dat / application/ms-tnef part is a container: replace it with
		// the real files it carries. If it is not parseable TNEF, fall through and
		// surface the original part so nothing is lost.
		if isTNEFMediaType(mt) || strings.EqualFold(filename, "winmail.dat") {
			if expandTNEFInto(out, []byte(decoded)) {
				continue
			}
		}
		if mt == "" {
			mt = "application/octet-stream"
		}
		if filename == "" {
			filename = fmt.Sprintf("attachment-%d", len(*out)+1)
		}
		*out = append(*out, attachmentPart{
			filename:    filename,
			contentType: mt,
			data:        []byte(decoded),
		})
	}
}

// attachmentFilename pulls a filename from the Content-Type name parameter.
func attachmentFilename(h map[string][]string, ctypeParams map[string]string) string {
	if ctypeParams != nil && ctypeParams["name"] != "" {
		return ctypeParams["name"]
	}
	_ = h
	return ""
}

// listAttachments projects a raw message's attachments for the message detail.
func listAttachments(raw []byte) []AttachmentInfo {
	parts := collectAttachments(raw)
	if len(parts) == 0 {
		return nil
	}
	infos := make([]AttachmentInfo, 0, len(parts))
	for i, p := range parts {
		infos = append(infos, AttachmentInfo{
			Filename:    p.filename,
			ContentType: p.contentType,
			Size:        len(p.data),
			Index:       i,
		})
	}
	return infos
}

// handleMailAttachment streams one attachment of a received message by index.
// GET /api/v1/mail/attachment?id=<msgid>&index=<n>
func (h *MailHandler) handleMailAttachment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	user := r.Context().Value("user")
	userEmail, ok := user.(string)
	if !ok || userEmail == "" {
		h.sendError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		h.sendError(w, http.StatusBadRequest, "Message ID required")
		return
	}
	index, err := strconv.Atoi(r.URL.Query().Get("index"))
	if err != nil || index < 0 {
		h.sendError(w, http.StatusBadRequest, "Valid attachment index required")
		return
	}

	_, _, meta, found := h.findMessage(userEmail, id)
	if !found || h.msgStore == nil {
		h.sendError(w, http.StatusNotFound, "Message not found")
		return
	}
	raw, err := h.msgStore.ReadMessage(userEmail, meta.MessageID)
	if err != nil {
		h.sendError(w, http.StatusNotFound, "Message not found")
		return
	}
	parts := collectAttachments(raw)
	if index >= len(parts) {
		h.sendError(w, http.StatusNotFound, "Attachment not found")
		return
	}
	part := parts[index]

	w.Header().Set("Content-Type", part.contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", sanitizeHeaderValue(part.filename)))
	w.Header().Set("Content-Length", strconv.Itoa(len(part.data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(part.data); err != nil {
		return
	}
}

// isTNEFMediaType reports whether a MIME media type is the Microsoft TNEF
// (winmail.dat) container type.
func isTNEFMediaType(mt string) bool {
	switch strings.ToLower(mt) {
	case "application/ms-tnef", "application/vnd.ms-tnef":
		return true
	default:
		return false
	}
}

// expandTNEFInto decodes a TNEF (winmail.dat) blob and appends its contained
// files to out. It returns false (appending nothing) when the bytes are not
// parseable TNEF or carry no attachments, so the caller can keep the original
// part instead of dropping it.
func expandTNEFInto(out *[]attachmentPart, data []byte) bool {
	if !tnef.IsTNEF(data) {
		return false
	}
	msg, _, err := tnef.Parse(data)
	if err != nil || len(msg.Attachments) == 0 {
		return false
	}
	for i, a := range msg.Attachments {
		name := a.Filename
		if name == "" {
			name = fmt.Sprintf("attachment-%d", i+1)
		}
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		*out = append(*out, attachmentPart{filename: name, contentType: ct, data: a.Data})
	}
	return true
}

// tnefBody returns the decoded body of a top-level application/ms-tnef message
// (the case where the human-readable body lives only inside winmail.dat). It
// returns ok=false for non-TNEF, multipart, or unparseable messages, leaving the
// normal body extraction in charge.
func tnefBody(raw []byte) (string, bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", false
	}
	mediaType, _, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || !isTNEFMediaType(mediaType) {
		return "", false
	}
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return "", false
	}
	decoded := []byte(decodeBody(body, msg.Header.Get("Content-Transfer-Encoding")))
	if !tnef.IsTNEF(decoded) {
		return "", false
	}
	m, _, perr := tnef.Parse(decoded)
	if perr != nil {
		return "", false
	}
	if m.BodyText != "" {
		return m.BodyText, true
	}
	if m.BodyHTML != "" {
		return m.BodyHTML, true
	}
	return "", false
}
