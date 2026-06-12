package wire

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// TestPropValueRoundTrip verifies every scalar/string/binary value type the
// minimal MAPI path uses survives Push→Pull, the property model's core
// guarantee.
func TestPropValueRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		typ  PropType
		val  any
	}{
		{"short", PtShort, uint16(0x1234)},
		{"long", PtLong, uint32(0xCAFEBABE)},
		{"error", PtError, uint32(0x8004010F)},
		{"float", PtFloat, float32(2.5)},
		{"double", PtDouble, float64(-9.75)},
		{"bool-true", PtBoolean, true},
		{"bool-false", PtBoolean, false},
		{"i8", PtI8, uint64(0x1122334455667788)},
		{"systime", PtSysTime, uint64(116444736000000000)},
		{"string8", PtString8, "ascii"},
		{"unicode", PtUnicode, "ünïçode"},
		{"clsid", PtClsid, GUID{TimeLow: 1, TimeMid: 2, TimeHiAndVersion: 3, ClockSeq: [2]byte{4, 5}, Node: [6]byte{6, 7, 8, 9, 10, 11}}},
		{"binary", PtBinary, []byte{0xDE, 0xAD, 0xBE, 0xEF}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewPush(FlagUTF16)
			if err := PushPropValue(p, c.typ, c.val); err != nil {
				t.Fatalf("push: %v", err)
			}
			got, err := PullPropValue(NewPull(p.Bytes(), FlagUTF16), c.typ)
			if err != nil {
				t.Fatalf("pull: %v", err)
			}
			if !valueEqual(got, c.val) {
				t.Errorf("round-trip = %#v, want %#v", got, c.val)
			}
		})
	}
}

// TestPropValueGoTypeMismatch confirms a wrong Go value type is rejected rather
// than silently mis-encoded.
func TestPropValueGoTypeMismatch(t *testing.T) {
	if err := PushPropValue(NewPush(0), PtLong, "not a uint32"); !errors.Is(err, ErrValueType) {
		t.Errorf("err = %v, want ErrValueType", err)
	}
}

// TestABKPresenceMarker verifies the address-book presence byte: a value gets a
// 0xFF prefix, a nil value collapses to a single 0x00, and both decode back.
func TestABKPresenceMarker(t *testing.T) {
	p := NewPush(FlagABK | FlagUTF16)
	if err := PushPropValue(p, PtUnicode, "x"); err != nil {
		t.Fatal(err)
	}
	// 0xFF present, 'x'=0x0078, terminator 0x0000.
	if want := []byte{0xFF, 0x78, 0x00, 0x00, 0x00}; !bytes.Equal(p.Bytes(), want) {
		t.Errorf("present layout = % x, want % x", p.Bytes(), want)
	}

	pn := NewPush(FlagABK | FlagUTF16)
	if err := PushPropValue(pn, PtUnicode, nil); err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x00}; !bytes.Equal(pn.Bytes(), want) {
		t.Errorf("absent layout = % x, want % x", pn.Bytes(), want)
	}
	got, err := PullPropValue(NewPull(pn.Bytes(), FlagABK|FlagUTF16), PtUnicode)
	if err != nil || got != nil {
		t.Errorf("absent round-trip = %#v, %v, want nil, nil", got, err)
	}
}

// TestFileTimeConversion pins the FILETIME epoch math and checks a sub-second
// round-trip.
func TestFileTimeConversion(t *testing.T) {
	if ft := FileTimeFromTime(time.Unix(0, 0).UTC()); ft != 116444736000000000 {
		t.Errorf("Unix epoch FILETIME = %d, want 116444736000000000", ft)
	}
	orig := time.Date(2026, 6, 11, 12, 34, 56, 700*int(time.Microsecond), time.UTC)
	if got := TimeFromFileTime(FileTimeFromTime(orig)); !got.Equal(orig) {
		t.Errorf("FILETIME round-trip = %v, want %v", got, orig)
	}
	if !TimeFromFileTime(0).IsZero() {
		t.Error("FILETIME 0 should map to the zero time")
	}
}

