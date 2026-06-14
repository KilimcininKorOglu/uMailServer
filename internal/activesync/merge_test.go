package activesync

import (
	"strings"
	"testing"
	"time"
)

// crlf joins lines with the CRLF endings the canonical store uses, so the test
// fixtures match what GetEvent/GetContact actually return.
func crlf(lines ...string) string {
	return strings.Join(lines, "\r\n") + "\r\n"
}

// TestMergeICalEventPreservesRecurrenceAndAlarm proves that editing only the
// modeled fields of an event on a phone does not erase the canonical event's
// recurrence, attendees, organizer, or alarm — a write-back that truncated them
// would corrupt the shared store, not converge on it. The two DESCRIPTION lines
// (one at VEVENT level, one inside the VALARM) are the discriminating case: a
// naive name-based filter would drop both and rebuild only the event's, losing
// the alarm's; the nesting-aware merge must replace the event's and keep the
// alarm's verbatim.
func TestMergeICalEventPreservesRecurrenceAndAlarm(t *testing.T) {
	existing := crlf(
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//test//EN",
		"BEGIN:VEVENT",
		"UID:evt-1",
		"DTSTART:20260101T090000Z",
		"DTEND:20260101T100000Z",
		"SUMMARY:Old standup",
		"DESCRIPTION:Old body",
		"RRULE:FREQ=WEEKLY;BYDAY=MO",
		"ATTENDEE;CN=Bob:mailto:bob@example.com",
		"ORGANIZER:mailto:alice@example.com",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"DESCRIPTION:Standup reminder",
		"TRIGGER:-PT15M",
		"END:VALARM",
		"END:VEVENT",
		"END:VCALENDAR",
	)
	it := CalendarItem{
		UID:     "evt-1",
		Subject: "New standup",
		Body:    "New body",
		Start:   time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
		End:     time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
	}
	out := MergeICalEvent(existing, it)

	// Modeled fields carry the client's new values.
	mustContain(t, out, "SUMMARY:New standup")
	mustContain(t, out, "DESCRIPTION:New body")
	mustNotContain(t, out, "Old standup")
	mustNotContain(t, out, "DESCRIPTION:Old body")

	// Unmodeled top-level properties survive the edit.
	mustContain(t, out, "RRULE:FREQ=WEEKLY;BYDAY=MO")
	mustContain(t, out, "ATTENDEE;CN=Bob:mailto:bob@example.com")
	mustContain(t, out, "ORGANIZER:mailto:alice@example.com")

	// The nested VALARM is preserved verbatim, including its own DESCRIPTION.
	mustContain(t, out, "BEGIN:VALARM")
	mustContain(t, out, "DESCRIPTION:Standup reminder")
	mustContain(t, out, "END:VALARM")

	// No duplicate identity, and preserved lines land inside the VEVENT.
	if n := strings.Count(out, "UID:evt-1"); n != 1 {
		t.Fatalf("UID appears %d times, want 1:\n%s", n, out)
	}
	if i, j := strings.Index(out, "RRULE:"), strings.Index(out, "END:VEVENT"); i < 0 || i > j {
		t.Fatalf("RRULE not inside VEVENT (rrule=%d end=%d):\n%s", i, j, out)
	}
}

// TestMergeVCardPreservesExtendedFields proves that editing only a phone number
// on a card does not erase the card's photo, categories, or other extended
// properties that the EAS contact projection cannot represent.
func TestMergeVCardPreservesExtendedFields(t *testing.T) {
	existing := crlf(
		"BEGIN:VCARD",
		"VERSION:3.0",
		"UID:con-1",
		"N:Doe;Jane;;;",
		"FN:Jane Doe",
		"TEL;TYPE=CELL:+1-555-0100",
		"PHOTO;ENCODING=b;TYPE=JPEG:/9j/abc",
		"CATEGORIES:VIP,Friends",
		"X-CUSTOM-FIELD:keep me",
		"END:VCARD",
	)
	it := ContactItem{UID: "con-1", FirstName: "Jane", LastName: "Doe", MobilePhone: "+1-555-0199"}
	out := MergeVCard(existing, it)

	mustContain(t, out, "TEL;TYPE=CELL:+1-555-0199") // modeled field updated
	mustNotContain(t, out, "+1-555-0100")            // old value gone
	mustContain(t, out, "PHOTO;ENCODING=b;TYPE=JPEG:/9j/abc")
	mustContain(t, out, "CATEGORIES:VIP,Friends")
	mustContain(t, out, "X-CUSTOM-FIELD:keep me")
	if n := strings.Count(out, "VERSION:3.0"); n != 1 {
		t.Fatalf("VERSION appears %d times, want 1:\n%s", n, out)
	}
}

// TestMergeVTODOPreservesRecurrenceAndCategories proves that editing a to-do's
// subject on a phone does not erase its recurrence, categories, or reminder.
func TestMergeVTODOPreservesRecurrenceAndCategories(t *testing.T) {
	existing := crlf(
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//test//EN",
		"BEGIN:VTODO",
		"UID:tsk-1",
		"SUMMARY:Old chore",
		"STATUS:NEEDS-ACTION",
		"RRULE:FREQ=DAILY",
		"CATEGORIES:Home",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"DESCRIPTION:Do it",
		"TRIGGER:-PT1H",
		"END:VALARM",
		"END:VTODO",
		"END:VCALENDAR",
	)
	it := TaskItem{UID: "tsk-1", Subject: "New chore"}
	out := MergeVTODO(existing, it)

	mustContain(t, out, "SUMMARY:New chore")
	mustNotContain(t, out, "Old chore")
	mustContain(t, out, "RRULE:FREQ=DAILY")
	mustContain(t, out, "CATEGORIES:Home")
	mustContain(t, out, "BEGIN:VALARM")
	mustContain(t, out, "DESCRIPTION:Do it")
}

// TestMergeFallsBackToFreshBuild proves that a Change against a missing record
// (or any empty existing) degrades to a plain Build*, never a merge against an
// empty object.
func TestMergeFallsBackToFreshBuild(t *testing.T) {
	it := CalendarItem{UID: "evt-x", Subject: "Solo", Start: time.Date(2026, 2, 2, 8, 0, 0, 0, time.UTC)}
	if got, want := MergeICalEvent("", it), BuildICalEvent(it); got != want {
		t.Fatalf("empty-existing merge != fresh build:\ngot:  %q\nwant: %q", got, want)
	}
	if got, want := MergeICalEvent("   \r\n", it), BuildICalEvent(it); got != want {
		t.Fatalf("blank-existing merge != fresh build")
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("output missing %q:\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("output unexpectedly contains %q:\n%s", needle, haystack)
	}
}
