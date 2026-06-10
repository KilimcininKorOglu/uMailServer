// Package tnef decodes Microsoft TNEF ("winmail.dat", application/ms-tnef)
// streams into their contained attachments and message body, so a recipient on
// a non-Exchange client sees the real files and text instead of an opaque
// winmail.dat blob.
//
// # Format
//
// The wire layout follows the MS-OXTNEF specification (TNEF attribute and MAPI
// property block layout, attribute-id and property-tag constants) and MS-OXRTFCP
// (compressed-RTF / LZFu).
//
// A TNEF stream is a 6-byte header (signature uint32 0x223e9f78 + key uint16)
// followed by length-prefixed attributes. Each attribute is:
//
//	level    uint8   (1 = message, 2 = attachment)
//	id       uint32  (little-endian; type word in the high 16 bits, id in the low)
//	length   uint32  (byte length of the payload)
//	payload  [length]byte
//	checksum uint16  (sum of payload bytes mod 65536)
//
// Because every attribute is length-prefixed, attributes this decoder does not
// interpret are skipped cleanly without desyncing the stream. The only payloads
// parsed structurally are the two MAPI property blocks (attATTACHMENT and
// attMSGPROPS); an unknown property type inside a block cannot be length-skipped,
// so parsing of that one block stops and the gap is recorded in Report.
//
// # Scope
//
// This is a decoder (read path) only — it does not generate TNEF. The message
// body is taken from PR_BODY (plain text) and PR_HTML (HTML); PR_RTF_COMPRESSED
// is LZFu-decompressed and exposed as Message.RTF, but RTF-to-HTML
// de-encapsulation is intentionally left to a follow-up and is noted in Report
// when RTF is the only body carrier.
package tnef

