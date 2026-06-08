package jmap

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/caldav"
	"github.com/umailserver/umailserver/internal/icaltz"
)

// JMAP Calendars (RFC 8620 method patterns; objects in JSCalendar, RFC 8984) are
// backed by the canonical caldav.CollabStore — the same semcore collaboration
// folder EWS, CalDAV, and the webmail calendar read and write. An event created
// over JMAP is therefore visible from every surface and vice versa; each surface
// only translates the shared iCalendar at its own boundary.
//
// The single calendar exposed mirrors the one calendar EWS and the webmail
// calendar already present (caldav's single-folder model). A CalendarEvent's
// JMAP id is its iCalendar UID, which is the canonical key the collaboration
// store upserts on, so ids are stable across edits and across protocols.

const (
	jmapDefaultCalendarID   = "default"
	jmapDefaultCalendarName = "Calendar"
)

// calendarsEnabled reports whether the Calendar capability is wired.
func (s *Server) calendarsEnabled() bool { return s.calStore != nil }

// calendarState returns an opaque state string derived from the collection
// ETag, so CalendarEvent/get|query|changes agree on a single state value.
func (s *Server) calendarState(user string) string {
	etag := s.calStore.GetCalendarETag(user, jmapDefaultCalendarID)
	return strings.Trim(etag, `"`)
}

// ---- Calendar/get -----------------------------------------------------------

func (s *Server) handleCalendarGet(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "Calendar/get", call.ID); !valid {
		return resp
	}
	if !s.calendarsEnabled() {
		return jmapError(call.ID, "notSupported", "calendars are not available")
	}

	cal := map[string]interface{}{
		"id":              jmapDefaultCalendarID,
		"name":            jmapDefaultCalendarName,
		"color":           "#3b82f6",
		"sortOrder":       float64(0),
		"isDefault":       true,
		"isSubscribed":    true,
		"isVisible":       true,
		"mayReadFreeBusy": true,
		"mayReadItems":    true,
		"mayAddItems":     true,
		"mayModifyItems":  true,
		"mayRemoveItems":  true,
		"mayRename":       false,
		"mayDelete":       false,
		"myRights":        map[string]interface{}{},
	}

	ids, hasIDs := call.Args["ids"].([]interface{})
	list := []interface{}{}
	notFound := []string{}
	if hasIDs {
		for _, raw := range ids {
			id, isStr := raw.(string)
			switch {
			case isStr && id == jmapDefaultCalendarID:
				list = append(list, cal)
			case isStr:
				notFound = append(notFound, id)
			}
		}
	} else {
		list = append(list, cal)
	}

	return Response{
		Name: "Calendar/get",
		Args: map[string]interface{}{
			"accountId": accountID,
			"state":     s.calendarState(user),
			"list":      list,
			"notFound":  notFound,
		},
		ID: call.ID,
	}
}

// ---- CalendarEvent/get ------------------------------------------------------

func (s *Server) handleCalendarEventGet(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "CalendarEvent/get", call.ID); !valid {
		return resp
	}
	if !s.calendarsEnabled() {
		return jmapError(call.ID, "notSupported", "calendars are not available")
	}

	raws, err := s.calStore.GetEvents(user, jmapDefaultCalendarID)
	if err != nil {
		return jmapError(call.ID, "serverFail", err.Error())
	}
	byUID := map[string]map[string]interface{}{}
	for _, ics := range raws {
		if ev, ok := parseICalEvent(ics); ok {
			byUID[ev.UID] = icalEventToJSCalendar(ev)
		}
	}

	list := []interface{}{}
	notFound := []string{}
	if ids, hasIDs := call.Args["ids"].([]interface{}); hasIDs {
		for _, raw := range ids {
			id, isStr := raw.(string)
			if !isStr {
				continue
			}
			if obj, found := byUID[id]; found {
				list = append(list, obj)
			} else {
				notFound = append(notFound, id)
			}
		}
	} else {
		// Deterministic order for stable responses and tests.
		uids := make([]string, 0, len(byUID))
		for uid := range byUID {
			uids = append(uids, uid)
		}
		sort.Strings(uids)
		for _, uid := range uids {
			list = append(list, byUID[uid])
		}
	}

	return Response{
		Name: "CalendarEvent/get",
		Args: map[string]interface{}{
			"accountId": accountID,
			"state":     s.calendarState(user),
			"list":      list,
			"notFound":  notFound,
		},
		ID: call.ID,
	}
}

