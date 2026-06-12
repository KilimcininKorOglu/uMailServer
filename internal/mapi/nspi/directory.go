package nspi

import (
	"strings"

	"github.com/umailserver/umailserver/internal/mapi/wire"
)

// Directory is the GAL source the address-book operations read. ResolveGAL with
// an empty entry returns the full GAL; with a name it returns matches. It
// surfaces the same data the EWS ResolveNames and webmail GAL surfaces serve and
// already filters hidden entries and tenancy boundaries.
type Directory interface {
	ResolveGAL(entry string) []DirectoryEntry
}

// DirectoryEntry is one GAL entry.
type DirectoryEntry struct {
	Email       string
	DisplayName string
	ObjectClass string // "User", "DistributionList", "Room", "Equipment", "Contact"
}

// Address-book minimal-id markers (MS-OXNSPI 2.2.8) and the base above them where
// real entry ids begin, so an entry id never collides with a marker.
const (
	midEnd  uint32 = 0x00000002
	midBase uint32 = 0x00000010
)

// Display and object types (MS-OXCDATA 2.11.1.5 / MS-OXNSPI).
const (
	dtMailUser  uint32 = 0 // DT_MAILUSER
	dtDistList  uint32 = 1 // DT_DISTLIST
	objMailUser uint32 = 6 // MAPI_MAILUSER
	objDistList uint32 = 8 // MAPI_DISTLIST
)

// entryMid returns the minimal id of the GAL entry at the given table position.
func entryMid(index int) uint32 { return midBase + uint32(index) }

// midIndex returns the GAL table position a minimal id refers to, or -1 when the
// id is a marker or out of range.
func midIndex(mid uint32, total int) int {
	if mid < midBase {
		return -1
	}
	idx := int(mid - midBase)
	if idx >= total {
		return -1
	}
	return idx
}

func (e DirectoryEntry) isDistList() bool { return e.ObjectClass == "DistributionList" }

func (e DirectoryEntry) displayType() uint32 {
	if e.isDistList() {
		return dtDistList
	}
	return dtMailUser
}

func (e DirectoryEntry) objectType() uint32 {
	if e.isDistList() {
		return objDistList
	}
	return objMailUser
}

// x500DN returns the entry's address-book distinguished name, built from the
// shared organization prefix so every surface agrees on the EX identity.
func (e DirectoryEntry) x500DN() string {
	cn := e.Email
	if lp, _, ok := strings.Cut(e.Email, "@"); ok {
		cn = lp
	}
	return wire.BuildESSDN(cn)
}

// entryProperty returns the value of one column for the entry. Unmapped columns
// report unavailable so the row can flag them. The value's Go type matches the
// column's property type (MS-OXCDATA 2.11.1).
func entryProperty(tag wire.PropTag, e DirectoryEntry) (any, bool) {
	switch tag {
	case wire.PidTagDisplayName:
		return e.DisplayName, true
	case wire.PidTagSmtpAddress:
		return e.Email, true
	case wire.PidTagAddressType:
		return "EX", true
	case wire.PidTagEmailAddress, wire.PidTagAccount:
		return e.x500DN(), true
	case wire.PidTagObjectType:
		return e.objectType(), true
	case wire.PidTagDisplayType:
		return e.displayType(), true
	case wire.PidTagEntryID:
		return wire.PermanentEntryID{DisplayType: e.displayType(), X500DN: e.x500DN()}.Bytes(), true
	default:
		return nil, false
	}
}
