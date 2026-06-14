package activesync

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// CalendarItem is one calendar event projected for an EAS Sync Add/Change of a
// calendar collection. Times are absolute UTC instants; the wire projection
// renders them as Compact DateTime (MS-ASDTYPE 2.7.2) and pairs them with a UTC
// Timezone blob, so a client shows the correct moment. ETag drives the
// enumerate-and-diff cursor (a calendar collection has no change journal).
type CalendarItem struct {
	ServerID    string
	ETag        string
	UID         string
	Subject     string
	Body        string // plain-text description
	Location    string
	Start       time.Time
	End         time.Time
	AllDay      bool
	Sensitivity string // EAS: "0" normal, "1" personal, "2" private, "3" confidential
	BusyStatus  string // EAS: "0" free, "1" tentative, "2" busy, "3" out-of-office
	OrgName     string
	OrgEmail    string
	DtStamp     time.Time
}

// CalendarSource supplies a calendar collection's events for the Sync command.
// folderID is the canonical (semcore) calendar folder id — the routing prefix
// already stripped. Implementations read the same collaboration store every
// other surface (EWS, CalDAV, JMAP) reads, so a phone's calendar converges with
// them on one source.
type CalendarSource interface {
	// ListItems returns the calendar folder's current events.
	ListItems(email, folderID string) ([]CalendarItem, error)
}

// compactDateTime is the EAS Calendar Compact DateTime layout (MS-ASDTYPE
// 2.7.2): basic ISO 8601 UTC, no separators, trailing Z.
const compactDateTime = "20060102T150405Z"

// utcTimeZoneBlob is the base64 EAS TimeZone structure (MS-ASDTYPE 2.7.4) for
// UTC: a 172-byte TIME_ZONE_INFORMATION of all zeros is bias 0 with no daylight
// transition. Calendar times are projected to UTC instants, so every event
// carries this and the client renders the correct absolute moment.
var utcTimeZoneBlob = base64.StdEncoding.EncodeToString(make([]byte, 172))

// CalendarItemFromICal projects a canonical iCalendar payload (the collab
// store's RawData) into a CalendarItem. serverID is the stable EAS item id (the
// collab item id) and etag the collab ETag. The EAS surface owns this
// projection rather than sharing one with EWS/JMAP — each surface maps the
// canonical event into its own wire shape (mirroring MessageFromRaw for mail).
func CalendarItemFromICal(serverID, etag, raw string) CalendarItem {
	item := CalendarItem{ServerID: serverID, ETag: etag, Sensitivity: "0", BusyStatus: "2"}
	vevent := sectionBody(raw, "VEVENT")
	if vevent == "" {
		return item
	}
	for _, line := range unfoldICal(vevent) {
		name, params, value := parseICalLine(line)
		switch name {
		case "UID":
			item.UID = unescapeICalText(value)
		case "SUMMARY":
			item.Subject = unescapeICalText(value)
		case "DESCRIPTION":
			item.Body = unescapeICalText(value)
		case "LOCATION":
			item.Location = unescapeICalText(value)
		case "DTSTART":
			item.Start, item.AllDay = parseICalTime(params, value)
		case "DTEND":
			item.End, _ = parseICalTime(params, value)
		case "DTSTAMP":
			item.DtStamp, _ = parseICalTime(params, value)
		case "ORGANIZER":
			item.OrgName = params["CN"]
			item.OrgEmail = strings.TrimPrefix(strings.ToLower(value), "mailto:")
		case "CLASS":
			item.Sensitivity = sensitivityOf(value)
		case "TRANSP":
			if strings.EqualFold(value, "TRANSPARENT") {
				item.BusyStatus = "0"
			}
		case "STATUS":
			if strings.EqualFold(value, "TENTATIVE") {
				item.BusyStatus = "1"
			}
		}
	}
	if item.End.IsZero() {
		item.End = item.Start
	}
	if item.DtStamp.IsZero() {
		item.DtStamp = item.Start
	}
	return item
}

