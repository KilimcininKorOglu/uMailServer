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
