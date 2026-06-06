package caldav

import "github.com/umailserver/umailserver/internal/semcore"

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

// collabBackend is the calendar/task identity surface the semcore-backed CalDAV
// collab stores (CollabStore, CollabTaskStore) need. *semcore.BoltCollaborationStore
// satisfies it; holding the interface keeps CalDAV free of a concrete
// semantic-core dependency so a relational backend can slot in later.
type collabBackend interface {
	FindCalendarItemByUID(folderID semcore.FolderId, icalUID string) (msgKey string, rec *semcore.StoredCalendarItemIdentity, found bool, err error)
	ListCalendarItemsByFolder(folderID semcore.FolderId) ([]semcore.StoredCalendarItemIdentity, error)
	PutCalendarItemIdentityUnsafe(msgKey string, rec *semcore.StoredCalendarItemIdentity) error
	DeleteCalendarItemByUID(folderID semcore.FolderId, icalUID string) error
	FindTaskByUID(folderID semcore.FolderId, icalUID string) (msgKey string, rec *semcore.StoredTaskIdentity, found bool, err error)
	ListTasksByFolder(folderID semcore.FolderId) ([]semcore.StoredTaskIdentity, error)
	PutTaskIdentityUnsafe(msgKey string, rec *semcore.StoredTaskIdentity) error
	DeleteTaskByUID(folderID semcore.FolderId, icalUID string) error
}

// identityBackend is the folder-identity resolution surface the CalDAV collab
// stores need.
type identityBackend interface {
	EnsureFolderId(mboxKey, folderName, role string) (semcore.FolderId, error)
	GetFolderID(mboxKey, folderName string) (semcore.FolderId, error)
}

var (
	_ collabBackend   = (*semcore.BoltCollaborationStore)(nil)
	_ identityBackend = (*semcore.BoltIdentityStore)(nil)
)
