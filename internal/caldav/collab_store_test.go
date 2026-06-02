package caldav

import (
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestCollabStoreCrossProtocol verifies the semcore-backed calendar Store writes
// events into the same calendar folder EWS reads from, so an event created
// through the webmail/CalDAV surface is visible over EWS (one source of truth),
// and that upsert-by-UID keeps a single record across edits.
func TestCollabStoreCrossProtocol(t *testing.T) {
	store, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})

	cs := NewCollabStore(store.Collaboration(), store.Identity())
	user := "alice@ex.test"

	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:evt-1\r\nSUMMARY:Standup\r\n" +
		"DTSTART:20260603T090000Z\r\nDTEND:20260603T100000Z\r\nEND:VEVENT\r\nEND:VCALENDAR"
	ev := &CalendarEvent{UID: "evt-1", Summary: "Standup"}
	if err := cs.SaveEvent(user, defaultCalendarID, ev, ics); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	// Round-trip through the Store surface (webmail/CalDAV view).
	got, err := cs.GetEvent(user, defaultCalendarID, "evt-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if !strings.Contains(got, "UID:evt-1") {
		t.Errorf("GetEvent missing event, got: %q", got)
	}
	events, err := cs.GetEvents(user, defaultCalendarID)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	// Cross-protocol: the event lands in the calendar folder EWS reads, so the
	// EWS read path (ListCalendarItemsByFolder) sees the webmail-created event.
	folderID, err := store.Identity().GetFolderID(user, "calendar")
	if err != nil {
		t.Fatalf("GetFolderID: %v", err)
	}
	items, err := store.Collaboration().ListCalendarItemsByFolder(folderID)
	if err != nil {
		t.Fatalf("ListCalendarItemsByFolder: %v", err)
	}
	if len(items) != 1 || items[0].IcalUID != "evt-1" {
		t.Fatalf("EWS read path does not see the webmail-created event: %+v", items)
	}

	// Upsert by UID: editing the same UID must keep exactly one record.
	ics2 := strings.Replace(ics, "Standup", "Standup v2", 1)
	ev.Summary = "Standup v2"
	if err := cs.SaveEvent(user, defaultCalendarID, ev, ics2); err != nil {
		t.Fatalf("SaveEvent update: %v", err)
	}
	events, err = cs.GetEvents(user, defaultCalendarID)
	if err != nil {
		t.Fatalf("GetEvents after update: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event after upsert, got %d", len(events))
	}
	if !strings.Contains(events[0], "Standup v2") {
		t.Errorf("upsert did not update the record: %q", events[0])
	}

	// Delete by UID.
	if err := cs.DeleteEvent(user, defaultCalendarID, "evt-1"); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	events, err = cs.GetEvents(user, defaultCalendarID)
	if err != nil {
		t.Fatalf("GetEvents after delete: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events after delete, got %d", len(events))
	}
}
