package ews

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestComputeFreeBusyRealBlocks verifies GetUserAvailability/computeFreeBusy
// returns real busy blocks read from the stored calendar items' DTSTART/DTEND
// (not the old empty stub), filtered to the requested window.
func TestComputeFreeBusyRealBlocks(t *testing.T) {
	identity, sync, tomb, msgStore, policyStore, collabStore, cleanup := tmpDirectoryStores(t)
	t.Cleanup(cleanup)

	srv := NewServer(identity, sync, tomb, msgStore, nil, nil, nil, nil, nil, collabStore, policyStore, nil, nil, nil)

	email := "alice@ex.test"
	folderID, err := identity.EnsureFolderId(email, "calendar", "calendar")
	if err != nil {
		t.Fatalf("EnsureFolderId: %v", err)
	}
	mailboxID, err := semcore.NewMailboxId(email)
	if err != nil {
		t.Fatalf("NewMailboxId: %v", err)
	}

	// One event inside the window (09:00–10:00), one well outside it (next day).
	inWindow := "BEGIN:VEVENT\r\nSUMMARY:Standup\r\nORGANIZER:mailto:boss@ex.test\r\n" +
		"DTSTART:20260603T090000Z\r\nDTEND:20260603T100000Z\r\nX-MICROSOFT-CDO-BUSYSTATUS:BUSY\r\nEND:VEVENT"
	outWindow := "BEGIN:VEVENT\r\nSUMMARY:NextDay\r\nDTSTART:20260604T090000Z\r\nDTEND:20260604T100000Z\r\nEND:VEVENT"

	for i, raw := range []string{inWindow, outWindow} {
		rec := &semcore.StoredCalendarItemIdentity{
			FolderID: folderID, MailboxID: mailboxID, Kind: semcore.CollabKindEvent, RawData: raw,
		}
		if err := collabStore.PutCalendarItemIdentityUnsafe("evt-"+string(rune('a'+i)), rec); err != nil {
			t.Fatalf("seed calendar item %d: %v", i, err)
		}
	}

	winStart := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	winEnd := time.Date(2026, 6, 3, 18, 0, 0, 0, time.UTC)
	resp := srv.computeFreeBusy(context.Background(), email, "Alice", winStart, winEnd, "Detailed")

	if resp.ResponseMessage == nil || resp.ResponseMessage.ResponseClass != "Success" {
		t.Fatalf("expected Success, got %+v", resp.ResponseMessage)
	}
	if resp.FreeBusyView == nil || resp.FreeBusyView.CalendarEventArray == nil {
		t.Fatalf("expected a CalendarEventArray, got %+v", resp.FreeBusyView)
	}
	events := resp.FreeBusyView.CalendarEventArray.Events
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 in-window busy block, got %d: %+v", len(events), events)
	}
	ev := events[0]
	if !strings.HasPrefix(ev.StartTime, "2026-06-03T09:00:00") {
		t.Errorf("StartTime = %q, want 2026-06-03T09:00:00...", ev.StartTime)
	}
	if !strings.HasPrefix(ev.EndTime, "2026-06-03T10:00:00") {
		t.Errorf("EndTime = %q, want 2026-06-03T10:00:00...", ev.EndTime)
	}
	if ev.BusyType != "Busy" {
		t.Errorf("BusyType = %q, want Busy", ev.BusyType)
	}
	if ev.CalendarEventDetails == nil || ev.CalendarEventDetails.Subject != "Standup" {
		t.Errorf("expected CalendarEventDetails.Subject=Standup, got %+v", ev.CalendarEventDetails)
	}
	if !ev.CalendarEventDetails.IsMeeting {
		t.Errorf("expected IsMeeting true (event has ORGANIZER)")
	}
	if resp.FreeBusyView.MergedFreeBusy == "" {
		t.Errorf("expected non-empty MergedFreeBusy")
	}
}
