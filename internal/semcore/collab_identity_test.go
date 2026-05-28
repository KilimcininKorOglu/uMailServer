package semcore

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// CalendarItemId tests
// ---------------------------------------------------------------------------

func TestCalendarItemId_NewAndBasic(t *testing.T) {
	id := MustCalendarItemId("cal-item-abc123")
	if id.String() != "cal-item-abc123" {
		t.Errorf("String() = %q, want %q", id.String(), "cal-item-abc123")
	}
	if id.IsZero() {
		t.Error("IsZero() = true, want false for non-empty CalendarItemId")
	}
}

func TestCalendarItemId_Empty(t *testing.T) {
	_, err := NewCalendarItemId("")
	if err == nil {
		t.Error("NewCalendarItemId(empty) should error")
	}
}

func TestCalendarItemId_Equal(t *testing.T) {
	id1 := MustCalendarItemId("same-id")
	id2 := MustCalendarItemId("same-id")
	id3 := MustCalendarItemId("different-id")

	if !id1.Equal(id2) {
		t.Error("Same IDs should be equal")
	}
	if id1.Equal(id3) {
		t.Error("Different IDs should not be equal")
	}
}

func TestCalendarItemId_JSON(t *testing.T) {
	id := MustCalendarItemId("json-cal-item")

	data, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var id2 CalendarItemId
	if err := json.Unmarshal(data, &id2); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !id.Equal(id2) {
		t.Errorf("Round-trip CalendarItemId: %v != %v", id, id2)
	}
}

func TestCalendarItemId_MustPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustCalendarItemId('') should panic")
		}
	}()
	MustCalendarItemId("")
}

// ---------------------------------------------------------------------------
// ContactId tests
// ---------------------------------------------------------------------------

func TestContactId_NewAndBasic(t *testing.T) {
	id := MustContactId("contact-xyz789")
	if id.String() != "contact-xyz789" {
		t.Errorf("String() = %q, want %q", id.String(), "contact-xyz789")
	}
	if id.IsZero() {
		t.Error("IsZero() = true, want false")
	}
}

func TestContactId_Empty(t *testing.T) {
	_, err := NewContactId("")
	if err == nil {
		t.Error("NewContactId(empty) should error")
	}
}

func TestContactId_Equal(t *testing.T) {
	id1 := MustContactId("same-contact")
	id2 := MustContactId("same-contact")

	if !id1.Equal(id2) {
		t.Error("Same contact IDs should be equal")
	}
}

func TestContactId_JSON(t *testing.T) {
	id := MustContactId("json-contact")

	data, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var id2 ContactId
	if err := json.Unmarshal(data, &id2); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !id.Equal(id2) {
		t.Errorf("Round-trip ContactId: %v != %v", id, id2)
	}
}

// ---------------------------------------------------------------------------
// TaskId tests
// ---------------------------------------------------------------------------

func TestTaskId_NewAndBasic(t *testing.T) {
	id := MustTaskId("task-12345")
	if id.String() != "task-12345" {
		t.Errorf("String() = %q, want %q", id.String(), "task-12345")
	}
	if id.IsZero() {
		t.Error("IsZero() = true, want false")
	}
}

func TestTaskId_Empty(t *testing.T) {
	_, err := NewTaskId("")
	if err == nil {
		t.Error("NewTaskId(empty) should error")
	}
}

func TestTaskId_Equal(t *testing.T) {
	id1 := MustTaskId("same-task")
	id2 := MustTaskId("same-task")

	if !id1.Equal(id2) {
		t.Error("Same task IDs should be equal")
	}
}

func TestTaskId_JSON(t *testing.T) {
	id := MustTaskId("json-task")

	data, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var id2 TaskId
	if err := json.Unmarshal(data, &id2); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !id.Equal(id2) {
		t.Errorf("Round-trip TaskId: %v != %v", id, id2)
	}
}

// ---------------------------------------------------------------------------
// RecurrenceId tests
// ---------------------------------------------------------------------------

func TestRecurrenceId_NewAndBasic(t *testing.T) {
	id := MustRecurrenceId("20240115T100000Z")
	if id.String() != "20240115T100000Z" {
		t.Errorf("String() = %q, want %q", id.String(), "20240115T100000Z")
	}
	if id.IsZero() {
		t.Error("IsZero() = true, want false")
	}
}

func TestRecurrenceId_Empty(t *testing.T) {
	_, err := NewRecurrenceId("")
	if err == nil {
		t.Error("NewRecurrenceId(empty) should error")
	}
}

func TestRecurrenceId_Equal(t *testing.T) {
	id1 := MustRecurrenceId("20240115T100000Z")
	id2 := MustRecurrenceId("20240115T100000Z")
	id3 := MustRecurrenceId("20240116T100000Z")

	if !id1.Equal(id2) {
		t.Error("Same RecurrenceIds should be equal")
	}
	if id1.Equal(id3) {
		t.Error("Different RecurrenceIds should not be equal")
	}
}