// InviteEventFromMIME extracts the meeting event from a raw iMIP message — the
// text/calendar part of a meeting-request email — into a CalendarItem. It is the
// source for a MeetingResponse: the accepted/declined invite's event is read
// here, then written to (or removed from) the calendar. Returns false when the
// message carries no calendar part or the event has no UID.
func InviteEventFromMIME(raw []byte) (CalendarItem, bool) {
	ical := extractPart(raw, "text/calendar")
	if ical == nil {
		return CalendarItem{}, false
	}
	it := CalendarItemFromICal("", "", string(ical))
	if it.UID == "" {
		return CalendarItem{}, false
	}
	return it, true
}

// calendarAppData projects a CalendarItem into its EAS ApplicationData elements:
// the Calendar-class fields (code page 4) plus the AirSyncBase Body and Location
// (code page 17), which carry the description and location for 16.x clients (the
// page-4 Body/Location tokens are 2.5/≤14.1 legacy).
func calendarAppData(it CalendarItem) []*wbxml.Element {
	cal := func(name, text string) *wbxml.Element {
		return &wbxml.Element{Page: wbxml.PageCalendar, Name: name, Text: text}
	}
	allDay := "0"
	if it.AllDay {
		allDay = "1"
	}
	els := []*wbxml.Element{
		cal("Timezone", utcTimeZoneBlob),
		cal("DtStamp", it.DtStamp.UTC().Format(compactDateTime)),
		cal("StartTime", it.Start.UTC().Format(compactDateTime)),
		cal("EndTime", it.End.UTC().Format(compactDateTime)),
		cal("Subject", it.Subject),
		cal("UID", it.UID),
		cal("AllDayEvent", allDay),
		cal("BusyStatus", it.BusyStatus),
		cal("Sensitivity", it.Sensitivity),
		cal("MeetingStatus", "0"),
	}
	if it.OrgEmail != "" {
		els = append(els, cal("OrganizerEmail", it.OrgEmail))
	}
	if it.OrgName != "" {
		els = append(els, cal("OrganizerName", it.OrgName))
	}
	els = append(els, &wbxml.Element{Page: wbxml.PageAirSyncBase, Name: "Body", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSyncBase, Name: "Type", Text: "1"},
		{Page: wbxml.PageAirSyncBase, Name: "EstimatedDataSize", Text: strconv.Itoa(len(it.Body))},
		{Page: wbxml.PageAirSyncBase, Name: "Truncated", Text: "0"},
		{Page: wbxml.PageAirSyncBase, Name: "Data", Text: it.Body},
	}})
	if it.Location != "" {
		els = append(els, &wbxml.Element{Page: wbxml.PageAirSyncBase, Name: "Location", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSyncBase, Name: "DisplayName", Text: it.Location},
		}})
	}
	return els
}

// calendarItemFromAppData parses a client's calendar ApplicationData (an EAS Add
// or Change body) into a CalendarItem. It is the inverse of calendarAppData: the
// scheduling fields come from the Calendar page (4) and the description/location
// from AirSyncBase (page 17), the shape a 16.x client sends. Times are EAS
// Compact DateTime (UTC); ServerID/ETag are not set (the caller assigns them).
func calendarItemFromAppData(app *wbxml.Element) CalendarItem {
	it := CalendarItem{Sensitivity: "0", BusyStatus: "2"}
	if app == nil {
		return it
	}
	get := func(name string) string {
		if e := app.Sub(name); e != nil {
			return e.Text
		}
		return ""
	}
	it.UID = get("UID")
	it.Subject = get("Subject")
	it.Location = get("Location") // page-4 Location (<=14.1); AirSyncBase overrides below
	it.AllDay = get("AllDayEvent") == "1"
	if s := get("Sensitivity"); s != "" {
		it.Sensitivity = s
	}
	if b := get("BusyStatus"); b != "" {
		it.BusyStatus = b
	}
	it.Start = parseCompactTime(get("StartTime"))
	it.End = parseCompactTime(get("EndTime"))
	// AirSyncBase Body (Data) and Location (DisplayName) are the 16.x carriers.
	if body := app.Sub("Body"); body != nil {
		if d := body.Sub("Data"); d != nil {
			it.Body = d.Text
		}
	}
	if loc := app.Sub("Location"); loc != nil {
		if dn := loc.Sub("DisplayName"); dn != nil {
			it.Location = dn.Text
		}
	}
	return it
}

