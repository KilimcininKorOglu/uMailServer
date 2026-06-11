package msg

import (
	"encoding/binary"
	"net/mail"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// ---------------------------------------------------------------------------
// A small OXMSG builder for tests. It lays out a v3 compound file whose streams
// all live in the mini stream (the dominant .msg shape) and supports nested
// storages, so a message with recipient/attachment sub-storages can be built
// and round-tripped through Parse. Independent confirmation that the bytes are
// genuine OXMSG comes from the probe (an external reader parses the fixture).
// ---------------------------------------------------------------------------

type tNode struct {
	name     string
	storage  bool
	data     []byte // streams only
	children []*tNode
	idx      int
	right    uint32
	start    uint32 // starting mini-sector (streams)
}

func tStream(name string, data []byte) *tNode { return &tNode{name: name, data: data} }
func tStorage(name string, kids ...*tNode) *tNode {
	return &tNode{name: name, storage: true, children: kids}
}

// cfbLess orders directory siblings as CFBF requires: by name length, then by
// uppercased name.
func cfbLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return strings.ToUpper(a) < strings.ToUpper(b)
}

// buildMSG serializes top-level nodes into an OXMSG .msg byte slice.
func buildMSG(topLevel ...*tNode) []byte {
	root := &tNode{name: "Root Entry", storage: true, children: topLevel}

	var entries []*tNode
	var assign func(n *tNode)
	assign = func(n *tNode) {
		n.idx = len(entries)
		n.right = noStream
		entries = append(entries, n)
		sort.Slice(n.children, func(i, j int) bool { return cfbLess(n.children[i].name, n.children[j].name) })
		for _, c := range n.children {
			assign(c)
		}
		for i := 0; i+1 < len(n.children); i++ {
			n.children[i].right = uint32(n.children[i+1].idx)
		}
	}
	assign(root)

	const miniSize = 64
	var miniStream []byte
	var miniFAT []uint32
	for _, n := range entries {
		if n.storage {
			continue
		}
		n.start = uint32(len(miniStream) / miniSize)
		sectors := (len(n.data) + miniSize - 1) / miniSize
		if sectors == 0 {
			sectors = 1
		}
		for s := 0; s < sectors; s++ {
			if s == sectors-1 {
				miniFAT = append(miniFAT, endOfChain)
			} else {
				miniFAT = append(miniFAT, uint32(len(miniFAT)+1))
			}
		}
		padded := make([]byte, sectors*miniSize)
		copy(padded, n.data)
		miniStream = append(miniStream, padded...)
	}

	const sectorSize = 512
	dirSectors := (len(entries)*128 + sectorSize - 1) / sectorSize
	miniStreamSectors := (len(miniStream) + sectorSize - 1) / sectorSize
	miniFATSectors := (len(miniFAT)*4 + sectorSize - 1) / sectorSize
	if miniFATSectors == 0 {
		miniFATSectors = 1
	}
	dirStart, miniStart := 0, dirSectors
	miniFATStart := miniStart + miniStreamSectors
	fatStart := miniFATStart + miniFATSectors
	totalSectors := fatStart + 1

	buf := make([]byte, sectorSize+totalSectors*sectorSize)
	sectorAt := func(n int) []byte { return buf[sectorSize+n*sectorSize : sectorSize+(n+1)*sectorSize] }

	copy(buf[0:8], cfbSig)
	binary.LittleEndian.PutUint16(buf[26:28], 3)
	binary.LittleEndian.PutUint16(buf[28:30], 0xFFFE)
	binary.LittleEndian.PutUint16(buf[30:32], 9)
	binary.LittleEndian.PutUint16(buf[32:34], 6)
	binary.LittleEndian.PutUint32(buf[44:48], 1)
	binary.LittleEndian.PutUint32(buf[48:52], uint32(dirStart))
	binary.LittleEndian.PutUint32(buf[56:60], 4096)
	binary.LittleEndian.PutUint32(buf[60:64], uint32(miniFATStart))
	binary.LittleEndian.PutUint32(buf[64:68], uint32(miniFATSectors))
	binary.LittleEndian.PutUint32(buf[68:72], endOfChain)
	binary.LittleEndian.PutUint32(buf[72:76], 0)
	binary.LittleEndian.PutUint32(buf[76:80], uint32(fatStart))
	for i := 1; i < 109; i++ {
		binary.LittleEndian.PutUint32(buf[76+i*4:80+i*4], freeSect)
	}

	fat := make([]uint32, totalSectors)
	for i := range fat {
		fat[i] = freeSect
	}
	chain := func(start, count int) {
		for i := 0; i < count; i++ {
			if i == count-1 {
				fat[start+i] = endOfChain
			} else {
				fat[start+i] = uint32(start + i + 1)
			}
		}
	}
	chain(dirStart, dirSectors)
	chain(miniStart, miniStreamSectors)
	chain(miniFATStart, miniFATSectors)
	fat[fatStart] = fatSect
	fatSec := sectorAt(fatStart)
	for i, v := range fat {
		binary.LittleEndian.PutUint32(fatSec[i*4:i*4+4], v)
	}

	for i := 0; i < miniFATSectors*sectorSize/4; i++ {
		v := uint32(freeSect)
		if i < len(miniFAT) {
			v = miniFAT[i]
		}
		sec, off := miniFATStart+(i*4)/sectorSize, (i*4)%sectorSize
		binary.LittleEndian.PutUint32(sectorAt(sec)[off:off+4], v)
	}

	copy(buf[sectorSize+miniStart*sectorSize:], miniStream)

	dirBase := sectorSize + dirStart*sectorSize
	for _, n := range entries {
		writeNodeEntry(buf[dirBase+n.idx*128:dirBase+n.idx*128+128], n)
	}
	// The root entry's stream IS the mini-stream container.
	rootEntry := buf[dirBase : dirBase+128]
	binary.LittleEndian.PutUint32(rootEntry[116:120], uint32(miniStart))
	binary.LittleEndian.PutUint64(rootEntry[120:128], uint64(len(miniStream)))
	return buf
}

