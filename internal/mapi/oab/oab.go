// Package oab builds Offline Address Book (OAB) version 4 files and the
// accompanying manifest for Outlook clients (MS-OXOAB, MS-OXWOAB).
//
// An OAB consists of an uncompressed binary "Full Details" file (a header
// record plus one record per GAL entry), an LZX-compressed wrapper around it,
// a minimal display-template file, and an XML manifest that ties them together
// with sizes and SHA-1 digests. This package produces the uncompressed binary
// forms and the manifest; the LZX wrapper lives in internal/mapi/lzx.
package oab

import "github.com/umailserver/umailserver/internal/mapi/wire"

// File-format versions (MS-OXOAB §2.9.1, §2.2.1).
const (
	// Version4 is the OAB v4 Full Details file version (ulVersion).
	Version4 uint32 = 0x20
	// TemplateVersion is the display-template file version.
	TemplateVersion uint32 = 0x07
)

// schemaProp is one entry of an OAB_PROP_TABLE: a property tag and its
// attribute flags (MS-OXOAB §2.9.2). The flags are a fixed part of the schema
// the client reads to interpret each record.
type schemaProp struct {
	tag   wire.PropTag
	flags uint32
}

// headerSchema describes the single header record (MS-OXOAB §2.9.3).
var headerSchema = []schemaProp{
	{wire.PidTagOfflineAddressBookName, 0},
	{wire.PidTagOfflineAddressBookDistinguishedName, 0},
	{wire.PidTagOfflineAddressBookSequence, 0},
	{wire.PidTagOfflineAddressBookContainerGuid, 0},
}

