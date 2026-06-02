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

// TestComputeFreeBusyMergesProvider verifies that busy intervals contributed by
// the external free/busy provider (the CalDAV/webmail calendar bridge) are
// merged into the EWS free/busy view alongside the collaboration store's items,
// and that provider intervals outside the requested window are excluded.
func TestComputeFreeBusyMergesProvider(t *testing.T) {
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

	// One collaboration-store event 09:00–10:00.
	rec := &semcore.StoredCalendarItemIdentity{
		FolderID: folderID, MailboxID: mailboxID, Kind: semcore.CollabKindEvent,
		RawData: "BEGIN:VEVENT\r\nSUMMARY:Standup\r\nDTSTART:20260603T090000Z\r\nDTEND:20260603T100000Z\r\nEND:VEVENT",
	}
	if err := collabStore.PutCalendarItemIdentityUnsafe("evt-a", rec); err != nil {
		t.Fatalf("seed calendar item: %v", err)
	}

	// Provider adds a CalDAV/webmail event 14:00–15:00 (in window) plus one
	// outside the window that must be filtered out.
	srv.SetFreeBusyProvider(func(e string, from, to time.Time) []FreeBusyInterval {
		if e != email {
			return nil
		}
		return []FreeBusyInterval{
			{Start: time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC), End: time.Date(2026, 6, 3, 15, 0, 0, 0, time.UTC)},
			{Start: time.Date(2026, 6, 5, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)},
		}
	})

	winStart := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	winEnd := time.Date(2026, 6, 3, 18, 0, 0, 0, time.UTC)
	resp := srv.computeFreeBusy(context.Background(), email, "Alice", winStart, winEnd, "Detailed")

	if resp.FreeBusyView == nil || resp.FreeBusyView.CalendarEventArray == nil {
		t.Fatalf("expected a CalendarEventArray, got %+v", resp.FreeBusyView)
	}
	events := resp.FreeBusyView.CalendarEventArray.Events
	if len(events) != 2 {
		t.Fatalf("expected 2 busy blocks (1 collab + 1 in-window provider), got %d: %+v", len(events), events)
	}
	if !strings.Contains(resp.FreeBusyView.MergedFreeBusy, "2026-06-03T14:00:00") {
		t.Errorf("MergedFreeBusy missing provider block: %q", resp.FreeBusyView.MergedFreeBusy)
	}
}

// TestComputeFreeBusyProviderWithoutCalendarFolder verifies that the external
// provider still contributes busy intervals when the mailbox has no EWS
// calendar folder — the case of a user who only ever created events through
// webmail or CalDAV.
func TestComputeFreeBusyProviderWithoutCalendarFolder(t *testing.T) {
	identity, sync, tomb, msgStore, policyStore, collabStore, cleanup := tmpDirectoryStores(t)
	t.Cleanup(cleanup)

	srv := NewServer(identity, sync, tomb, msgStore, nil, nil, nil, nil, nil, collabStore, policyStore, nil, nil, nil)

	email := "bob@ex.test" // no calendar folder is ensured for this mailbox

	srv.SetFreeBusyProvider(func(e string, from, to time.Time) []FreeBusyInterval {
		return []FreeBusyInterval{
			{Start: time.Date(2026, 6, 3, 11, 0, 0, 0, time.UTC), End: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)},
		}
	})

	winStart := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	winEnd := time.Date(2026, 6, 3, 18, 0, 0, 0, time.UTC)
	resp := srv.computeFreeBusy(context.Background(), email, "Bob", winStart, winEnd, "Detailed")

	if resp.ResponseMessage == nil || resp.ResponseMessage.ResponseClass != "Success" {
		t.Fatalf("expected Success, got %+v", resp.ResponseMessage)
	}
	if resp.FreeBusyView == nil || resp.FreeBusyView.MergedFreeBusy == "" {
		t.Fatalf("expected a provider busy block even without a calendar folder, got %+v", resp.FreeBusyView)
	}
	if !strings.Contains(resp.FreeBusyView.MergedFreeBusy, "2026-06-03T11:00:00") {
		t.Errorf("MergedFreeBusy missing provider block: %q", resp.FreeBusyView.MergedFreeBusy)
	}
}
