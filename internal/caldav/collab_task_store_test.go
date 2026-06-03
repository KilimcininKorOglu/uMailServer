package caldav

import (
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestCollabTaskStoreSharesEWSFolder verifies a task saved through the webmail
// task store lands as a StoredTaskIdentity in the SAME role-"tasks" folder EWS
// lists, so webmail tasks and EWS tasks are one source of truth (not the old
// filesystem split).
func TestCollabTaskStoreSharesEWSFolder(t *testing.T) {
	store, err := semcore.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	cs := NewCollabTaskStore(store.Collaboration(), store.Identity())
	user := "alice@ex.test"
	vtodo := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\nUID:t-1\r\nSUMMARY:Write report\r\nSTATUS:NEEDS-ACTION\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"

	if err := cs.SaveEvent(user, defaultTaskListID, &CalendarEvent{UID: "t-1"}, vtodo); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	// Webmail read path.
	raws, err := cs.GetEvents(user, defaultTaskListID)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(raws) != 1 || !strings.Contains(raws[0], "SUMMARY:Write report") {
		t.Fatalf("webmail GetEvents mismatch: %v", raws)
	}

	// EWS read path: the same folder's task list must contain the record.
	folder, err := store.Identity().GetFolderID(user, "tasks")
	if err != nil || folder.IsZero() {
		t.Fatalf("tasks folder not found: %v", err)
	}
	tasks, err := store.Collaboration().ListTasksByFolder(folder)
	if err != nil {
		t.Fatalf("ListTasksByFolder: %v", err)
	}
	if len(tasks) != 1 || tasks[0].IcalUID != "t-1" || !strings.Contains(tasks[0].RawData, "Write report") {
		t.Fatalf("EWS task list does not see the webmail task: %+v", tasks)
	}

	// Update by UID preserves identity (one record, not two).
	updated := strings.Replace(vtodo, "Write report", "Write final report", 1)
	if err := cs.SaveEvent(user, defaultTaskListID, &CalendarEvent{UID: "t-1"}, updated); err != nil {
		t.Fatalf("SaveEvent update: %v", err)
	}
	tasks, err = store.Collaboration().ListTasksByFolder(folder)
	if err != nil {
		t.Fatalf("ListTasksByFolder after update: %v", err)
	}
	if len(tasks) != 1 || !strings.Contains(tasks[0].RawData, "final report") {
		t.Fatalf("update did not upsert in place: %+v", tasks)
	}

	// Delete by UID.
	if err := cs.DeleteEvent(user, defaultTaskListID, "t-1"); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	got, err := cs.GetEvent(user, defaultTaskListID, "t-1")
	if err != nil {
		t.Fatalf("GetEvent after delete: %v", err)
	}
	if got != "" {
		t.Errorf("task still present after delete: %q", got)
	}
}
