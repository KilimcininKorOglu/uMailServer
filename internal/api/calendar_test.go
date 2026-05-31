package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestICSEvent_RoundTrip(t *testing.T) {
	in := CalendarEventDTO{
		UID:         "evt-1",
		Summary:     "Team sync; weekly",
		Description: "Discuss\nroadmap, and goals",
		Location:    "Room 2",
		Start:       "2026-06-02T09:00:00Z",
		End:         "2026-06-02T10:00:00Z",
	}
	ics, err := buildICSEvent(in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	out, ok := parseICSEvent(ics)
	if !ok {
		t.Fatalf("parse failed for:\n%s", ics)
	}
	if out.UID != in.UID || out.Summary != in.Summary || out.Location != in.Location {
		t.Errorf("text fields lost: got %+v", out)
	}
	if out.Description != in.Description {
		t.Errorf("description lost escaping: got %q want %q", out.Description, in.Description)
	}
	if out.Start != in.Start || out.End != in.End {
		t.Errorf("times lost: got start=%q end=%q", out.Start, out.End)
	}
}

func TestICSEvent_AllDay(t *testing.T) {
	ics, err := buildICSEvent(CalendarEventDTO{UID: "d1", Summary: "Holiday", Start: "2026-07-01", AllDay: true})
	if err != nil {
		t.Fatalf("build all-day: %v", err)
	}
	if !strings.Contains(ics, "DTSTART;VALUE=DATE:20260701") {
		t.Errorf("expected all-day DATE value, got:\n%s", ics)
	}
	out, ok := parseICSEvent(ics)
	if !ok || !out.AllDay || out.Start != "2026-07-01" {
		t.Errorf("all-day round-trip failed: %+v ok=%v", out, ok)
	}
}

func TestCalendarHandler_CRUD(t *testing.T) {
	h := NewCalendarHandler(t.TempDir())

	// Create.
	rec := httptest.NewRecorder()
	h.handleCalendarEvents(rec, reqAsUser(http.MethodPost, "/api/v1/calendar/events",
		`{"summary":"Standup","start":"2026-06-02T09:00:00Z","end":"2026-06-02T09:15:00Z"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created CalendarEventDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.UID == "" {
		t.Fatal("expected a generated UID")
	}

	// List.
	rec = httptest.NewRecorder()
	h.handleCalendarEvents(rec, reqAsUser(http.MethodGet, "/api/v1/calendar/events", ""))
	if !strings.Contains(rec.Body.String(), "Standup") {
		t.Fatalf("expected Standup in list, got %s", rec.Body.String())
	}

	// Update.
	rec = httptest.NewRecorder()
	h.handleCalendarEventDetail(rec, reqAsUser(http.MethodPut, "/api/v1/calendar/events/"+created.UID,
		`{"summary":"Standup (moved)","start":"2026-06-02T10:00:00Z"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.handleCalendarEvents(rec, reqAsUser(http.MethodGet, "/api/v1/calendar/events", ""))
	if !strings.Contains(rec.Body.String(), "Standup (moved)") {
		t.Fatalf("expected updated summary, got %s", rec.Body.String())
	}

	// Delete.
	rec = httptest.NewRecorder()
	h.handleCalendarEventDetail(rec, reqAsUser(http.MethodDelete, "/api/v1/calendar/events/"+created.UID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.handleCalendarEvents(rec, reqAsUser(http.MethodGet, "/api/v1/calendar/events", ""))
	if strings.Contains(rec.Body.String(), "Standup") {
		t.Fatalf("expected no events after delete, got %s", rec.Body.String())
	}
}
