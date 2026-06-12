package wire

// PropType is a MAPI property type (the low 16 bits of a property tag),
// per MS-OXCDATA section 2.11.1.
type PropType uint16

// MAPI property types (MS-OXCDATA 2.11.1). Only the types the MAPI/HTTP
// surfaces serialize are enumerated; multivalue (PT_MV_*) variants set the
// 0x1000 bit on their single-value base type.
const (
	PtUnspecified PropType = 0x0000
	PtNull        PropType = 0x0001
	PtShort       PropType = 0x0002 // PtypInteger16
	PtLong        PropType = 0x0003 // PtypInteger32
	PtFloat       PropType = 0x0004 // PtypFloating32
	PtDouble      PropType = 0x0005 // PtypFloating64
	PtCurrency    PropType = 0x0006 // PtypCurrency
	PtAppTime     PropType = 0x0007 // PtypFloatingTime
	PtError       PropType = 0x000A // PtypErrorCode
	PtBoolean     PropType = 0x000B // PtypBoolean
	PtObject      PropType = 0x000D // PtypObject / PtypEmbeddedTable
	PtI8          PropType = 0x0014 // PtypInteger64
	PtString8     PropType = 0x001E // PtypString8 (8-bit, code-page)
	PtUnicode     PropType = 0x001F // PtypString (UTF-16LE)
	PtSysTime     PropType = 0x0040 // PtypTime (FILETIME)
	PtClsid       PropType = 0x0048 // PtypGuid
	PtSvrEid      PropType = 0x00FB // PtypServerId
	PtRestriction PropType = 0x00FD // PtypRestriction
	PtActions     PropType = 0x00FE // PtypRuleAction
	PtBinary      PropType = 0x0102 // PtypBinary

	// PtMvFlag is OR'd onto a base type to form its multivalue variant.
	PtMvFlag PropType = 0x1000

	PtMvShort   PropType = 0x1002
	PtMvLong    PropType = 0x1003
	PtMvFloat   PropType = 0x1004
	PtMvDouble  PropType = 0x1005
	PtMvI8      PropType = 0x1014
	PtMvString8 PropType = 0x101E
	PtMvUnicode PropType = 0x101F
	PtMvSysTime PropType = 0x1040
	PtMvClsid   PropType = 0x1048
	PtMvBinary  PropType = 0x1102
)

// PropTag is a 32-bit MAPI property tag: the high 16 bits are the property id,
// the low 16 bits the PropType.
type PropTag uint32

// MakeTag combines a property id and type into a PropTag.
func MakeTag(id uint16, t PropType) PropTag {
	return PropTag(uint32(id)<<16 | uint32(t))
}

// ID returns the property id (high 16 bits).
func (t PropTag) ID() uint16 { return uint16(t >> 16) }

// Type returns the PropType (low 16 bits).
func (t PropTag) Type() PropType { return PropType(t & 0xFFFF) }

// IsMultivalue reports whether the tag's type carries the 0x1000 multivalue bit.
func (t PropTag) IsMultivalue() bool { return t.Type()&PtMvFlag != 0 }