// TestTPropValArrayRoundTrip covers the tagged-value array used by GetProps and
// message property reads.
func TestTPropValArrayRoundTrip(t *testing.T) {
	in := []TaggedPropertyValue{
		{Tag: PidTagDisplayName, Value: "Alice Example"},
		{Tag: PidTagMessageSize, Value: uint32(2048)},
		{Tag: PidTagMessageDeliveryTime, Value: uint64(116444736000000000)},
	}
	p := NewPush(FlagUTF16)
	if err := PushTPropValArray(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := PullTPropValArray(NewPull(p.Bytes(), FlagUTF16))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("count = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].Tag != in[i].Tag || !valueEqual(out[i].Value, in[i].Value) {
			t.Errorf("value[%d] = %+v, want %+v", i, out[i], in[i])
		}
	}
}

// TestPropertyTagArrayRoundTrip pins the count-prefixed tag list (SetColumns
// input).
func TestPropertyTagArrayRoundTrip(t *testing.T) {
	in := []PropTag{PidTagDisplayName, PidTagMessageSize, PidTagEntryID}
	p := NewPush(0)
	PushPropertyTagArray(p, in)
	if want := []byte{0x03, 0x00}; !bytes.Equal(p.Bytes()[:2], want) {
		t.Errorf("count prefix = % x, want % x", p.Bytes()[:2], want)
	}
	out := PullPropertyTagArray(NewPull(p.Bytes(), 0))
	if len(out) != len(in) {
		t.Fatalf("count = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("tag[%d] = %#x, want %#x", i, uint32(out[i]), uint32(in[i]))
		}
	}
}

// TestPropertyRowFlagged exercises the flagged row form used in QueryRows
// responses, including an available value, an error placeholder, and an
// unavailable column.
func TestPropertyRowFlagged(t *testing.T) {
	cols := []PropTag{PidTagDisplayName, PidTagMessageSize, PidTagSubject}
	row := PropertyRow{
		Flag: RowFlagFlagged,
		Values: []any{
			FlaggedPropertyValue{Flag: FlaggedAvailable, Value: "Bob"},
			FlaggedPropertyValue{Flag: FlaggedError, Value: uint32(0x8004010F)},
			FlaggedPropertyValue{Flag: FlaggedUnavailable},
		},
	}
	p := NewPush(FlagUTF16)
	if err := PushPropertyRow(p, cols, row); err != nil {
		t.Fatal(err)
	}
	got, err := PullPropertyRow(NewPull(p.Bytes(), FlagUTF16), cols)
	if err != nil {
		t.Fatal(err)
	}
	if got.Flag != RowFlagFlagged || len(got.Values) != 3 {
		t.Fatalf("row = %+v", got)
	}
	v0, ok := got.Values[0].(FlaggedPropertyValue)
	if !ok || v0.Flag != FlaggedAvailable {
		t.Fatalf("col0 = %+v, want available", got.Values[0])
	}
	if s, ok := v0.Value.(string); !ok || s != "Bob" {
		t.Errorf("col0 value = %q, want Bob", s)
	}
	v1, ok := got.Values[1].(FlaggedPropertyValue)
	if !ok || v1.Flag != FlaggedError {
		t.Fatalf("col1 = %+v, want error", got.Values[1])
	}
	if code, ok := v1.Value.(uint32); !ok || code != 0x8004010F {
		t.Errorf("col1 code = %#x, want 0x8004010F", code)
	}
	v2, ok := got.Values[2].(FlaggedPropertyValue)
	if !ok || v2.Flag != FlaggedUnavailable {
		t.Errorf("col2 = %+v, want unavailable", got.Values[2])
	}
}

// TestPropertyRowNone exercises the untagged row form (every column present).
func TestPropertyRowNone(t *testing.T) {
	cols := []PropTag{PidTagDisplayName, PidTagMessageSize}
	row := PropertyRow{Flag: RowFlagNone, Values: []any{"Carol", uint32(99)}}
	p := NewPush(FlagUTF16)
	if err := PushPropertyRow(p, cols, row); err != nil {
		t.Fatal(err)
	}
	got, err := PullPropertyRow(NewPull(p.Bytes(), FlagUTF16), cols)
	if err != nil {
		t.Fatal(err)
	}
	name, nameOK := got.Values[0].(string)
	size, sizeOK := got.Values[1].(uint32)
	if !nameOK || !sizeOK || name != "Carol" || size != 99 {
		t.Errorf("row = %+v, want Carol/99", got.Values)
	}
}

// valueEqual compares decoded property values, treating []byte specially.
func valueEqual(a, b any) bool {
	if ab, ok := a.([]byte); ok {
		bb, ok := b.([]byte)
		return ok && bytes.Equal(ab, bb)
	}
	return a == b
}
