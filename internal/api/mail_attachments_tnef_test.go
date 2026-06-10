package api

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func leU16(b *bytes.Buffer, v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
func leU32(b *bytes.Buffer, v uint32) {
	b.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
}

// buildTNEFAttr emits one TNEF attribute (level, id, length-prefixed payload,
// checksum) per the MS-OXTNEF layout. Kept independent of internal/tnef's own
// builders so this test exercises the api wiring against the format directly.
func buildTNEFAttr(level byte, id uint32, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteByte(level)
	leU32(&b, id)
	leU32(&b, uint32(len(payload)))
	b.Write(payload)
	var sum uint32
	for _, c := range payload {
		sum = (sum + uint32(c)) & 0xFFFF
	}
	leU16(&b, uint16(sum))
	return b.Bytes()
}

func buildTNEFStream(attrs ...[]byte) []byte {
	var b bytes.Buffer
	leU32(&b, 0x223e9f78) // signature
	leU16(&b, 0x0001)     // key
	for _, a := range attrs {
		b.Write(a)
	}
	return b.Bytes()
}

// TNEF attribute ids reused here (see internal/tnef).
const (
	tAttBody        = 0x0002800C
	tAttAttachData  = 0x0006800F
	tAttAttachTitle = 0x00018010
	tAttAttachRend  = 0x00069002
)

func winmailWithFile(name, content string) []byte {
	return buildTNEFStream(
		buildTNEFAttr(0x02, tAttAttachRend, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}),
		buildTNEFAttr(0x02, tAttAttachTitle, append([]byte(name), 0)),
		buildTNEFAttr(0x02, tAttAttachData, []byte(content)),
	)
}

func TestCollectAttachmentsExpandsTNEFPart(t *testing.T) {
	// A multipart/mixed message with a text part and a winmail.dat part: the
	// attachment listing must show the file inside winmail.dat, never the opaque
	// winmail.dat blob itself.
	wm := base64.StdEncoding.EncodeToString(winmailWithFile("invoice.pdf", "%PDF-1.7 inner"))
	raw := "Content-Type: multipart/mixed; boundary=\"BND\"\r\n\r\n" +
		"--BND\r\nContent-Type: text/plain\r\n\r\nhello\r\n" +
		"--BND\r\nContent-Type: application/ms-tnef; name=\"winmail.dat\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"winmail.dat\"\r\n\r\n" +
		wm + "\r\n--BND--\r\n"

	parts := collectAttachments([]byte(raw))
	if len(parts) != 1 {
		t.Fatalf("attachments = %d, want 1", len(parts))
	}
	if parts[0].filename != "invoice.pdf" {
		t.Errorf("filename = %q, want invoice.pdf (winmail.dat must be expanded)", parts[0].filename)
	}
	if string(parts[0].data) != "%PDF-1.7 inner" {
		t.Errorf("data = %q, want the inner file content", parts[0].data)
	}
	for _, p := range parts {
		if strings.EqualFold(p.filename, "winmail.dat") {
			t.Errorf("opaque winmail.dat was surfaced instead of its contents")
		}
	}
}

func TestExtractBodyTopLevelTNEF(t *testing.T) {
	// A top-level application/ms-tnef message: the body lives in the TNEF stream,
	// so extractBody must decode it rather than return the raw base64.
	body := "Bu gövde winmail.dat içinde."
	wm := buildTNEFStream(buildTNEFAttr(0x01, tAttBody, []byte(body)))
	raw := "Content-Type: application/ms-tnef\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString(wm) + "\r\n"

	h := &MailHandler{}
	if got := h.extractBody(raw); got != body {
		t.Errorf("extractBody = %q, want %q", got, body)
	}
}

func TestCollectAttachmentsNonTNEFOctetStreamUnchanged(t *testing.T) {
	// A real winmail.dat-named part that is NOT valid TNEF must fall through and
	// still be surfaced (never silently dropped).
	raw := "Content-Type: multipart/mixed; boundary=\"BND\"\r\n\r\n" +
		"--BND\r\nContent-Type: application/ms-tnef; name=\"winmail.dat\"\r\n" +
		"Content-Disposition: attachment; filename=\"winmail.dat\"\r\n\r\n" +
		"not actually tnef\r\n--BND--\r\n"

	parts := collectAttachments([]byte(raw))
	if len(parts) != 1 {
		t.Fatalf("attachments = %d, want 1 (fallback to original part)", len(parts))
	}
	if parts[0].filename != "winmail.dat" {
		t.Errorf("filename = %q, want winmail.dat fallback", parts[0].filename)
	}
}
