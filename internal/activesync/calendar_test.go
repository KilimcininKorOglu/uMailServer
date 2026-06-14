package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// stubCalendar is a controllable CalendarSource: ListItems returns whatever
// events the test stages, so a test can simulate adds/changes/deletes between
// syncs by mutating the slice.
type stubCalendar struct{ items []CalendarItem }

func (c *stubCalendar) ListItems(string, string) ([]CalendarItem, error) { return c.items, nil }

// TestCalendarItemFromICal verifies the iCalendar projection resolves the three
// DTSTART forms to the correct absolute instant and decodes the descriptive
// fields — because a calendar item displayed on a phone must land at the right
// moment regardless of how the canonical event encoded its time.
func TestCalendarItemFromICal(t *testing.T) {
	// A TZID-anchored civil-local time must convert to the right UTC instant:
	// Europe/Istanbul is UTC+03, so 10:00 local is 07:00Z.
	tz := CalendarItemFromICal("id1", "etagA", "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\n"+
		"UID:evt-123\r\nSUMMARY:Team Sync\r\nLOCATION:Room 4\r\nDESCRIPTION:Weekly\\nstandup\r\n"+
		"DTSTART;TZID=Europe/Istanbul:20260615T100000\r\nDTEND;TZID=Europe/Istanbul:20260615T110000\r\n"+
		"DTSTAMP:20260601T080000Z\r\nCLASS:PRIVATE\r\nTRANSP:TRANSPARENT\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if tz.UID != "evt-123" || tz.Subject != "Team Sync" || tz.Location != "Room 4" {
		t.Fatalf("fields wrong: %+v", tz)
	}
	if tz.Body != "Weekly\nstandup" {
		t.Fatalf("DESCRIPTION unescaping wrong: %q", tz.Body)
	}
	if got := tz.Start.UTC().Format(compactDateTime); got != "20260615T070000Z" {
		t.Fatalf("TZID start = %q, want 20260615T070000Z (10:00 +03)", got)
	}
	if got := tz.End.UTC().Format(compactDateTime); got != "20260615T080000Z" {
		t.Fatalf("TZID end = %q, want 20260615T080000Z", got)
	}
	if tz.Sensitivity != "2" { // CLASS:PRIVATE
		t.Fatalf("Sensitivity = %q, want 2 (PRIVATE)", tz.Sensitivity)
	}
	if tz.BusyStatus != "0" { // TRANSP:TRANSPARENT -> free
		t.Fatalf("BusyStatus = %q, want 0 (free)", tz.BusyStatus)
	}
	if tz.AllDay {
		t.Fatalf("timed event must not be all-day")
	}

	// A UTC DTSTART (trailing Z) is taken verbatim; defaults apply (busy/normal).
	utc := CalendarItemFromICal("id2", "e", "BEGIN:VEVENT\r\nUID:u2\r\nSUMMARY:UTC\r\nDTSTART:20260701T090000Z\r\nDTEND:20260701T093000Z\r\nEND:VEVENT")
	if got := utc.Start.UTC().Format(compactDateTime); got != "20260701T090000Z" {
		t.Fatalf("UTC start = %q, want 20260701T090000Z", got)
	}
	if utc.BusyStatus != "2" || utc.Sensitivity != "0" {
		t.Fatalf("defaults wrong: busy=%q sens=%q, want 2/0", utc.BusyStatus, utc.Sensitivity)
	}

	// A VALUE=DATE DTSTART is an all-day event at UTC midnight.
	allday := CalendarItemFromICal("id3", "e", "BEGIN:VEVENT\r\nUID:u3\r\nSUMMARY:Holiday\r\nDTSTART;VALUE=DATE:20260704\r\nDTEND;VALUE=DATE:20260705\r\nEND:VEVENT")
	if !allday.AllDay {
		t.Fatalf("VALUE=DATE must be all-day")
	}
	if got := allday.Start.UTC().Format(compactDateTime); got != "20260704T000000Z" {
		t.Fatalf("all-day start = %q, want 20260704T000000Z", got)
	}
}

