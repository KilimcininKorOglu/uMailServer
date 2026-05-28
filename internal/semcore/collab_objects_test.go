package semcore

import (
	"encoding/json"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// RecurrenceRule tests
// ---------------------------------------------------------------------------

func TestRecurrenceRule_IsZero(t *testing.T) {
	var zeroRR RecurrenceRule
	if !zeroRR.IsZero() {
		t.Error("Zero RecurrenceRule should be IsZero")
	}

	rr := RecurrenceRule{Freq: "DAILY"}
	if rr.IsZero() {
		t.Error("Non-zero RecurrenceRule should not be IsZero")
	}
}

func TestRecurrenceRule_RRULEText(t *testing.T) {
	// Empty rule.
	var emptyRR RecurrenceRule
	if text := emptyRR.RRULEText(); text != "" {
		t.Errorf("Empty RRULEText = %q, want ''", text)
	}

	// Simple DAILY with interval.
	rr := RecurrenceRule{Freq: "DAILY", Interval: 2}
	text := rr.RRULEText()
	if text != "FREQ=DAILY;INTERVAL=2" {
		t.Errorf("RRULEText = %q, want %q", text, "FREQ=DAILY;INTERVAL=2")
	}

	// With UNTIL.
	until := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)
	rr2 := RecurrenceRule{Freq: "WEEKLY", UseUntil: true, Until: until}
	text2 := rr2.RRULEText()
	if text2 != "FREQ=WEEKLY;UNTIL=20251231T235959Z" {
		t.Errorf("RRULEText = %q, want %q", text2, "FREQ=WEEKLY;UNTIL=20251231T235959Z")
	}
}

// ---------------------------------------------------------------------------
// ReminderTrigger tests
// ---------------------------------------------------------------------------

func TestReminderTrigger_IsZero(t *testing.T) {
	var zeroRT ReminderTrigger
	if !zeroRT.IsZero() {
		t.Error("Zero ReminderTrigger should be IsZero")
	}

	rt := ReminderTrigger{Duration: 900}
	if rt.IsZero() {
		t.Error("Non-zero ReminderTrigger should not be IsZero")
	}
}

// ---------------------------------------------------------------------------
// CalendarItem tests
// ---------------------------------------------------------------------------

func TestCalendarItem_IsEvent(t *testing.T) {
	ci := &CalendarItem{Kind: CollabKindEvent}
	if !ci.IsEvent() {
		t.Error("IsEvent() = false, want true for CollabKindEvent")
	}

	ci.Kind = CollabKindTodo
	if ci.IsEvent() {
		t.Error("IsEvent() = true, want false for CollabKindTodo")
	}
}

func TestCalendarItem_IsTodo(t *testing.T) {
	ci := &CalendarItem{Kind: CollabKindTodo}
	if !ci.IsTodo() {
		t.Error("IsTodo() = false, want true for CollabKindTodo")
	}

	ci.Kind = CollabKindEvent
	if ci.IsTodo() {
		t.Error("IsTodo() = true, want false for CollabKindEvent")
	}
}

func TestCalendarItem_IsException(t *testing.T) {
	ci := &CalendarItem{RecurrenceID: MustRecurrenceId("20240115T100000Z")}
	if !ci.IsException() {
		t.Error("IsException() = false, want true when RecurrenceID is set")
	}

	ci = &CalendarItem{}
	if ci.IsException() {
		t.Error("IsException() = true, want false when RecurrenceID is zero")
	}
}

func TestCalendarItem_ETagValue(t *testing.T) {
	// Zero ChangeKey -> empty ETag.
	ci := &CalendarItem{}
	if etag := ci.ETagValue(); etag != "" {
		t.Errorf("ETagValue() with zero ChangeKey = %q, want ''", etag)
	}

	// Non-zero ChangeKey -> ETag = ChangeKey string.
	ck := MustCalendarChangeKey("ck-cal-item-abc")
	ci = &CalendarItem{ChangeKey: ck}
	if etag := ci.ETagValue(); etag != "ck-cal-item-abc" {
		t.Errorf("ETagValue() = %q, want %q", etag, "ck-cal-item-abc")
	}
}

