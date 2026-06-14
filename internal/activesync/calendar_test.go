package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// stubCalendar is a controllable CalendarSource: ListItems returns whatever
// events the test stages, so a test can simulate adds/changes/deletes between
// syncs by mutating the slice.
type stubCalendar struct{ items []CalendarItem }

func (c *stubCalendar) ListItems(string, string) ([]CalendarItem, error) { return c.items, nil }

// stubCalendarMutator records the up-sync changes applied to it and reflects a
// create into src so the reconciliation path (a just-added item must not echo
// back) is exercised. failOn fails the command whose subject/server id matches.
type stubCalendarMutator struct {
	src             *stubCalendar
	created, edited map[string]CalendarItem
	deleted         []string
	failOn          string
}

func (m *stubCalendarMutator) CreateItem(_, _ string, it CalendarItem) (string, error) {
	if it.Subject == m.failOn {
		return "", errMutator
	}
	id := it.UID
	if id == "" {
		id = "srv-" + it.Subject
	}
	it.ServerID, it.ETag = id, "e1"
	if m.created == nil {
		m.created = map[string]CalendarItem{}
	}
	m.created[id] = it
	if m.src != nil {
		m.src.items = append(m.src.items, it)
	}
	return id, nil
}

func (m *stubCalendarMutator) UpdateItem(_, _, serverID string, it CalendarItem) error {
	if serverID == m.failOn {
		return errMutator
	}
	if m.edited == nil {
		m.edited = map[string]CalendarItem{}
	}
	m.edited[serverID] = it
	return nil
}

func (m *stubCalendarMutator) DeleteItem(_, _, serverID string) error {
	if serverID == m.failOn {
		return errMutator
	}
	m.deleted = append(m.deleted, serverID)
	return nil
}

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

// TestBuildICalEventRoundTrip verifies the write projection produces canonical
// iCalendar that the read projection parses back identically — an event authored
// on a phone must survive the store round-trip so it renders the same on every
// surface.
func TestBuildICalEventRoundTrip(t *testing.T) {
	src := CalendarItem{
		UID: "rt-1", Subject: "Quarterly review", Body: "Bring; notes, and slides",
		Location: "Boardroom", Start: time.Date(2026, 9, 20, 14, 0, 0, 0, time.UTC),
		End: time.Date(2026, 9, 20, 15, 30, 0, 0, time.UTC), Sensitivity: "2", BusyStatus: "2",
	}
	ics := BuildICalEvent(src)
	got := CalendarItemFromICal("rt-1", "e", ics)
	if got.UID != src.UID || got.Subject != src.Subject || got.Location != src.Location {
		t.Fatalf("text fields lost in round-trip: %+v", got)
	}
	if got.Body != src.Body {
		t.Fatalf("DESCRIPTION escaping lost in round-trip: %q != %q", got.Body, src.Body)
	}
	if !got.Start.Equal(src.Start) || !got.End.Equal(src.End) {
		t.Fatalf("times lost: start %v end %v", got.Start, got.End)
	}
	if got.Sensitivity != "2" {
		t.Fatalf("CLASS:PRIVATE not round-tripped: sensitivity=%q", got.Sensitivity)
	}
}

// TestCalendarItemFromAppData verifies the client ApplicationData parse reads the
// scheduling fields from the Calendar page and the body/location from AirSyncBase
// — the shape a 16.x client sends in an up-sync Add/Change.
func TestCalendarItemFromAppData(t *testing.T) {
	app := &wbxml.Element{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: []*wbxml.Element{
		{Page: wbxml.PageCalendar, Name: "UID", Text: "c-1"},
		{Page: wbxml.PageCalendar, Name: "Subject", Text: "Sync up"},
		{Page: wbxml.PageCalendar, Name: "StartTime", Text: "20260920T140000Z"},
		{Page: wbxml.PageCalendar, Name: "EndTime", Text: "20260920T150000Z"},
		{Page: wbxml.PageCalendar, Name: "AllDayEvent", Text: "0"},
		{Page: wbxml.PageAirSyncBase, Name: "Body", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSyncBase, Name: "Data", Text: "agenda"},
		}},
		{Page: wbxml.PageAirSyncBase, Name: "Location", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSyncBase, Name: "DisplayName", Text: "Room 9"},
		}},
	}}
	it := calendarItemFromAppData(app)
	if it.UID != "c-1" || it.Subject != "Sync up" || it.Body != "agenda" || it.Location != "Room 9" {
		t.Fatalf("parsed fields wrong: %+v", it)
	}
	if it.Start.UTC().Format(compactDateTime) != "20260920T140000Z" {
		t.Fatalf("StartTime parse wrong: %v", it.Start)
	}
}

// calAddCmd builds a client up-sync Add command (ClientId + page-4 ApplicationData).
func calAddCmd(clientID, uid, subject, start string) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageAirSync, Name: "Add", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "ClientId", Text: clientID},
		{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: []*wbxml.Element{
			{Page: wbxml.PageCalendar, Name: "UID", Text: uid},
			{Page: wbxml.PageCalendar, Name: "Subject", Text: subject},
			{Page: wbxml.PageCalendar, Name: "StartTime", Text: start},
			{Page: wbxml.PageCalendar, Name: "EndTime", Text: start},
		}},
	}}
}

// doCalSyncCmds drives a calendar Sync carrying client up-sync Commands.
func doCalSyncCmds(t *testing.T, s *Server, syncKey string, cmds ...*wbxml.Element) *wbxml.Element {
	t.Helper()
	coll := []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: CalendarCollectionID("CAL1")},
		{Page: wbxml.PageAirSync, Name: "Commands", Children: cmds},
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

// TestCalendarUpSync verifies the client up-sync path: an Add creates the event
// through the mutator and is echoed as an Add response mapping the ClientId to
// the assigned ServerId, without being re-emitted as a server-side change in the
// same response; a Delete removes it through the mutator.
func TestCalendarUpSync(t *testing.T) {
	cal := &stubCalendar{}
	mut := &stubCalendarMutator{src: cal}
	s := calServer(cal)
	s.SetCalendarMutator(mut)
	doCalSync(t, s, "0", 0) // prime -> key 1

	coll := doCalSyncCmds(t, s, "1", calAddCmd("c1", "ev-up-1", "Planning", "20260920T140000Z"))

	add := coll.Sub("Responses").Sub("Add")
	if add == nil || add.Sub("ClientId").Text != "c1" || add.Sub("ServerId").Text != "ev-up-1" {
		t.Fatalf("Add response must map ClientId->ServerId: %v", add)
	}
	if add.Sub("Status").Text != syncStatusSuccess {
		t.Fatalf("Add response status = %v, want 1", add.Sub("Status"))
	}
	if mut.created["ev-up-1"].Subject != "Planning" {
		t.Fatalf("CreateItem not applied with parsed fields: %+v", mut.created)
	}
	// Reconciliation: the client's own Add must not echo back as a server Command.
	if countOps(coll, "Add") != 0 {
		t.Fatalf("client's own Add must not be re-emitted as a server Add: %d", countOps(coll, "Add"))
	}

	// Delete the event on the client; the mutator removes it from the canonical store.
	cur := coll.Sub("SyncKey").Text
	doCalSyncCmds(t, s, cur, deleteCmd("ev-up-1"))
	if len(mut.deleted) != 1 || mut.deleted[0] != "ev-up-1" {
		t.Fatalf("DeleteItem not applied: %v", mut.deleted)
	}
}
