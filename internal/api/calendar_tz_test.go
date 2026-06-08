package api

import (
	"strings"
	"testing"
)

// TestBuildICSEvent_TimezoneAnchorsRecurrence verifies a timed event with a
// timezone is stored as civil-local DTSTART;TZID plus a VTIMEZONE (so a
// recurrence keeps its wall time across DST), and that the value round-trips.
func TestBuildICSEvent_TimezoneAnchorsRecurrence(t *testing.T) {
	dto := CalendarEventDTO{
		UID:        "evt-tz",
		Summary:    "Weekly standup",
		Start:      "2026-06-01T18:00:00Z", // 14:00 in America/New_York (EDT)
		End:        "2026-06-01T18:30:00Z",
		Recurrence: "FREQ=WEEKLY",
		Timezone:   "America/New_York",
	}
	ics, err := buildICSEvent(dto)
	if err != nil {
		t.Fatalf("buildICSEvent: %v", err)
	}
	for _, want := range []string{
		"BEGIN:VTIMEZONE",
		"TZID:America/New_York",
		"DTSTART;TZID=America/New_York:20260601T140000",
		"DTEND;TZID=America/New_York:20260601T143000",
		"RRULE:FREQ=WEEKLY",
	} {
		if !strings.Contains(ics, want) {
			t.Errorf("iCal missing %q\n---\n%s", want, ics)
		}
	}
	// It must NOT emit a bare UTC DTSTART for a zoned event.
	if strings.Contains(ics, "DTSTART:20260601T180000Z") {
		t.Errorf("zoned event must not emit bare UTC DTSTART\n%s", ics)
	}

	// Round-trip: parsing the stored iCal recovers the timezone and the instant.
	got, ok := parseICSEvent(ics)
	if !ok {
		t.Fatal("parseICSEvent failed")
	}
	if got.Timezone != "America/New_York" {
		t.Errorf("round-trip Timezone = %q, want America/New_York", got.Timezone)
	}
	if got.Start != "2026-06-01T18:00:00Z" {
		t.Errorf("round-trip Start = %q, want 2026-06-01T18:00:00Z (the correct UTC instant)", got.Start)
	}
}

// TestBuildICSEvent_NoTimezoneStaysUTC verifies one-off/zoneless events keep the
// bare-UTC form (unchanged behavior).
func TestBuildICSEvent_NoTimezoneStaysUTC(t *testing.T) {
	dto := CalendarEventDTO{
		UID:     "evt-utc",
		Summary: "One off",
		Start:   "2026-06-01T18:00:00Z",
		End:     "2026-06-01T18:30:00Z",
	}
	ics, err := buildICSEvent(dto)
	if err != nil {
		t.Fatalf("buildICSEvent: %v", err)
	}
	if !strings.Contains(ics, "DTSTART:20260601T180000Z") {
		t.Errorf("zoneless event should emit bare UTC DTSTART\n%s", ics)
	}
	if strings.Contains(ics, "BEGIN:VTIMEZONE") {
		t.Errorf("zoneless event must not emit a VTIMEZONE\n%s", ics)
	}
}

// TestParseICSTime_TZIDInstant guards the parse-side bug fix: a civil-local
// DTSTART;TZID value must resolve to the correct UTC instant, not be treated as
// UTC (which would shift the event by the zone offset).
func TestParseICSTime_TZIDInstant(t *testing.T) {
	got, allDay := parseICSTime("TZID=America/New_York", "20260601T140000")
	if allDay {
		t.Fatal("should not be all-day")
	}
	if got != "2026-06-01T18:00:00Z" {
		t.Errorf("TZID parse = %q, want 2026-06-01T18:00:00Z", got)
	}
}
