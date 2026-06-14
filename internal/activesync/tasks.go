package activesync

import (
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// TaskItem is one to-do projected for an EAS Sync Add/Change of a tasks
// collection. EAS tasks carry the start/due dates as Compact DateTime, the
// completion state, an importance and a sensitivity; the body rides AirSyncBase.
// ETag drives the enumerate-and-diff cursor (a tasks collection has no change
// journal).
type TaskItem struct {
	ServerID string
	ETag     string
	UID      string

	Subject string
	Body    string

	Start         time.Time
	Due           time.Time
	Complete      bool
	DateCompleted time.Time

	Importance  string // EAS: "0" low, "1" normal, "2" high
	Sensitivity string // EAS: "0" normal, "1" personal, "2" private, "3" confidential
}

// TaskSource supplies a tasks collection's to-dos for the Sync command. folderID
// is the canonical (semcore) tasks folder id — the routing prefix already
// stripped. Implementations read the same collaboration store EWS/CalDAV/webmail
// read, so a phone's task list converges with them on one source.
type TaskSource interface {
	// ListItems returns the tasks folder's current to-dos.
	ListItems(email, folderID string) ([]TaskItem, error)
}

// TaskItemFromVTODO projects a canonical iCalendar VTODO (the collab store's
// RawData) into a TaskItem. serverID is the stable EAS item id (the VTODO UID,
// which the canonical store keys on) and etag the collab ETag. The EAS surface
// owns this projection rather than sharing one with EWS — each surface maps the
// canonical to-do into its own wire shape.
func TaskItemFromVTODO(serverID, etag, raw string) TaskItem {
	item := TaskItem{ServerID: serverID, ETag: etag, Importance: "1", Sensitivity: "0"}
	vtodo := sectionBody(raw, "VTODO")
	if vtodo == "" {
		return item
	}
	for _, line := range unfoldICal(vtodo) {
		name, params, value := parseICalLine(line)
		switch name {
		case "UID":
			item.UID = unescapeICalText(value)
		case "SUMMARY":
			item.Subject = unescapeICalText(value)
		case "DESCRIPTION":
			item.Body = unescapeICalText(value)
		case "DTSTART":
			item.Start, _ = parseICalTime(params, value)
		case "DUE":
			item.Due, _ = parseICalTime(params, value)
		case "COMPLETED":
			item.DateCompleted, _ = parseICalTime(params, value)
			item.Complete = true
		case "STATUS":
			if strings.EqualFold(value, "COMPLETED") {
				item.Complete = true
			}
		case "PERCENT-COMPLETE":
			if value == "100" {
				item.Complete = true
			}
		case "PRIORITY":
			item.Importance = taskImportanceOf(value)
		case "CLASS":
			item.Sensitivity = sensitivityOf(value)
		}
	}
	return item
}

// taskAppData projects a TaskItem into its EAS ApplicationData elements: the
// Tasks-class fields (code page 9) plus the AirSyncBase Body (code page 17),
// which carries the notes for 16.x clients (the page-9 Body token is 2.5 legacy).
// EAS pairs each date with its UTC twin; task dates are projected as UTC instants
// so the local and UTC values coincide.
func taskAppData(it TaskItem) []*wbxml.Element {
	task := func(name, text string) *wbxml.Element {
		return &wbxml.Element{Page: wbxml.PageTasks, Name: name, Text: text}
	}
	els := []*wbxml.Element{task("Subject", it.Subject)}
	if !it.Start.IsZero() {
		start := it.Start.UTC().Format(compactDateTime)
		els = append(els, task("StartDate", start), task("UtcStartDate", start))
	}
	if !it.Due.IsZero() {
		due := it.Due.UTC().Format(compactDateTime)
		els = append(els, task("DueDate", due), task("UtcDueDate", due))
	}
	els = append(els, task("Importance", it.Importance))
	complete := "0"
	if it.Complete {
		complete = "1"
	}
	els = append(els, task("Complete", complete))
	if it.Complete && !it.DateCompleted.IsZero() {
		els = append(els, task("DateCompleted", it.DateCompleted.UTC().Format(compactDateTime)))
	}
	els = append(els, task("ReminderSet", "0"), task("Sensitivity", it.Sensitivity))
	els = append(els, &wbxml.Element{Page: wbxml.PageAirSyncBase, Name: "Body", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSyncBase, Name: "Type", Text: "1"},
		{Page: wbxml.PageAirSyncBase, Name: "EstimatedDataSize", Text: strconv.Itoa(len(it.Body))},
		{Page: wbxml.PageAirSyncBase, Name: "Truncated", Text: "0"},
		{Page: wbxml.PageAirSyncBase, Name: "Data", Text: it.Body},
	}})
	return els
}

