package tnef

import (
	"bytes"
	"fmt"
	"unicode/utf16"
)

// Encode builds a TNEF (winmail.dat) stream from a decoded message: its body
// (PR_BODY plain text, PR_HTML, and/or PR_RTF_COMPRESSED) and attachments. It is
// the inverse of Parse — Parse(Encode(m)) recovers the same body and attachments
// — so a message can be exported in the Exchange-native TNEF container.
//
// RTF is stored in the verbatim ("MELA") compressed-RTF form, so no LZFu encoder
// is needed and Parse decompresses it back unchanged. Strings are PT_UNICODE
// (NUL-terminated UTF-16LE).
func Encode(msg *Message) ([]byte, error) {
	if msg == nil {
		return nil, fmt.Errorf("tnef: nil message")
	}
	var b bytes.Buffer
	wU32(&b, signature)
	wU16(&b, 0) // key (any value; Parse ignores it)

	// Message class — Outlook expects IPM.Note for a mail item.
	writeAttr(&b, 0x01, attMessageClass, append([]byte("IPM.Note"), 0))

	// Message-level MAPI property block: the body carriers that are present.
	var records bytes.Buffer
	count := 0
	if msg.BodyText != "" {
		writeStringProp(&records, prBody, msg.BodyText)
		count++
	}
	if msg.BodyHTML != "" {
		writeBinaryProp(&records, prHTML, []byte(msg.BodyHTML))
		count++
	}
	if len(msg.RTF) > 0 {
		writeBinaryProp(&records, prRTFCompressed, rtfVerbatim(msg.RTF))
		count++
	}
	if count > 0 {
		var block bytes.Buffer
		wU32(&block, uint32(count))
		block.Write(records.Bytes())
		writeAttr(&b, 0x01, attMsgProps, block.Bytes())
	}

	// Attachments: each is an AttachRendData (which begins the attachment block)
	// followed by a MAPI property block carrying the long filename, content, and
	// MIME type.
	for i, a := range msg.Attachments {
		writeAttr(&b, 0x02, attAttachRendData, attachRendData(i))

		var arecords bytes.Buffer
		acount := 0
		name := a.Filename
		if name == "" {
			name = fmt.Sprintf("attachment-%d", i+1)
		}
		writeStringProp(&arecords, prAttachLongFilename, name)
		acount++
		writeBinaryProp(&arecords, prAttachDataBin, a.Data)
		acount++
		if a.ContentType != "" {
			writeStringProp(&arecords, prAttachMimeTag, a.ContentType)
			acount++
		}
		var ablock bytes.Buffer
		wU32(&ablock, uint32(acount))
		ablock.Write(arecords.Bytes())
		writeAttr(&b, 0x02, attAttachment, ablock.Bytes())
	}

	return b.Bytes(), nil
}

// writeAttr writes one TNEF attribute: level, id(u32 LE), length(u32 LE),
// payload, checksum(u16 LE) — the framing Parse reads.
func writeAttr(b *bytes.Buffer, level byte, id uint32, payload []byte) {
	b.WriteByte(level)
	wU32(b, id)
	wU32(b, uint32(len(payload)))
	b.Write(payload)
	wU16(b, checksum(payload))
}

// writeStringProp writes a PT_UNICODE property record (NUL-terminated UTF-16LE,
// 4-byte padded). tag's low 16 bits are the type, the high 16 the property id.
func writeStringProp(b *bytes.Buffer, tag uint32, value string) {
	wU16(b, uint16(tag&0xFFFF))
	wU16(b, uint16(tag>>16))
	data := encodeUTF16(value)
	wU32(b, 1)
	wU32(b, uint32(len(data)))
	b.Write(data)
	b.Write(make([]byte, pad4(len(data))))
}

// writeBinaryProp writes a PT_BINARY property record (4-byte padded).
func writeBinaryProp(b *bytes.Buffer, tag uint32, value []byte) {
	wU16(b, uint16(tag&0xFFFF))
	wU16(b, uint16(tag>>16))
	wU32(b, 1)
	wU32(b, uint32(len(value)))
	b.Write(value)
	b.Write(make([]byte, pad4(len(value))))
}

// encodeUTF16 encodes s as NUL-terminated UTF-16LE, the form Parse's
// readCountedString expects for a PT_UNICODE value.
func encodeUTF16(s string) []byte {
	u := utf16.Encode([]rune(s))
	u = append(u, 0) // NUL terminator
	out := make([]byte, 0, len(u)*2)
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

// rtfVerbatim wraps raw RTF in the uncompressed ("MELA") compressed-RTF header
// that decompressRTF reads back verbatim: size, raw-size, magic, CRC (CRC is not
// validated on decode), then the raw bytes.
func rtfVerbatim(raw []byte) []byte {
	var b bytes.Buffer
	wU32(&b, uint32(len(raw)+12)) // size = total length - 4
	wU32(&b, uint32(len(raw)))    // raw (uncompressed) size
	wU32(&b, rtfUncompressed)     // "MELA"
	wU32(&b, 0)                   // CRC (unused on decode)
	b.Write(raw)
	return b.Bytes()
}

// attachRendData builds a minimal AttachRendData structure (attach-by-value file,
// position = attachment index). Parse does not read its fields, but real TNEF
// readers expect a well-formed 14-byte block to begin an attachment.
func attachRendData(index int) []byte {
	var b bytes.Buffer
	wU16(&b, 1)             // atyp: attach by value (file)
	wU32(&b, uint32(index)) // ulPosition
	wU16(&b, 0)             // dxWidth
	wU16(&b, 0)             // dyHeight
	wU32(&b, 0)             // dwFlags
	return b.Bytes()
}

// wU16 / wU32 write little-endian integers (bytes.Buffer.Write never errors).
func wU16(b *bytes.Buffer, v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }
func wU32(b *bytes.Buffer, v uint32) {
	b.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
}
