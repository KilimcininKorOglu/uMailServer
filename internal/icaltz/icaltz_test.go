package icaltz

import (
	"strings"
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestIsUTC(t *testing.T) {
	for _, tz := range []string{"", "UTC", "utc", "Not/AZone"} {
		if !IsUTC(tz) {
			t.Errorf("IsUTC(%q) = false, want true", tz)
		}
	}
	for _, tz := range []string{"America/New_York", "Asia/Tokyo", "Europe/Istanbul"} {
		if IsUTC(tz) {
			t.Errorf("IsUTC(%q) = true, want false", tz)
		}
	}
}

// TestFormatPropertyPreservesCivilTimeAcrossDST is the heart of the fix: the
// SAME civil wall time (14:00) must be emitted with TZID for instants on either
// side of a DST boundary, so a recurrence anchored to it keeps its wall time.
func TestFormatPropertyPreservesCivilTime(t *testing.T) {
	ny := mustLoad(t, "America/New_York")

	// 14:00 EDT on 2026-06-01 is 18:00 UTC.
	summer := time.Date(2026, 6, 1, 14, 0, 0, 0, ny)
	if got := FormatProperty("DTSTART", "America/New_York", summer); got != "DTSTART;TZID=America/New_York:20260601T140000" {
		t.Errorf("summer DTSTART = %q", got)
	}
	// 14:00 EST on 2026-01-05 is 19:00 UTC — different offset, SAME civil time.
	winter := time.Date(2026, 1, 5, 14, 0, 0, 0, ny)
	if got := FormatProperty("DTSTART", "America/New_York", winter); got != "DTSTART;TZID=America/New_York:20260105T140000" {
		t.Errorf("winter DTSTART = %q", got)
	}

	// UTC / empty zone fall back to a bare Z instant.
	utcInstant := time.Date(2026, 6, 1, 18, 0, 0, 0, time.UTC)
	if got := FormatProperty("DTSTART", "UTC", utcInstant); got != "DTSTART:20260601T180000Z" {
		t.Errorf("UTC DTSTART = %q", got)
	}
	if got := FormatProperty("DTEND", "", utcInstant); got != "DTEND:20260601T180000Z" {
		t.Errorf("empty-zone DTEND = %q", got)
	}
}

func TestVTimezoneUTCEmpty(t *testing.T) {
	if got := VTimezone("UTC", time.Now().UTC()); got != "" {
		t.Errorf("VTimezone(UTC) = %q, want empty", got)
	}
	if got := VTimezone("", time.Now().UTC()); got != "" {
		t.Errorf("VTimezone(empty) = %q, want empty", got)
	}
}

func TestVTimezoneDST_NewYork(t *testing.T) {
	ref := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	v := VTimezone("America/New_York", ref)
	for _, want := range []string{
		"BEGIN:VTIMEZONE",
		"TZID:America/New_York",
		"BEGIN:DAYLIGHT",
		"TZOFFSETFROM:-0500",
		"TZOFFSETTO:-0400",
		"FREQ=YEARLY;BYMONTH=3;BYDAY=2SU", // 2026 DST starts Sun Mar 8
		"BEGIN:STANDARD",
		"TZOFFSETTO:-0500",
		"FREQ=YEARLY;BYMONTH=11;BYDAY=1SU", // 2026 DST ends Sun Nov 1
		"END:VTIMEZONE",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("VTIMEZONE missing %q\n---\n%s", want, v)
		}
	}
}

func TestVTimezoneNoDST_Tokyo(t *testing.T) {
	ref := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	v := VTimezone("Asia/Tokyo", ref)
	if !strings.Contains(v, "TZID:Asia/Tokyo") {
		t.Errorf("missing TZID: %s", v)
	}
	if !strings.Contains(v, "TZOFFSETTO:+0900") {
		t.Errorf("missing +0900 offset: %s", v)
	}
	if strings.Contains(v, "BEGIN:DAYLIGHT") {
		t.Errorf("Tokyo must have no DAYLIGHT component: %s", v)
	}
	if !strings.Contains(v, "BEGIN:STANDARD") {
		t.Errorf("missing STANDARD component: %s", v)
	}
}

// TestRecurrenceInstantsShiftAcrossDST documents the user-visible guarantee: a
// civil time anchored to a DST zone maps to DIFFERENT UTC instants before and
// after the boundary (so the wall-clock time is preserved each occurrence).
func TestRecurrenceInstantsShiftAcrossDST(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	winter := time.Date(2026, 1, 5, 14, 0, 0, 0, ny).UTC() // 14:00 EST -> 19:00 UTC
	summer := time.Date(2026, 7, 6, 14, 0, 0, 0, ny).UTC() // 14:00 EDT -> 18:00 UTC
	if winter.Hour() != 19 {
		t.Errorf("winter 14:00 NY should be 19:00 UTC, got %d", winter.Hour())
	}
	if summer.Hour() != 18 {
		t.Errorf("summer 14:00 NY should be 18:00 UTC, got %d", summer.Hour())
	}
}