// Common property tags (MS-OXPROPS) used by the address book and mailbox
// surfaces. Listed by canonical name with the id and type spelled out so the
// id↔name mapping is auditable against the spec.
const (
	PidTagEntryID                PropTag = PropTag(0x0FFF)<<16 | PropTag(PtBinary)
	PidTagObjectType             PropTag = PropTag(0x0FFE)<<16 | PropTag(PtLong)
	PidTagDisplayType            PropTag = PropTag(0x3900)<<16 | PropTag(PtLong)
	PidTagDisplayTypeEx          PropTag = PropTag(0x3905)<<16 | PropTag(PtLong)
	PidTagDisplayName            PropTag = PropTag(0x3001)<<16 | PropTag(PtUnicode)
	PidTagAddressType            PropTag = PropTag(0x3002)<<16 | PropTag(PtUnicode)
	PidTagEmailAddress           PropTag = PropTag(0x3003)<<16 | PropTag(PtUnicode)
	PidTagSmtpAddress            PropTag = PropTag(0x39FE)<<16 | PropTag(PtUnicode)
	PidTagAccount                PropTag = PropTag(0x3A00)<<16 | PropTag(PtUnicode)
	PidTagSurname                PropTag = PropTag(0x3A11)<<16 | PropTag(PtUnicode)
	PidTagGivenName              PropTag = PropTag(0x3A06)<<16 | PropTag(PtUnicode)
	PidTagTitle                  PropTag = PropTag(0x3A17)<<16 | PropTag(PtUnicode)
	PidTagCompanyName            PropTag = PropTag(0x3A16)<<16 | PropTag(PtUnicode)
	PidTagDepartmentName         PropTag = PropTag(0x3A18)<<16 | PropTag(PtUnicode)
	PidTagOfficeLocation         PropTag = PropTag(0x3A19)<<16 | PropTag(PtUnicode)
	PidTagBusinessTelephone      PropTag = PropTag(0x3A08)<<16 | PropTag(PtUnicode)
	PidTagSearchKey              PropTag = PropTag(0x300B)<<16 | PropTag(PtBinary)
	PidTagInstanceKey            PropTag = PropTag(0x0FF6)<<16 | PropTag(PtBinary)
	PidTagContainerFlags         PropTag = PropTag(0x3600)<<16 | PropTag(PtLong)
	PidTagDepth                  PropTag = PropTag(0x3005)<<16 | PropTag(PtLong)
	PidTagAddressBookContainerID PropTag = PropTag(0xFFFD)<<16 | PropTag(PtLong)

	// Mailbox / message tags.
	PidTagSubject              PropTag = PropTag(0x0037)<<16 | PropTag(PtUnicode)
	PidTagNormalizedSubject    PropTag = PropTag(0x0E1D)<<16 | PropTag(PtUnicode)
	PidTagMessageClass         PropTag = PropTag(0x001A)<<16 | PropTag(PtUnicode)
	PidTagMessageSize          PropTag = PropTag(0x0E08)<<16 | PropTag(PtLong)
	PidTagMessageFlags         PropTag = PropTag(0x0E07)<<16 | PropTag(PtLong)
	PidTagMessageDeliveryTime  PropTag = PropTag(0x0E06)<<16 | PropTag(PtSysTime)
	PidTagLastModificationTime PropTag = PropTag(0x3008)<<16 | PropTag(PtSysTime)
	PidTagFolderId             PropTag = PropTag(0x6748)<<16 | PropTag(PtI8)
	PidTagMid                  PropTag = PropTag(0x674A)<<16 | PropTag(PtI8)
	PidTagContentCount         PropTag = PropTag(0x3602)<<16 | PropTag(PtLong)
	PidTagContentUnreadCount   PropTag = PropTag(0x3603)<<16 | PropTag(PtLong)
	PidTagSubfolders           PropTag = PropTag(0x360A)<<16 | PropTag(PtBoolean)
	PidTagContainerClass       PropTag = PropTag(0x3613)<<16 | PropTag(PtUnicode)
	PidTagBody                 PropTag = PropTag(0x1000)<<16 | PropTag(PtUnicode)
	PidTagHtml                 PropTag = PropTag(0x1013)<<16 | PropTag(PtBinary)
	PidTagInstID               PropTag = PropTag(0x674D)<<16 | PropTag(PtI8)
	PidTagInstanceNum          PropTag = PropTag(0x674E)<<16 | PropTag(PtLong)

	// PidTagAddressBookIsMaster (MS-OXNSPI) marks whether an address-book
	// container is the master; the NSPI special table carries it per container.
	PidTagAddressBookIsMaster PropTag = PropTag(0xFFFB)<<16 | PropTag(PtBoolean)

	// PidTagAnr (MS-OXOABK) is the ambiguous-name-resolution key a GetMatches
	// restriction targets to search the GAL by partial name or address.
	PidTagAnr PropTag = PropTag(0x360C)<<16 | PropTag(PtUnicode)
)