// ---- CalendarEvent/query ----------------------------------------------------

func (s *Server) handleCalendarEventQuery(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "CalendarEvent/query", call.ID); !valid {
		return resp
	}
	if !s.calendarsEnabled() {
		return jmapError(call.ID, "notSupported", "calendars are not available")
	}

	raws, err := s.calStore.GetEvents(user, jmapDefaultCalendarID)
	if err != nil {
		return jmapError(call.ID, "serverFail", err.Error())
	}
	ids := []string{}
	for _, ics := range raws {
		if ev, ok := parseICalEvent(ics); ok {
			ids = append(ids, ev.UID)
		}
	}
	sort.Strings(ids)

	return Response{
		Name: "CalendarEvent/query",
		Args: map[string]interface{}{
			"accountId":           accountID,
			"queryState":          s.calendarState(user),
			"canCalculateChanges": false,
			"position":            float64(0),
			"total":               float64(len(ids)),
			"ids":                 toIfaceStrings(ids),
		},
		ID: call.ID,
	}
}

// ---- CalendarEvent/changes --------------------------------------------------

func (s *Server) handleCalendarEventChanges(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "CalendarEvent/changes", call.ID); !valid {
		return resp
	}
	if !s.calendarsEnabled() {
		return jmapError(call.ID, "notSupported", "calendars are not available")
	}
	current := s.calendarState(user)
	since := argString(call.Args, "sinceState")
	if since == current {
		return Response{
			Name: "CalendarEvent/changes",
			Args: map[string]interface{}{
				"accountId":      accountID,
				"oldState":       since,
				"newState":       current,
				"hasMoreChanges": false,
				"created":        []interface{}{},
				"updated":        []interface{}{},
				"destroyed":      []interface{}{},
			},
			ID: call.ID,
		}
	}
	// The canonical store keeps per-item change keys but no ordered change log
	// keyed by an arbitrary prior state, so we honestly signal that deltas
	// cannot be computed and the client must re-fetch (RFC 8620 §5.2).
	return jmapError(call.ID, "cannotCalculateChanges", "calendar change log is not available; re-fetch with CalendarEvent/get")
}

// ---- CalendarEvent/set ------------------------------------------------------

