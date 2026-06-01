package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedEvent creates one event for the reqAsUser user via the handler.
func seedEvent(t *testing.T, h *CalendarHandler, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleCalendarEvents(rec, reqAsUser(http.MethodPost, "/api/v1/calendar/events", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed event: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFreeBusy_MergesOverlapAndExcludesOutsideWindow(t *testing.T) {
	h := NewCalendarHandler(t.TempDir())

	// Two overlapping meetings 09:00-10:00 and 09:30-11:00 → one busy block.
	seedEvent(t, h, `{"summary":"A","start":"2026-06-02T09:00:00Z","end":"2026-06-02T10:00:00Z"}`)
	seedEvent(t, h, `{"summary":"B","start":"2026-06-02T09:30:00Z","end":"2026-06-02T11:00:00Z"}`)
	// A meeting on a different day that falls outside the query window.
	seedEvent(t, h, `{"summary":"C","start":"2026-06-20T09:00:00Z","end":"2026-06-20T10:00:00Z"}`)

	rec := httptest.NewRecorder()
	h.handleFreeBusy(rec, reqAsUser(http.MethodGet,
		"/api/v1/calendar/freebusy?start=2026-06-02T00:00:00Z&end=2026-06-03T00:00:00Z", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("freebusy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		FreeBusy []userFreeBusy `json:"freeBusy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.FreeBusy) != 1 {
		t.Fatalf("expected 1 user result, got %d", len(resp.FreeBusy))
	}
	busy := resp.FreeBusy[0].Busy
	if len(busy) != 1 {
		t.Fatalf("expected the two overlapping events merged into 1 block (C is outside the window), got %d: %+v", len(busy), busy)
	}
	if busy[0].Start != "2026-06-02T09:00:00Z" || busy[0].End != "2026-06-02T11:00:00Z" {
		t.Errorf("merged block wrong: got %+v", busy[0])
	}
}

func TestFreeBusy_AllDayEventSpansWholeDay(t *testing.T) {
	h := NewCalendarHandler(t.TempDir())
	seedEvent(t, h, `{"summary":"Holiday","start":"2026-07-01","allDay":true}`)

	rec := httptest.NewRecorder()
	h.handleFreeBusy(rec, reqAsUser(http.MethodGet,
		"/api/v1/calendar/freebusy?start=2026-07-01T00:00:00Z&end=2026-07-02T00:00:00Z", ""))
	var resp struct {
		FreeBusy []userFreeBusy `json:"freeBusy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.FreeBusy) != 1 || len(resp.FreeBusy[0].Busy) != 1 {
		t.Fatalf("expected one all-day busy block, got %+v", resp.FreeBusy)
	}
	b := resp.FreeBusy[0].Busy[0]
	if b.Start != "2026-07-01T00:00:00Z" || b.End != "2026-07-02T00:00:00Z" {
		t.Errorf("all-day block wrong: got %+v", b)
	}
}

func TestFreeBusy_NoEventsIsEmpty(t *testing.T) {
	h := NewCalendarHandler(t.TempDir())
	rec := httptest.NewRecorder()
	h.handleFreeBusy(rec, reqAsUser(http.MethodGet, "/api/v1/calendar/freebusy", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		FreeBusy []userFreeBusy `json:"freeBusy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.FreeBusy) != 1 || len(resp.FreeBusy[0].Busy) != 0 {
		t.Fatalf("expected one user with no busy intervals, got %+v", resp.FreeBusy)
	}
}
