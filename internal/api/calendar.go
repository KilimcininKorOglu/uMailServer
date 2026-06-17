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
	"github.com/umailserver/umailserver/internal/icaltz"
)

// CalendarHandler bridges the webmail REST surface to the same CalDAV file
// store the CalDAV protocol server uses, so events created in webmail are
// visible to CalDAV clients (and vice versa). It mirrors ContactsHandler.
type CalendarHandler struct {
	dataDir string
	// deliver sends meeting-invite (iMIP) emails to attendees through the
	// shared submission path. When nil, events are still saved to the
	// organizer's calendar but no invitations are emailed.
	deliver func(from string, to []string, data []byte) error
	// roomLookup reports whether an attendee address is a bookable room and
	// whether it auto-accepts, enabling resource auto-booking. When nil, room
	// attendees are treated as ordinary invitees.
	roomLookup func(email string) (autoAccept bool, ok bool)
	// store, when set, is the canonical semcore-backed calendar store so webmail
	// reads/writes the same calendar data as EWS and CalDAV. When nil, the
	// legacy filesystem store rooted at dataDir is used.
	store caldav.Store
}

// NewCalendarHandler creates a calendar REST handler rooted at dataDir.
func NewCalendarHandler(dataDir string) *CalendarHandler {
	return &CalendarHandler{dataDir: dataDir}
}

// SetStore wires the canonical calendar store (semcore-backed), unifying the
// webmail calendar with the EWS/CalDAV source of truth.
func (h *CalendarHandler) SetStore(store caldav.Store) {
	h.store = store
}

// wireCollabCalendarStore points the calendar handler at the canonical
// semcore-backed calendar store when both the handler and the semcore store are
// present, so webmail shares one source of truth with EWS and CalDAV.
func (s *Server) wireCollabCalendarStore() {
	if s.semStore == nil || s.calendarHandler == nil {
		return
	}
	s.calendarHandler.SetStore(caldav.NewCollabStore(s.semStore.Collaboration(), s.semStore.Identity()))
}

// SetDeliveryFunc wires the outbound delivery path used to email meeting invites.
func (h *CalendarHandler) SetDeliveryFunc(fn func(from string, to []string, data []byte) error) {
	h.deliver = fn
}

// SetRoomLookup wires the resource resolver used for room auto-booking.
func (h *CalendarHandler) SetRoomLookup(fn func(email string) (autoAccept bool, ok bool)) {
	h.roomLookup = fn
}

// bookRooms reserves any auto-accepting room among the attendees by saving the
// event to the room's own calendar when the room is free in the event window.
// It returns the rooms it could not book because of a conflict.
func (h *CalendarHandler) bookRooms(dto CalendarEventDTO) []string {
	if h.roomLookup == nil || len(dto.Attendees) == 0 {
		return nil
	}
	start, end, ok := eventBounds(dto)
	var conflicts []string
	for _, room := range dto.Attendees {
		autoAccept, isRoom := h.roomLookup(room)
		if !isRoom || !autoAccept {
			continue
		}
		if ok && len(h.busyForUser(room, start, end)) > 0 {
			conflicts = append(conflicts, room)
			continue
		}
		roomCalID, err := h.ensureCalendar(room)
		if err != nil {
			continue
		}
		ics, err := buildICSEvent(dto)
		if err != nil {
			continue
		}
		ev := &caldav.CalendarEvent{UID: dto.UID, Summary: dto.Summary, Created: time.Now(), Modified: time.Now()}
		if err := h.getStorage().SaveEvent(room, roomCalID, ev, ics); err != nil {
			continue
		}
	}
	return conflicts
}

// getStorage returns the calendar store. When the canonical semcore-backed
// store is wired (production), it is returned so webmail shares one source of
// truth with EWS and CalDAV. Otherwise it falls back to the filesystem store
// (caldav.NewStorage appends the "caldav" subdirectory, matching the CalDAV
// protocol server at DataDir/caldav/caldav).
func (h *CalendarHandler) getStorage() caldav.Store {
	if h.store != nil {
		return h.store
	}
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
	UID         string   `json:"uid"`
	Summary     string   `json:"summary"`
	Description string   `json:"description,omitempty"`
	Location    string   `json:"location,omitempty"`
	Start       string   `json:"start"` // RFC3339 (or YYYY-MM-DD when allDay)
	End         string   `json:"end,omitempty"`
	AllDay      bool     `json:"allDay,omitempty"`
	Organizer   string   `json:"organizer,omitempty"`
	Attendees   []string `json:"attendees,omitempty"`
	Recurrence  string   `json:"recurrence,omitempty"` // raw RRULE value, e.g. "FREQ=WEEKLY"
	// Timezone is the IANA zone the civil Start/End are anchored to (e.g.
	// "America/New_York"). When set, the event is stored as DTSTART;TZID=...
	// civil local time plus a VTIMEZONE so recurring events keep their wall
	// time across DST. Empty means a floating/UTC instant (one-off events).
	Timezone string `json:"timezone,omitempty"`
}