func (s *Server) handleCalendarEventSet(user string, call MethodCall, createdIDs map[string]string) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "CalendarEvent/set", call.ID); !valid {
		return resp
	}
	if !s.calendarsEnabled() {
		return jmapError(call.ID, "notSupported", "calendars are not available")
	}

	oldState := s.calendarState(user)
	created := map[string]interface{}{}
	notCreated := map[string]interface{}{}
	updated := map[string]interface{}{}
	notUpdated := map[string]interface{}{}
	destroyed := []string{}
	notDestroyed := map[string]interface{}{}

	for creationID, raw := range argMap(call.Args, "create") {
		props, ok := raw.(map[string]interface{})
		if !ok {
			notCreated[creationID] = map[string]interface{}{"type": "invalidProperties"}
			continue
		}
		ev := icalEvent{UID: argString(props, "uid")}
		if ev.UID == "" {
			ev.UID = uuid.New().String()
		}
		if err := applyJSCalendarPatch(&ev, props); err != nil {
			notCreated[creationID] = map[string]interface{}{"type": "invalidProperties", "description": err.Error()}
			continue
		}
		if ev.Start.IsZero() {
			notCreated[creationID] = map[string]interface{}{"type": "invalidProperties", "description": "start is required"}
			continue
		}
		if err := s.calStore.SaveEvent(user, jmapDefaultCalendarID, &caldav.CalendarEvent{UID: ev.UID}, buildICalEvent(ev)); err != nil {
			notCreated[creationID] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		created[creationID] = map[string]interface{}{"id": ev.UID}
		if createdIDs != nil {
			createdIDs[creationID] = ev.UID
		}
	}

	for id, raw := range argMap(call.Args, "update") {
		patch, ok := raw.(map[string]interface{})
		if !ok {
			notUpdated[id] = map[string]interface{}{"type": "invalidPatch"}
			continue
		}
		existingICS, err := s.calStore.GetEvent(user, jmapDefaultCalendarID, id)
		if err != nil {
			notUpdated[id] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		ev, ok := parseICalEvent(existingICS)
		if existingICS == "" || !ok {
			notUpdated[id] = map[string]interface{}{"type": "notFound"}
			continue
		}
		ev.UID = id // never let a patch change the canonical key
		if err := applyJSCalendarPatch(&ev, patch); err != nil {
			notUpdated[id] = map[string]interface{}{"type": "invalidProperties", "description": err.Error()}
			continue
		}
		if err := s.calStore.SaveEvent(user, jmapDefaultCalendarID, &caldav.CalendarEvent{UID: ev.UID}, buildICalEvent(ev)); err != nil {
			notUpdated[id] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		updated[id] = nil
	}

	for _, raw := range argSlice(call.Args, "destroy") {
		id, ok := raw.(string)
		if !ok {
			continue
		}
		if err := s.calStore.DeleteEvent(user, jmapDefaultCalendarID, id); err != nil {
			notDestroyed[id] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		destroyed = append(destroyed, id)
	}

	return Response{
		Name: "CalendarEvent/set",
		Args: map[string]interface{}{
			"accountId":    accountID,
			"oldState":     oldState,
			"newState":     s.calendarState(user),
			"created":      created,
			"updated":      updated,
			"destroyed":    destroyed,
			"notCreated":   notCreated,
			"notUpdated":   notUpdated,
			"notDestroyed": notDestroyed,
		},
		ID: call.ID,
	}
}

// =============================================================================
// iCalendar <-> JSCalendar conversion (canonical RawData is iCalendar)
// =============================================================================

// icalEvent is the structured view of a VEVENT used to translate between the
// canonical iCalendar and the JMAP JSCalendar shape. Properties we do not model
// (RRULE, ATTENDEE, ORGANIZER, ...) are carried verbatim in passthrough so an
// edit over JMAP never drops data the other protocols depend on.
type icalEvent struct {
	UID         string
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	AllDay      bool
	// Timezone is the IANA zone the civil Start/End are anchored to (from a
	// DTSTART;TZID parameter). Preserved so recurring events keep their wall time
	// across DST instead of being normalized to a bare UTC instant.
	Timezone    string
	passthrough []string
}

// parseICalEvent extracts the first VEVENT from an iCalendar document.
func parseICalEvent(ics string) (icalEvent, bool) {
	var ev icalEvent
	inEvent := false
	for _, line := range unfoldICSLines(ics) {
		switch {
		case strings.EqualFold(line, "BEGIN:VEVENT"):
			inEvent = true
			continue
		case strings.EqualFold(line, "END:VEVENT"):
			inEvent = false
			continue
		}
		if !inEvent {
			continue
		}
		name, params, value := splitICSLine(line)
		switch name {
		case "UID":
			ev.UID = value
		case "SUMMARY":
			ev.Summary = icsUnescape(value)
		case "DESCRIPTION":
			ev.Description = icsUnescape(value)
		case "LOCATION":
			ev.Location = icsUnescape(value)
		case "DTSTART":
			ev.Start, ev.AllDay = parseICSTime(params, value)
			if tzid := icsParam(params, "TZID"); tzid != "" {
				ev.Timezone = tzid
			}
		case "DTEND":
			ev.End, _ = parseICSTime(params, value)
		case "DTSTAMP", "":
			// Regenerated on build (or unparseable); do not preserve.
		default:
			ev.passthrough = append(ev.passthrough, line)
		}
	}
	if ev.UID == "" || ev.Start.IsZero() {
		return ev, false
	}
	return ev, true
}

// buildICalEvent renders an icalEvent back to a complete iCalendar document,
// re-emitting any preserved passthrough properties.
func buildICalEvent(ev icalEvent) string {
	lines := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//uMailServer//JMAP//EN",
		"BEGIN:VEVENT",
		"UID:" + ev.UID,
		"DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z"),
	}
	if ev.AllDay {
		lines = append(lines, "DTSTART;VALUE=DATE:"+ev.Start.Format("20060102"))
		if !ev.End.IsZero() {
			lines = append(lines, "DTEND;VALUE=DATE:"+ev.End.Format("20060102"))
		}
	} else {
		// Preserve the timezone anchor (DTSTART;TZID + VTIMEZONE) so recurring
		// events keep their wall time across DST; bare UTC for floating events.
		if vtz := icaltz.VTimezone(ev.Timezone, time.Now()); vtz != "" {
			// VTIMEZONE is a VCALENDAR child, so insert it before BEGIN:VEVENT.
			for i, l := range lines {
				if l == "BEGIN:VEVENT" {
					block := strings.Split(strings.TrimRight(vtz, "\r\n"), "\r\n")
					lines = append(lines[:i], append(block, lines[i:]...)...)
					break
				}
			}
		}
		lines = append(lines, icaltz.FormatProperty("DTSTART", ev.Timezone, ev.Start))
		if !ev.End.IsZero() {
			lines = append(lines, icaltz.FormatProperty("DTEND", ev.Timezone, ev.End))
		}
	}
	lines = append(lines, foldICSLine("SUMMARY:"+icsEscape(ev.Summary)))
	if ev.Location != "" {
		lines = append(lines, foldICSLine("LOCATION:"+icsEscape(ev.Location)))
	}
	if ev.Description != "" {
		lines = append(lines, foldICSLine("DESCRIPTION:"+icsEscape(ev.Description)))
	}
	lines = append(lines, ev.passthrough...)
	lines = append(lines, "END:VEVENT", "END:VCALENDAR")
	return strings.Join(lines, "\r\n") + "\r\n"
}

// icalEventToJSCalendar projects an icalEvent onto a JSCalendar Event object.
func icalEventToJSCalendar(ev icalEvent) map[string]interface{} {
	obj := map[string]interface{}{
		"@type":       "Event",
		"id":          ev.UID,
		"uid":         ev.UID,
		"title":       ev.Summary,
		"calendarIds": map[string]interface{}{jmapDefaultCalendarID: true},
	}
	if ev.Description != "" {
		obj["description"] = ev.Description
	}
	if ev.Location != "" {
		obj["locations"] = map[string]interface{}{
			"1": map[string]interface{}{"@type": "Location", "name": ev.Location},
		}
	}
	if ev.AllDay {
		obj["showWithoutTime"] = true
		obj["start"] = ev.Start.Format("2006-01-02T00:00:00")
		if !ev.End.IsZero() && ev.End.After(ev.Start) {
			obj["duration"] = formatISODuration(ev.End.Sub(ev.Start))
		}
	} else if ev.Timezone != "" && !strings.EqualFold(ev.Timezone, "UTC") {
		// Express start as civil-local in the event's zone so the recurrence
		// anchor (and DST behavior) survives the JSCalendar round-trip.
		if loc, err := time.LoadLocation(ev.Timezone); err == nil {
			obj["start"] = ev.Start.In(loc).Format("2006-01-02T15:04:05")
			obj["timeZone"] = ev.Timezone
		} else {
			obj["start"] = ev.Start.UTC().Format("2006-01-02T15:04:05")
			obj["timeZone"] = "Etc/UTC"
		}
		if !ev.End.IsZero() && ev.End.After(ev.Start) {
			obj["duration"] = formatISODuration(ev.End.Sub(ev.Start))
		}
	} else {
		obj["start"] = ev.Start.UTC().Format("2006-01-02T15:04:05")
		obj["timeZone"] = "Etc/UTC"
		if !ev.End.IsZero() && ev.End.After(ev.Start) {
			obj["duration"] = formatISODuration(ev.End.Sub(ev.Start))
		}
	}
	return obj
}

// applyJSCalendarPatch merges a JSCalendar Event patch onto an icalEvent.
func applyJSCalendarPatch(ev *icalEvent, patch map[string]interface{}) error {
	if v, ok := patch["title"]; ok {
		if sv, isStr := v.(string); isStr {
			ev.Summary = sv
		}
	}
	if v, ok := patch["description"]; ok {
		if sv, isStr := v.(string); isStr {
			ev.Description = sv
		}
	}
	if v, ok := patch["locations"]; ok {
		ev.Location = firstLocationName(v)
	}
	if v, ok := patch["showWithoutTime"]; ok {
		if b, isBool := v.(bool); isBool {
			ev.AllDay = b
		}
	}
	tz := argString(patch, "timeZone")
	if tz != "" && !strings.EqualFold(tz, "UTC") && !strings.EqualFold(tz, "Etc/UTC") {
		ev.Timezone = tz
	} else if tz != "" {
		ev.Timezone = ""
	}
	if v, ok := patch["start"]; ok {
		sv, isStr := v.(string)
		if !isStr || sv == "" {
			return fmt.Errorf("start must be a non-empty local date-time string")
		}
		start, allDay, err := parseJSCalStart(sv, tz, ev.AllDay)
		if err != nil {
			return err
		}
		ev.Start = start
		if allDay {
			ev.AllDay = true
		}
	}
	if v, ok := patch["duration"]; ok {
		if sv, isStr := v.(string); isStr && sv != "" {
			d, err := parseISODuration(sv)
			if err != nil {
				return err
			}
			if !ev.Start.IsZero() {
				ev.End = ev.Start.Add(d)
			}
		}
	}
	return nil
}

// firstLocationName returns the name of the first JSCalendar Location in a
// locations map, or "" when absent/malformed.
func firstLocationName(v interface{}) string {
	locs, ok := v.(map[string]interface{})
	if !ok {
		return ""
	}
	keys := make([]string, 0, len(locs))
	for k := range locs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if loc, isMap := locs[k].(map[string]interface{}); isMap {
			if name, isStr := loc["name"].(string); isStr && name != "" {
				return name
			}
		}
	}
	return ""
}

