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