// TestCalendarAppData verifies the wire projection emits the calendar times in
// Compact DateTime (the MS-ASCAL format, distinct from the mail DateReceived
// format) with the UTC Timezone blob, and routes body/location through
// AirSyncBase as a 16.x client expects.
func TestCalendarAppData(t *testing.T) {
	it := CalendarItemFromICal("c1", "e", "BEGIN:VEVENT\r\nUID:u\r\nSUMMARY:Demo\r\nLOCATION:HQ\r\nDESCRIPTION:notes\r\nDTSTART:20260615T120000Z\r\nDTEND:20260615T130000Z\r\nDTSTAMP:20260601T000000Z\r\nEND:VEVENT")
	els := calendarAppData(it)
	find := func(page byte, name string) *wbxml.Element {
		for _, e := range els {
			if e.Page == page && e.Name == name {
				return e
			}
		}
		return nil
	}
	if e := find(wbxml.PageCalendar, "StartTime"); e == nil || e.Text != "20260615T120000Z" {
		t.Fatalf("StartTime element = %v, want compact 20260615T120000Z", e)
	}
	if e := find(wbxml.PageCalendar, "EndTime"); e == nil || e.Text != "20260615T130000Z" {
		t.Fatalf("EndTime element = %v", e)
	}
	if e := find(wbxml.PageCalendar, "Subject"); e == nil || e.Text != "Demo" {
		t.Fatalf("Subject element = %v", e)
	}
	if e := find(wbxml.PageCalendar, "UID"); e == nil || e.Text != "u" {
		t.Fatalf("UID element = %v", e)
	}
	if e := find(wbxml.PageCalendar, "Timezone"); e == nil || e.Text != utcTimeZoneBlob {
		t.Fatalf("Timezone element must carry the UTC blob")
	}
	body := find(wbxml.PageAirSyncBase, "Body")
	if body == nil || body.Sub("Data").Text != "notes" {
		t.Fatalf("body must ride AirSyncBase Body>Data: %v", body)
	}
	loc := find(wbxml.PageAirSyncBase, "Location")
	if loc == nil || loc.Sub("DisplayName").Text != "HQ" {
		t.Fatalf("location must ride AirSyncBase Location>DisplayName: %v", loc)
	}
}

// TestDiffCalendar exercises the enumerate-and-diff cursor a calendar uses in
// place of a change journal: a fresh cursor adds everything; an unchanged set
// produces nothing; an advanced ETag is a Change; a vanished id is a Delete; and
// a windowed drain converges by carrying the emitted changes into the cursor.
func TestDiffCalendar(t *testing.T) {
	a := CalendarItem{ServerID: "a", ETag: "1"}
	b := CalendarItem{ServerID: "b", ETag: "1"}

	// Fresh cursor -> both are Adds.
	cmds, cur, more := diffCalendar(map[string]string{}, []CalendarItem{a, b}, 0)
	if len(cmds) != 2 || cmds[0].op != "Add" || more {
		t.Fatalf("fresh sync must add all: %+v more=%v", cmds, more)
	}

	// Re-sync with the produced cursor and the same set -> no changes.
	prev := decodeCalCursor(cur)
	cmds, _, _ = diffCalendar(prev, []CalendarItem{a, b}, 0)
	if len(cmds) != 0 {
		t.Fatalf("unchanged set must produce no commands: %+v", cmds)
	}

	// Advance b's ETag and drop a -> one Change, one Delete.
	b2 := CalendarItem{ServerID: "b", ETag: "2"}
	cmds, _, _ = diffCalendar(prev, []CalendarItem{b2}, 0)
	var change, del int
	for _, c := range cmds {
		switch c.op {
		case "Change":
			change++
		case "Delete":
			del++
		}
	}
	if change != 1 || del != 1 {
		t.Fatalf("expected 1 Change + 1 Delete, got %+v", cmds)
	}

	// Windowed drain: window of 1 emits one command, sets more, and the carried
	// cursor lets the next diff emit the remainder (convergence).
	cmds, cur, more = diffCalendar(map[string]string{}, []CalendarItem{a, b}, 1)
	if len(cmds) != 1 || !more {
		t.Fatalf("windowed drain must emit 1 with more=true: %+v more=%v", cmds, more)
	}
	cmds, _, more = diffCalendar(decodeCalCursor(cur), []CalendarItem{a, b}, 1)
	if len(cmds) != 1 || more {
		t.Fatalf("second window must emit the remaining 1 with more=false: %+v more=%v", cmds, more)
	}
}