// parseJSCalStart parses a JSCalendar LocalDateTime ("2006-01-02T15:04:05") or
// date ("2006-01-02") with an optional time zone, returning a UTC time and
// whether the value was date-only (all-day).
func parseJSCalStart(value, tz string, allDayHint bool) (time.Time, bool, error) {
	if len(value) == 10 || allDayHint {
		if t, err := time.Parse("2006-01-02", value[:min(10, len(value))]); err == nil {
			return t, true, nil
		}
	}
	loc := time.UTC
	if tz != "" && !strings.EqualFold(tz, "UTC") && !strings.EqualFold(tz, "Etc/UTC") {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", value, loc); err == nil {
		return t.UTC(), false, nil
	}
	// Tolerate a trailing Z or offset by falling back to RFC3339.
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), false, nil
	}
	return time.Time{}, false, fmt.Errorf("invalid start date-time %q", value)
}

// ---- low-level iCalendar tokenizing (per-protocol boundary, mirrors webmail) ----

func unfoldICSLines(ics string) []string {
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

func parseICSTime(params, value string) (time.Time, bool) {
	if strings.Contains(strings.ToUpper(params), "VALUE=DATE") || len(value) == 8 {
		if t, err := time.Parse("20060102", value); err == nil {
			return t, true
		}
	}
	if strings.HasSuffix(value, "Z") {
		if t, err := time.Parse("20060102T150405Z", value); err == nil {
			return t.UTC(), false
		}
	}
	// TZID-qualified civil time: parse in that zone so the instant is correct.
	if tzid := icsParam(params, "TZID"); tzid != "" {
		if loc, err := time.LoadLocation(tzid); err == nil {
			if t, err := time.ParseInLocation("20060102T150405", value, loc); err == nil {
				return t.UTC(), false
			}
		}
	}
	if t, err := time.Parse("20060102T150405", value); err == nil {
		return t.UTC(), false
	}
	return time.Time{}, false
}

// icsParam returns a named iCalendar property parameter value (e.g. "TZID").
func icsParam(params, key string) string {
	for _, p := range strings.Split(params, ";") {
		if eq := strings.IndexByte(p, '='); eq > 0 && strings.EqualFold(strings.TrimSpace(p[:eq]), key) {
			return strings.TrimSpace(p[eq+1:])
		}
	}
	return ""
}

func icsEscape(s string) string {
	r := strings.NewReplacer("\\", "\\\\", ";", "\\;", ",", "\\,", "\n", "\\n")
	return r.Replace(s)
}

func icsUnescape(s string) string {
	r := strings.NewReplacer("\\n", "\n", "\\N", "\n", "\\,", ",", "\\;", ";", "\\\\", "\\")
	return r.Replace(s)
}

// foldICSLine folds a content line to 75 octets per RFC 5545.
func foldICSLine(line string) string {
	const limit = 75
	if len(line) <= limit {
		return line
	}
	var b strings.Builder
	b.WriteString(line[:limit])
	rest := line[limit:]
	for len(rest) > 0 {
		chunk := limit - 1
		if chunk > len(rest) {
			chunk = len(rest)
		}
		b.WriteString("\r\n ")
		b.WriteString(rest[:chunk])
		rest = rest[chunk:]
	}
	return b.String()
}

// ---- ISO 8601 duration (PnDTnHnMnS subset) ----------------------------------

func formatISODuration(d time.Duration) string {
	if d <= 0 {
		return "PT0S"
	}
	days := int(d / (24 * time.Hour))
	rem := d % (24 * time.Hour)
	h := int(rem / time.Hour)
	rem %= time.Hour
	m := int(rem / time.Minute)
	rem %= time.Minute
	sec := int(rem / time.Second)
	var b strings.Builder
	b.WriteByte('P')
	if days > 0 {
		fmt.Fprintf(&b, "%dD", days)
	}
	if h > 0 || m > 0 || sec > 0 {
		b.WriteByte('T')
		if h > 0 {
			fmt.Fprintf(&b, "%dH", h)
		}
		if m > 0 {
			fmt.Fprintf(&b, "%dM", m)
		}
		if sec > 0 {
			fmt.Fprintf(&b, "%dS", sec)
		}
	}
	if b.Len() == 1 {
		return "P0D"
	}
	return b.String()
}

func parseISODuration(s string) (time.Duration, error) {
	if s == "" || s[0] != 'P' {
		return 0, fmt.Errorf("invalid ISO 8601 duration %q", s)
	}
	var total time.Duration
	inTime := false
	num := strings.Builder{}
	for _, r := range s[1:] {
		switch {
		case r == 'T':
			inTime = true
		case r >= '0' && r <= '9':
			num.WriteRune(r)
		default:
			n, err := strconv.Atoi(num.String())
			if err != nil {
				return 0, fmt.Errorf("invalid ISO 8601 duration %q", s)
			}
			num.Reset()
			switch r {
			case 'W':
				total += time.Duration(n) * 7 * 24 * time.Hour
			case 'D':
				total += time.Duration(n) * 24 * time.Hour
			case 'H':
				total += time.Duration(n) * time.Hour
			case 'M':
				if inTime {
					total += time.Duration(n) * time.Minute
				} // a month component is not representable; ignore
			case 'S':
				total += time.Duration(n) * time.Second
			default:
				return 0, fmt.Errorf("invalid ISO 8601 duration component %q", string(r))
			}
		}
	}
	return total, nil
}

// toIfaceStrings converts a []string to []interface{} for a JMAP args slice.
func toIfaceStrings(in []string) []interface{} {
	out := make([]interface{}, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
