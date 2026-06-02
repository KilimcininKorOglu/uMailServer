package api

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// busyInterval is one occupied time range. Free/busy intentionally exposes only
// the interval, never the event's subject/location, so querying another user's
// availability does not leak their calendar contents.
type busyInterval struct {
	Start string `json:"start"` // RFC3339 UTC
	End   string `json:"end"`   // RFC3339 UTC
}

// userFreeBusy holds the busy intervals computed for one mailbox.
type userFreeBusy struct {
	User string         `json:"user"`
	Busy []busyInterval `json:"busy"`
}

// handleFreeBusy reports the busy intervals of one or more users within a time
// window, computed from their real CalDAV events. Unlike the EWS
// GetUserAvailability stub, this reads actual VEVENT DTSTART/DTEND values.
//
// GET /api/v1/calendar/freebusy?users=a@x,b@y&start=<RFC3339>&end=<RFC3339>
func (h *CalendarHandler) handleFreeBusy(w http.ResponseWriter, r *http.Request) {
	caller, ok := r.Context().Value("user").(string)
	if !ok || caller == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Window: default now → now+7d.
	from := time.Now().UTC()
	to := from.Add(7 * 24 * time.Hour)
	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t.UTC()
		}
	}
	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t.UTC()
		}
	}
	if !to.After(from) {
		h.sendError(w, http.StatusBadRequest, "end must be after start")
		return
	}

	// Users to query: explicit ?users=, else the caller's own calendar.
	var users []string
	for _, u := range strings.Split(r.URL.Query().Get("users"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			users = append(users, u)
		}
	}
	if len(users) == 0 {
		users = []string{caller}
	}

	results := make([]userFreeBusy, 0, len(users))
	for _, u := range users {
		results = append(results, userFreeBusy{User: u, Busy: h.busyForUser(u, from, to)})
	}
	h.sendJSON(w, http.StatusOK, map[string]interface{}{"freeBusy": results})
}

// busyForUser gathers the busy intervals for one user within [from, to],
// reading every calendar collection except the task list.
func (h *CalendarHandler) busyForUser(user string, from, to time.Time) []busyInterval {
	store := h.getStorage()
	cals, err := store.GetCalendars(user)
	if err != nil {
		return []busyInterval{}
	}

	var intervals []busyInterval
	for _, c := range cals {
		if c.ID == taskListID {
			continue
		}
		raws, err := store.GetEvents(user, c.ID)
		if err != nil {
			continue
		}
		for _, raw := range raws {
			dto, ok := parseICSEvent(raw)
			if !ok {
				continue
			}
			start, end, ok := eventBounds(dto)
			if !ok {
				continue
			}
			// Keep only events that overlap the requested window.
			if end.After(from) && start.Before(to) {
				intervals = append(intervals, busyInterval{
					Start: start.UTC().Format(time.RFC3339),
					End:   end.UTC().Format(time.RFC3339),
				})
			}
		}
	}
	return mergeBusy(intervals)
}

// FreeBusyUTC returns the user's busy intervals within [from, to] as UTC time
// pairs ({start, end}), reusing the same CalDAV-backed computation behind
// GET /calendar/freebusy. It is exported so the EWS GetUserAvailability handler
// can merge CalDAV/webmail events into its free/busy view without depending on
// the api package's internal types. Only time ranges are exposed — never event
// subjects or locations — matching the privacy contract of busyInterval.
func (h *CalendarHandler) FreeBusyUTC(user string, from, to time.Time) [][2]time.Time {
	ivs := h.busyForUser(user, from, to)
	out := make([][2]time.Time, 0, len(ivs))
	for _, iv := range ivs {
		start, errS := time.Parse(time.RFC3339, iv.Start)
		end, errE := time.Parse(time.RFC3339, iv.End)
		if errS != nil || errE != nil {
			continue
		}
		out = append(out, [2]time.Time{start.UTC(), end.UTC()})
	}
	return out
}

// eventBounds resolves a DTO's start/end as concrete UTC times. All-day events
// span the whole day; a timed event without an end is treated as one hour.
func eventBounds(dto CalendarEventDTO) (time.Time, time.Time, bool) {
	if dto.AllDay {
		start, err := time.Parse("2006-01-02", dto.Start[:min(10, len(dto.Start))])
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
		start = start.UTC()
		end := start.Add(24 * time.Hour)
		if dto.End != "" {
			if e, err := time.Parse("2006-01-02", dto.End[:min(10, len(dto.End))]); err == nil && e.After(start) {
				end = e.UTC()
			}
		}
		return start, end, true
	}
	start, err := time.Parse(time.RFC3339, dto.Start)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	end := start.Add(time.Hour)
	if dto.End != "" {
		if e, err := time.Parse(time.RFC3339, dto.End); err == nil && e.After(start) {
			end = e
		}
	}
	return start.UTC(), end.UTC(), true
}

// mergeBusy sorts and coalesces overlapping/adjacent intervals so the response
// is a clean, non-overlapping busy view.
func mergeBusy(in []busyInterval) []busyInterval {
	if len(in) == 0 {
		return []busyInterval{}
	}
	type span struct{ start, end time.Time }
	spans := make([]span, 0, len(in))
	for _, iv := range in {
		s, errS := time.Parse(time.RFC3339, iv.Start)
		e, errE := time.Parse(time.RFC3339, iv.End)
		if errS != nil || errE != nil {
			continue
		}
		spans = append(spans, span{s, e})
	}
	if len(spans) == 0 {
		return []busyInterval{}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start.Before(spans[j].start) })

	merged := []span{spans[0]}
	for _, s := range spans[1:] {
		last := &merged[len(merged)-1]
		if !s.start.After(last.end) { // overlapping or adjacent
			if s.end.After(last.end) {
				last.end = s.end
			}
			continue
		}
		merged = append(merged, s)
	}

	out := make([]busyInterval, 0, len(merged))
	for _, s := range merged {
		out = append(out, busyInterval{
			Start: s.start.UTC().Format(time.RFC3339),
			End:   s.end.UTC().Format(time.RFC3339),
		})
	}
	return out
}