// taskItemFromAppData parses a client's task ApplicationData (an EAS Add or
// Change body) into a TaskItem. It is the inverse of taskAppData: the fields come
// from the Tasks page (9) and the notes from AirSyncBase (page 17). The UTC date
// twin is preferred over the local one. ServerID/ETag are not set (the caller
// assigns them).
func taskItemFromAppData(app *wbxml.Element) TaskItem {
	it := TaskItem{Importance: "1", Sensitivity: "0"}
	if app == nil {
		return it
	}
	get := func(name string) string {
		if e := app.Sub(name); e != nil {
			return e.Text
		}
		return ""
	}
	it.Subject = get("Subject")
	it.Start = parseCompactTime(firstNonEmpty(get("UtcStartDate"), get("StartDate")))
	it.Due = parseCompactTime(firstNonEmpty(get("UtcDueDate"), get("DueDate")))
	it.Complete = get("Complete") == "1"
	it.DateCompleted = parseCompactTime(get("DateCompleted"))
	if imp := get("Importance"); imp != "" {
		it.Importance = imp
	}
	if s := get("Sensitivity"); s != "" {
		it.Sensitivity = s
	}
	if body := app.Sub("Body"); body != nil {
		if d := body.Sub("Data"); d != nil {
			it.Body = d.Text
		}
	}
	return it
}

// BuildVTODO renders a TaskItem as a canonical RFC 5545 VTODO. The EAS surface
// owns this builder; the payload is stored verbatim and read back by EWS/webmail,
// so it carries the cross-surface UID and the to-do's core fields. Task dates are
// UTC instants.
func BuildVTODO(it TaskItem) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//uMailServer//ActiveSync//EN\r\n")
	b.WriteString("BEGIN:VTODO\r\n")
	b.WriteString("UID:" + it.UID + "\r\n")
	b.WriteString("DTSTAMP:" + time.Now().UTC().Format(compactDateTime) + "\r\n")
	if !it.Start.IsZero() {
		b.WriteString("DTSTART:" + it.Start.UTC().Format(compactDateTime) + "\r\n")
	}
	if !it.Due.IsZero() {
		b.WriteString("DUE:" + it.Due.UTC().Format(compactDateTime) + "\r\n")
	}
	b.WriteString("SUMMARY:" + escapeICalText(it.Subject) + "\r\n")
	if it.Body != "" {
		b.WriteString("DESCRIPTION:" + escapeICalText(it.Body) + "\r\n")
	}
	if it.Complete {
		b.WriteString("STATUS:COMPLETED\r\nPERCENT-COMPLETE:100\r\n")
		if !it.DateCompleted.IsZero() {
			b.WriteString("COMPLETED:" + it.DateCompleted.UTC().Format(compactDateTime) + "\r\n")
		}
	} else {
		b.WriteString("STATUS:NEEDS-ACTION\r\n")
	}
	if p := priorityOf(it.Importance); p != "" {
		b.WriteString("PRIORITY:" + p + "\r\n")
	}
	if c := classOfSensitivity(it.Sensitivity); c != "" {
		b.WriteString("CLASS:" + c + "\r\n")
	}
	b.WriteString("END:VTODO\r\nEND:VCALENDAR\r\n")
	return b.String()
}

// taskOwnedProps is the set of VTODO properties BuildVTODO emits — the only ones
// the EAS task projection can represent. MergeVTODO replaces exactly these on a
// client edit and preserves everything else (RRULE, VALARM, CATEGORIES, X-*).
// Keep it in lockstep with BuildVTODO.
var taskOwnedProps = map[string]bool{
	"UID": true, "DTSTAMP": true, "DTSTART": true, "DUE": true, "SUMMARY": true,
	"DESCRIPTION": true, "STATUS": true, "PERCENT-COMPLETE": true, "COMPLETED": true,
	"PRIORITY": true, "CLASS": true,
}

// MergeVTODO rebuilds the to-do from the edited item but preserves every VTODO
// property the projection does not model, so a phone edit that touches only
// modeled fields does not erase the canonical task's recurrence, reminders, or
// categories. Falls back to a fresh build when there is no existing record.
func MergeVTODO(existing string, it TaskItem) string {
	rebuilt := BuildVTODO(it)
	if strings.TrimSpace(existing) == "" {
		return rebuilt
	}
	return mergeRFCSection(existing, rebuilt, "VTODO", taskOwnedProps)
}

// taskImportanceOf maps an iCalendar PRIORITY (1 highest .. 9 lowest, 0
// undefined) to an EAS task Importance ("0" low, "1" normal, "2" high). It is
// distinct from the mail importanceOf, which maps the RFC 4021 header words.
func taskImportanceOf(priority string) string {
	p, err := strconv.Atoi(strings.TrimSpace(priority))
	if err != nil {
		return "1"
	}
	switch {
	case p >= 1 && p <= 4:
		return "2"
	case p >= 6 && p <= 9:
		return "0"
	default:
		return "1"
	}
}

// priorityOf maps an EAS task Importance to an iCalendar PRIORITY, or "" for the
// default (normal) importance.
func priorityOf(importance string) string {
	switch importance {
	case "2":
		return "1"
	case "0":
		return "9"
	default:
		return ""
	}
}

// firstNonEmpty returns the first non-empty argument, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
