package wire

import "strings"

// MuidEMSAB is the address-book (EMSAB) provider GUID that prefixes every
// permanent address-book entry ID (MS-OXNSPI 2.2.4 / 2.3.8.2).
var MuidEMSAB = GUID{
	TimeLow:          0xC840A7DC,
	TimeMid:          0x42C0,
	TimeHiAndVersion: 0x1A10,
	ClockSeq:         [2]byte{0xB4, 0xB9},
	Node:             [6]byte{0x08, 0x00, 0x2B, 0x2F, 0xE1, 0x82},
}

// Address-book entry ID type bytes (MS-OXNSPI 2.3.8).
const (
	ephemeralIDType = 0x87 // EphemeralEntryID first byte
	permanentIDType = 0x00 // PermanentEntryID first byte
)

// Folder/message entry-id store types (MS-OXCDATA 2.2.4.1/2.2.4.2).
const (
	EIDTypePrivateFolder  uint16 = 0x0001
	EIDTypePublicFolder   uint16 = 0x0003
	EIDTypePrivateMessage uint16 = 0x0007
	EIDTypePublicMessage  uint16 = 0x0009
)

// FolderEntryID is a 46-byte folder entry ID (MS-OXCDATA 2.2.4.1).
type FolderEntryID struct {
	Flags         uint32
	ProviderUID   GUID
	FolderType    uint16
	DatabaseGUID  GUID
	GlobalCounter [6]byte
	Pad           [2]byte
}

// Push serializes the folder entry ID.
func (e FolderEntryID) Push(p *Push) {
	p.Uint32(e.Flags)
	p.GUID(e.ProviderUID)
	p.Uint16(e.FolderType)
	p.GUID(e.DatabaseGUID)
	p.Raw(e.GlobalCounter[:])
	p.Raw(e.Pad[:])
}

// Bytes returns the folder entry ID as a standalone byte slice (for a
// PidTagEntryID property value).
func (e FolderEntryID) Bytes() []byte {
	p := NewPush(0)
	e.Push(p)
	return p.Bytes()
}

// PullFolderEntryID deserializes a folder entry ID.
func PullFolderEntryID(p *Pull) FolderEntryID {
	var e FolderEntryID
	e.Flags = p.Uint32()
	e.ProviderUID = p.GUID()
	e.FolderType = p.Uint16()
	e.DatabaseGUID = p.GUID()
	copy(e.GlobalCounter[:], p.Bytes(6))
	copy(e.Pad[:], p.Bytes(2))
	return e
}

// MessageEntryID is a 70-byte message entry ID (MS-OXCDATA 2.2.4.2).
type MessageEntryID struct {
	Flags                uint32
	ProviderUID          GUID
	MessageType          uint16
	FolderDatabaseGUID   GUID
	FolderGlobalCounter  [6]byte
	Pad1                 [2]byte
	MessageDatabaseGUID  GUID
	MessageGlobalCounter [6]byte
	Pad2                 [2]byte
}

// Push serializes the message entry ID.
func (e MessageEntryID) Push(p *Push) {
	p.Uint32(e.Flags)
	p.GUID(e.ProviderUID)
	p.Uint16(e.MessageType)
	p.GUID(e.FolderDatabaseGUID)
	p.Raw(e.FolderGlobalCounter[:])
	p.Raw(e.Pad1[:])
	p.GUID(e.MessageDatabaseGUID)
	p.Raw(e.MessageGlobalCounter[:])
	p.Raw(e.Pad2[:])
}

// Bytes returns the message entry ID as a standalone byte slice.
func (e MessageEntryID) Bytes() []byte {
	p := NewPush(0)
	e.Push(p)
	return p.Bytes()
}

// PullMessageEntryID deserializes a message entry ID.
func PullMessageEntryID(p *Pull) MessageEntryID {
	var e MessageEntryID
	e.Flags = p.Uint32()
	e.ProviderUID = p.GUID()
	e.MessageType = p.Uint16()
	e.FolderDatabaseGUID = p.GUID()
	copy(e.FolderGlobalCounter[:], p.Bytes(6))
	copy(e.Pad1[:], p.Bytes(2))
	e.MessageDatabaseGUID = p.GUID()
	copy(e.MessageGlobalCounter[:], p.Bytes(6))
	copy(e.Pad2[:], p.Bytes(2))
	return e
}

