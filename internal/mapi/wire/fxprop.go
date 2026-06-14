package wire

import (
	"fmt"
	"unicode/utf16"
)

// PushFastTransferPropval serializes one property as a FastTransfer propValue
// (MS-OXCFXICS 2.2.4.1.1.1): a propdef — proptype (u16 LE) then propid (u16 LE) —
// followed by the value in the FastTransfer encoding. That encoding differs from the
// ROP TaggedPropertyValue framing (see PushPropValue): strings carry a u32 byte
// count that INCLUDES the terminating NUL, binaries carry a u32 byte count, and a
// boolean is written as a u16. The Go value types match PushPropValue's mapping
// (uint32 for PtLong, string for PtString8/PtUnicode, []byte for PtBinary, etc.).
//
// Only tagged (non-named) properties are supported; a named-property id (>= 0x8000)
// is rejected, since its propdef carries an extra GUID+kind block that the contents
// download of ordinary message properties does not need (a later refinement).
func PushFastTransferPropval(p *Push, tag PropTag, v any) error {
	if tag.ID() >= 0x8000 {
		return fmt.Errorf("%w: named property %#08x in FastTransfer stream", ErrUnsupportedType, uint32(tag))
	}
	t := tag.Type()
	p.Uint16(uint16(t))        // propdef: proptype
	p.Uint16(uint16(tag.ID())) // propdef: propid
	switch t {
	case PtShort:
		x, ok := v.(uint16)
		if !ok {
			return valueTypeErr(t, v)
		}
		p.Uint16(x)
	case PtLong, PtError:
		x, ok := v.(uint32)
		if !ok {
			return valueTypeErr(t, v)
		}
		p.Uint32(x)
	case PtFloat:
		x, ok := v.(float32)
		if !ok {
			return valueTypeErr(t, v)
		}
		p.Float32(x)
	case PtDouble, PtAppTime:
		x, ok := v.(float64)
		if !ok {
			return valueTypeErr(t, v)
		}
		p.Float64(x)
	case PtBoolean:
		x, ok := v.(bool)
		if !ok {
			return valueTypeErr(t, v)
		}
		var b uint16
		if x {
			b = 1
		}
		p.Uint16(b) // FastTransfer writes a boolean as a u16, not a single byte
	case PtCurrency, PtSysTime, PtI8:
		x, ok := v.(uint64)
		if !ok {
			return valueTypeErr(t, v)
		}
		p.Uint64(x)
	case PtString8:
		x, ok := v.(string)
		if !ok {
			return valueTypeErr(t, v)
		}
		p.Uint32(uint32(len(x)) + 1) // count includes the terminating NUL
		p.Raw([]byte(x))
		p.Uint8(0)
	case PtUnicode:
		x, ok := v.(string)
		if !ok {
			return valueTypeErr(t, v)
		}
		pushFTUnicode(p, x)
	case PtClsid:
		x, ok := v.(GUID)
		if !ok {
			return valueTypeErr(t, v)
		}
		p.GUID(x)
	case PtBinary:
		x, ok := v.([]byte)
		if !ok {
			return valueTypeErr(t, v)
		}
		p.Uint32(uint32(len(x)))
		p.Raw(x)
	default:
		return fmt.Errorf("%w: %#04x", ErrUnsupportedType, uint16(t))
	}
	return nil
}

