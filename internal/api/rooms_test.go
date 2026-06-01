package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// bookRooms is exercised directly with a fake room lookup so the test does not
// need a full semcore store; the store-backed path is covered by handleRooms's
// own integration with semcore elsewhere.
func TestCalendarHandler_BooksAutoAcceptRoomWhenFree(t *testing.T) {
	h := NewCalendarHandler(t.TempDir())
	h.SetRoomLookup(func(email string) (bool, bool) {
		return email == "room-a@test.com", email == "room-a@test.com"
	})

	dto := CalendarEventDTO{
		UID:       "evt-room-1",
		Summary:   "Design review",
		Start:     "2026-06-10T09:00:00Z",
		End:       "2026-06-10T10:00:00Z",
		Attendees: []string{"room-a@test.com", "bob@test.com"},
	}
	conflicts := h.bookRooms(dto)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts on a free room, got %v", conflicts)
	}
	// The room's own calendar must now show the event (i.e. it is reserved).
	rec := httptest.NewRecorder()
	h.handleFreeBusy(rec, reqAsUser(http.MethodGet,
		"/api/v1/calendar/freebusy?users=room-a@test.com&start=2026-06-10T00:00:00Z&end=2026-06-11T00:00:00Z", ""))
	if !strings.Contains(rec.Body.String(), "2026-06-10T09:00:00Z") {
		t.Fatalf("expected the room to be busy after booking, got %s", rec.Body.String())
	}
}

func TestCalendarHandler_RoomConflictReported(t *testing.T) {
	h := NewCalendarHandler(t.TempDir())
	h.SetRoomLookup(func(email string) (bool, bool) {
		return email == "room-a@test.com", email == "room-a@test.com"
	})

	// First booking takes the slot.
	first := CalendarEventDTO{UID: "evt-1", Summary: "A", Start: "2026-06-10T09:00:00Z", End: "2026-06-10T10:00:00Z", Attendees: []string{"room-a@test.com"}}
	if c := h.bookRooms(first); len(c) != 0 {
		t.Fatalf("first booking should succeed, got conflicts %v", c)
	}

	// A second, overlapping booking must be reported as a conflict and not booked.
	second := CalendarEventDTO{UID: "evt-2", Summary: "B", Start: "2026-06-10T09:30:00Z", End: "2026-06-10T10:30:00Z", Attendees: []string{"room-a@test.com"}}
	conflicts := h.bookRooms(second)
	if len(conflicts) != 1 || conflicts[0] != "room-a@test.com" {
		t.Fatalf("expected room-a to be reported as a conflict, got %v", conflicts)
	}
}

func TestCalendarHandler_NonRoomAttendeeNotBooked(t *testing.T) {
	h := NewCalendarHandler(t.TempDir())
	h.SetRoomLookup(func(_ string) (bool, bool) { return false, false })
	dto := CalendarEventDTO{UID: "evt-3", Summary: "C", Start: "2026-06-10T09:00:00Z", End: "2026-06-10T10:00:00Z", Attendees: []string{"bob@test.com"}}
	if c := h.bookRooms(dto); c != nil {
		t.Fatalf("non-room attendees must not be booked, got %v", c)
	}
}
