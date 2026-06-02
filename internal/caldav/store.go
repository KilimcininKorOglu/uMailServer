package caldav

// Store is the calendar persistence surface used by the CalDAV protocol server
// and the webmail calendar handler. Two implementations exist: the legacy
// filesystem *Storage and the canonical *CollabStore (semcore collaboration
// store). Routing both surfaces through one Store keeps EWS, CalDAV, and the
// webmail calendar reading and writing a single source of truth — an event
// created through any surface is visible from all of them.
type Store interface {
	CreateCalendar(username string, cal *Calendar) error
	GetCalendar(username, calendarID string) (*Calendar, error)
	GetCalendars(username string) ([]*Calendar, error)
	UpdateCalendar(username string, cal *Calendar) error
	DeleteCalendar(username, calendarID string) error
	SaveEvent(username, calendarID string, event *CalendarEvent, icsData string) error
	GetEvent(username, calendarID, eventUID string) (string, error)
	GetEvents(username, calendarID string) ([]string, error)
	DeleteEvent(username, calendarID, eventUID string) error
	SetETag(username, calendarID, eventUID, etag string) error
	GetETag(username, calendarID, eventUID string) string
	GetCalendarETag(username, calendarID string) string
}

// compile-time assertion: the filesystem Storage satisfies Store.
var _ Store = (*Storage)(nil)
