package jmap

import (
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/caldav"
	"github.com/umailserver/umailserver/internal/semcore"
)

// newCalendarTestServer builds a JMAP server wired to a real semcore-backed
// calendar store — the same store EWS and CalDAV use — so the tests exercise the
// canonical cross-protocol path, not a mock.
func newCalendarTestServer(t *testing.T) (*Server, *caldav.CollabStore) {
	t.Helper()
	store, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	cs := caldav.NewCollabStore(store.Collaboration(), store.Identity())
	return &Server{calStore: cs}, cs
}

// TestCalendarEventSetThenGet verifies an event created over JMAP lands in the
// canonical store and reads back with its core JSCalendar fields intact.
func TestCalendarEventSetThenGet(t *testing.T) {
	srv, cs := newCalendarTestServer(t)
	user := "alice@ex.test"

	setResp := srv.handleCalendarEventSet(user, MethodCall{
		ID:   "c1",
		Name: "CalendarEvent/set",
		Args: map[string]interface{}{
			"accountId": user,
			"create": map[string]interface{}{
				"new1": map[string]interface{}{
					"@type":       "Event",
					"uid":         "evt-1",
					"title":       "Standup",
					"description": "Daily sync",
					"start":       "2026-06-10T09:00:00",
					"timeZone":    "Etc/UTC",
					"duration":    "PT30M",
					"locations":   map[string]interface{}{"1": map[string]interface{}{"@type": "Location", "name": "Room A"}},
				},
			},
		},
	}, map[string]string{})

	created := asMap(setResp.Args["created"])
	obj := asMap(created["new1"])
	if obj == nil {
		t.Fatalf("expected new1 created, got: %+v", setResp.Args)
	}
	if obj["id"] != "evt-1" {
		t.Errorf("expected id evt-1, got %v", obj["id"])
	}

	// Cross-protocol: the canonical store (EWS/CalDAV read path) holds the iCal.
	raws, err := cs.GetEvents(user, jmapDefaultCalendarID)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(raws) != 1 || !strings.Contains(raws[0], "SUMMARY:Standup") || !strings.Contains(raws[0], "UID:evt-1") {
		t.Fatalf("canonical iCal missing or wrong: %v", raws)
	}
	if !strings.Contains(raws[0], "DTSTART:20260610T090000Z") {
		t.Errorf("expected UTC DTSTART in canonical iCal: %v", raws[0])
	}

	// JMAP read-back projects the JSCalendar object.
	getResp := srv.handleCalendarEventGet(user, MethodCall{
		ID: "c2", Name: "CalendarEvent/get",
		Args: map[string]interface{}{"accountId": user, "ids": []interface{}{"evt-1"}},
	})
	list := asSlice(getResp.Args["list"])
	if len(list) != 1 {
		t.Fatalf("expected 1 event, got %d", len(list))
	}
	ev := asMap(list[0])
	if ev["title"] != "Standup" || ev["description"] != "Daily sync" {
		t.Errorf("title/description round-trip mismatch: %+v", ev)
	}
	if ev["start"] != "2026-06-10T09:00:00" || ev["timeZone"] != "Etc/UTC" {
		t.Errorf("start/timeZone round-trip mismatch: %+v", ev)
	}
	if ev["duration"] != "PT30M" {
		t.Errorf("expected duration PT30M, got %v", ev["duration"])
	}
	locs := asMap(ev["locations"])
	loc := asMap(locs["1"])
	if loc["name"] != "Room A" {
		t.Errorf("location round-trip mismatch: %+v", ev["locations"])
	}
}

// TestCalendarEventUpdatePreservesUnknownProperties verifies an update over JMAP
// rewrites only the patched fields and carries verbatim any iCal property JMAP
// does not model (e.g. RRULE), so CalDAV/EWS sync data is never silently lost.
func TestCalendarEventUpdatePreservesUnknownProperties(t *testing.T) {
	srv, cs := newCalendarTestServer(t)
	user := "bob@ex.test"

	// Seed the canonical store directly with a recurring event (as CalDAV/EWS would).
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:rec-1\r\n" +
		"DTSTART:20260601T100000Z\r\nSUMMARY:Weekly\r\nRRULE:FREQ=WEEKLY;COUNT=5\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := cs.SaveEvent(user, jmapDefaultCalendarID, &caldav.CalendarEvent{UID: "rec-1"}, ics); err != nil {
		t.Fatalf("seed SaveEvent: %v", err)
	}

	resp := srv.handleCalendarEventSet(user, MethodCall{
		ID: "u1", Name: "CalendarEvent/set",
		Args: map[string]interface{}{
			"accountId": user,
			"update": map[string]interface{}{
				"rec-1": map[string]interface{}{"title": "Weekly sync"},
			},
		},
	}, nil)
	updated := asMap(resp.Args["updated"])
	if _, ok := updated["rec-1"]; !ok {
		t.Fatalf("expected rec-1 updated, got: %+v", resp.Args)
	}

	raw, err := cs.GetEvent(user, jmapDefaultCalendarID, "rec-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if !strings.Contains(raw, "SUMMARY:Weekly sync") {
		t.Errorf("title not updated: %v", raw)
	}
	if !strings.Contains(raw, "RRULE:FREQ=WEEKLY;COUNT=5") {
		t.Errorf("RRULE dropped on update — cross-protocol data loss: %v", raw)
	}
}

// TestCalendarNotSupportedWhenUnwired verifies handlers report notSupported when
// the canonical store is not wired.
func TestCalendarNotSupportedWhenUnwired(t *testing.T) {
	srv := &Server{}
	resp := srv.handleCalendarEventGet("alice@ex.test", MethodCall{
		ID: "c1", Name: "CalendarEvent/get",
		Args: map[string]interface{}{"accountId": "alice@ex.test"},
	})
	if resp.Name != "error" || resp.Args["type"] != "notSupported" {
		t.Errorf("expected notSupported error, got %+v", resp)
	}
}

func TestParseISODuration(t *testing.T) {
	cases := map[string]int64{ // seconds
		"PT30M":   1800,
		"PT1H":    3600,
		"PT1H30M": 5400,
		"P1D":     86400,
		"P1DT2H":  93600,
		"PT45S":   45,
		"P1W":     604800,
	}
	for in, wantSec := range cases {
		d, err := parseISODuration(in)
		if err != nil {
			t.Errorf("parseISODuration(%q): %v", in, err)
			continue
		}
		if int64(d.Seconds()) != wantSec {
			t.Errorf("parseISODuration(%q) = %ds, want %ds", in, int64(d.Seconds()), wantSec)
		}
	}
	if _, err := parseISODuration("30M"); err == nil {
		t.Error("expected error for duration without leading P")
	}
}