// parseCompactTime parses an EAS Compact DateTime (UTC) into a time, or the zero
// time when empty or malformed.
func parseCompactTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(compactDateTime, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// BuildICalEvent renders a CalendarItem as a canonical RFC 5545 iCalendar object.
// EAS calendar times are UTC instants (the Timezone element is for display and
// recurrence, not the stored instant), so timed events are written as UTC
// DTSTART/DTEND and all-day events as VALUE=DATE. The EAS surface owns this
// builder; the payload is stored verbatim and read back by EWS/CalDAV/JMAP, so
// it carries the cross-surface UID and the core scheduling fields.
func BuildICalEvent(it CalendarItem) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//uMailServer//ActiveSync//EN\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	b.WriteString("UID:" + it.UID + "\r\n")
	b.WriteString("DTSTAMP:" + time.Now().UTC().Format(compactDateTime) + "\r\n")
	if it.AllDay {
		b.WriteString("DTSTART;VALUE=DATE:" + it.Start.UTC().Format("20060102") + "\r\n")
		if !it.End.IsZero() {
			b.WriteString("DTEND;VALUE=DATE:" + it.End.UTC().Format("20060102") + "\r\n")
		}
	} else {
		b.WriteString("DTSTART:" + it.Start.UTC().Format(compactDateTime) + "\r\n")
		if !it.End.IsZero() {
			b.WriteString("DTEND:" + it.End.UTC().Format(compactDateTime) + "\r\n")
		}
	}
	b.WriteString("SUMMARY:" + escapeICalText(it.Subject) + "\r\n")
	if it.Location != "" {
		b.WriteString("LOCATION:" + escapeICalText(it.Location) + "\r\n")
	}
	if it.Body != "" {
		b.WriteString("DESCRIPTION:" + escapeICalText(it.Body) + "\r\n")
	}
	if c := classOfSensitivity(it.Sensitivity); c != "" {
		b.WriteString("CLASS:" + c + "\r\n")
	}
	if it.BusyStatus == "0" {
		b.WriteString("TRANSP:TRANSPARENT\r\n")
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

// calendarOwnedProps is the set of VEVENT properties BuildICalEvent emits — the
// only ones the EAS calendar projection can represent. MergeICalEvent replaces
// exactly these on a client edit and preserves everything else (RRULE, EXDATE,
// ATTENDEE, ORGANIZER, VALARM, STATUS, X-*). Keep it in lockstep with
// BuildICalEvent: a property emitted there but missing here duplicates on edit;
// one listed here but not emitted is dropped on edit.
var calendarOwnedProps = map[string]bool{
	"UID": true, "DTSTAMP": true, "DTSTART": true, "DTEND": true,
	"SUMMARY": true, "LOCATION": true, "DESCRIPTION": true, "CLASS": true, "TRANSP": true,
}

// MergeICalEvent rebuilds the VEVENT from the edited item but preserves every
// VEVENT property the projection does not model, so a phone edit that touches
// only modeled fields does not erase the canonical event's recurrence,
// attendees, or alarms. Falls back to a fresh build when there is no existing
// record (a Change against a missing event, or a first write).
func MergeICalEvent(existing string, it CalendarItem) string {
	rebuilt := BuildICalEvent(it)
	if strings.TrimSpace(existing) == "" {
		return rebuilt
	}
	return mergeRFCSection(existing, rebuilt, "VEVENT", calendarOwnedProps)
}

// classOfSensitivity maps an EAS Sensitivity to an iCalendar CLASS, or "" for
// the default (normal) sensitivity.
func classOfSensitivity(s string) string {
	switch s {
	case "2":
		return "PRIVATE"
	case "3":
		return "CONFIDENTIAL"
	default:
		return ""
	}
}

// escapeICalText applies RFC 5545 TEXT escaping (\\ \, \; \n).
func escapeICalText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`)
	return r.Replace(s)
}

// sensitivityOf maps an iCalendar CLASS to an EAS Sensitivity value.
func sensitivityOf(class string) string {
	switch strings.ToUpper(strings.TrimSpace(class)) {
	case "PRIVATE":
		return "2"
	case "CONFIDENTIAL":
		return "3"
	default:
		return "0"
	}
}

// sectionBody returns the text between the first BEGIN:<name> and its matching
// END:<name>, or "" when the section is absent.
func sectionBody(raw, name string) string {
	_, after, ok := strings.Cut(raw, "BEGIN:"+name)
	if !ok {
		return ""
	}
	body, _, ok := strings.Cut(after, "END:"+name)
	if !ok {
		return ""
	}
	return body
}

// unfoldICal splits an iCalendar body into logical lines, undoing RFC 5545 line
// folding (a CRLF followed by a space or tab continues the previous line).
func unfoldICal(body string) []string {
	raw := strings.ReplaceAll(body, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var lines []string
	for ln := range strings.SplitSeq(raw, "\n") {
		if ln == "" {
			continue
		}
		if (ln[0] == ' ' || ln[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += ln[1:]
			continue
		}
		lines = append(lines, ln)
	}
	return lines
}

// parseICalLine splits a content line into its property name, parameters and
// value. The property name and parameters precede the first colon; parameters
// are semicolon-separated NAME=value pairs (iCal parameter values do not carry
// unquoted colons, so a first-colon split is sufficient here).
func parseICalLine(line string) (name string, params map[string]string, value string) {
	params = map[string]string{}
	head, value, ok := strings.Cut(line, ":")
	if !ok {
		return strings.ToUpper(line), params, ""
	}
	parts := strings.Split(head, ";")
	name = strings.ToUpper(parts[0])
	for _, p := range parts[1:] {
		if k, v, ok := strings.Cut(p, "="); ok {
			params[strings.ToUpper(k)] = strings.Trim(v, "\"")
		}
	}
	return name, params, value
}

// parseICalTime resolves an iCalendar DATE/DATE-TIME value to an absolute UTC
// instant. A VALUE=DATE (or bare 8-digit date) is an all-day value at midnight;
// a trailing Z is UTC; a TZID parameter anchors a civil-local value to that IANA
// zone; an unqualified value is treated as UTC (best effort).
func parseICalTime(params map[string]string, value string) (t time.Time, allDay bool) {
	value = strings.TrimSpace(value)
	if params["VALUE"] == "DATE" || (len(value) == 8 && !strings.ContainsAny(value, "TZ")) {
		if d, err := time.Parse("20060102", value); err == nil {
			return d.UTC(), true
		}
	}
	if strings.HasSuffix(value, "Z") {
		if dt, err := time.Parse("20060102T150405Z", value); err == nil {
			return dt.UTC(), false
		}
	}
	if tz := params["TZID"]; tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			if dt, err := time.ParseInLocation("20060102T150405", value, loc); err == nil {
				return dt.UTC(), false
			}
		}
	}
	if dt, err := time.Parse("20060102T150405", value); err == nil {
		return dt.UTC(), false
	}
	return time.Time{}, false
}

// unescapeICalText reverses RFC 5545 TEXT escaping (\\ \, \; \n).
func unescapeICalText(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return r.Replace(s)
}