const (
	defaultCalendarID = "default"
	// taskListID is the dedicated collection for VTODO items; the events
	// calendar deliberately excludes it so tasks and events stay separate.
	taskListID = "tasks"
)

// ensureCalendar returns the ID of the user's events calendar, creating a
// default one if none exists. The task list collection is never treated as the
// events calendar.
func (h *CalendarHandler) ensureCalendar(user string) (string, error) {
	store := h.getStorage()
	cals, err := store.GetCalendars(user)
	if err == nil {
		for _, c := range cals {
			if c.ID != taskListID {
				return c.ID, nil
			}
		}
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

// handleCalendarExport serves all events from all of the user's calendars as a
// single .ics file.
// GET /api/v1/calendar/export
func (h *CalendarHandler) handleCalendarExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	store := h.getStorage()
	if store == nil {
		h.sendError(w, http.StatusInternalServerError, "calendar store unavailable")
		return
	}

	calendars, err := store.GetCalendars(user)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to load calendars")
		return
	}

	var buf strings.Builder
	buf.WriteString("BEGIN:VCALENDAR\r\n")
	buf.WriteString("VERSION:2.0\r\n")
	buf.WriteString("PRODID:-//uMailServer//Webmail//EN\r\n")
	buf.WriteString("CALSCALE:GREGORIAN\r\n")
	buf.WriteString("METHOD:PUBLISH\r\n")

	for _, cal := range calendars {
		raws, err := store.GetEvents(user, cal.ID)
		if err != nil {
			continue
		}
		for _, raw := range raws {
			dto, ok := parseICSEvent(raw)
			if !ok {
				continue
			}
			ics, err := buildICSEvent(dto)
			if err != nil {
				continue
			}
			// Strip the surrounding VCALENDAR wrapper from each event since we
			// are embedding events into the outer calendar wrapper.
			for _, line := range strings.Split(ics, "\r\n") {
				if line == "BEGIN:VCALENDAR" || line == "VERSION:2.0" ||
					line == "PRODID:-//uMailServer//Webmail//EN" || line == "END:VCALENDAR" {
					continue
				}
				buf.WriteString(line + "\r\n")
			}
		}
	}
	buf.WriteString("END:VCALENDAR\r\n")

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"calendar.ics\"")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
	if _, err := w.Write([]byte(buf.String())); err != nil {
		fmt.Printf("ERROR: failed to write calendar export: %v\n", err)
	}
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
		// A meeting with attendees records the organizer so the stored copy and
		// the emailed invitation agree on who owns it.
		if dto.Organizer == "" && len(dto.Attendees) > 0 {
			dto.Organizer = user
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
		conflicts := h.bookRooms(dto)
		if err := h.sendInvites(user, dto); err != nil {
			h.sendError(w, http.StatusBadGateway, "event saved but invitations could not be sent")
			return
		}
		h.sendJSON(w, http.StatusCreated, struct {
			CalendarEventDTO
			UnbookedRooms []string `json:"unbookedRooms,omitempty"`
		}{dto, conflicts})
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
		if dto.Organizer == "" && len(dto.Attendees) > 0 {
			dto.Organizer = user
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
		// Re-send the invitation so attendees get the updated details.
		if err := h.sendInvites(user, dto); err != nil {
			h.sendError(w, http.StatusBadGateway, "event saved but the updated invitation could not be sent")
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

// sendInvites emails an iMIP meeting request to the event's attendees. It is a
// best-effort notification: a delivery error is returned so the caller can
// surface it, but the event has already been saved to the organizer's calendar.
func (h *CalendarHandler) sendInvites(organizer string, dto CalendarEventDTO) error {
	if h.deliver == nil || len(dto.Attendees) == 0 {
		return nil
	}
	// Room resources are reserved directly on their own calendar (bookRooms);
	// they have no human mailbox to email, so only human attendees receive the
	// iMIP invitation.
	var humans []string
	for _, a := range dto.Attendees {
		if h.roomLookup != nil {
			if _, isRoom := h.roomLookup(a); isRoom {
				continue
			}
		}
		humans = append(humans, a)
	}
	if len(humans) == 0 {
		return nil
	}

	invite := dto
	invite.Organizer = organizer
	ics, err := buildICSEvent(invite)
	if err != nil {
		return err
	}
	// Promote the calendar to a METHOD:REQUEST so recipients treat it as an
	// actionable invitation (the RSVP UI keys on METHOD:REQUEST).
	ics = strings.Replace(ics, "VERSION:2.0\r\n", "VERSION:2.0\r\nMETHOD:REQUEST\r\n", 1)

	subject := sanitizeHeaderValue("Invitation: " + dto.Summary)
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", sanitizeHeaderValue(organizer))
	fmt.Fprintf(&sb, "To: %s\r\n", sanitizeHeaderValue(strings.Join(humans, ", ")))
	fmt.Fprintf(&sb, "Subject: %s\r\n", subject)
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(ics)

	return h.deliver(organizer, humans, []byte(sb.String()))
}

// CalendarDTO is the JSON projection of a calendar.
type CalendarDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color,omitempty"` // hex RGB value
	IsDefault   bool   `json:"isDefault"`
}

// handleCalendars lists (GET) or creates (POST) calendars.
func (h *CalendarHandler) handleCalendars(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	store := h.getStorage()

	switch r.Method {
	case http.MethodGet:
		cals, err := store.GetCalendars(user)
		if err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to list calendars")
			return
		}
		dtos := make([]CalendarDTO, 0, len(cals))
		for _, c := range cals {
			dtos = append(dtos, CalendarDTO{
				ID:          c.ID,
				Name:        c.Name,
				Description: c.Description,
				Color:       c.Color,
				IsDefault:   c.ID == defaultCalendarID,
			})
		}
		h.sendJSON(w, http.StatusOK, map[string]interface{}{"calendars": dtos})
	case http.MethodPost:
		var req struct {
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Color       string `json:"color,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			h.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			h.sendError(w, http.StatusBadRequest, "name is required")
			return
		}
		cal := &caldav.Calendar{
			Name:        req.Name,
			Description: req.Description,
			Color:       req.Color,
			Created:     time.Now(),
			Modified:    time.Now(),
		}
		if err := store.CreateCalendar(user, cal); err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to create calendar")
			return
		}
		h.sendJSON(w, http.StatusCreated, CalendarDTO{
			ID:          cal.ID,
			Name:        cal.Name,
			Description: cal.Description,
			Color:       cal.Color,
			IsDefault:   false,
		})
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleCalendarDetail returns (GET), updates (PATCH), or deletes (DELETE) one calendar.
func (h *CalendarHandler) handleCalendarDetail(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		h.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := path.Base(r.URL.Path)
	if id == "" || id == "calendars" {
		h.sendError(w, http.StatusBadRequest, "calendar id required")
		return
	}
	store := h.getStorage()

	switch r.Method {
	case http.MethodGet:
		cal, err := store.GetCalendar(user, id)
		if err != nil {
			h.sendError(w, http.StatusNotFound, "calendar not found")
			return
		}
		h.sendJSON(w, http.StatusOK, CalendarDTO{
			ID:          cal.ID,
			Name:        cal.Name,
			Description: cal.Description,
			Color:       cal.Color,
			IsDefault:   cal.ID == defaultCalendarID,
		})
	case http.MethodPatch:
		var req struct {
			Name        *string `json:"name,omitempty"`
			Description *string `json:"description,omitempty"`
			Color       *string `json:"color,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			h.sendError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		cal, err := store.GetCalendar(user, id)
		if err != nil {
			h.sendError(w, http.StatusNotFound, "calendar not found")
			return
		}
		if req.Name != nil {
			cal.Name = *req.Name
		}
		if req.Description != nil {
			cal.Description = *req.Description
		}
		if req.Color != nil {
			cal.Color = *req.Color
		}
		cal.Modified = time.Now()
		if err := store.UpdateCalendar(user, cal); err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to update calendar")
			return
		}
		h.sendJSON(w, http.StatusOK, CalendarDTO{
			ID:          cal.ID,
			Name:        cal.Name,
			Description: cal.Description,
			Color:       cal.Color,
			IsDefault:   cal.ID == defaultCalendarID,
		})
	case http.MethodDelete:
		if id == defaultCalendarID {
			h.sendError(w, http.StatusBadRequest, "cannot delete the default calendar")
			return
		}
		if err := store.DeleteCalendar(user, id); err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to delete calendar")
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
	// Timezone only applies to timed events; all-day events are floating dates.
	tzid := ""
	if !dto.AllDay {
		tzid = dto.Timezone
	}
	dtStart, err := icsDateTimeProperty("DTSTART", dto.Start, dto.AllDay, tzid)
	if err != nil {
		return "", fmt.Errorf("invalid start time")
	}
	var lines []string
	lines = append(lines,
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//uMailServer//Webmail//EN",
	)
	// A VTIMEZONE for the event's zone keeps DTSTART;TZID recurrences DST-correct
	// for strict clients (no-op for UTC/floating events).
	if vtz := icaltz.VTimezone(tzid, time.Now()); vtz != "" {
		lines = append(lines, strings.TrimRight(vtz, "\r\n"))
	}
	lines = append(lines,
		"BEGIN:VEVENT",
		"UID:"+dto.UID,
		"DTSTAMP:"+time.Now().UTC().Format("20060102T150405Z"),
		dtStart,
	)
	if dto.End != "" {
		dtEnd, err := icsDateTimeProperty("DTEND", dto.End, dto.AllDay, tzid)
		if err != nil {
			return "", fmt.Errorf("invalid end time")
		}
		lines = append(lines, dtEnd)
	}
	lines = append(lines, foldICSLine("SUMMARY:"+icsEscape(dto.Summary)))
	if dto.Location != "" {
		lines = append(lines, foldICSLine("LOCATION:"+icsEscape(dto.Location)))
	}
	if dto.Description != "" {
		lines = append(lines, foldICSLine("DESCRIPTION:"+icsEscape(dto.Description)))
	}
	if dto.Recurrence != "" {
		lines = append(lines, foldICSLine("RRULE:"+dto.Recurrence))
	}
	if dto.Organizer != "" {
		lines = append(lines, foldICSLine("ORGANIZER:mailto:"+dto.Organizer))
	}
	for _, a := range dto.Attendees {
		if a == "" {
			continue
		}
		lines = append(lines, foldICSLine("ATTENDEE;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:"+a))
	}
	lines = append(lines, "END:VEVENT", "END:VCALENDAR")
	return strings.Join(lines, "\r\n") + "\r\n", nil
}

// icsDateTimeProperty renders a date-time property line (DTSTART/DTEND) for the
// given RFC3339 (or YYYY-MM-DD when allDay) value. With a non-empty IANA tzid it
// emits the civil local time tagged with TZID (so recurrences keep their wall
// time across DST); otherwise it emits the bare UTC instant.
func icsDateTimeProperty(name, value string, allDay bool, tzid string) (string, error) {
	if allDay {
		t, err := time.Parse("2006-01-02", value[:min(10, len(value))])
		if err != nil {
			return "", err
		}
		return name + ";VALUE=DATE:" + t.Format("20060102"), nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", err
	}
	return icaltz.FormatProperty(name, tzid, t), nil
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
			if tzid := icsParam(params, "TZID"); tzid != "" {
				dto.Timezone = tzid
			}
		case "DTEND":
			dto.End, _ = parseICSTime(params, value)
		case "RRULE":
			dto.Recurrence = value
		case "ORGANIZER":
			dto.Organizer = strings.TrimPrefix(strings.ToLower(value), "mailto:")
		case "ATTENDEE":
			if addr := strings.TrimPrefix(strings.ToLower(value), "mailto:"); addr != "" {
				dto.Attendees = append(dto.Attendees, addr)
			}
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
	// TZID-qualified local time: parse in that zone so the instant is correct
	// (treating it as UTC would shift the event by the zone's offset).
	if tzid := icsParam(params, "TZID"); tzid != "" {
		if loc, err := time.LoadLocation(tzid); err == nil {
			if t, err := time.ParseInLocation("20060102T150405", value, loc); err == nil {
				return t.UTC().Format(time.RFC3339), false
			}
		}
	}
	// Floating local time: best-effort parse as UTC.
	if t, err := time.Parse("20060102T150405", value); err == nil {
		return t.UTC().Format(time.RFC3339), false
	}
	return "", false
}

// icsParam returns the value of a named iCalendar property parameter (e.g.
// "TZID") from a parameter string like "TZID=America/New_York;VALUE=DATE-TIME".
func icsParam(params, key string) string {
	for _, p := range strings.Split(params, ";") {
		if eq := strings.IndexByte(p, '='); eq > 0 {
			if strings.EqualFold(strings.TrimSpace(p[:eq]), key) {
				return strings.TrimSpace(p[eq+1:])
			}
		}
	}
	return ""
}