// calServer builds an EAS server wired for calendar Sync. A mail source is set
// too because handleSync's guard requires one (production always wires both).
func calServer(cal CalendarSource) *Server {
	s := NewServer(allowAuth)
	s.SetMailSource(&stubMail{})
	s.SetCalendarSource(cal)
	s.SetSyncState(&memSyncState{m: map[string]string{}})
	return s
}

// doCalSync drives a Sync against the calendar collection and returns the
// response Collection.
func doCalSync(t *testing.T, s *Server, syncKey string, window int) *wbxml.Element {
	t.Helper()
	coll := []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: CalendarCollectionID("CAL1")},
	}
	if window > 0 {
		coll = append(coll, &wbxml.Element{Page: wbxml.PageAirSync, Name: "WindowSize", Text: strconv.Itoa(window)})
	}
	body, err := wbxml.Marshal(&wbxml.Element{Page: wbxml.PageAirSync, Name: "Sync", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "Collections", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSync, Name: "Collection", Children: coll},
		}},
	}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=Sync&DeviceId=DEV1", bytes.NewReader(body))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("calendar Sync status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return resp.Sub("Collections").Sub("Collection")
}

// TestCalendarSyncFlow exercises the calendar Sync end to end through the
// dispatcher: SyncKey 0 primes; the first real sync streams the event as an Add
// with the compact StartTime; an unchanged re-sync is empty; advancing the ETag
// produces a Change; removing the event produces a Delete. This proves the "cal:"
// CollectionId routes to the calendar path, not the mail path.
func TestCalendarSyncFlow(t *testing.T) {
	cal := &stubCalendar{items: []CalendarItem{
		CalendarItemFromICal("ev1", "e1", "BEGIN:VEVENT\r\nUID:ev1\r\nSUMMARY:Standup\r\nDTSTART:20260615T090000Z\r\nDTEND:20260615T093000Z\r\nEND:VEVENT"),
	}}
	s := calServer(cal)

	prime := doCalSync(t, s, "0", 0)
	if prime.Sub("SyncKey").Text != "1" || prime.Sub("Status").Text != syncStatusSuccess {
		t.Fatalf("prime wrong: %v", prime)
	}
	if prime.Sub("Commands") != nil {
		t.Fatalf("prime must carry no commands")
	}

	first := doCalSync(t, s, "1", 0)
	if first.Sub("SyncKey").Text != "2" {
		t.Fatalf("first sync key = %q, want 2", first.Sub("SyncKey").Text)
	}
	if countOps(first, "Add") != 1 {
		t.Fatalf("first sync must Add the event: %d", countOps(first, "Add"))
	}
	add := first.Sub("Commands").Sub("Add")
	if add.Sub("ServerId").Text != "ev1" {
		t.Fatalf("Add ServerId = %q, want ev1 (collab item id)", add.Sub("ServerId").Text)
	}
	if st := add.Sub("ApplicationData").Sub("StartTime"); st == nil || st.Text != "20260615T090000Z" {
		t.Fatalf("Add StartTime = %v, want compact 20260615T090000Z", st)
	}

	// Unchanged set -> no commands.
	second := doCalSync(t, s, "2", 0)
	if second.Sub("Commands") != nil {
		t.Fatalf("unchanged calendar must produce no commands")
	}

	// Advance the ETag -> a Change.
	cal.items[0] = CalendarItemFromICal("ev1", "e2", "BEGIN:VEVENT\r\nUID:ev1\r\nSUMMARY:Standup moved\r\nDTSTART:20260615T100000Z\r\nDTEND:20260615T103000Z\r\nEND:VEVENT")
	third := doCalSync(t, s, "3", 0)
	if countOps(third, "Change") != 1 {
		t.Fatalf("ETag change must produce a Change: %d", countOps(third, "Change"))
	}

	// Remove the event -> a Delete.
	cal.items = nil
	fourth := doCalSync(t, s, "4", 0)
	if countOps(fourth, "Delete") != 1 {
		t.Fatalf("removed event must produce a Delete: %d", countOps(fourth, "Delete"))
	}
}