// writeNodeEntry encodes one built node's 128-byte directory entry.
func writeNodeEntry(e []byte, n *tNode) {
	u16 := utf16.Encode([]rune(n.name))
	for i, c := range u16 {
		binary.LittleEndian.PutUint16(e[i*2:i*2+2], c)
	}
	binary.LittleEndian.PutUint16(e[64:66], uint16(len(u16)*2+2))
	child := uint32(noStream)
	objType := byte(objStream)
	var startSect uint32
	var size uint64
	if n.storage {
		objType = objStorage
		if n.idx == 0 {
			objType = objRoot // root's startSect/size are patched by buildMSG
		}
		if len(n.children) > 0 {
			child = uint32(n.children[0].idx)
		}
	} else {
		startSect = n.start
		size = uint64(len(n.data))
	}
	binary.LittleEndian.PutUint32(e[68:72], noStream) // left
	binary.LittleEndian.PutUint32(e[72:76], n.right)
	binary.LittleEndian.PutUint32(e[76:80], child)
	binary.LittleEndian.PutUint32(e[116:120], startSect)
	binary.LittleEndian.PutUint64(e[120:128], size)
	e[66] = objType
}

func substgName(id, typ uint16) string { return "__substg1.0_" + upperHex(uint32(id)<<16|uint32(typ)) }

func upperHex(v uint32) string {
	const digits = "0123456789ABCDEF"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = digits[v&0xF]
		v >>= 4
	}
	return string(out)
}

func uniStream(id uint16, s string) *tNode {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:i*2+2], c)
	}
	return tStream(substgName(id, ptUnicode), b)
}

// recipProps builds a __properties_version1.0 stream (8-byte storage header)
// setting PidTagRecipientType.
func recipProps(t uint32) *tNode {
	b := make([]byte, 8+16)
	binary.LittleEndian.PutUint32(b[8:12], tag(pidRecipientType, ptLong))
	binary.LittleEndian.PutUint32(b[16:20], t)
	return tStream("__properties_version1.0", b)
}