func TestCalendarItem_ItemClass(t *testing.T) {
	ci := &CalendarItem{}
	if class := ci.ItemClass(); class != "PUBLIC" {
		t.Errorf("ItemClass() = %q, want %q", class, "PUBLIC")
	}
}

// ---------------------------------------------------------------------------
// Contact tests
// ---------------------------------------------------------------------------

func TestContact_IsZero(t *testing.T) {
	c := &Contact{}
	if !c.IsZero() {
		t.Error("Zero Contact should be IsZero")
	}

	c.ID = MustContactId("some-contact")
	if c.IsZero() {
		t.Error("Contact with non-zero ID should not be IsZero")
	}
}

func TestContact_ETagValue(t *testing.T) {
	c := &Contact{}
	if etag := c.ETagValue(); etag != "" {
		t.Errorf("ETagValue() with zero ChangeKey = %q, want ''", etag)
	}

	ck := MustContactChangeKey("ck-contact-xyz")
	c = &Contact{ChangeKey: ck}
	if etag := c.ETagValue(); etag != "ck-contact-xyz" {
		t.Errorf("ETagValue() = %q, want %q", etag, "ck-contact-xyz")
	}
}

func TestContact_ItemClass(t *testing.T) {
	c := &Contact{}
	if class := c.ItemClass(); class != "PUBLIC" {
		t.Errorf("ItemClass() = %q, want %q", class, "PUBLIC")
	}
}

func TestContact_JSONRoundTrip(t *testing.T) {
	ck := MustContactChangeKey("ck-contact-json")
	id := MustContactId("contact-json-id")
	folderID := MustFolderId("folder-ab")
	mboxID := MustMailboxId("mbox-json")

	c := &Contact{
		ID:        id,
		FolderID:  folderID,
		MailboxID: mboxID,
		ChangeKey: ck,
		FullName:  "Test Contact",
		ETag:      "stored-etag",
	}

	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var c2 Contact
	if err := json.Unmarshal(data, &c2); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !c2.ID.Equal(c.ID) {
		t.Errorf("Round-trip Contact ID: %v != %v", c2.ID, c.ID)
	}
	if !c2.ChangeKey.Equal(c.ChangeKey) {
		t.Errorf("Round-trip Contact ChangeKey: %v != %v", c2.ChangeKey, c.ChangeKey)
	}
	if c2.FullName != c.FullName {
		t.Errorf("Round-trip Contact FullName: %v != %v", c2.FullName, c.FullName)
	}
}

// ---------------------------------------------------------------------------
// Task tests
// ---------------------------------------------------------------------------

func TestTask_IsZero(t *testing.T) {
	tk := &Task{}
	if !tk.IsZero() {
		t.Error("Zero Task should be IsZero")
	}

	tk.ID = MustTaskId("some-task")
	if tk.IsZero() {
		t.Error("Task with non-zero ID should not be IsZero")
	}
}

func TestTask_ETagValue(t *testing.T) {
	tk := &Task{}
	if etag := tk.ETagValue(); etag != "" {
		t.Errorf("ETagValue() with zero ChangeKey = %q, want ''", etag)
	}

	ck := MustTaskChangeKey("ck-task-abc")
	tk = &Task{ChangeKey: ck}
	if etag := tk.ETagValue(); etag != "ck-task-abc" {
		t.Errorf("ETagValue() = %q, want %q", etag, "ck-task-abc")
	}
}

func TestTask_Status(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   string
	}{
		{TaskStatusNeedsAction, "NEEDS-ACTION"},
		{TaskStatusCompleted, "COMPLETED"},
		{TaskStatusInProcess, "IN-PROCESS"},
		//nolint:misspell // RFC 5545 uses British spelling CANCELLED
		{TaskStatusCancelled, "CANCELLED"},
	}
	for _, tc := range tests {
		if string(tc.status) != tc.want {
			t.Errorf("TaskStatus = %q, want %q", tc.status, tc.want)
		}
	}
}
