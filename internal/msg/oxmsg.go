package msg

import (
	"bytes"
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// MAPI property ids (the high 16 bits of a property tag) read from an OXMSG
// message. The low 16 bits select the type; string ids are matched in either
// the PT_UNICODE (001F) or PT_STRING8 (001E) form. Names follow MS-OXPROPS.
const (
	pidSubject              = 0x0037
	pidBody                 = 0x1000
	pidHTML                 = 0x1013
	pidInternetMessageID    = 0x1035
	pidClientSubmitTime     = 0x0039
	pidMessageDeliveryTime  = 0x0E06
	pidSenderName           = 0x0C1A
	pidSenderEmailAddress   = 0x0C1F
	pidSenderSMTPAddress    = 0x5D01
	pidSentReprName         = 0x0042
	pidSentReprEmailAddress = 0x0065
	pidSentReprSMTPAddress  = 0x5D02
	pidDisplayName          = 0x3001
	pidEmailAddress         = 0x3003
	pidSMTPAddress          = 0x39FE
	pidRecipientType        = 0x0C15
	pidAttachLongFilename   = 0x3707
	pidAttachFilename       = 0x3704
	pidAttachDataBinary     = 0x3701
	pidAttachMimeTag        = 0x370E
	pidAttachContentID      = 0x3712
)

// MAPI property types (the low 16 bits of a property tag).
const (
	ptLong    = 0x0003
	ptSysTime = 0x0040
	ptBinary  = 0x0102
	ptUnicode = 0x001F
	ptString8 = 0x001E
)

// Recipient types (PidTagRecipientType).
const (
	recipTo  = 1
	recipCc  = 2
	recipBcc = 3
)

// Attachment is one decoded OXMSG attachment.
type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string
	Data        []byte
}

// Message is the subset of an OXMSG message needed to reconstruct a faithful
// RFC 5322 message: envelope fields, both body alternatives, and attachments.
type Message struct {
	From        string
	To          []string
	Cc          []string
	Subject     string
	Date        time.Time
	MessageID   string
	BodyText    string
	BodyHTML    []byte
	Attachments []Attachment
}

// Parse decodes an .msg (OXMSG) byte slice into a Message.
func Parse(b []byte) (*Message, error) {
	r, err := openCFB(b)
	if err != nil {
		return nil, err
	}

	substg, props := r.collectProps(0, 32) // top-level header is 32 bytes (MS-OXMSG §2.4)
	m := &Message{
		Subject:   strProp(substg, pidSubject),
		BodyText:  strProp(substg, pidBody),
		MessageID: strProp(substg, pidInternetMessageID),
	}
	if html, ok := substg[tag(pidHTML, ptBinary)]; ok {
		m.BodyHTML = html
	}
	m.Date = sysTimeProp(props, pidClientSubmitTime)
	if m.Date.IsZero() {
		m.Date = sysTimeProp(props, pidMessageDeliveryTime)
	}
	m.From = senderAddress(substg)

	// Recipients and attachments are child storages of the root.
	for _, idx := range r.children(0) {
		e := r.entries[idx]
		if e.objType != objStorage {
			continue
		}
		switch {
		case strings.HasPrefix(e.name, "__recip_version1.0_"):
			rsub, rprops := r.collectProps(idx, 8)
			addr := recipientAddress(rsub)
			if addr == "" {
				continue
			}
			switch longProp(rprops, pidRecipientType) {
			case recipCc:
				m.Cc = append(m.Cc, addr)
			case recipBcc:
				// Bcc is intentionally dropped from the reconstructed headers.
			default: // recipTo or unspecified
				m.To = append(m.To, addr)
			}
		case strings.HasPrefix(e.name, "__attach_version1.0_"):
			asub, _ := r.collectProps(idx, 8)
			if att, ok := attachment(asub); ok {
				m.Attachments = append(m.Attachments, att)
			}
		}
	}
	return m, nil
}

// FileToMIME reads an .msg file and returns its reconstructed RFC 5322 bytes.
func FileToMIME(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("msg: read %q: %w", path, err)
	}
	m, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("msg: parse %q: %w", path, err)
	}
	return m.MIME()
}

// collectProps gathers a storage's variable-length properties (the
// __substg1.0_<tag> streams) keyed by full tag, plus its fixed-length
// properties (the 16-byte entries of __properties_version1.0 after a
// headerSize-byte header) keyed by tag to their 8-byte value.
func (r *cfbReader) collectProps(storageIdx, headerSize int) (map[uint32][]byte, map[uint32][8]byte) {
	substg := make(map[uint32][]byte)
	props := make(map[uint32][8]byte)
	for _, idx := range r.children(storageIdx) {
		e := r.entries[idx]
		switch {
		case e.objType == objStream && strings.HasPrefix(e.name, "__substg1.0_") && len(e.name) >= len("__substg1.0_")+8:
			hex := e.name[len("__substg1.0_") : len("__substg1.0_")+8]
			t, err := strconv.ParseUint(hex, 16, 32)
			if err != nil {
				continue
			}
			if data, derr := r.streamData(idx); derr == nil {
				substg[uint32(t)] = data
			}
		case e.objType == objStream && e.name == "__properties_version1.0":
			data, derr := r.streamData(idx)
			if derr != nil {
				continue
			}
			for off := headerSize; off+16 <= len(data); off += 16 {
				t := uint32(data[off]) | uint32(data[off+1])<<8 | uint32(data[off+2])<<16 | uint32(data[off+3])<<24
				var v [8]byte
				copy(v[:], data[off+8:off+16])
				props[t] = v
			}
		}
	}
	return substg, props
}

