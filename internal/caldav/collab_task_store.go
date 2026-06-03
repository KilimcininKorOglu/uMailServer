package caldav

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/umailserver/umailserver/internal/semcore"
)

// defaultTaskListID mirrors the single "tasks" collection the webmail task
// handler and EWS already use; every task list id maps to the mailbox's one
// task folder (role "tasks").
const (
	defaultTaskListID   = "tasks"
	defaultTaskListName = "Tasks"
)

// CollabTaskStore is a semcore-backed Store for VTODO tasks. It writes to the
// SAME collaboration task records EWS reads/writes (role "tasks" folder,
// StoredTaskIdentity keyed by iCalendar UID), so a task created over webmail is
// visible over EWS and vice versa. It satisfies the calendar-shaped Store
// interface by treating the task list as a single "calendar".
type CollabTaskStore struct {
	collab   *semcore.BoltCollaborationStore
	identity *semcore.BoltIdentityStore
}

// NewCollabTaskStore builds a semcore-backed task Store.
func NewCollabTaskStore(collab *semcore.BoltCollaborationStore, identity *semcore.BoltIdentityStore) *CollabTaskStore {
	return &CollabTaskStore{collab: collab, identity: identity}
}

// compile-time assertion.
var _ Store = (*CollabTaskStore)(nil)

// tasksFolder resolves (creating if needed) the mailbox's single task folder,
// mirroring the EWS task create path (internal/ews/collab.go) so both write to
// the same FolderId.
func (c *CollabTaskStore) tasksFolder(username string) (semcore.FolderId, error) {
	if fid, err := c.identity.GetFolderID(username, "tasks"); err == nil && !fid.IsZero() {
		return fid, nil
	}
	return c.identity.EnsureFolderId(username, "tasks", "tasks")
}

func (c *CollabTaskStore) defaultTaskList() *Calendar {
	return &Calendar{ID: defaultTaskListID, Name: defaultTaskListName, Color: "#6b7280"}
}

// CreateCalendar ensures the mailbox's task folder exists.
func (c *CollabTaskStore) CreateCalendar(username string, cal *Calendar) error {
	if _, err := c.tasksFolder(username); err != nil {
		return err
	}
	if cal.ID == "" {
		cal.ID = defaultTaskListID
	}
	return nil
}

// GetCalendar returns the mailbox's single task list.
func (c *CollabTaskStore) GetCalendar(username, calendarID string) (*Calendar, error) {
	if _, err := c.tasksFolder(username); err != nil {
		return nil, err
	}
	return c.defaultTaskList(), nil
}

// GetCalendars lists the mailbox's task lists (a single default list).
func (c *CollabTaskStore) GetCalendars(username string) ([]*Calendar, error) {
	if _, err := c.tasksFolder(username); err != nil {
		return nil, err
	}
	return []*Calendar{c.defaultTaskList()}, nil
}

// UpdateCalendar is a no-op (no per-list metadata beyond the folder).
func (c *CollabTaskStore) UpdateCalendar(username string, cal *Calendar) error {
	_, err := c.tasksFolder(username)
	return err
}

// DeleteCalendar clears every task in the mailbox's task folder.
func (c *CollabTaskStore) DeleteCalendar(username, calendarID string) error {
	folder, err := c.tasksFolder(username)
	if err != nil {
		return err
	}
	items, err := c.collab.ListTasksByFolder(folder)
	if err != nil {
		return err
	}
	for _, it := range items {
		if derr := c.collab.DeleteTaskByUID(folder, it.IcalUID); derr != nil {
			return derr
		}
	}
	return nil
}

// SaveEvent upserts a VTODO into the canonical task store keyed by its UID, so
// editing a task from any surface updates the same record.
func (c *CollabTaskStore) SaveEvent(username, calendarID string, event *CalendarEvent, icsData string) error {
	folder, err := c.tasksFolder(username)
	if err != nil {
		return err
	}
	mboxID, err := semcore.NewMailboxId(username)
	if err != nil {
		return err
	}
	ck, err := semcore.NewTaskChangeKey(uuid.New().String())
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(icsData))
	rawHash := fmt.Sprintf("%x", sum)

	msgKey, existing, found, ferr := c.collab.FindTaskByUID(folder, event.UID)
	if ferr != nil {
		return ferr
	}
	var taskID semcore.TaskId
	if found {
		taskID = existing.ID // preserve identity across edits
	} else {
		taskID, err = semcore.NewTaskId(uuid.New().String())
		if err != nil {
			return err
		}
		msgKey = fmt.Sprintf("task:%s:%s", folder.String(), event.UID)
	}

	rec := semcore.NewStoredTaskIdentity(taskID, folder, mboxID, ck, event.UID, rawHash)
	rec.RawData = icsData
	rec.ETag = ck.String()
	return c.collab.PutTaskIdentityUnsafe(msgKey, rec)
}

// GetEvent returns the raw VTODO for one task, or "" when absent.
func (c *CollabTaskStore) GetEvent(username, calendarID, eventUID string) (string, error) {
	folder, err := c.tasksFolder(username)
	if err != nil {
		return "", err
	}
	_, rec, found, ferr := c.collab.FindTaskByUID(folder, eventUID)
	if ferr != nil {
		return "", ferr
	}
	if !found || rec == nil {
		return "", nil
	}
	return rec.RawData, nil
}

// GetEvents returns the raw VTODO of every task in the list.
func (c *CollabTaskStore) GetEvents(username, calendarID string) ([]string, error) {
	folder, err := c.tasksFolder(username)
	if err != nil {
		return nil, err
	}
	items, err := c.collab.ListTasksByFolder(folder)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.RawData != "" {
			out = append(out, it.RawData)
		}
	}
	return out, nil
}

// DeleteEvent removes a task by UID (idempotent).
func (c *CollabTaskStore) DeleteEvent(username, calendarID, eventUID string) error {
	folder, err := c.tasksFolder(username)
	if err != nil {
		return err
	}
	return c.collab.DeleteTaskByUID(folder, eventUID)
}

// SetETag is a no-op: the canonical ETag is the TaskChangeKey assigned on write.
func (c *CollabTaskStore) SetETag(username, calendarID, eventUID, etag string) error { return nil }

// GetETag returns the task's ChangeKey-based DAV ETag.
func (c *CollabTaskStore) GetETag(username, calendarID, eventUID string) string {
	folder, err := c.tasksFolder(username)
	if err != nil {
		return quotedRandomETag()
	}
	_, rec, found, ferr := c.collab.FindTaskByUID(folder, eventUID)
	if ferr != nil || !found || rec == nil {
		return quotedRandomETag()
	}
	etag := rec.ETag
	if etag == "" {
		etag = rec.ChangeKey.String()
	}
	return fmt.Sprintf("%q", etag)
}

// GetCalendarETag returns a collection ETag derived from the contained tasks'
// ETags, so DAV clients detect any change in the task list.
func (c *CollabTaskStore) GetCalendarETag(username, calendarID string) string {
	folder, err := c.tasksFolder(username)
	if err != nil {
		return quotedRandomETag()
	}
	items, err := c.collab.ListTasksByFolder(folder)
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
