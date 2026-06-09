package ews

import (
	"strconv"
	"testing"
	"time"
)

// TestDeferredSendTime_AbsoluteWins verifies PidTagDeferredSendTime (0x3FEF) is
// honored as an absolute instant and takes precedence over the relative pair.
func TestDeferredSendTime_AbsoluteWins(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	abs := "2026-06-10T15:30:00Z"
	props := []ExtendedPropertyType{
		{ExtendedFieldURI: ExtendedFieldURIType{PropertyTag: "0x3FEF", PropertyType: "SystemTime"}, Value: abs},
		{ExtendedFieldURI: ExtendedFieldURIType{PropertyTag: "0x3FEB", PropertyType: "Integer"}, Value: "5"},
		{ExtendedFieldURI: ExtendedFieldURIType{PropertyTag: "0x3FEC", PropertyType: "Integer"}, Value: "0"},
	}
	got := deferredSendTime(props, now)
	want, err := time.Parse(time.RFC3339, abs)
	if err != nil {
		t.Fatalf("parse want: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("absolute deferred-send = %v, want %v", got, want)
	}
}

// TestDeferredSendTime_RelativeUnits verifies the relative number/units pair maps
// minutes/hours/days/weeks per MS-OXOMSG (mirrors Exchange the deferred-send interval).
func TestDeferredSendTime_RelativeUnits(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		units int
		dur   time.Duration
	}{
		{0, 90 * time.Minute},
		{1, 90 * time.Hour},
		{2, 90 * 24 * time.Hour},
		{3, 90 * 7 * 24 * time.Hour},
	}
	for _, c := range cases {
		props := []ExtendedPropertyType{
			{ExtendedFieldURI: ExtendedFieldURIType{PropertyTag: "0x3FEB"}, Value: "90"},
			{ExtendedFieldURI: ExtendedFieldURIType{PropertyTag: "0x3FEC"}, Value: strconv.Itoa(c.units)},
		}
		got := deferredSendTime(props, now)
		if want := now.Add(c.dur); !got.Equal(want) {
			t.Errorf("units %d: got %v, want %v", c.units, got, want)
		}
	}
}

// TestDeferredSendTime_NoneOrPartial verifies the zero time is returned when no
// deferred-send properties (or only a partial relative pair) are present.
func TestDeferredSendTime_NoneOrPartial(t *testing.T) {
	now := time.Now()
	if got := deferredSendTime(nil, now); !got.IsZero() {
		t.Errorf("no props: got %v, want zero", got)
	}
	props := []ExtendedPropertyType{{ExtendedFieldURI: ExtendedFieldURIType{PropertyTag: "0x3FEB"}, Value: "5"}}
	if got := deferredSendTime(props, now); !got.IsZero() {
		t.Errorf("number-only (no units): got %v, want zero", got)
	}
}

// TestParsePropertyTag accepts both hex ("0x3FEF") and decimal ("16367") tags.
func TestParsePropertyTag(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"0x3FEF", 0x3FEF, true},
		{"0X3fef", 0x3FEF, true},
		{"16367", 16367, true}, // decimal form of 0x3FEF
		{"", 0, false},
		{"zzz", 0, false},
	}
	for _, c := range cases {
		got, ok := parsePropertyTag(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parsePropertyTag(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