// PermanentEntryID is an address-book permanent entry ID (MS-OXNSPI 2.3.8.2): a
// stable, DN-based identity returned in address-book rows and resolved names.
type PermanentEntryID struct {
	DisplayType uint32
	X500DN      string
}

// Push serializes the permanent entry ID.
func (e PermanentEntryID) Push(p *Push) {
	p.Uint32(permanentIDType) // ID type 0x00 + 3 reserved bytes
	p.GUID(MuidEMSAB)
	p.Uint32(1) // R4, constant
	p.Uint32(e.DisplayType)
	p.Str(e.X500DN) // ASCII, NUL-terminated
}

// Bytes returns the permanent entry ID as a standalone byte slice.
func (e PermanentEntryID) Bytes() []byte {
	p := NewPush(0)
	e.Push(p)
	return p.Bytes()
}

// EphemeralEntryID is an address-book ephemeral entry ID (MS-OXNSPI 2.3.8.1): a
// session-scoped identity keyed by the minimal id (MId) under the NSPI server's
// per-session provider GUID.
type EphemeralEntryID struct {
	ProviderUID GUID // the NSPI session server GUID returned by Bind
	DisplayType uint32
	Mid         uint32
}

// Push serializes the ephemeral entry ID.
func (e EphemeralEntryID) Push(p *Push) {
	p.Uint32(ephemeralIDType) // ID type 0x87 + 3 reserved bytes
	p.GUID(e.ProviderUID)
	p.Uint32(1) // R4, constant
	p.Uint32(e.DisplayType)
	p.Uint32(e.Mid)
}

// Bytes returns the ephemeral entry ID as a standalone byte slice.
func (e EphemeralEntryID) Bytes() []byte {
	p := NewPush(0)
	e.Push(p)
	return p.Bytes()
}

// PullPermanentEntryID deserializes a permanent entry ID, validating the
// constant ID type and provider GUID.
func PullPermanentEntryID(p *Pull) (PermanentEntryID, error) {
	if p.Uint32() != permanentIDType {
		if p.err == nil {
			p.err = ErrFormat
		}
		return PermanentEntryID{}, p.err
	}
	if g := p.GUID(); g != MuidEMSAB {
		if p.err == nil {
			p.err = ErrFormat
		}
		return PermanentEntryID{}, p.err
	}
	_ = p.Uint32() // R4, constant 1
	dt := p.Uint32()
	dn := p.Str()
	return PermanentEntryID{DisplayType: dt, X500DN: dn}, p.err
}

// PullEphemeralEntryID deserializes an ephemeral entry ID, validating the
// constant ID type.
func PullEphemeralEntryID(p *Pull) (EphemeralEntryID, error) {
	if p.Uint32() != ephemeralIDType {
		if p.err == nil {
			p.err = ErrFormat
		}
		return EphemeralEntryID{}, p.err
	}
	g := p.GUID()
	_ = p.Uint32() // R4, constant 1
	dt := p.Uint32()
	mid := p.Uint32()
	return EphemeralEntryID{ProviderUID: g, DisplayType: dt, Mid: mid}, p.err
}

// essdnPrefix is the X.500 legacy-DN prefix the organization advertises; the
// local part of a mailbox is appended to form its essdn. It matches the
// Autodiscover LegacyDN so a client resolves the same identity across surfaces.
const essdnPrefix = "/o=uMailServer/ou=Exchange Administrative Group (FYDIBOHF23SPDLT)/cn=Recipients/cn="

// BuildESSDN returns the Exchange legacy distinguished name (essdn) for a
// mailbox local part.
func BuildESSDN(localPart string) string {
	return essdnPrefix + localPart
}

// ParseESSDN extracts the mailbox local part from an essdn, matching the
// "/cn=Recipients/cn=" segment case-insensitively (Exchange DNs are
// case-insensitive). It reports ok=false if the DN does not have that shape.
func ParseESSDN(dn string) (localPart string, ok bool) {
	const marker = "/cn=recipients/cn="
	lower := strings.ToLower(dn)
	idx := strings.LastIndex(lower, marker)
	if idx < 0 {
		return "", false
	}
	local := dn[idx+len(marker):]
	if local == "" {
		return "", false
	}
	return local, true
}