func TestRecurrenceId_JSON(t *testing.T) {
	id := MustRecurrenceId("20240115T100000Z")

	data, err := json.Marshal(id)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var id2 RecurrenceId
	if err := json.Unmarshal(data, &id2); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !id.Equal(id2) {
		t.Errorf("Round-trip RecurrenceId: %v != %v", id, id2)
	}
}

// ---------------------------------------------------------------------------
// CalendarChangeKey tests
// ---------------------------------------------------------------------------

func TestCalendarChangeKey_NewAndBasic(t *testing.T) {
	ck := MustCalendarChangeKey("ck-cal-abc123")
	if ck.String() != "ck-cal-abc123" {
		t.Errorf("String() = %q, want %q", ck.String(), "ck-cal-abc123")
	}
	if ck.IsZero() {
		t.Error("IsZero() = true, want false")
	}
}

func TestCalendarChangeKey_Empty(t *testing.T) {
	_, err := NewCalendarChangeKey("")
	if err == nil {
		t.Error("NewCalendarChangeKey(empty) should error")
	}
}

func TestCalendarChangeKey_Equal(t *testing.T) {
	ck1 := MustCalendarChangeKey("same-cal-ck")
	ck2 := MustCalendarChangeKey("same-cal-ck")
	ck3 := MustCalendarChangeKey("different-cal-ck")

	if !ck1.Equal(ck2) {
		t.Error("Same CalendarChangeKeys should be equal")
	}
	if ck1.Equal(ck3) {
		t.Error("Different CalendarChangeKeys should not be equal")
	}
}

func TestCalendarChangeKey_JSON(t *testing.T) {
	ck := MustCalendarChangeKey("json-cal-ck")

	data, err := json.Marshal(ck)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var ck2 CalendarChangeKey
	if err := json.Unmarshal(data, &ck2); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !ck.Equal(ck2) {
		t.Errorf("Round-trip CalendarChangeKey: %v != %v", ck, ck2)
	}
}

// ---------------------------------------------------------------------------
// ContactChangeKey tests
// ---------------------------------------------------------------------------

func TestContactChangeKey_NewAndBasic(t *testing.T) {
	ck := MustContactChangeKey("ck-contact-xyz")
	if ck.String() != "ck-contact-xyz" {
		t.Errorf("String() = %q, want %q", ck.String(), "ck-contact-xyz")
	}
}

func TestContactChangeKey_Equal(t *testing.T) {
	ck1 := MustContactChangeKey("same-contact-ck")
	ck2 := MustContactChangeKey("same-contact-ck")

	if !ck1.Equal(ck2) {
		t.Error("Same ContactChangeKeys should be equal")
	}
}

func TestContactChangeKey_JSON(t *testing.T) {
	ck := MustContactChangeKey("json-contact-ck")

	data, err := json.Marshal(ck)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var ck2 ContactChangeKey
	if err := json.Unmarshal(data, &ck2); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !ck.Equal(ck2) {
		t.Errorf("Round-trip ContactChangeKey: %v != %v", ck, ck2)
	}
}

// ---------------------------------------------------------------------------
// TaskChangeKey tests
// ---------------------------------------------------------------------------

func TestTaskChangeKey_NewAndBasic(t *testing.T) {
	ck := MustTaskChangeKey("ck-task-abc")
	if ck.String() != "ck-task-abc" {
		t.Errorf("String() = %q, want %q", ck.String(), "ck-task-abc")
	}
}

func TestTaskChangeKey_Equal(t *testing.T) {
	ck1 := MustTaskChangeKey("same-task-ck")
	ck2 := MustTaskChangeKey("same-task-ck")

	if !ck1.Equal(ck2) {
		t.Error("Same TaskChangeKeys should be equal")
	}
}

func TestTaskChangeKey_JSON(t *testing.T) {
	ck := MustTaskChangeKey("json-task-ck")

	data, err := json.Marshal(ck)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var ck2 TaskChangeKey
	if err := json.Unmarshal(data, &ck2); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !ck.Equal(ck2) {
		t.Errorf("Round-trip TaskChangeKey: %v != %v", ck, ck2)
	}
}

// ---------------------------------------------------------------------------
// CollabKind tests
// ---------------------------------------------------------------------------

func TestCollabKind_String(t *testing.T) {
	tests := []struct {
		kind CollabKind
		want string
	}{
		{CollabKindEvent, "event"},
		{CollabKindTodo, "todo"},
		{CollabKindContact, "contact"},
		{CollabKindTask, "task"},
		{CollabKind(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("CollabKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