// buildSampleMSG builds a representative .msg: subject, plain body, sender, one
// To recipient, and one attachment. Reused by Parse tests and the fixture.
func buildSampleMSG() []byte {
	return buildMSG(
		uniStream(pidSubject, "Quarterly numbers"),
		uniStream(pidBody, "Please review the attached figures."),
		uniStream(pidSenderName, "Boss Person"),
		uniStream(pidSenderSMTPAddress, "boss@corp.example"),
		tStorage("__recip_version1.0_#00000000",
			uniStream(pidDisplayName, "Alice"),
			uniStream(pidSMTPAddress, "alice@local.test"),
			recipProps(recipTo),
		),
		tStorage("__attach_version1.0_#00000000",
			uniStream(pidAttachLongFilename, "report.txt"),
			uniStream(pidAttachMimeTag, "text/plain"),
			tStream(substgName(pidAttachDataBinary, ptBinary), []byte("column,total\nQ3,42\n")),
		),
	)
}

func TestParseMSG_Fields(t *testing.T) {
	m, err := Parse(buildSampleMSG())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Subject != "Quarterly numbers" {
		t.Errorf("Subject = %q", m.Subject)
	}
	if !strings.Contains(m.BodyText, "attached figures") {
		t.Errorf("BodyText = %q", m.BodyText)
	}
	if !strings.Contains(m.From, "boss@corp.example") || !strings.Contains(m.From, "Boss Person") {
		t.Errorf("From = %q", m.From)
	}
	if len(m.To) != 1 || !strings.Contains(m.To[0], "alice@local.test") {
		t.Errorf("To = %v", m.To)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(m.Attachments))
	}
	if m.Attachments[0].Filename != "report.txt" || string(m.Attachments[0].Data) != "column,total\nQ3,42\n" {
		t.Errorf("attachment = %+v", m.Attachments[0])
	}
}

func TestMessageMIME_RoundTrip(t *testing.T) {
	mime, err := Parse(buildSampleMSG())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	raw, err := mime.MIME()
	if err != nil {
		t.Fatalf("MIME: %v", err)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("re-parse MIME: %v\n%s", err, raw)
	}
	if got := parsed.Header.Get("Subject"); got != "Quarterly numbers" {
		t.Errorf("Subject header = %q", got)
	}
	if !strings.Contains(parsed.Header.Get("To"), "alice@local.test") {
		t.Errorf("To header = %q", parsed.Header.Get("To"))
	}
	ct := parsed.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/mixed") {
		t.Errorf("Content-Type = %q, want multipart/mixed (has attachment)", ct)
	}
	if !strings.Contains(string(raw), "report.txt") {
		t.Error("attachment filename missing from MIME")
	}
}

func TestMessageMIME_AlternativeNoAttachment(t *testing.T) {
	m := &Message{
		From:     "a@b.test",
		Subject:  "hi",
		BodyText: "plain version",
		BodyHTML: []byte("<p>html version</p>"),
		Date:     time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC),
	}
	raw, err := m.MIME()
	if err != nil {
		t.Fatalf("MIME: %v", err)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if ct := parsed.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/alternative") {
		t.Errorf("Content-Type = %q, want multipart/alternative", ct)
	}
}

func TestDecoders(t *testing.T) {
	if got := decodeUTF16(utf16LE("héllo")); got != "héllo" {
		t.Errorf("decodeUTF16 = %q", got)
	}
	if got := decodeString8([]byte("ascii\x00")); got != "ascii" {
		t.Errorf("decodeString8 = %q", got)
	}
	// FILETIME for 1970-01-01T00:00:00Z is the epoch difference in 100-ns ticks.
	props := map[uint32][8]byte{}
	var v [8]byte
	binary.LittleEndian.PutUint64(v[:], 11644473600*10_000_000)
	props[tag(pidClientSubmitTime, ptSysTime)] = v
	if got := sysTimeProp(props, pidClientSubmitTime); got.Unix() != 0 {
		t.Errorf("sysTimeProp = %v (unix %d), want unix 0", got, got.Unix())
	}
}

func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:i*2+2], c)
	}
	return b
}
