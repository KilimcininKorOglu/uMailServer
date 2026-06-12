package wire

import (
	"errors"
	"fmt"
)

// Property-value codec errors.
var (
	// ErrUnsupportedType is returned for a property type the codec does not yet
	// serialize (e.g. a multivalue type not on the implemented path).
	ErrUnsupportedType = errors.New("mapi/wire: unsupported property type")
	// ErrValueType is returned when the Go value does not match the property type.
	ErrValueType = errors.New("mapi/wire: property value Go type mismatch")
)

// isABKVariable reports whether a type carries the address-book presence marker
// (string, binary, or any multivalue type) under FlagABK.
func isABKVariable(t PropType) bool {
	return t == PtString8 || t == PtUnicode || t == PtBinary || t&PtMvFlag != 0
}

// PushPropValue serializes one MAPI property value of the given type, per
// MS-OXCDATA 2.11.2. Under FlagABK a string/binary/multivalue value is preceded
// by a presence byte (0x00 absent, 0xFF present); a nil value writes the absent
// marker. The Go type stored in v must match the property type: uint16 for
// PtShort, uint32 for PtLong/PtError, float32 for PtFloat, float64 for
// PtDouble/PtAppTime, bool for PtBoolean, uint64 for PtCurrency/PtSysTime/PtI8,
// string for PtString8/PtUnicode, GUID for PtClsid, and []byte for PtBinary.
func PushPropValue(p *Push, t PropType, v any) error {
	if p.flags&FlagABK != 0 && isABKVariable(t) {
		if v == nil {
			p.Uint8(0)
			return nil
		}
		p.Uint8(0xFF)
	}
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
		p.Bool(x)
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
		p.Str(x)
	case PtUnicode:
		x, ok := v.(string)
		if !ok {
			return valueTypeErr(t, v)
		}
		p.WStr(x)
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
		p.Bin(x)
	default:
		return fmt.Errorf("%w: %#04x", ErrUnsupportedType, uint16(t))
	}
	return nil
}

// PullPropValue deserializes one MAPI property value of the given type. It
// returns the Go value (see PushPropValue for the type mapping) or nil when an
// address-book value is marked absent.
func PullPropValue(p *Pull, t PropType) (any, error) {
	if p.flags&FlagABK != 0 && isABKVariable(t) {
		switch p.Uint8() {
		case 0x00:
			return nil, nil
		case 0xFF:
			// present; fall through to read the value
		default:
			if p.err == nil {
				p.err = ErrFormat
			}
			return nil, p.err
		}
	}
	switch t {
	case PtShort:
		return p.Uint16(), p.err
	case PtLong, PtError:
		return p.Uint32(), p.err
	case PtFloat:
		return p.Float32(), p.err
	case PtDouble, PtAppTime:
		return p.Float64(), p.err
	case PtBoolean:
		return p.Bool(), p.err
	case PtCurrency, PtSysTime, PtI8:
		return p.Uint64(), p.err
	case PtString8:
		return p.Str(), p.err
	case PtUnicode:
		return p.WStr(), p.err
	case PtClsid:
		return p.GUID(), p.err
	case PtBinary:
		return p.Bin(), p.err
	default:
		return nil, fmt.Errorf("%w: %#04x", ErrUnsupportedType, uint16(t))
	}
}

func valueTypeErr(t PropType, v any) error {
	return fmt.Errorf("%w: type %#04x got %T", ErrValueType, uint16(t), v)
}

// TaggedPropertyValue is a property tag paired with its value (MS-OXCDATA
// 2.11.4 TaggedPropertyValue): a 4-byte tag followed by the value encoded per
// the tag's type.
type TaggedPropertyValue struct {
	Tag   PropTag
	Value any
}

// Push serializes the tagged property value.
func (t TaggedPropertyValue) Push(p *Push) error {
	p.Uint32(uint32(t.Tag))
	return PushPropValue(p, t.Tag.Type(), t.Value)
}

// PullTaggedPropertyValue deserializes a tagged property value.
func PullTaggedPropertyValue(p *Pull) (TaggedPropertyValue, error) {
	tag := PropTag(p.Uint32())
	v, err := PullPropValue(p, tag.Type())
	return TaggedPropertyValue{Tag: tag, Value: v}, err
}