import (
	"encoding/binary"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// signature is the leading uint32 of every TNEF stream (TNEF_SIGNATURE).
const signature = 0x223e9f78

// Attribute ids (full 32-bit values, type word in the high 16 bits). Names and
// values follow the MS-OXTNEF attribute definitions.
const (
	attBody           = 0x0002800C // message plain-text body (raw payload)
	attMessageClass   = 0x00078008 // message class (raw payload)
	attAttachData     = 0x0006800F // short attachment content (raw payload)
	attAttachTitle    = 0x00018010 // short (8.3) attachment name (raw ASCII)
	attAttachRendData = 0x00069002 // begins one attachment block
	attMsgProps       = 0x00069003 // message-level MAPI property block
	attAttachment     = 0x00069005 // per-attachment MAPI property block
)

// MAPI property tags, computed as (id<<16)|type per the MAPI PROP_TAG convention. String tags
// are matched by id (high 16 bits) so either the PT_UNICODE or PT_STRING8 form
// is accepted.
const (
	prBody               = 0x1000001F // PT_UNICODE PidTagBody
	prHTML               = 0x10130102 // PT_BINARY  PidTagHtml
	prRTFCompressed      = 0x10090102 // PT_BINARY  PidTagRtfCompressed
	prAttachDataBin      = 0x37010102 // PT_BINARY  PidTagAttachDataBinary
	prAttachFilename     = 0x3704001F // PT_UNICODE PidTagAttachFilename (8.3)
	prAttachLongFilename = 0x3707001F // PT_UNICODE PidTagAttachLongFilename
	prAttachMimeTag      = 0x370E001F // PT_UNICODE PidTagAttachMimeTag
)

// MAPI property type codes (low 16 bits of a property tag).
const (
	ptShort    = 0x0002
	ptLong     = 0x0003
	ptFloat    = 0x0004
	ptDouble   = 0x0005
	ptCurrency = 0x0006
	ptError    = 0x000A
	ptBoolean  = 0x000B
	ptObject   = 0x000D
	ptI8       = 0x0014
	ptString8  = 0x001E
	ptUnicode  = 0x001F
	ptSysTime  = 0x0040
	ptAppTime  = 0x0007
	ptClsid    = 0x0048
	ptBinary   = 0x0102
	ptMVFlag   = 0x1000
)

// mnidString marks a named property identified by a Unicode string (vs MNID_ID).
const mnidString = 0x00000001

// Attachment is one file extracted from a TNEF stream.
type Attachment struct {
	// Filename is the long filename when available, else the 8.3 short name.
	Filename string
	// ContentType is taken from PR_ATTACH_MIME_TAG when present, otherwise
	// guessed from the filename extension, otherwise application/octet-stream.
	ContentType string
	// Data is the decoded file content.
	Data []byte
}

// Message is the decoded content of a TNEF (winmail.dat) stream.
type Message struct {
	// Attachments are the files carried in the stream, in document order.
	Attachments []Attachment
	// BodyText is the PR_BODY plain-text body, if present.
	BodyText string
	// BodyHTML is the PR_HTML body, if present.
	BodyHTML string
	// RTF is the LZFu-decompressed PR_RTF_COMPRESSED body, if present. It is not
	// de-encapsulated to HTML here (see package doc).
	RTF []byte
}

// Report records lossy or skipped outcomes so the caller can surface them
// (never drop data silently).
type Report struct {
	// SkippedAttributes counts top-level attributes that were recognized-as-
	// skippable and not interpreted (informational; framing stays in sync).
	SkippedAttributes int
	// ChecksumMismatches counts attributes whose trailing checksum did not match
	// the payload (the payload is still used).
	ChecksumMismatches int
	// Notes holds human-readable degradation messages.
	Notes []string
}

func (rep *Report) note(format string, args ...any) {
	rep.Notes = append(rep.Notes, fmt.Sprintf(format, args...))
}

// IsTNEF reports whether b begins with the TNEF signature.
func IsTNEF(b []byte) bool {
	return len(b) >= 4 && binary.LittleEndian.Uint32(b) == signature
}

// Parse decodes a TNEF (winmail.dat) byte stream into its attachments and body.
// It returns an error only when the stream is not TNEF or is truncated at the
// framing level; recoverable issues are recorded in Report.
func Parse(b []byte) (*Message, Report, error) {
	var rep Report
	r := &reader{b: b}
	if r.u32() != signature {
		return nil, rep, fmt.Errorf("tnef: bad signature")
	}
	_ = r.u16() // key
	if r.err {
		return nil, rep, fmt.Errorf("tnef: truncated header")
	}

	msg := &Message{}
	var cur *Attachment     // attachment block currently being assembled
	var curShortData []byte // attATTACHDATA payload for the current block
	var curLongData []byte  // PR_ATTACH_DATA_BIN for the current block
	var curMime string      // PR_ATTACH_MIME_TAG for the current block

	finish := func() {
		if cur == nil {
			return
		}
		data := curLongData
		if data == nil {
			data = curShortData
		}
		cur.Data = data
		cur.ContentType = resolveContentType(curMime, cur.Filename)
		msg.Attachments = append(msg.Attachments, *cur)
		cur, curShortData, curLongData, curMime = nil, nil, nil, ""
	}

	for r.remaining() > 0 {
		_ = r.u8() // level
		id := r.u32()
		length := r.u32()
		payload := r.bytes(int(length))
		sum := r.u16()
		if r.err {
			break
		}
		if sum != checksum(payload) {
			rep.ChecksumMismatches++
			rep.note("attribute 0x%08x checksum mismatch", id)
		}

		switch id {
		case attAttachRendData:
			finish()
			cur = &Attachment{}
		case attAttachTitle:
			if cur == nil {
				cur = &Attachment{}
			}
			if name := trimCString(string(payload)); name != "" && cur.Filename == "" {
				cur.Filename = name
			}
		case attAttachData:
			if cur == nil {
				cur = &Attachment{}
			}
			curShortData = payload
		case attAttachment:
			if cur == nil {
				cur = &Attachment{}
			}
			props := parsePropertyBlock(payload, &rep)
			if name := props.str(prAttachLongFilename); name != "" {
				cur.Filename = name
			} else if name := props.str(prAttachFilename); name != "" && cur.Filename == "" {
				cur.Filename = name
			}
			if bin, ok := props.bin(prAttachDataBin); ok {
				curLongData = bin
			}
			if mt := props.str(prAttachMimeTag); mt != "" {
				curMime = mt
			}
		case attMsgProps:
			props := parsePropertyBlock(payload, &rep)
			if s := props.str(prBody); s != "" {
				msg.BodyText = s
			}
			if bin, ok := props.bin(prHTML); ok {
				msg.BodyHTML = string(bin)
			}
			if bin, ok := props.bin(prRTFCompressed); ok {
				if rtf, derr := decompressRTF(bin); derr == nil {
					msg.RTF = rtf
				} else {
					rep.note("PR_RTF_COMPRESSED decompress failed: %v", derr)
				}
			}
		case attBody:
			if msg.BodyText == "" {
				msg.BodyText = trimCString(string(payload))
			}
		case attMessageClass:
			// recognized, not needed for attachment/body extraction
		default:
			rep.SkippedAttributes++
		}
	}
	finish()

	if msg.BodyText == "" && msg.BodyHTML == "" && len(msg.RTF) > 0 {
		rep.note("body present only as compressed RTF (not de-encapsulated to HTML)")
	}
	return msg, rep, nil
}

// resolveContentType picks a MIME type: the TNEF mime tag, else a guess from the
// filename extension, else application/octet-stream.
func resolveContentType(mimeTag, filename string) string {
	if mimeTag != "" {
		return mimeTag
	}
	if ext := filepath.Ext(filename); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			return ct
		}
	}
	return "application/octet-stream"
}

