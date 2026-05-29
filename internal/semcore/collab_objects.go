// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the canonical object models for collaboration objects:
// calendar items (events and tasks), contacts, and standalone tasks. These
// models are the authoritative source of truth for identity, version, recurrence,
// reminder, and lifecycle semantics across CalDAV, CardDAV, and future EWS
// collaboration projections.
//
// # Design Rules
//
//   - All IDs are opaque; only equality comparisons are meaningful.
//   - ChangeKey advances on every semantically-visible mutation.
//   - A stale ChangeKey on write must be rejected explicitly (version conflict).
//   - Recurrence data is parsed into structured form; raw iCal is also preserved.
//   - Reminder state is persisted so it survives across projection rereads.
//   - Lifecycle events are emitted for all create/update/delete operations.
package semcore

import (
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Collaboration object kind
// ---------------------------------------------------------------------------

// CollabKind identifies the kind of collaboration object.
type CollabKind uint8

const (
	CollabKindEvent   CollabKind = iota // VEVENT: calendar event
	CollabKindTodo                      // VTODO: calendar task
	CollabKindContact                   // VCARD: contact
	CollabKindTask                      // standalone task
)

// String returns a human-readable label for the kind.
func (k CollabKind) String() string {
	switch k {
	case CollabKindEvent:
		return "event"
	case CollabKindTodo:
		return "todo"
	case CollabKindContact:
		return "contact"
	case CollabKindTask:
		return "task"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// Recurrence
//
// Canonical recurrence model. RFC 5545 RRULE is preserved as raw text for
// wire compatibility, but the structured RecurrenceRule field holds the
// parsed form for server-side expansion and conflict detection.
// ---------------------------------------------------------------------------

// RecurrenceRule represents a parsed iCalendar recurrence rule (RFC 5545).
type RecurrenceRule struct {
	Freq     string // DAILY, WEEKLY, MONTHLY, YEARLY
	Interval int    // every N freq units

	// Until or Count (mutually exclusive)
	Until    time.Time
	UseUntil bool
	Count    int
	UseCount bool

	// WEEKLY
	ByDay []string // MO, TU, WE, TH, FR, SA, SU

	// MONTHLY
	ByMonthDay   []int // 1-31
	ByDayMonthly bool  // weekday index (e.g. 2TU = second Tuesday)

	// YEARLY
	ByMonth   []int // 1-12
	ByYearDay []int // 1-366
	ByWeekNo  []int // week numbers

	// Set positions (RFC 7986)
	BySetPos []int
}

// IsZero returns true for an empty/unset rule.
func (r *RecurrenceRule) IsZero() bool {
	return r.Freq == "" && r.Interval == 0 && !r.UseUntil && !r.UseCount
}

// RRULEText returns the RRULE wire format string for this rule.
// Returns "" when the rule is zero.
func (r *RecurrenceRule) RRULEText() string {
	if r.IsZero() {
		return ""
	}
	var parts []string
	parts = append(parts, "FREQ="+r.Freq)
	if r.Interval > 1 {
		parts = append(parts, "INTERVAL="+itoa(r.Interval))
	}
	if r.UseUntil {
		parts = append(parts, "UNTIL="+formatICalDate(r.Until))
	}
	if r.UseCount {
		parts = append(parts, "COUNT="+itoa(r.Count))
	}
	if len(r.ByDay) > 0 {
		parts = append(parts, "BYDAY="+strings.Join(r.ByDay, ","))
	}
	if len(r.ByMonthDay) > 0 {
		parts = append(parts, "BYMONTHDAY="+joinInts(r.ByMonthDay, ","))
	}
	if len(r.ByMonth) > 0 {
		parts = append(parts, "BYMONTH="+joinInts(r.ByMonth, ","))
	}
	if len(r.ByYearDay) > 0 {
		parts = append(parts, "BYYEARDAY="+joinInts(r.ByYearDay, ","))
	}
	if len(r.ByWeekNo) > 0 {
		parts = append(parts, "BYWEEKNO="+joinInts(r.ByWeekNo, ","))
	}
	if len(r.BySetPos) > 0 {
		parts = append(parts, "BYSETPOS="+joinInts(r.BySetPos, ","))
	}
	return strings.Join(parts, ";")
}

// itoa converts a non-negative integer to a decimal string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append(b, byte('0'+n%10))
		n /= 10
	}
	// Reverse.
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

func formatICalDate(t time.Time) string {
	return t.Format("20060102T150405Z")
}

func joinInts(ns []int, sep string) string {
	if len(ns) == 0 {
		return ""
	}
	var parts []string
	for _, n := range ns {
		parts = append(parts, itoa(n))
	}
	return strings.Join(parts, sep)
}

// ---------------------------------------------------------------------------
// Exception dates
// ---------------------------------------------------------------------------

// ExceptionDate records a date/time that is excluded from a recurrence series.
// Stored as the RFC 5545 RECURRENCE-ID wire format string.
type ExceptionDate struct {
	RecurrenceID RecurrenceId
	Reason       string // optional human-readable reason for the exclusion
}

// ---------------------------------------------------------------------------
// RecurrenceID range (RFC 4791 §9.2.1)
// ---------------------------------------------------------------------------

// RecurrenceRange indicates whether a RECURRENCE-ID is a specific occurrence,
// a range starting from an occurrence, or a range ending at an occurrence.
type RecurrenceRange uint8

const (
	RecurrenceRangeThisAndFuture RecurrenceRange = iota // "ThisAndFuture"
	RecurrenceRangeThisOnly                             // exact occurrence only
)

// ---------------------------------------------------------------------------
// Attendee
// ---------------------------------------------------------------------------

// AttendeeRole is the iCalendar role for an attendee.
type AttendeeRole string

const (
	AttendeeRoleChair          AttendeeRole = "CHAIR"
	AttendeeRoleRequired       AttendeeRole = "REQ-PARTICIPANT"
	AttendeeRoleOptional       AttendeeRole = "OPT-PARTICIPANT"
	AttendeeRoleNonParticipant AttendeeRole = "NON-PARTICIPANT"
)

// AttendeePartStat is the participation status for an attendee.
type AttendeePartStat string

const (
	AttendeePartStatNeedsAction AttendeePartStat = "NEEDS-ACTION"
	AttendeePartStatAccepted    AttendeePartStat = "ACCEPTED"
	AttendeePartStatDeclined    AttendeePartStat = "DECLINED"
	AttendeePartStatTentative   AttendeePartStat = "TENTATIVE"
	AttendeePartStatDelegated   AttendeePartStat = "DELEGATED"
)

// Attendee represents a calendar event attendee or organizer.
type Attendee struct {
	CalAddress string // mailto: URI
	CN         string // display name
	Role       AttendeeRole
	PartStat   AttendeePartStat
	RSVP       bool // true if response requested
}

// ---------------------------------------------------------------------------
// Reminder
// ---------------------------------------------------------------------------

// ReminderAction is what the system does when a reminder fires.
type ReminderAction uint8

const (
	ReminderActionEmail     ReminderAction = iota // send email notification
	ReminderActionDisplay                         // display notification
	ReminderActionProcedure                       // execute a procedure/script
)

// ReminderTrigger describes when a reminder fires relative to an event.
type ReminderTrigger struct {
	Relative bool      // true = duration before event; false = absolute time
	Duration int       // seconds before event (when Relative=true)
	Due      time.Time // absolute due time (when Relative=false)
	Action   ReminderAction
}

// IsZero returns true for an unset trigger.
func (t *ReminderTrigger) IsZero() bool {
	return t.Duration == 0 && t.Due.IsZero()
}

// ---------------------------------------------------------------------------
// CalendarItem
//
// Canonical calendar object. Represents a VEVENT or VTODO component.
// CalendarItemId is scoped to a FolderId (calendar collection).
// ---------------------------------------------------------------------------

// CalendarItem is the canonical representation of a calendar event or task
// (VTODO). It holds all semantically-meaningful fields parsed from the
// iCalendar data. The raw iCal bytes are preserved in RawICal for wire
// compatibility with CalDAV GET/PUT responses.
//
// Identity: CalendarItemId — stable across reads, edits, and moves.
// Version: CalendarChangeKey — advances on every semantically-visible mutation.
// Recurrence: RRULE and RDATE describe the recurrence series; RecurrenceId
// identifies a specific occurrence within the series.
//
// Lifecycle: A CalendarItem carries a master identity. When an exception
// (RECURRENCE-ID) is stored, it refers back to the master CalendarItemId
// but is treated as a separate semantic entity with its own ChangeKey.
type CalendarItem struct {
	// Identity
	ID        CalendarItemId
	MasterID  CalendarItemId // zero for master; non-zero for exceptions
	FolderID  FolderId
	MailboxID MailboxId
	ChangeKey CalendarChangeKey

	// Object kind: event or todo
	Kind CollabKind

	// iCal UID (RFC 5545) — stored as a cross-reference, but the stable
	// semantic identity is CalendarItemId. The UID may be used to correlate
	// with external calendar systems.
	IcalUID string

	// Time range
	DTStart  time.Time
	DTEnd    time.Time
	IsAllDay bool

	// For all-day events, DTStart is date-only (00:00:00 UTC).
	// For timed events, DTStart/DTEnd carry timezone info in TZID.
	DTStartTZID string

	// Summary (SUMMARY) and description (DESCRIPTION)
	Summary     string
	Description string

	// Location (LOCATION)
	Location string

	// Organizer and attendees
	Organizer *Attendee
	Attendees []Attendee

	// Transparency (TRANSP): OPAQUE or TRANSPARENT
	Transparency string // "OPAQUE" or "TRANSPARENT"

	// Priority (PRIORITY): 0-9, where 0 = undefined, 1-4 = high, 5 = medium, 6-9 = low
	Priority int

	// Status (STATUS): CONFIRMED, TENTATIVE, CANCELLED for events;
	// NEEDS-ACTION, COMPLETED, IN-PROCESS, CANCELLED for todos
	//nolint:misspell // RFC 5545 uses British spelling CANCELLED
	Status string

	// Recurrence: master event only (Kind = CollabKindEvent)
	IsRecurring      bool
	IsRecurrenceRoot bool // true = this is the RRULE master
	RecurrenceRule   *RecurrenceRule
	RecurrenceDates  []time.Time // RDATE exclusions/additions
	ExceptionDates   []ExceptionDate
	RecurrenceID     RecurrenceId // non-zero for exceptions
	RecurrenceRange  RecurrenceRange

	// Reminders
	HasReminder bool
	Reminder    *ReminderTrigger

	// Sequence number (RFC 4791 SEQUENCE)
	Sequence int

	// Created / modified times
	Created     time.Time
	Modified    time.Time
	CreatedRaw  string // RFC 5322 CREATED
	ModifiedRaw string // RFC 5322 LAST-MODIFIED

	// Transparency and busy status
	IsTransparent bool

	// Raw iCal data — preserved for CalDAV GET/PUT wire compatibility.
	// When serving CalDAV, the server serializes this back to the client.
	// When parsing, the server should populate structured fields and
	// preserve the raw text for round-trip fidelity.
	RawICal string

	// Size in bytes of the raw iCal data
	Size int64

	// ETags: the ETag exposed to CalDAV clients is derived from ChangeKey.
	// The CalDAV ETag header = fmt.Sprintf("%q, %s", changeKey.String(), rawHash)
	// where rawHash is a hash of RawICal for content-change detection.
	ETag string
}

// ItemClass returns the "ITEMClass" Outlook property equivalent.
// For tasks (Kind == CollabKindTodo), this is "PUBLIC" or "PRIVATE".
func (c *CalendarItem) ItemClass() string {
	return "PUBLIC"
}

// IsEvent returns true for VEVENT components.
func (c *CalendarItem) IsEvent() bool { return c.Kind == CollabKindEvent }

// IsTodo returns true for VTODO components.
func (c *CalendarItem) IsTodo() bool { return c.Kind == CollabKindTodo }

// IsException returns true when this item is an exception to a recurring series.
func (c *CalendarItem) IsException() bool { return !c.RecurrenceID.IsZero() }

// IsZero returns true for a nil/uninitialized CalendarItem.
func (c *CalendarItem) IsZero() bool { return c.ID.IsZero() }

// ETagValue returns the DAV-compliant ETag value for this calendar item.
// It is derived from the CalendarChangeKey. This replaces mtime-based
// ETag computation in CalDAV storage.
func (c *CalendarItem) ETagValue() string {
	if c.ChangeKey.IsZero() {
		return ""
	}
	return c.ChangeKey.String()
}

// ---------------------------------------------------------------------------
// Contact
// ---------------------------------------------------------------------------

// Contact is the canonical representation of a vCard contact.
// ContactId is scoped to a FolderId (addressbook collection).
type Contact struct {
	// Identity
	ID        ContactId
	FolderID  FolderId
	MailboxID MailboxId
	ChangeKey ContactChangeKey

	// vCard fields (RFC 6350)
	IcalUID   string                        // FORMMAYBE: X-UID if present
	FullName  string                        // FN
	NameParts NameParts                     // N structured
	Nicknames []string                      // NICKNAME
	Emails    []LabeledValue[string]        // EMAIL
	Phones    []LabeledValue[string]        // TEL
	Addresses []LabeledValue[PostalAddress] // ADR
	URLs      []LabeledValue[string]        // URL
	PhotoURL  string                        // PHOTO (external reference)

	// Organization
	Organization string   // ORG
	Title        string   // TITLE
	Role         string   // ROLE
	Departments  []string // ORG member values

	// Timestamps
	Birthday    time.Time // BDAY
	Anniversary time.Time // ANNIVERSARY

	// Notes
	Note string // NOTE

	// Group (distribution list membership)
	IsGroup bool // BEGIN:VCARD / END:VCARD with one member

	// Raw vCard data — preserved for CardDAV GET/PUT wire compatibility.
	RawVCard string

	// Size in bytes of the raw vCard data
	Size int64

	// ETag: DAV ETag derived from ContactChangeKey.
	ETag string
}

// NameParts represents the structured N: field from vCard.
type NameParts struct {
	Family     string
	Given      string
	Additional []string // second, third, etc.
	Prefix     string   // honorific prefixes
	Suffix     string   // honorific suffixes
}

// PostalAddress represents the structured ADR: field from vCard.
type PostalAddress struct {
	POBox      string
	Extended   string
	Street     string
	City       string
	Region     string
	PostalCode string
	Country    string
}

// LabeledValue pairs a label (TYPE parameter) with a value.
type LabeledValue[T any] struct {
	Label string // TYPE parameter: HOME, WORK, CELL, etc.
	Pref  int    // PREF parameter: 1 = most preferred
	Value T
}

// ItemClass returns the "ITEMClass" Outlook property equivalent for contacts.
func (c *Contact) ItemClass() string {
	return "PUBLIC"
}

// IsZero returns true for a nil/uninitialized Contact.
func (c *Contact) IsZero() bool { return c.ID.IsZero() }

// ETagValue returns the DAV-compliant ETag value for this contact.
// It is derived from the ContactChangeKey. This replaces mtime-based
// ETag computation in CardDAV storage.
func (c *Contact) ETagValue() string {
	if c.ChangeKey.IsZero() {
		return ""
	}
	return c.ChangeKey.String()
}

// ---------------------------------------------------------------------------
// Task
// ---------------------------------------------------------------------------

// TaskStatus is the task status value (RFC 7986).
type TaskStatus string

const (
	TaskStatusNeedsAction TaskStatus = "NEEDS-ACTION"
	TaskStatusCompleted   TaskStatus = "COMPLETED"
	TaskStatusInProcess   TaskStatus = "IN-PROCESS"
	TaskStatusCancelled   TaskStatus = "CANCELLED" //nolint:misspell // RFC 5545 uses British spelling CANCELLED
)

// Task is the canonical representation of a standalone task object.
// TaskId is scoped to a FolderId. Tasks that live inside calendar collections
// (as VTODO components) are stored as CalendarItem with Kind = CollabKindTodo
// and are NOT represented by this type.
//
// This distinction matters: a CalendarItem with Kind=CollabKindTodo lives in a
// calendar folder and participates in free/busy; a standalone Task lives in
// a task folder and does not. Both share the same recurrence/reminder/assignment
// semantics, but differ in folder semantics and free/busy impact.
type Task struct {
	// Identity
	ID        TaskId
	FolderID  FolderId
	MailboxID MailboxId
	ChangeKey TaskChangeKey

	// Core task fields (RFC 7986)
	IcalUID  string
	Summary  string
	Due      time.Time
	Start    time.Time // DUE:STARTDATE
	DoneTime time.Time // COMPLETED

	// Status and completion
	Status          TaskStatus
	PercentComplete float64 // 0.0 to 1.0

	// Priority
	Priority int // 0 = undefined, 1-4 = high, 5 = medium, 6-9 = low

	// Recurrence (VTODO)
	IsRecurring    bool
	RecurrenceRule *RecurrenceRule
	ExceptionDates []ExceptionDate

	// Reminder
	HasReminder bool
	Reminder    *ReminderTrigger

	// Assignment
	Organizer *Attendee
	Attendees []Attendee

	// Body
	Description string

	// Timestamps
	Created  time.Time
	Modified time.Time

	// Raw iCal VTODO data
	RawICal string

	// Size in bytes
	Size int64

	// ETag derived from TaskChangeKey
	ETag string
}

// IsZero returns true for a nil/uninitialized Task.
func (t *Task) IsZero() bool { return t.ID.IsZero() }

// ETagValue returns the DAV-compliant ETag value for this task.
func (t *Task) ETagValue() string {
	if t.ChangeKey.IsZero() {
		return ""
	}
	return t.ChangeKey.String()
}
