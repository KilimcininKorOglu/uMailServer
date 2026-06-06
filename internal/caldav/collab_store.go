package caldav

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/semcore"
)

// defaultCalendarID and defaultCalendarName mirror the single calendar EWS and
// the webmail calendar already expose. The CollabStore maps every CalDAV
// calendarID to the mailbox's one calendar folder, so all surfaces share it.
const (
	defaultCalendarID   = "default"
	defaultCalendarName = "Calendar"
)

// CollabStore is the canonical, semcore-backed implementation of Store. Calendar
// events live in the same BoltCollaborationStore folder EWS writes to, so an
// event created via CalDAV or webmail is visible over EWS and vice versa. Each
// mailbox maps to a single calendar folder (role "calendar"), matching the one
// calendar EWS and the webmail calendar already present.
type CollabStore struct {
	collab   collabBackend
	identity identityBackend
}

// NewCollabStore builds a semcore-backed calendar Store.
func NewCollabStore(collab collabBackend, identity identityBackend) *CollabStore {
	return &CollabStore{collab: collab, identity: identity}
}

// compile-time assertion.
var _ Store = (*CollabStore)(nil)

// calendarFolder resolves (creating if needed) the mailbox's single calendar
// folder. It mirrors the EWS create path (internal/ews/collab.go) so both write
// to the same FolderId.
func (c *CollabStore) calendarFolder(username string) (semcore.FolderId, error) {
	if fid, err := c.identity.GetFolderID(username, "calendar"); err == nil && !fid.IsZero() {
		return fid, nil
	}
	if fid, err := c.identity.GetFolderID(username, "calendars"); err == nil && !fid.IsZero() {
		return fid, nil
	}
	return c.identity.EnsureFolderId(username, "calendar", "calendar")
}

func quotedRandomETag() string { return fmt.Sprintf("%q", uuid.New().String()) }

func (c *CollabStore) defaultCalendar() *Calendar {
	return &Calendar{ID: defaultCalendarID, Name: defaultCalendarName, Color: "#3b82f6"}
}

// CreateCalendar ensures the mailbox's calendar folder exists. In the
// single-calendar model the created calendar is always the default one.
func (c *CollabStore) CreateCalendar(username string, cal *Calendar) error {
	if _, err := c.calendarFolder(username); err != nil {
		return err
	}
	if cal.ID == "" {
		cal.ID = defaultCalendarID
	}
	return nil
}

// GetCalendar returns the mailbox's single calendar.
func (c *CollabStore) GetCalendar(username, calendarID string) (*Calendar, error) {
	if _, err := c.calendarFolder(username); err != nil {
		return nil, err
	}
	return c.defaultCalendar(), nil
}

// GetCalendars lists the mailbox's calendars (a single default calendar).
func (c *CollabStore) GetCalendars(username string) ([]*Calendar, error) {
	if _, err := c.calendarFolder(username); err != nil {
		return nil, err
	}
	return []*Calendar{c.defaultCalendar()}, nil
}

// UpdateCalendar is a no-op in the single-calendar model (no per-calendar
// metadata is persisted separately from the folder).
func (c *CollabStore) UpdateCalendar(username string, cal *Calendar) error {
	_, err := c.calendarFolder(username)
	return err
}

// DeleteCalendar clears every event in the mailbox's calendar folder.
func (c *CollabStore) DeleteCalendar(username, calendarID string) error {
	folder, err := c.calendarFolder(username)
	if err != nil {
		return err
	}
	items, err := c.collab.ListCalendarItemsByFolder(folder)
	if err != nil {
		return err
	}
	for _, it := range items {
		if derr := c.collab.DeleteCalendarItemByUID(folder, it.IcalUID); derr != nil {
			return derr
		}
	}
	return nil
}

// SaveEvent upserts an event into the canonical store keyed by its iCalendar
// UID, so editing an event from any surface updates the same record.
func (c *CollabStore) SaveEvent(username, calendarID string, event *CalendarEvent, icsData string) error {
	folder, err := c.calendarFolder(username)
	if err != nil {
		return err
	}
	mboxID, err := semcore.NewMailboxId(username)
	if err != nil {
		return err
	}
	ck, err := semcore.NewCalendarChangeKey(uuid.New().String())
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(icsData))
	rawHash := fmt.Sprintf("%x", sum)

	msgKey, existing, found, ferr := c.collab.FindCalendarItemByUID(folder, event.UID)
	if ferr != nil {
		return ferr
	}
	var calItemID semcore.CalendarItemId
	if found {
		calItemID = existing.ID // preserve identity across edits
	} else {
		calItemID, err = semcore.NewCalendarItemId(uuid.New().String())
		if err != nil {
			return err
		}
		msgKey = fmt.Sprintf("cal:%s:%s", folder.String(), event.UID)
	}

	rec := semcore.NewStoredCalendarItemIdentity(calItemID, folder, mboxID, ck, semcore.CollabKindEvent, event.UID, rawHash)
	rec.RawData = icsData
	rec.ETag = ck.String()
	return c.collab.PutCalendarItemIdentityUnsafe(msgKey, rec)
}

// GetEvent returns the raw iCalendar for one event, or "" when absent.
func (c *CollabStore) GetEvent(username, calendarID, eventUID string) (string, error) {
	folder, err := c.calendarFolder(username)
	if err != nil {
		return "", err
	}
	_, rec, found, ferr := c.collab.FindCalendarItemByUID(folder, eventUID)
	if ferr != nil {
		return "", ferr
	}
	if !found || rec == nil {
		return "", nil
	}
	return rec.RawData, nil
}

// GetEvents returns the raw iCalendar of every event in the calendar.
func (c *CollabStore) GetEvents(username, calendarID string) ([]string, error) {
	folder, err := c.calendarFolder(username)
	if err != nil {
		return nil, err
	}
	items, err := c.collab.ListCalendarItemsByFolder(folder)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.Kind == semcore.CollabKindEvent && it.RawData != "" {
			out = append(out, it.RawData)
		}
	}
	return out, nil
}

// DeleteEvent removes an event by UID (idempotent).
func (c *CollabStore) DeleteEvent(username, calendarID, eventUID string) error {
	folder, err := c.calendarFolder(username)
	if err != nil {
		return err
	}
	return c.collab.DeleteCalendarItemByUID(folder, eventUID)
}

// SetETag is a no-op: the canonical ETag is the CalendarChangeKey assigned on
// write, returned by GetETag.
func (c *CollabStore) SetETag(username, calendarID, eventUID, etag string) error { return nil }

// GetETag returns the event's ChangeKey-based DAV ETag.
func (c *CollabStore) GetETag(username, calendarID, eventUID string) string {
	folder, err := c.calendarFolder(username)
	if err != nil {
		return quotedRandomETag()
	}
	_, rec, found, ferr := c.collab.FindCalendarItemByUID(folder, eventUID)
	if ferr != nil || !found || rec == nil {
		return quotedRandomETag()
	}
	etag := rec.ETag
	if etag == "" {
		etag = rec.ChangeKey.String()
	}
	return fmt.Sprintf("%q", etag)
}

// GetCalendarETag returns a collection ETag derived from the contained events'
// ETags, so DAV clients detect any change in the calendar.
func (c *CollabStore) GetCalendarETag(username, calendarID string) string {
	folder, err := c.calendarFolder(username)
	if err != nil {
		return quotedRandomETag()
	}
	items, err := c.collab.ListCalendarItemsByFolder(folder)
	if err != nil {
		return quotedRandomETag()
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.ETag)
		b.WriteString(it.IcalUID)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%q", fmt.Sprintf("%x", sum))
}