// objectSchema describes every GAL object record (MS-OXOAB §2.9.3). The order
// is significant: record values are written in this order, gated by a presence
// bit array. The flags mirror the canonical Exchange OAB schema.
var objectSchema = []schemaProp{
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

// GALName is the distinguished display name of the Global Address List, stored
// in the OAB header record and echoed in the manifest.
const GALName = "\\Global Address List"

// Record is one GAL entry's OAB object record (MS-OXOAB §2.9.4). Empty string
// fields are absent: they are flagged off in the presence bit array and not
// serialized (MS-OXOAB §2.9.6.3). ObjectType and DisplayType are always
// written.
type Record struct {
	X500DN      string // PidTagEmailAddress: the EX distinguished name
	SMTP        string // PidTagSmtpAddress
	DisplayName string // PidTagDisplayName
	ObjectType  uint32 // PidTagObjectType (MAPI_MAILUSER / MAPI_DISTLIST)
	DisplayType uint32 // PidTagDisplayType (DT_MAILUSER / DT_DISTLIST)

	GivenName  string
	Surname    string
	Title      string
	Department string
	Company    string
	Office     string
	Phone      string

	DisplayTypeEx    uint32 // PidTagDisplayTypeEx, written only when present
	HasDisplayTypeEx bool
}

// BuildFullDetails serializes the uncompressed OAB v4 Full Details file
// (MS-OXOAB §2.9) for records, using the given sequence number, container GUID
// string, and address-list distinguished name. The result is the input to the
// LZX wrapper.
func BuildFullDetails(records []Record, sequence uint32, containerGUID, oabDN string) []byte {
	var w writer

	// OAB_HDR (§2.9.1): version, serial (CRC32 of the body, patched last),
	// total record count (excluding the header record).
	w.u32(Version4)
	serialOff := w.size()
	w.u32(0)
	w.u32(uint32(len(records)))

	// OAB_META_DATA (§2.9.2): the header and object property schemas, framed
	// by a self-inclusive size.
	metaOff := w.beginRecord()
	w.u32(uint32(len(headerSchema)))
	for _, p := range headerSchema {
		w.u32(uint32(p.tag))
		w.u32(p.flags)
	}
	w.u32(uint32(len(objectSchema)))
	for _, p := range objectSchema {
		w.u32(uint32(p.tag))
		w.u32(p.flags)
	}
	w.endRecord(metaOff)

	// Header record (§2.9.4): all four header properties are present, so the
	// one-byte presence array is 0xF0 (the four high bits set).
	hdrOff := w.beginRecord()
	w.u8(0xF0)
	w.str(GALName)
	w.str(oabDN)
	w.varui(sequence)
	w.str(containerGUID)
	w.endRecord(hdrOff)

	// One object record per GAL entry.
	for i := range records {
		writeObjectRecord(&w, &records[i])
	}

	// Patch ulSerial with the running CRC32 of everything after the 12-byte
	// OAB_HDR (§2.9.1).
	w.patch(serialOff, crc32OAB(w.bytes()[12:]))
	return w.bytes()
}

// writeObjectRecord serializes one OAB_V4_REC: a 2-byte presence bit array
// (MSB of the first byte is property 0) followed by the present values in
// schema order. ObjectType and DisplayType are unconditional.
func writeObjectRecord(w *writer, r *Record) {
	off := w.beginRecord()

	var p0, p1 uint8
	if r.X500DN != "" {
		p0 |= 0x80 // PidTagEmailAddress
	}
	if r.SMTP != "" {
		p0 |= 0x40 // PidTagSmtpAddress
	}
	if r.DisplayName != "" {
		p0 |= 0x20 // PidTagDisplayName
	}
	p0 |= 0x10 // PidTagObjectType (always)
	p0 |= 0x08 // PidTagDisplayType (always)
	if r.HasDisplayTypeEx {
		p0 |= 0x04 // PidTagDisplayTypeEx
	}
	if r.GivenName != "" {
		p0 |= 0x02 // PidTagGivenName
	}
	if r.Surname != "" {
		p0 |= 0x01 // PidTagSurname
	}
	if r.Title != "" {
		p1 |= 0x80 // PidTagTitle
	}
	if r.Department != "" {
		p1 |= 0x40 // PidTagDepartmentName
	}
	if r.Company != "" {
		p1 |= 0x20 // PidTagCompanyName
	}
	if r.Office != "" {
		p1 |= 0x10 // PidTagOfficeLocation
	}
	if r.Phone != "" {
		p1 |= 0x08 // PidTagBusinessTelephone
	}
	// PidTagOfflineAddressBookTruncatedProperties is always absent.

	w.u8(p0)
	w.u8(p1)

	if p0&0x80 != 0 {
		w.str(r.X500DN)
	}
	if p0&0x40 != 0 {
		w.str(r.SMTP)
	}
	if p0&0x20 != 0 {
		w.str(r.DisplayName)
	}
	w.varui(r.ObjectType)
	w.varui(r.DisplayType)
	if p0&0x04 != 0 {
		w.varui(r.DisplayTypeEx)
	}
	if p0&0x02 != 0 {
		w.str(r.GivenName)
	}
	if p0&0x01 != 0 {
		w.str(r.Surname)
	}
	if p1&0x80 != 0 {
		w.str(r.Title)
	}
	if p1&0x40 != 0 {
		w.str(r.Department)
	}
	if p1&0x20 != 0 {
		w.str(r.Company)
	}
	if p1&0x10 != 0 {
		w.str(r.Office)
	}
	if p1&0x08 != 0 {
		w.str(r.Phone)
	}

	w.endRecord(off)
}

// BuildTemplate serializes a minimal OAB display-template file (MS-OXOAB §2.2).
// It carries no template entries or named properties; clients fall back to
// their built-in templates, but the manifest still requires the file to exist
// (MS-OXWOAB).
func BuildTemplate() []byte {
	var w writer

	// OAB_HDR: template version, serial 0, total records 0.
	w.u32(TemplateVersion)
	w.u32(0)
	w.u32(0)

	// Seven empty TMPLT_ENTRY structures of 32 bytes each.
	for range 7 * (32 / 4) {
		w.u32(0)
	}

	// NAMES_STRUCT: no named properties.
	w.u8(0)
	w.u8(0) // cIDsNames
	w.u8(0)
	w.u8(0)  // cGuids
	w.u32(0) // oIDs
	w.u32(0) // oGuids
	w.u32(0) // oNames

	// Address-template count.
	w.u32(0)
	return w.bytes()
}
