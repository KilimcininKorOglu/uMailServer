package wire

import "fmt"

// FlaggedPropertyValue flags (MS-OXCDATA 2.11.5).
const (
	FlaggedAvailable   uint8 = 0x00 // the value follows
	FlaggedUnavailable uint8 = 0x01 // no value present
	FlaggedError       uint8 = 0x0A // a 4-byte PtypErrorCode follows
)

// PropertyRow flags (MS-OXCDATA 2.8.1).
const (
	RowFlagNone    uint8 = 0x00 // every column value is present and untagged
	RowFlagFlagged uint8 = 0x01 // each column value is a FlaggedPropertyValue
)

// PushPropertyTagArray serializes a PropertyTagArray (MS-OXCDATA 2.9): a 16-bit
// count followed by that many 4-byte property tags.
func PushPropertyTagArray(p *Push, tags []PropTag) {
	p.Uint16(uint16(len(tags)))
	for _, t := range tags {
		p.Uint32(uint32(t))
	}
}

// PullPropertyTagArray deserializes a PropertyTagArray.
func PullPropertyTagArray(p *Pull) []PropTag {
	n := int(p.Uint16())
	if n == 0 {
		return nil
	}
	out := make([]PropTag, 0, n)
	for i := 0; i < n && p.err == nil; i++ {
		out = append(out, PropTag(p.Uint32()))
	}
	return out
}

// PushTPropValArray serializes a TPROPVAL_ARRAY (MS-OXCDATA 2.11.5
// PropertyValueArray addressed by a 16-bit count): a 16-bit count followed by
// that many TaggedPropertyValue structures.
func PushTPropValArray(p *Push, vals []TaggedPropertyValue) error {
	p.Uint16(uint16(len(vals)))
	for _, v := range vals {
		if err := v.Push(p); err != nil {
			return err
		}
	}
	return nil
}

// PullTPropValArray deserializes a TPROPVAL_ARRAY.
func PullTPropValArray(p *Pull) ([]TaggedPropertyValue, error) {
	n := int(p.Uint16())
	if n == 0 {
		return nil, p.err
	}
	out := make([]TaggedPropertyValue, 0, n)
	for i := 0; i < n && p.err == nil; i++ {
		v, err := PullTaggedPropertyValue(p)
		if err != nil {
			return out, err
		}
		out = append(out, v)
	}
	return out, p.err
}

// FlaggedPropertyValue is a property value tagged with availability (MS-OXCDATA
// 2.11.5): a flag byte followed, when available, by the value, or when in error
// by a 4-byte error code.
type FlaggedPropertyValue struct {
	Flag  uint8
	Value any // the value for FlaggedAvailable, a uint32 code for FlaggedError, nil otherwise
}

// PushFlaggedPropertyValue serializes a flagged value of the given concrete
// property type. The PtypUnspecified (type-prefixed) form is not used by this
// server's responses and is rejected.
func PushFlaggedPropertyValue(p *Push, t PropType, fv FlaggedPropertyValue) error {
	if t == PtUnspecified {
		return fmt.Errorf("%w: flagged PtypUnspecified", ErrUnsupportedType)
	}
	p.Uint8(fv.Flag)
	switch fv.Flag {
	case FlaggedAvailable:
		return PushPropValue(p, t, fv.Value)
	case FlaggedUnavailable:
		return nil
	case FlaggedError:
		code, ok := fv.Value.(uint32)
		if !ok {
			return valueTypeErr(PtError, fv.Value)
		}
		p.Uint32(code)
		return nil
	default:
		return fmt.Errorf("%w: flagged value flag %#x", ErrFormat, fv.Flag)
	}
}

// PullFlaggedPropertyValue deserializes a flagged value of the given type.
func PullFlaggedPropertyValue(p *Pull, t PropType) (FlaggedPropertyValue, error) {
	flag := p.Uint8()
	switch flag {
	case FlaggedAvailable:
		v, err := PullPropValue(p, t)
		return FlaggedPropertyValue{Flag: flag, Value: v}, err
	case FlaggedUnavailable:
		return FlaggedPropertyValue{Flag: flag}, p.err
	case FlaggedError:
		return FlaggedPropertyValue{Flag: flag, Value: p.Uint32()}, p.err
	default:
		if p.err == nil {
			p.err = ErrFormat
		}
		return FlaggedPropertyValue{Flag: flag}, p.err
	}
}

// PropertyRow is one row of a property table (MS-OXCDATA 2.8.1 PropertyRow). The
// row's Flag selects the per-value encoding; Values has one entry per column,
// holding the raw value for RowFlagNone or a FlaggedPropertyValue for
// RowFlagFlagged.
type PropertyRow struct {
	Flag   uint8
	Values []any
}

// PushPropertyRow serializes a row against its column tags (the column types
// drive each value's encoding).
func PushPropertyRow(p *Push, cols []PropTag, r PropertyRow) error {
	if len(r.Values) != len(cols) {
		return fmt.Errorf("%w: row has %d values for %d columns", ErrFormat, len(r.Values), len(cols))
	}
	p.Uint8(r.Flag)
	switch r.Flag {
	case RowFlagNone:
		for i, c := range cols {
			if err := PushPropValue(p, c.Type(), r.Values[i]); err != nil {
				return err
			}
		}
	case RowFlagFlagged:
		for i, c := range cols {
			fv, ok := r.Values[i].(FlaggedPropertyValue)
			if !ok {
				return fmt.Errorf("%w: flagged row needs FlaggedPropertyValue, got %T", ErrValueType, r.Values[i])
			}
			if err := PushFlaggedPropertyValue(p, c.Type(), fv); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: property row flag %#x", ErrFormat, r.Flag)
	}
	return nil
}

// PullPropertyRow deserializes a row against its column tags.
func PullPropertyRow(p *Pull, cols []PropTag) (PropertyRow, error) {
	r := PropertyRow{Flag: p.Uint8(), Values: make([]any, 0, len(cols))}
	switch r.Flag {
	case RowFlagNone:
		for _, c := range cols {
			v, err := PullPropValue(p, c.Type())
			if err != nil {
				return r, err
			}
			r.Values = append(r.Values, v)
		}
	case RowFlagFlagged:
		for _, c := range cols {
			fv, err := PullFlaggedPropertyValue(p, c.Type())
			if err != nil {
				return r, err
			}
			r.Values = append(r.Values, fv)
		}
	default:
		if p.err == nil {
			p.err = ErrFormat
		}
		return r, p.err
	}
	return r, p.err
}
