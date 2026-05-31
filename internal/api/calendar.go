package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/umailserver/umailserver/internal/caldav"
)

// CalendarHandler bridges the webmail REST surface to the same CalDAV file
// store the CalDAV protocol server uses, so events created in webmail are
// visible to CalDAV clients (and vice versa). It mirrors ContactsHandler.
type CalendarHandler struct {
	dataDir string
}

// NewCalendarHandler creates a calendar REST handler rooted at dataDir.
func NewCalendarHandler(dataDir string) *CalendarHandler {
	return &CalendarHandler{dataDir: dataDir}
}

// getStorage returns the CalDAV storage. caldav.NewStorage appends the "caldav"
// subdirectory, so the path matches the CalDAV server (DataDir/caldav/caldav).
func (h *CalendarHandler) getStorage() *caldav.Storage {
	return caldav.NewStorage(filepath.Join(h.dataDir, "caldav"))
}

func (h *CalendarHandler) sendJSON(w http.ResponseWriter, code int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return
	}
}

func (h *CalendarHandler) sendError(w http.ResponseWriter, code int, msg string) {
	h.sendJSON(w, code, map[string]string{"error": msg})
}

// CalendarEventDTO is the JSON projection of a VEVENT exchanged with webmail.
type CalendarEventDTO struct {
	UID         string `json:"uid"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	Start       string `json:"start"` // RFC3339 (or YYYY-MM-DD when allDay)
	End         string `json:"end,omitempty"`
	AllDay      bool   `json:"allDay,omitempty"`
}

const defaultCalendarID = "default"

// ensureCalendar returns the ID of the user's calendar, creating a default one
// if none exists yet.
func (h *CalendarHandler) ensureCalendar(user string) (string, error) {
	store := h.getStorage()
	cals, err := store.GetCalendars(user)
	if err == nil && len(cals) > 0 {
		return cals[0].ID, nil
	}
	cal := &caldav.Calendar{
		ID:       defaultCalendarID,
		Name:     "Calendar",
		Created:  time.Now(),
		Modified: time.Now(),
	}
	if err := store.CreateCalendar(user, cal); err != nil {
		return "", err
	}
	return defaultCalendarID, nil
}

// handleCalendarEvents lists (GET) or creates (POST) events in the user's
// calendar. GET accepts optional ?start=&end= RFC3339 bounds to filter.
func (h *CalendarHandler) handleCalendarEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	calID, err := h.ensureCalendar(user)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to open calendar")
		return
	}
	store := h.getStorage()

	switch r.Method {
	case http.MethodGet:
		raws, err := store.GetEvents(user, calID)
		if err != nil {
			raws = nil
		}
		var from, to time.Time
		if v := r.URL.Query().Get("start"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				from = t
			}
		}
		if v := r.URL.Query().Get("end"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				to = t
			}
		}
		events := make([]CalendarEventDTO, 0, len(raws))
		for _, raw := range raws {
			dto, ok := parseICSEvent(raw)
			if !ok {
				continue
			}
			if !from.IsZero() || !to.IsZero() {
				if st, err := time.Parse(time.RFC3339, dto.Start); err == nil {
					if !from.IsZero() && st.Before(from) {
						continue
					}
					if !to.IsZero() && st.After(to) {
						continue
					}
				}
			}
			events = append(events, dto)
		}
		h.sendJSON(w, http.StatusOK, map[string]interface{}{"events": events})
	case http.MethodPost:
		var dto CalendarEventDTO
		if err := decodeJSON(r, &dto); err != nil {
			h.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(dto.Summary) == "" {
			h.sendError(w, http.StatusBadRequest, "summary is required")
			return
		}
		if dto.UID == "" {
			dto.UID = uuid.New().String()
		}
		ics, err := buildICSEvent(dto)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, err.Error())
			return
		}
		event := &caldav.CalendarEvent{UID: dto.UID, Summary: dto.Summary, Created: time.Now(), Modified: time.Now()}
		if err := store.SaveEvent(user, calID, event, ics); err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to save event")
			return
		}
		h.sendJSON(w, http.StatusCreated, dto)
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleCalendarEventDetail updates (PUT) or deletes (DELETE) one event by UID
// at /api/v1/calendar/events/{uid}.
func (h *CalendarHandler) handleCalendarEventDetail(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	uid := path.Base(r.URL.Path)
	if uid == "" || uid == "events" {
		h.sendError(w, http.StatusBadRequest, "event id required")
		return
	}
	calID, err := h.ensureCalendar(user)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to open calendar")
		return
	}
	store := h.getStorage()

	switch r.Method {
	case http.MethodPut:
		var dto CalendarEventDTO
		if err := decodeJSON(r, &dto); err != nil {
			h.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		dto.UID = uid
		if strings.TrimSpace(dto.Summary) == "" {
			h.sendError(w, http.StatusBadRequest, "summary is required")
			return
		}
		ics, err := buildICSEvent(dto)
		if err != nil {
			h.sendError(w, http.StatusBadRequest, err.Error())
			return
		}
		event := &caldav.CalendarEvent{UID: uid, Summary: dto.Summary, Modified: time.Now()}
		if err := store.SaveEvent(user, calID, event, ics); err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to save event")
			return
		}
		h.sendJSON(w, http.StatusOK, dto)
	case http.MethodDelete:
		if err := store.DeleteEvent(user, calID, uid); err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to delete event")
			return
		}
		h.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- minimal iCalendar (RFC 5545) VEVENT parse/generate ---

// icsEscape escapes a text value for iCalendar.
func icsEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\r\n", "\\n")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// icsUnescape reverses icsEscape.
func icsUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				b.WriteByte('\n')
			case '\\', ';', ',':
				b.WriteByte(s[i+1])
			default:
				b.WriteByte(s[i+1])
			}
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// foldICSLine folds a content line at 75 octets per RFC 5545 (continuation
// lines start with a single space).
func foldICSLine(line string) string {
	if len(line) <= 75 {
		return line
	}
	var b strings.Builder
	for len(line) > 75 {
		b.WriteString(line[:75])
		b.WriteString("\r\n ")
		line = line[75:]
	}
	b.WriteString(line)
	return b.String()
}

// buildICSEvent renders a CalendarEventDTO as a full VCALENDAR document.
func buildICSEvent(dto CalendarEventDTO) (string, error) {
	dtStart, err := icsTimeValue(dto.Start, dto.AllDay)
	if err != nil {
		return "", fmt.Errorf("invalid start time")
	}
	var lines []string
	lines = append(lines,
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//uMailServer//Webmail//EN",
		"BEGIN:VEVENT",
		"UID:"+dto.UID,
		"DTSTAMP:"+time.Now().UTC().Format("20060102T150405Z"),
		dtStart,
	)
	if dto.End != "" {
		dtEnd, err := icsTimeValue(dto.End, dto.AllDay)
		if err != nil {
			return "", fmt.Errorf("invalid end time")
		}
		// icsTimeValue emits the DTSTART property name; swap for DTEND.
		lines = append(lines, strings.Replace(dtEnd, "DTSTART", "DTEND", 1))
	}
	lines = append(lines, foldICSLine("SUMMARY:"+icsEscape(dto.Summary)))
	if dto.Location != "" {
		lines = append(lines, foldICSLine("LOCATION:"+icsEscape(dto.Location)))
	}
	if dto.Description != "" {
		lines = append(lines, foldICSLine("DESCRIPTION:"+icsEscape(dto.Description)))
	}
	lines = append(lines, "END:VEVENT", "END:VCALENDAR")
	return strings.Join(lines, "\r\n") + "\r\n", nil
}

// icsTimeValue renders a DTSTART line for the given RFC3339 (or YYYY-MM-DD when
// allDay) value, emitting UTC for timed events.
func icsTimeValue(value string, allDay bool) (string, error) {
	if allDay {
		t, err := time.Parse("2006-01-02", value[:min(10, len(value))])
		if err != nil {
			return "", err
		}
		return "DTSTART;VALUE=DATE:" + t.Format("20060102"), nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", err
	}
	return "DTSTART:" + t.UTC().Format("20060102T150405Z"), nil
}

// parseICSEvent extracts the first VEVENT from an iCalendar document into a DTO.
func parseICSEvent(ics string) (CalendarEventDTO, bool) {
	var dto CalendarEventDTO
	for _, line := range unfoldICS(ics) {
		name, params, value := splitICSLine(line)
		switch name {
		case "UID":
			dto.UID = value
		case "SUMMARY":
			dto.Summary = icsUnescape(value)
		case "DESCRIPTION":
			dto.Description = icsUnescape(value)
		case "LOCATION":
			dto.Location = icsUnescape(value)
		case "DTSTART":
			dto.Start, dto.AllDay = parseICSTime(params, value)
		case "DTEND":
			dto.End, _ = parseICSTime(params, value)
		}
	}
	if dto.UID == "" || dto.Start == "" {
		return dto, false
	}
	return dto, true
}

// unfoldICS splits an iCalendar blob into logical lines, undoing RFC 5545 line
// folding (continuation lines begin with a space or tab).
func unfoldICS(ics string) []string {
	rawLines := strings.Split(strings.ReplaceAll(ics, "\r\n", "\n"), "\n")
	var out []string
	for _, l := range rawLines {
		if len(l) > 0 && (l[0] == ' ' || l[0] == '\t') && len(out) > 0 {
			out[len(out)-1] += l[1:]
			continue
		}
		out = append(out, l)
	}
	return out
}

// splitICSLine splits "NAME;PARAM=VAL:VALUE" into name, params, and value.
func splitICSLine(line string) (name, params, value string) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return "", "", ""
	}
	left := line[:colon]
	value = line[colon+1:]
	if semi := strings.IndexByte(left, ';'); semi >= 0 {
		name = strings.ToUpper(left[:semi])
		params = left[semi+1:]
	} else {
		name = strings.ToUpper(left)
	}
	return name, params, value
}

// parseICSTime converts an iCalendar date/date-time value to RFC3339 (or
// YYYY-MM-DD for all-day) and reports whether it was an all-day date.
func parseICSTime(params, value string) (string, bool) {
	if strings.Contains(strings.ToUpper(params), "VALUE=DATE") || len(value) == 8 {
		if t, err := time.Parse("20060102", value); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	if strings.HasSuffix(value, "Z") {
		if t, err := time.Parse("20060102T150405Z", value); err == nil {
			return t.UTC().Format(time.RFC3339), false
		}
	}
	// Floating or TZID-qualified local time: best-effort parse as UTC.
	if t, err := time.Parse("20060102T150405", value); err == nil {
		return t.UTC().Format(time.RFC3339), false
	}
	return "", false
}
