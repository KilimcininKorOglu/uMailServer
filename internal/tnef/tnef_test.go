package tnef

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf16"
)

// --- little-endian writers (bytes.Buffer.Write never errors) -----------------

func leU16(b *bytes.Buffer, v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
func leU32(b *bytes.Buffer, v uint32) {
	b.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
}

// --- spec-faithful builders (independent of the parser's own helpers) --------

// buildAttr emits one TNEF attribute: level, id(u32 LE), len(u32 LE), payload,
// checksum(u16 LE). It is written from the MS-OXTNEF layout directly so
// the parser is validated against the spec, not against itself.
func buildAttr(level byte, id uint32, payload []byte) []byte {
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

// buildAttrBadChecksum is buildAttr but with a deliberately wrong checksum.
func buildAttrBadChecksum(level byte, id uint32, payload []byte) []byte {
	b := buildAttr(level, id, payload)
	b[len(b)-1] ^= 0xFF // corrupt the high checksum byte
	return b
}

// tnefStream prepends the signature + key to a sequence of attributes.
func tnefStream(attrs ...[]byte) []byte {
	var b bytes.Buffer
	leU32(&b, uint32(signature))
	leU16(&b, 0x1234) // key
	for _, a := range attrs {
		b.Write(a)
	}
	return b.Bytes()
}

// utf16le encodes s as NUL-terminated UTF-16LE (as Outlook writes string props).
func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	u = append(u, 0) // NUL terminator
	out := make([]byte, 0, len(u)*2)
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

// pad emits n NUL bytes.
func pad(n int) []byte { return make([]byte, n) }

// propUnicode emits a TNEF property record for a PT_UNICODE value:
// type, id, count(=1), bytelen, utf16le bytes, 4-byte padding.
func propUnicode(id uint16, value string) []byte {
	var b bytes.Buffer
	leU16(&b, uint16(ptUnicode))
	leU16(&b, id)
	data := utf16le(value)
	leU32(&b, 1)
	leU32(&b, uint32(len(data)))
	b.Write(data)
	b.Write(pad(pad4(len(data))))
	return b.Bytes()
}

// propBinary emits a TNEF property record for a PT_BINARY value.
func propBinary(id uint16, value []byte) []byte {
	var b bytes.Buffer
	leU16(&b, uint16(ptBinary))
	leU16(&b, id)
	leU32(&b, 1)
	leU32(&b, uint32(len(value)))
	b.Write(value)
	b.Write(pad(pad4(len(value))))
	return b.Bytes()
}

// propLong emits a TNEF property record for a PT_LONG value (to exercise the
// fixed-width skip path inside a property block).
func propLong(id uint16, value uint32) []byte {
	var b bytes.Buffer
	leU16(&b, uint16(ptLong))
	leU16(&b, id)
	leU32(&b, value)
	return b.Bytes()
}

// propBlock wraps property records with the leading count.
func propBlock(props ...[]byte) []byte {
	var b bytes.Buffer
	leU32(&b, uint32(len(props)))
	for _, p := range props {
		b.Write(p)
	}
	return b.Bytes()
}

// rtfcpUncompressed wraps raw RTF in a "MELA" (uncompressed) compressed-RTF
// header so it can be embedded as a PR_RTF_COMPRESSED value.
func rtfcpUncompressed(raw []byte) []byte {
	var b bytes.Buffer
	leU32(&b, uint32(len(raw)+12)) // size = total - 4
	leU32(&b, uint32(len(raw)))    // rawsize
	leU32(&b, rtfUncompressed)
	leU32(&b, 0) // crc
	b.Write(raw)
	return b.Bytes()
}

// --- tests -------------------------------------------------------------------

func TestRTFInitDictLength(t *testing.T) {
	// The preset dictionary length is load-bearing: the decompressor seeds the
	// write offset to it, so an off-by-one corrupts every back-reference.
	if len(rtfInitDict) != rtfInitDictLen {
		t.Fatalf("rtfInitDict length = %d, want %d", len(rtfInitDict), rtfInitDictLen)
	}
}

func TestIsTNEF(t *testing.T) {
	good := tnefStream()
	if !IsTNEF(good) {
		t.Error("IsTNEF(valid signature) = false")
	}
	if IsTNEF([]byte{0x01, 0x02, 0x03, 0x04}) {
		t.Error("IsTNEF(non-TNEF) = true")
	}
	if IsTNEF([]byte{0x01}) {
		t.Error("IsTNEF(too short) = true")
	}
}

func TestParseAttachmentShort(t *testing.T) {
	// The classic short form: RENDDATA begins the attachment, ATTACHTITLE gives
	// the 8.3 name, ATTACHDATA gives the content.
	stream := tnefStream(
		buildAttr(0x02, attAttachRendData, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}),
		buildAttr(0x02, attAttachTitle, append([]byte("report.txt"), 0)),
		buildAttr(0x02, attAttachData, []byte("hello world")),
	)
	msg, rep, err := Parse(stream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	a := msg.Attachments[0]
	if a.Filename != "report.txt" {
		t.Errorf("filename = %q, want report.txt", a.Filename)
	}
	if string(a.Data) != "hello world" {
		t.Errorf("data = %q, want hello world", a.Data)
	}
	if !strings.HasPrefix(a.ContentType, "text/plain") {
		t.Errorf("contentType = %q, want text/plain*", a.ContentType)
	}
	if rep.ChecksumMismatches != 0 {
		t.Errorf("unexpected checksum mismatches: %d", rep.ChecksumMismatches)
	}
}

func TestParseAttachmentMAPIProps(t *testing.T) {
	// The MAPI form: the long filename, binary content, and mime tag live in the
	// attATTACHMENT property block and must win over any short fields. A
	// non-ASCII filename exercises UTF-16 decoding.
	block := propBlock(
		propLong(0x3705, 1),                                // PR_ATTACH_METHOD (skipped fixed-width prop)
		propUnicode(0x3707, "Q3 Förecast.pdf"),             // PR_ATTACH_LONG_FILENAME
		propBinary(0x3701, []byte{0x25, 0x50, 0x44, 0x46}), // PR_ATTACH_DATA_BIN "%PDF"
		propUnicode(0x370E, "application/pdf"),             // PR_ATTACH_MIME_TAG
	)
	stream := tnefStream(
		buildAttr(0x02, attAttachRendData, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}),
		buildAttr(0x02, attAttachTitle, append([]byte("Q3FORE~1.PDF"), 0)),
		buildAttr(0x02, attAttachment, block),
	)
	msg, _, err := Parse(stream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	a := msg.Attachments[0]
	if a.Filename != "Q3 Förecast.pdf" {
		t.Errorf("filename = %q, want long unicode name", a.Filename)
	}
	if !bytes.Equal(a.Data, []byte("%PDF")) {
		t.Errorf("data = %q, want %%PDF", a.Data)
	}
	if a.ContentType != "application/pdf" {
		t.Errorf("contentType = %q, want application/pdf", a.ContentType)
	}
}

func TestParseBodyFromMsgProps(t *testing.T) {
	body := "Merhaba dünya — TNEF gövdesi"
	stream := tnefStream(
		buildAttr(0x01, attMsgProps, propBlock(propUnicode(0x1000, body))),
	)
	msg, _, err := Parse(stream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.BodyText != body {
		t.Errorf("bodyText = %q, want %q", msg.BodyText, body)
	}
}

func TestParseTwoAttachmentsInOrder(t *testing.T) {
	stream := tnefStream(
		buildAttr(0x02, attAttachRendData, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}),
		buildAttr(0x02, attAttachTitle, append([]byte("a.txt"), 0)),
		buildAttr(0x02, attAttachData, []byte("first")),
		buildAttr(0x02, attAttachRendData, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}),
		buildAttr(0x02, attAttachTitle, append([]byte("b.txt"), 0)),
		buildAttr(0x02, attAttachData, []byte("second")),
	)
	msg, _, err := Parse(stream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(msg.Attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(msg.Attachments))
	}
	if msg.Attachments[0].Filename != "a.txt" || string(msg.Attachments[0].Data) != "first" {
		t.Errorf("attachment 0 = %+v", msg.Attachments[0])
	}
	if msg.Attachments[1].Filename != "b.txt" || string(msg.Attachments[1].Data) != "second" {
		t.Errorf("attachment 1 = %+v", msg.Attachments[1])
	}
}

func TestParseChecksumMismatchStillDecodes(t *testing.T) {
	// A bad checksum must be surfaced (Report), not silently dropped, and the
	// payload is still used.
	stream := tnefStream(
		buildAttr(0x02, attAttachRendData, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}),
		buildAttrBadChecksum(0x02, attAttachTitle, append([]byte("x.bin"), 0)),
		buildAttr(0x02, attAttachData, []byte("data")),
	)
	msg, rep, err := Parse(stream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if rep.ChecksumMismatches != 1 {
		t.Errorf("checksum mismatches = %d, want 1", rep.ChecksumMismatches)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Filename != "x.bin" {
		t.Errorf("attachment not decoded despite recoverable checksum error: %+v", msg.Attachments)
	}
}

func TestParseSkipsUnrecognizedAttributes(t *testing.T) {
	// attFROM (0x00008000) and attMessageClass are not needed for extraction;
	// because attributes are length-prefixed they must be skipped cleanly and
	// not break the attachment that follows.
	const attFrom = 0x00008000
	stream := tnefStream(
		buildAttr(0x01, attFrom, []byte("sender@example.com\x00")),
		buildAttr(0x01, attMessageClass, append([]byte("IPM.Note"), 0)),
		buildAttr(0x02, attAttachRendData, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}),
		buildAttr(0x02, attAttachTitle, append([]byte("ok.txt"), 0)),
		buildAttr(0x02, attAttachData, []byte("payload")),
	)
	msg, rep, err := Parse(stream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if rep.SkippedAttributes != 1 {
		t.Errorf("skipped attributes = %d, want 1 (attFrom)", rep.SkippedAttributes)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Filename != "ok.txt" {
		t.Errorf("attachment after skipped attrs not decoded: %+v", msg.Attachments)
	}
}

func TestParseBadSignature(t *testing.T) {
	if _, _, err := Parse([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05}); err == nil {
		t.Error("Parse(non-TNEF) returned nil error")
	}
}

func TestDecompressRTFUncompressed(t *testing.T) {
	raw := []byte(`{\rtf1 hello}`)
	out, err := decompressRTF(rtfcpUncompressed(raw))
	if err != nil {
		t.Fatalf("decompressRTF: %v", err)
	}
	if !bytes.Equal(out, raw) {
		t.Errorf("out = %q, want %q", out, raw)
	}
}

func TestDecompressRTFCompressedLiterals(t *testing.T) {
	// A pure-literal LZFu stream: one control byte 0x00 (8 literals) followed by
	// 8 literal bytes. The loop exits on input exhaustion.
	lits := []byte("ABCDEFGH")
	payload := append([]byte{0x00}, lits...)
	var b bytes.Buffer
	total := rtfHeaderLen + len(payload)
	leU32(&b, uint32(total-4)) // size
	leU32(&b, uint32(len(lits)))
	leU32(&b, rtfCompressed)
	leU32(&b, 0)
	b.Write(payload)
	out, err := decompressRTF(b.Bytes())
	if err != nil {
		t.Fatalf("decompressRTF: %v", err)
	}
	if !bytes.Equal(out, lits) {
		t.Errorf("out = %q, want %q", out, lits)
	}
}

func TestDecompressRTFBackReference(t *testing.T) {
	// One control byte 0x01 (bit0 = dictionary reference, rest literal) plus a
	// reference {high=0,low=0} → offset 0, length 2 → copies the first two bytes
	// of the preset dictionary ("{\"). The next (literal) bit hits end-of-input
	// and terminates.
	payload := []byte{0x01, 0x00, 0x00}
	var b bytes.Buffer
	total := rtfHeaderLen + len(payload)
	leU32(&b, uint32(total-4))
	leU32(&b, 2)
	leU32(&b, rtfCompressed)
	leU32(&b, 0)
	b.Write(payload)
	out, err := decompressRTF(b.Bytes())
	if err != nil {
		t.Fatalf("decompressRTF: %v", err)
	}
	if string(out) != rtfInitDict[:2] {
		t.Errorf("out = %q, want %q", out, rtfInitDict[:2])
	}
}

func TestParseBodyOnlyRTFNoted(t *testing.T) {
	// When the body exists only as compressed RTF, the decoder exposes the RTF
	// and records that it was not de-encapsulated (Rule 10: surface, don't drop).
	raw := []byte(`{\rtf1 body}`)
	stream := tnefStream(
		buildAttr(0x01, attMsgProps, propBlock(propBinary(0x1009, rtfcpUncompressed(raw)))),
	)
	msg, rep, err := Parse(stream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !bytes.Equal(msg.RTF, raw) {
		t.Errorf("RTF = %q, want %q", msg.RTF, raw)
	}
	foundNote := false
	for _, n := range rep.Notes {
		if strings.Contains(n, "compressed RTF") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Errorf("expected a note about RTF-only body, got %v", rep.Notes)
	}
}