// pushFTUnicode writes a FastTransfer PtypString value: a u32 byte count followed by
// the UTF-16LE bytes, with the count and bytes both including the terminating NUL
// (MS-OXCFXICS 2.2.4.1.1.1; the reference always emits the trailing UTF-16 NUL).
func pushFTUnicode(p *Push, s string) {
	units := utf16.Encode([]rune(s))
	buf := make([]byte, 0, len(units)*2+2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	buf = append(buf, 0x00, 0x00) // UTF-16 NUL terminator, counted in the length
	p.Uint32(uint32(len(buf)))
	p.Raw(buf)
}

// FastTransfer stream markers (MS-OXCFXICS 2.2.4.1.4). A marker is a u32 atom whose
// property-id half lies in the reserved 0x4000-0x40FF range, so it never collides with
// a real (tagged) property's propdef in the stream — that range is reserved for
// markers. Only the markers this codec recognizes when parsing an incoming (upload)
// stream are named.
const (
	FXStartMessage       uint32 = 0x400C0003
	FXEndMessage         uint32 = 0x400D0003
	FXStartRecip         uint32 = 0x40030003
	FXEndToRecip         uint32 = 0x40040003
	FXNewAttach          uint32 = 0x40000003
	FXEndAttach          uint32 = 0x400E0003
	FXIncrSyncChg        uint32 = 0x40120003
	FXIncrSyncDel        uint32 = 0x40130003
	FXIncrSyncEnd        uint32 = 0x40140003
	FXIncrSyncMessage    uint32 = 0x40150003
	FXIncrSyncStateBegin uint32 = 0x403A0003
	FXIncrSyncStateEnd   uint32 = 0x403B0003
)

// fxMarkerSet is the set of recognized FastTransfer markers. An atom in this set is a
// structural marker, never a property value; an atom outside it is decoded as a
// property's propdef.
var fxMarkerSet = map[uint32]struct{}{
	FXStartMessage: {}, FXEndMessage: {},
	FXStartRecip: {}, FXEndToRecip: {},
	FXNewAttach: {}, FXEndAttach: {},
	FXIncrSyncChg: {}, FXIncrSyncDel: {}, FXIncrSyncEnd: {},
	FXIncrSyncMessage: {}, FXIncrSyncStateBegin: {}, FXIncrSyncStateEnd: {},
}

// FTElement is one element parsed from a FastTransfer stream: either a structural
// marker (Marker != 0) or a tagged property value (Marker == 0, read Tag and Value).
type FTElement struct {
	Marker uint32
	Tag    PropTag
	Value  any
}

// PullFastTransferElement reads one element from a FastTransfer stream (MS-OXCFXICS
// 2.2.4.1): a u32 atom that is either a recognized marker or a property's propdef
// (proptype in the low half, propid in the high half) followed by its
// FastTransfer-encoded value — the inverse of PushFastTransferPropval. An atom that is
// neither a known marker nor a supported, tagged property type is a hard error;
// silently skipping it would desync the rest of the stream into plausible garbage.
func PullFastTransferElement(p *Pull) (FTElement, error) {
	atom := p.Uint32()
	if p.Err() != nil {
		return FTElement{}, p.Err()
	}
	if _, ok := fxMarkerSet[atom]; ok {
		return FTElement{Marker: atom}, nil
	}
	// proptype | propid<<16 reads back as the PropTag value (propid<<16 | proptype).
	tag := PropTag(atom)
	if tag.ID() >= 0x8000 {
		return FTElement{}, fmt.Errorf("%w: named property %#08x in FastTransfer stream", ErrUnsupportedType, atom)
	}
	v, err := pullFTValue(p, tag.Type())
	if err != nil {
		return FTElement{}, err
	}
	return FTElement{Tag: tag, Value: v}, nil
}

// pullFTValue decodes a single FastTransfer-framed value of the given type, the
// inverse of the value branch of PushFastTransferPropval. Returned Go types match what
// PushFastTransferPropval accepts (uint32 for PtLong, string for PtString8/PtUnicode,
// []byte for PtBinary, bool for PtBoolean, and so on).
func pullFTValue(p *Pull, t PropType) (any, error) {
	switch t {
	case PtShort:
		return p.Uint16(), errOrNil(p)
	case PtLong, PtError:
		return p.Uint32(), errOrNil(p)
	case PtFloat:
		return p.Float32(), errOrNil(p)
	case PtDouble, PtAppTime:
		return p.Float64(), errOrNil(p)
	case PtBoolean:
		return p.Uint16() != 0, errOrNil(p) // FastTransfer frames a boolean as a u16
	case PtCurrency, PtSysTime, PtI8:
		return p.Uint64(), errOrNil(p)
	case PtString8:
		n := int(p.Uint32())
		b := p.Bytes(n)
		if p.Err() != nil {
			return nil, p.Err()
		}
		if n > 0 {
			b = b[:n-1] // the count includes the terminating NUL
		}
		return string(b), nil
	case PtUnicode:
		s := pullFTUnicode(p)
		if p.Err() != nil {
			return nil, p.Err()
		}
		return s, nil
	case PtClsid:
		g := p.GUID()
		return g, errOrNil(p)
	case PtBinary:
		n := int(p.Uint32())
		b := p.Bytes(n)
		if p.Err() != nil {
			return nil, p.Err()
		}
		return b, nil
	default:
		return nil, fmt.Errorf("%w: %#04x", ErrUnsupportedType, uint16(t))
	}
}

// pullFTUnicode decodes a FastTransfer PtypString value: a u32 byte count then the
// UTF-16LE bytes, both including the trailing UTF-16 NUL (the inverse of pushFTUnicode).
func pullFTUnicode(p *Pull) string {
	n := int(p.Uint32())
	b := p.Bytes(n)
	if p.Err() != nil || b == nil {
		return ""
	}
	if n >= 2 {
		b = b[:n-2] // drop the trailing UTF-16 NUL
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(units))
}

// errOrNil returns the pull's latched error, or nil; it lets the fixed-size value
// cases report a truncated read without repeating the check.
func errOrNil(p *Pull) error { return p.Err() }
