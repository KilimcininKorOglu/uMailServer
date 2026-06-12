package oab

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// reader walks an OAB binary file the way a client would, so tests can assert
// the on-the-wire structure rather than re-using the encoder.
type reader struct {
	t   *testing.T
	b   []byte
	off int
}

func (r *reader) u8() uint8 {
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u32() uint32 {
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) str() string {
	i := bytes.IndexByte(r.b[r.off:], 0)
	if i < 0 {
		r.t.Fatalf("unterminated string at offset %d", r.off)
	}
	s := string(r.b[r.off : r.off+i])
	r.off += i + 1
	return s
}

func (r *reader) varui() uint32 {
	b := r.u8()
	if b <= 0x7F {
		return uint32(b)
	}
	n := int(b - 0x80)
	var v uint32
	for i := range n {
		v |= uint32(r.u8()) << (8 * i)
	}
	return v
}

// TestFullDetailsHeader verifies the OAB_HDR fields and that ulSerial is the
// running CRC32 of the body (MS-OXOAB §2.9.1).
func TestFullDetailsHeader(t *testing.T) {
	recs := []Record{
		{X500DN: "/o=org/cn=a", SMTP: "a@x.test", DisplayName: "Alice", ObjectType: 6, DisplayType: 0},
		{X500DN: "/o=org/cn=b", SMTP: "b@x.test", DisplayName: "Bob", ObjectType: 6, DisplayType: 0},
	}
	data := BuildFullDetails(recs, 42, "container-guid", "/")

	if v := binary.LittleEndian.Uint32(data[0:]); v != Version4 {
		t.Errorf("ulVersion = %#x, want %#x", v, Version4)
	}
	if v := binary.LittleEndian.Uint32(data[8:]); v != uint32(len(recs)) {
		t.Errorf("ulTotRecs = %d, want %d", v, len(recs))
	}
	// The OAB CRC is the running CRC: the standard IEEE CRC32 without its final
	// XOR. Validating against the stdlib keeps the check independent.
	serial := binary.LittleEndian.Uint32(data[4:])
	if want := crc32.ChecksumIEEE(data[12:]) ^ 0xFFFFFFFF; serial != want {
		t.Errorf("ulSerial = %#x, want %#x", serial, want)
	}
}

// TestFullDetailsSchema verifies the OAB_META_DATA property tables carry the
// exact header and object schemas with the canonical tags and flags
// (MS-OXOAB §2.9.2).
func TestFullDetailsSchema(t *testing.T) {
	data := BuildFullDetails(nil, 1, "g", "/")
	r := &reader{t: t, b: data, off: 12} // skip OAB_HDR

	metaSize := r.u32()

	hdrCount := r.u32()
	if hdrCount != 4 {
		t.Fatalf("header prop count = %d, want 4", hdrCount)
	}
	wantHdr := []wire.PropTag{
		wire.PidTagOfflineAddressBookName,
		wire.PidTagOfflineAddressBookDistinguishedName,
		wire.PidTagOfflineAddressBookSequence,
		wire.PidTagOfflineAddressBookContainerGuid,
	}
	for i, want := range wantHdr {
		if tag := wire.PropTag(r.u32()); tag != want {
			t.Errorf("header prop %d tag = %#x, want %#x", i, tag, want)
		}
		if flags := r.u32(); flags != 0 {
			t.Errorf("header prop %d flags = %d, want 0", i, flags)
		}
	}

	objCount := r.u32()
	if objCount != 14 {
		t.Fatalf("object prop count = %d, want 14", objCount)
	}
	wantObj := []schemaProp{
		{wire.PidTagEmailAddress, 2},
		{wire.PidTagSmtpAddress, 2},
		{wire.PidTagDisplayName, 1},
		{wire.PidTagObjectType, 0},
		{wire.PidTagDisplayType, 0},
		{wire.PidTagDisplayTypeEx, 0},
		{wire.PidTagGivenName, 0},
		{wire.PidTagSurname, 0},
		{wire.PidTagTitle, 0},
		{wire.PidTagDepartmentName, 0},
		{wire.PidTagCompanyName, 0},
		{wire.PidTagOfficeLocation, 0},
		{wire.PidTagBusinessTelephone, 0},
		{wire.PidTagOfflineAddressBookTruncatedProperties, 0},
	}
	for i, want := range wantObj {
		if tag := wire.PropTag(r.u32()); tag != want.tag {
			t.Errorf("object prop %d tag = %#x, want %#x", i, tag, want.tag)
		}
		if flags := r.u32(); flags != want.flags {
			t.Errorf("object prop %d flags = %d, want %d", i, flags, want.flags)
		}
	}

	// OAB_META_DATA cbSize is self-inclusive (MS-OXOAB §2.9.5): from the size
	// field at offset 12 to the current offset.
	if int(metaSize) != r.off-12 {
		t.Errorf("meta cbSize = %d, want %d", metaSize, r.off-12)
	}
}

// TestHeaderRecord verifies the single header record: a full presence array and
// the four header values in schema order (MS-OXOAB §2.9.4).
func TestHeaderRecord(t *testing.T) {
	data := BuildFullDetails(nil, 0x1234, "the-guid", "/")
	r := &reader{t: t, b: data, off: 12}
	metaSize := r.u32()
	r.off = 12 + int(metaSize) // jump past meta-data to the header record

	recSize := r.u32()
	recStart := r.off - 4
	if p := r.u8(); p != 0xF0 {
		t.Errorf("header presence = %#x, want 0xF0", p)
	}
	if s := r.str(); s != GALName {
		t.Errorf("header name = %q, want %q", s, GALName)
	}
	if s := r.str(); s != "/" {
		t.Errorf("header dn = %q, want \"/\"", s)
	}
	if v := r.varui(); v != 0x1234 {
		t.Errorf("header sequence = %#x, want 0x1234", v)
	}
	if s := r.str(); s != "the-guid" {
		t.Errorf("header guid = %q, want \"the-guid\"", s)
	}
	if int(recSize) != r.off-recStart {
		t.Errorf("header record cbSize = %d, want %d", recSize, r.off-recStart)
	}
}

// TestObjectRecord verifies an object record's presence bits and value order:
// EMAIL/SMTP/DISPLAY present, the two type properties unconditional, all
// optional string properties absent (MS-OXOAB §2.9.4, §2.9.6.3).
func TestObjectRecord(t *testing.T) {
	rec := Record{X500DN: "/o=org/cn=a", SMTP: "a@x.test", DisplayName: "Alice", ObjectType: 6, DisplayType: 0}
	data := BuildFullDetails([]Record{rec}, 1, "g", "/")

	// Walk to the object record: skip OAB_HDR, meta-data, and the header record.
	r := &reader{t: t, b: data, off: 12}
	r.off = 12 + int(r.u32()) // past meta-data
	hdrSize := binary.LittleEndian.Uint32(data[r.off:])
	r.off += int(hdrSize) // past header record

	recSize := r.u32()
	recStart := r.off - 4
	// EMAIL|SMTP|DISPLAY|OBJTYPE|DISPTYPE present -> 0x80|0x40|0x20|0x10|0x08.
	if p0 := r.u8(); p0 != 0xF8 {
		t.Errorf("object presence[0] = %#x, want 0xF8", p0)
	}
	if p1 := r.u8(); p1 != 0x00 {
		t.Errorf("object presence[1] = %#x, want 0x00", p1)
	}
	if s := r.str(); s != rec.X500DN {
		t.Errorf("email = %q, want %q", s, rec.X500DN)
	}
	if s := r.str(); s != rec.SMTP {
		t.Errorf("smtp = %q, want %q", s, rec.SMTP)
	}
	if s := r.str(); s != rec.DisplayName {
		t.Errorf("display = %q, want %q", s, rec.DisplayName)
	}
	if v := r.varui(); v != rec.ObjectType {
		t.Errorf("object type = %d, want %d", v, rec.ObjectType)
	}
	if v := r.varui(); v != rec.DisplayType {
		t.Errorf("display type = %d, want %d", v, rec.DisplayType)
	}
	if int(recSize) != r.off-recStart {
		t.Errorf("object record cbSize = %d, want %d", recSize, r.off-recStart)
	}
}

// TestVarui verifies the variable-length integer encoding (MS-OXOAB §2.9.6.1).
func TestVarui(t *testing.T) {
	cases := []struct {
		v    uint32
		want []byte
	}{
		{0x00, []byte{0x00}},
		{0x7F, []byte{0x7F}},
		{0x80, []byte{0x81, 0x80}},
		{0xFF, []byte{0x81, 0xFF}},
		{0x1234, []byte{0x82, 0x34, 0x12}},
		{0x123456, []byte{0x83, 0x56, 0x34, 0x12}},
		{0x12345678, []byte{0x84, 0x78, 0x56, 0x34, 0x12}},
	}
	for _, c := range cases {
		var w writer
		w.varui(c.v)
		if !bytes.Equal(w.bytes(), c.want) {
			t.Errorf("varui(%#x) = % x, want % x", c.v, w.bytes(), c.want)
		}
	}
}

// TestTemplate verifies the minimal display-template file's header and size
// (MS-OXOAB §2.2): 12-byte header + 7×32 template entries + 16-byte names
// struct + 4-byte address-template count = 256 bytes.
func TestTemplate(t *testing.T) {
	data := BuildTemplate()
	if len(data) != 12+7*32+16+4 {
		t.Fatalf("template size = %d, want %d", len(data), 12+7*32+16+4)
	}
	if v := binary.LittleEndian.Uint32(data[0:]); v != TemplateVersion {
		t.Errorf("template version = %#x, want %#x", v, TemplateVersion)
	}
	if v := binary.LittleEndian.Uint32(data[4:]); v != 0 {
		t.Errorf("template serial = %d, want 0", v)
	}
	if v := binary.LittleEndian.Uint32(data[8:]); v != 0 {
		t.Errorf("template totrecs = %d, want 0", v)
	}
}
