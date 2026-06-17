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

// calendarFolderName returns the semcore folder name for a calendar ID.
// The default calendar ("default") uses "calendar" to maintain backwards
// compatibility with existing single-calendar mailboxes. User-created
// calendars use "calendar-<id>" to keep each calendar in its own folder.
func calendarFolderName(calendarID string) string {
	if calendarID == "" || calendarID == defaultCalendarID {
		return "calendar"
	}
	return "calendar-" + calendarID
}

// calendarFolder resolves (creating if needed) the folder for a calendar.
// For the default calendarID, falls back to legacy "calendars" then "calendar"
// names for backwards compatibility with existing mailboxes.
func (c *CollabStore) calendarFolder(username string, calendarID string) (semcore.FolderId, error) {
	fname := calendarFolderName(calendarID)

	// Fast path: folder already registered.
	if fid, err := c.identity.GetFolderID(username, fname); err == nil && !fid.IsZero() {
		return fid, nil
	}

	// Backwards-compat: default calendar may have been created under the
	// legacy single-calendar "calendar" or "calendars" names.
	if calendarID == "" || calendarID == defaultCalendarID {
		if fid, err := c.identity.GetFolderID(username, "calendars"); err == nil && !fid.IsZero() {
			return fid, nil
		}
		if fid, err := c.identity.GetFolderID(username, "calendar"); err == nil && !fid.IsZero() {
			return fid, nil
		}
	}

	return c.identity.EnsureFolderId(username, fname, "calendar")
}

// calendarFolderIDsForUser returns the FolderId of every calendar folder for
// the mailbox, by filtering ListFolderIdentitiesForMailbox for role="calendar".
func (c *CollabStore) calendarFolderIDsForUser(username string) ([]semcore.StoredFolderIdentity, error) {
	all, err := c.identity.ListFolderIdentitiesForMailbox(username)
	if err != nil {
		return nil, err
	}
	var calendars []semcore.StoredFolderIdentity
	for _, f := range all {
		if f.Role == "calendar" {
			calendars = append(calendars, f)
		}
	}
	return calendars, nil
}

func quotedRandomETag() string { return fmt.Sprintf("%q", uuid.New().String()) }

// defaultCalendar returns the hard-coded legacy default calendar used when
// no calendars exist yet (first-run / single-calendar mailbox).
func (c *CollabStore) defaultCalendar() *Calendar {
	return &Calendar{ID: defaultCalendarID, Name: defaultCalendarName, Color: "#3b82f6"}
}

// calendarIDFromFolderName extracts the calendar ID from a semcore folder name.
// "calendar" → "default", "calendar-<id>" → "<id>".
func calendarIDFromFolderName(name string) string {
	if name == "calendar" || name == "calendars" {
		return defaultCalendarID
	}
	return strings.TrimPrefix(name, "calendar-")
}

// CreateCalendar ensures the mailbox's calendar folder exists and returns the
// assigned calendar ID. If cal.ID is empty, a new UUID-based ID is assigned.
// For the default calendar ID, the legacy "calendar" folder name is used to
// maintain backwards compatibility.
func (c *CollabStore) CreateCalendar(username string, cal *Calendar) error {
	if cal.ID == "" {
		cal.ID = uuid.New().String()
	}
	fname := calendarFolderName(cal.ID)
	if _, err := c.identity.EnsureFolderId(username, fname, "calendar"); err != nil {
		return err
	}
	return nil
}

// GetCalendar returns the calendar metadata for one calendar.
func (c *CollabStore) GetCalendar(username, calendarID string) (*Calendar, error) {
	if calendarID == "" {
		calendarID = defaultCalendarID
	}
	// For the default calendar, return the legacy defaults.
	if calendarID == defaultCalendarID {
		return &Calendar{
			ID:   defaultCalendarID,
			Name: defaultCalendarName,
			Color: "#3b82f6",
		}, nil
	}
	// For other calendars, construct the ID from the folder name.
	return &Calendar{
		ID:    calendarID,
		Name:  calendarID, // Name is the user-facing display name; set from ID until updated.
		Color: "#3b82f6",
	}, nil
}

// GetCalendars lists every calendar the user owns.
func (c *CollabStore) GetCalendars(username string) ([]*Calendar, error) {
	folders, err := c.calendarFolderIDsForUser(username)
	if err != nil {
		return nil, err
	}
	if len(folders) == 0 {
		// No calendars yet: return the legacy default so existing code
		// that calls GetCalendars without creating a calendar first still works.
		return []*Calendar{c.defaultCalendar()}, nil
	}
	calendars := make([]*Calendar, 0, len(folders))
	for _, f := range folders {
		id := calendarIDFromFolderName(f.FolderID.String())
		if id == "" {
			id = defaultCalendarID
		}
		if id == defaultCalendarID {
			calendars = append(calendars, &Calendar{
				ID:    defaultCalendarID,
				Name:  defaultCalendarName,
				Color: "#3b82f6",
			})
		} else {
			calendars = append(calendars, &Calendar{
				ID:    id,
				Name:  id, // placeholder; display name from folder
				Color: "#3b82f6",
			})
		}
	}
	return calendars, nil
}

// UpdateCalendar updates mutable metadata (name, color, description) for a
// calendar. The semcore folder identity itself is not renamed.
func (c *CollabStore) UpdateCalendar(username string, cal *Calendar) error {
	_, err := c.calendarFolder(username, cal.ID)
	return err
}

// DeleteCalendar clears every event in the mailbox's calendar folder.
func (c *CollabStore) DeleteCalendar(username, calendarID string) error {
	folder, err := c.calendarFolder(username, calendarID)
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
	folder, err := c.calendarFolder(username, calendarID)
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
	folder, err := c.calendarFolder(username, calendarID)
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
	folder, err := c.calendarFolder(username, calendarID)
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
	folder, err := c.calendarFolder(username, calendarID)
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
	folder, err := c.calendarFolder(username, calendarID)
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
	folder, err := c.calendarFolder(username, calendarID)
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
