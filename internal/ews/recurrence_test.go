package ews

import (
	"strings"
	"testing"
	"time"
)

// TestEWSRecurrenceXML verifies that a stored iCal RRULE is rendered as an EWS
// <t:Recurrence> block using the element names exchangelib/Outlook expect (a
// pattern plus a boundary), so a recurring series renders client-side instead
// of collapsing to a single occurrence.
func TestEWSRecurrenceXML(t *testing.T) {
	// 2026-06-01 is a Monday.
	start := time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		rrule  string
		start  time.Time
		want   []string
		absent []string
	}{
		{
			name:  "weekly with explicit days and count boundary",
			rrule: "FREQ=WEEKLY;BYDAY=MO,WE;COUNT=10",
			start: start,
			want: []string{
				"<t:WeeklyRecurrence>", "<t:Interval>1</t:Interval>",
				"<t:DaysOfWeek>Monday Wednesday</t:DaysOfWeek>",
				"<t:NumberedRecurrence>", "<t:NumberOfOccurrences>10</t:NumberOfOccurrences>",
				"<t:StartDate>2026-06-01</t:StartDate>",
			},
		},
		{
			name:  "weekly without BYDAY falls back to start weekday, no-end",
			rrule: "FREQ=WEEKLY",
			start: start,
			want: []string{
				"<t:WeeklyRecurrence>", "<t:DaysOfWeek>Monday</t:DaysOfWeek>",
				"<t:NoEndRecurrence>",
			},
		},
		{
			name:  "daily with interval and until boundary",
			rrule: "FREQ=DAILY;INTERVAL=3;UNTIL=20261231T000000Z",
			start: start,
			want: []string{
				"<t:DailyRecurrence><t:Interval>3</t:Interval></t:DailyRecurrence>",
				"<t:EndDateRecurrence>", "<t:EndDate>2026-12-31</t:EndDate>",
			},
		},
		{
			name:  "absolute monthly by month-day",
			rrule: "FREQ=MONTHLY;BYMONTHDAY=15",
			start: start,
			want: []string{
				"<t:AbsoluteMonthlyRecurrence>", "<t:DayOfMonth>15</t:DayOfMonth>",
			},
			absent: []string{"<t:RelativeMonthlyRecurrence>"},
		},
		{
			name:  "relative monthly (second Monday)",
			rrule: "FREQ=MONTHLY;BYDAY=2MO",
			start: start,
			want: []string{
				"<t:RelativeMonthlyRecurrence>", "<t:DaysOfWeek>Monday</t:DaysOfWeek>",
				"<t:DayOfWeekIndex>Second</t:DayOfWeekIndex>",
			},
		},
		{
			name:  "absolute yearly",
			rrule: "FREQ=YEARLY;BYMONTH=6;BYMONTHDAY=1",
			start: start,
			want: []string{
				"<t:AbsoluteYearlyRecurrence>", "<t:DayOfMonth>1</t:DayOfMonth>",
				"<t:Month>June</t:Month>",
			},
		},
		{
			name:  "relative yearly (last Friday of November)",
			rrule: "FREQ=YEARLY;BYMONTH=11;BYDAY=-1FR",
			start: start,
			want: []string{
				"<t:RelativeYearlyRecurrence>", "<t:DaysOfWeek>Friday</t:DaysOfWeek>",
				"<t:DayOfWeekIndex>Last</t:DayOfWeekIndex>", "<t:Month>November</t:Month>",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ewsRecurrenceXML(tc.rrule, tc.start)
			if !strings.HasPrefix(got, "<t:Recurrence>") || !strings.HasSuffix(got, "</t:Recurrence>") {
				t.Fatalf("not wrapped in <t:Recurrence>: %s", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %q in:\n%s", a, got)
				}
			}
		})
	}

	// A blank or absent RRULE yields no recurrence block.
	if got := ewsRecurrenceXML("", start); got != "" {
		t.Errorf("empty RRULE should yield no recurrence, got %q", got)
	}
}

// TestICalComponentScopesVEVENT guards the bug where a timezone-anchored event's
// VTIMEZONE (which carries its own DST DTSTART/RRULE and precedes the VEVENT)
// leaked into the calendar projection: property reads must come from the VEVENT,
// so the event's weekly rule is not mistaken for the zone's yearly DST rule.
func TestICalComponentScopesVEVENT(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\n" +
		"BEGIN:DAYLIGHT\r\nDTSTART:19700308T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nEND:DAYLIGHT\r\n" +
		"END:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:evt-1\r\nSUMMARY:Weekly standup\r\n" +
		"DTSTART;TZID=America/New_York:20260601T140000\r\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"

	ev := icalComponent(raw, "VEVENT")
	// The whole-document scan would return the VTIMEZONE's yearly DST rule.
	if got := extractDirProp(raw, "RRULE"); got != "FREQ=YEARLY;BYMONTH=3;BYDAY=2SU" {
		t.Fatalf("precondition: whole-doc RRULE = %q (VTIMEZONE rule expected)", got)
	}
	if got := extractDirProp(ev, "RRULE"); got != "FREQ=WEEKLY;BYDAY=MO,WE" {
		t.Errorf("VEVENT-scoped RRULE = %q, want the event's weekly rule", got)
	}
	if got := extractDirProp(ev, "SUMMARY"); got != "Weekly standup" {
		t.Errorf("VEVENT-scoped SUMMARY = %q", got)
	}
	if tzid := extractDirPropParam(ev, "DTSTART", "TZID"); tzid != "America/New_York" {
		t.Errorf("VEVENT-scoped DTSTART TZID = %q", tzid)
	}
}