// checksum is the TNEF attribute checksum: sum of payload bytes mod 65536.
func checksum(b []byte) uint16 {
	var sum uint32
	for _, c := range b {
		sum = (sum + uint32(c)) & 0xFFFF
	}
	return uint16(sum)
}

// trimCString drops a trailing NUL and surrounding whitespace from a C string.
func trimCString(s string) string {
	return strings.TrimSpace(strings.TrimRight(s, "\x00"))
}

// ---------------------------------------------------------------------------
// MAPI property block (inside attATTACHMENT / attMSGPROPS)
// ---------------------------------------------------------------------------

// propValues holds the decoded properties of one MAPI property block, keyed by
// property id (the high 16 bits of a property tag), so a string is found whether
// the sender used PT_UNICODE or PT_STRING8.
type propValues struct {
	strs map[uint16]string
	bins map[uint16][]byte
}

// str returns the string value for the given full property tag, matched by id.
func (p propValues) str(tag uint32) string { return p.strs[uint16(tag>>16)] }

// bin returns the binary value for the given full property tag, matched by id.
func (p propValues) bin(tag uint32) ([]byte, bool) {
	v, ok := p.bins[uint16(tag>>16)]
	return v, ok
}

// parsePropertyBlock decodes a TNEF MAPI property block (count-prefixed list of
// type/id/value records). On an unknown property type the block cannot be safely
// advanced, so parsing stops and the gap is noted in rep; whatever was decoded
// before the gap is returned.
func parsePropertyBlock(b []byte, rep *Report) propValues {
	out := propValues{strs: map[uint16]string{}, bins: map[uint16][]byte{}}
	r := &reader{b: b}
	count := r.u32()
	for i := uint32(0); i < count && !r.err; i++ {
		ptype := r.u16()
		pid := r.u16()
		if pid >= 0x8000 { // named property: skip its name descriptor
			if !skipPropName(r) {
				rep.note("property block stopped at a malformed named property")
				return out
			}
		}
		multi := ptype&ptMVFlag != 0
		base := ptype &^ ptMVFlag
		if multi {
			if !skipMultiValue(r, base) {
				rep.note("property block stopped at unsupported multivalue type 0x%04x", base)
				return out
			}
			continue
		}
		switch base {
		case ptShort, ptBoolean:
			_ = r.u16()
			r.advance(2)
		case ptLong, ptError, ptFloat:
			r.advance(4)
		case ptDouble, ptAppTime, ptCurrency, ptI8, ptSysTime:
			r.advance(8)
		case ptClsid:
			r.advance(16)
		case ptString8:
			if s, ok := readCountedString(r, false); ok {
				out.strs[pid] = s
			} else {
				rep.note("property block stopped in a PT_STRING8 value")
				return out
			}
		case ptUnicode:
			if s, ok := readCountedString(r, true); ok {
				out.strs[pid] = s
			} else {
				rep.note("property block stopped in a PT_UNICODE value")
				return out
			}
		case ptBinary, ptObject:
			if v, ok := readCountedBinary(r); ok {
				out.bins[pid] = v
			} else {
				rep.note("property block stopped in a PT_BINARY value")
				return out
			}
		default:
			rep.note("property block stopped at unknown property type 0x%04x", base)
			return out
		}
	}
	return out
}