// tag composes a MAPI property tag from a property id and type.
func tag(id, typ uint16) uint32 { return uint32(id)<<16 | uint32(typ) }

// strProp returns a string property in either its unicode or string8 form.
func strProp(substg map[uint32][]byte, id uint16) string {
	if b, ok := substg[tag(id, ptUnicode)]; ok {
		return decodeUTF16(b)
	}
	if b, ok := substg[tag(id, ptString8)]; ok {
		return decodeString8(b)
	}
	return ""
}

// longProp returns a PT_LONG fixed property, or 0 when absent.
func longProp(props map[uint32][8]byte, id uint16) uint32 {
	if v, ok := props[tag(id, ptLong)]; ok {
		return uint32(v[0]) | uint32(v[1])<<8 | uint32(v[2])<<16 | uint32(v[3])<<24
	}
	return 0
}

// sysTimeProp returns a PT_SYSTIME fixed property as a time, or the zero time.
// PT_SYSTIME is a FILETIME: 100-ns ticks since 1601-01-01 UTC.
func sysTimeProp(props map[uint32][8]byte, id uint16) time.Time {
	v, ok := props[tag(id, ptSysTime)]
	if !ok {
		return time.Time{}
	}
	ft := uint64(v[0]) | uint64(v[1])<<8 | uint64(v[2])<<16 | uint64(v[3])<<24 |
		uint64(v[4])<<32 | uint64(v[5])<<40 | uint64(v[6])<<48 | uint64(v[7])<<56
	if ft == 0 {
		return time.Time{}
	}
	const ticksPerSecond = 10_000_000
	const epochDiff = 11644473600 // seconds between 1601-01-01 and 1970-01-01
	sec := int64(ft/ticksPerSecond) - epochDiff
	nsec := int64(ft%ticksPerSecond) * 100
	return time.Unix(sec, nsec).UTC()
}

// senderAddress builds the From value from the sender/sent-representing
// properties, preferring an SMTP address and a display name when present.
func senderAddress(substg map[uint32][]byte) string {
	email := firstNonEmpty(
		strProp(substg, pidSenderSMTPAddress),
		strProp(substg, pidSentReprSMTPAddress),
		strProp(substg, pidSenderEmailAddress),
		strProp(substg, pidSentReprEmailAddress),
	)
	name := firstNonEmpty(strProp(substg, pidSenderName), strProp(substg, pidSentReprName))
	return formatAddress(name, email)
}

// recipientAddress builds a recipient address from a __recip storage's props.
func recipientAddress(substg map[uint32][]byte) string {
	email := firstNonEmpty(strProp(substg, pidSMTPAddress), strProp(substg, pidEmailAddress))
	name := strProp(substg, pidDisplayName)
	return formatAddress(name, email)
}

// attachment builds an Attachment from a __attach storage's props. It reports
// false when there is no binary data to carry.
func attachment(substg map[uint32][]byte) (Attachment, bool) {
	data, ok := substg[tag(pidAttachDataBinary, ptBinary)]
	if !ok || len(data) == 0 {
		return Attachment{}, false
	}
	name := firstNonEmpty(strProp(substg, pidAttachLongFilename), strProp(substg, pidAttachFilename))
	if name == "" {
		name = "attachment.bin"
	}
	ct := strProp(substg, pidAttachMimeTag)
	if ct == "" {
		ct = "application/octet-stream"
	}
	return Attachment{
		Filename:    name,
		ContentType: ct,
		ContentID:   strProp(substg, pidAttachContentID),
		Data:        data,
	}, true
}

// formatAddress renders an RFC 5322 address, RFC 2047-encoding the display name
// when needed. An empty email yields an empty string (the field is omitted).
func formatAddress(name, email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	return (&mail.Address{Name: strings.TrimSpace(name), Address: email}).String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// decodeUTF16 decodes a little-endian UTF-16 byte slice (a PT_UNICODE value).
func decodeUTF16(b []byte) string {
	u16 := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u16 = append(u16, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return trimNUL(string(utf16.Decode(u16)))
}

// decodeString8 decodes a PT_STRING8 value: UTF-8 when valid, else Latin-1.
func decodeString8(b []byte) string {
	b = bytes.TrimRight(b, "\x00")
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}
