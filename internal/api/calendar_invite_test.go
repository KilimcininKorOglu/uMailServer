package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestICSEvent_RoundTripOrganizerAttendeesRecurrence(t *testing.T) {
	in := CalendarEventDTO{
		UID:        "mtg-1",
		Summary:    "Planning",
		Start:      "2026-06-02T09:00:00Z",
		End:        "2026-06-02T10:00:00Z",
		Organizer:  "alice@test.com",
		Attendees:  []string{"bob@test.com", "carol@test.com"},
		Recurrence: "FREQ=WEEKLY;COUNT=4",
	}
	ics, err := buildICSEvent(in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	out, ok := parseICSEvent(ics)
	if !ok {
		t.Fatalf("parse failed:\n%s", ics)
	}
	if out.Organizer != "alice@test.com" {
		t.Errorf("organizer lost: %q", out.Organizer)
	}
	if len(out.Attendees) != 2 || out.Attendees[0] != "bob@test.com" || out.Attendees[1] != "carol@test.com" {
		t.Errorf("attendees lost: %v", out.Attendees)
	}
	if out.Recurrence != "FREQ=WEEKLY;COUNT=4" {
		t.Errorf("recurrence lost: %q", out.Recurrence)
	}
}

func TestCalendarHandler_CreateMeetingEmailsInvite(t *testing.T) {
	h := NewCalendarHandler(t.TempDir())
	var gotFrom, gotMsg string
	var gotTo []string
	h.SetDeliveryFunc(func(from string, to []string, data []byte) error {
		gotFrom, gotTo, gotMsg = from, to, string(data)
		return nil
	})

	rec := httptest.NewRecorder()
	h.handleCalendarEvents(rec, reqAsUser(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Kickoff","start":"2026-06-02T09:00:00Z","end":"2026-06-02T10:00:00Z","attendees":["bob@test.com","carol@test.com"]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotFrom != "admin@test.com" {
		t.Errorf("organizer/from wrong: %q", gotFrom)
	}
	if len(gotTo) != 2 {
		t.Fatalf("expected 2 attendees emailed, got %v", gotTo)
	}
	if !strings.Contains(gotMsg, "METHOD:REQUEST") {
		t.Errorf("invite email is not a METHOD:REQUEST iMIP message:\n%s", gotMsg)
	}
	if !strings.Contains(gotMsg, "text/calendar") || !strings.Contains(gotMsg, "Kickoff") {
		t.Errorf("invite email missing calendar part or summary:\n%s", gotMsg)
	}
	// The stored organizer copy must list the attendees so the organizer sees them.
	rec = httptest.NewRecorder()
	h.handleCalendarEvents(rec, reqAsUser(http.MethodGet, "/api/v1/calendar/events", ""))
	if !strings.Contains(rec.Body.String(), "bob@test.com") {
		t.Errorf("stored event should list attendees, got %s", rec.Body.String())
	}
}

func TestCalendarHandler_NoAttendeesNoEmail(t *testing.T) {
	h := NewCalendarHandler(t.TempDir())
	called := false
	h.SetDeliveryFunc(func(_ string, _ []string, _ []byte) error { called = true; return nil })

	rec := httptest.NewRecorder()
	h.handleCalendarEvents(rec, reqAsUser(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Solo","start":"2026-06-02T09:00:00Z"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if called {
		t.Error("an event without attendees must not email anyone")
	}
}