// skipPropName consumes a named-property descriptor: GUID(16) + kind(uint32) and
// then either a LID (uint32) or a length-prefixed UTF-16 name padded to 4 bytes.
func skipPropName(r *reader) bool {
	r.advance(16) // GUID
	kind := r.u32()
	if kind == mnidString {
		n := r.u32()
		r.advance(int(n))
		r.advance(pad4(int(n)))
	} else {
		r.advance(4) // LID
	}
	return !r.err
}

// readCountedString reads a TNEF string value: count(uint32, must be 1),
// length(uint32 bytes), the bytes, then 4-byte padding. When unicode is true the
// bytes are UTF-16LE.
func readCountedString(r *reader, unicode bool) (string, bool) {
	if r.u32() != 1 {
		return "", false
	}
	n := int(r.u32())
	raw := r.bytes(n)
	r.advance(pad4(n))
	if r.err {
		return "", false
	}
	if unicode {
		return decodeUTF16(raw), true
	}
	return trimCString(string(raw)), true
}

// readCountedBinary reads a TNEF binary value: count(uint32, must be 1),
// length(uint32), the bytes, then 4-byte padding.
func readCountedBinary(r *reader) ([]byte, bool) {
	if r.u32() != 1 {
		return nil, false
	}
	n := int(r.u32())
	raw := r.bytes(n)
	r.advance(pad4(n))
	if r.err {
		return nil, false
	}
	return raw, true
}

// skipMultiValue consumes a multivalued property: count(uint32) then count
// values of the base type. Only the types real TNEF uses are handled; anything
// else returns false so the caller can stop cleanly.
func skipMultiValue(r *reader, base uint16) bool {
	count := r.u32()
	for i := uint32(0); i < count && !r.err; i++ {
		switch base {
		case ptShort:
			_ = r.u16()
			r.advance(2)
		case ptLong, ptError, ptFloat:
			r.advance(4)
		case ptDouble, ptAppTime, ptCurrency, ptI8, ptSysTime:
			r.advance(8)
		case ptClsid:
			r.advance(16)
		case ptString8:
			if _, ok := readCountedString(r, false); !ok {
				return false
			}
		case ptUnicode:
			if _, ok := readCountedString(r, true); !ok {
				return false
			}
		case ptBinary, ptObject:
			if _, ok := readCountedBinary(r); !ok {
				return false
			}
		default:
			return false
		}
	}
	return !r.err
}

// decodeUTF16 decodes a UTF-16LE byte slice (NUL-trimmed) to a UTF-8 string.
func decodeUTF16(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	return trimCString(string(utf16.Decode(u)))
}

// pad4 returns the number of padding bytes needed to round n up to a 4-byte
// boundary.
func pad4(n int) int { return (4 - n%4) % 4 }

// ---------------------------------------------------------------------------
// Little-endian byte cursor
// ---------------------------------------------------------------------------

// reader is a little-endian byte cursor. Once a read runs past the end, err is
// set and every subsequent read returns zero, so callers can check err once.
type reader struct {
	b   []byte
	off int
	err bool
}

func (r *reader) remaining() int { return len(r.b) - r.off }

func (r *reader) u8() uint8 {
	if r.err || r.off+1 > len(r.b) {
		r.err = true
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u16() uint16 {
	if r.err || r.off+2 > len(r.b) {
		r.err = true
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if r.err || r.off+4 > len(r.b) {
		r.err = true
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

// bytes returns the next n bytes (a copy), setting err on overflow.
func (r *reader) bytes(n int) []byte {
	if r.err || n < 0 || r.off+n > len(r.b) {
		r.err = true
		return nil
	}
	out := make([]byte, n)
	copy(out, r.b[r.off:r.off+n])
	r.off += n
	return out
}

// advance skips n bytes, setting err on overflow.
func (r *reader) advance(n int) {
	if r.err || n < 0 || r.off+n > len(r.b) {
		r.err = true
		return
	}
	r.off += n
}
