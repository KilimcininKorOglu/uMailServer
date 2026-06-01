package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/storage"
)

const inviteRaw = "From: organizer@test.com\r\n" +
	"To: admin@test.com\r\n" +
	"Subject: Project kickoff\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"b1\"\r\n" +
	"\r\n" +
	"--b1\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"You are invited.\r\n" +
	"--b1\r\n" +
	"Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n" +
	"\r\n" +
	"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
	"BEGIN:VEVENT\r\nUID:kickoff-1\r\nORGANIZER;CN=Org:mailto:organizer@test.com\r\n" +
	"SUMMARY:Project kickoff\r\nLOCATION:Room 5\r\n" +
	"DTSTART:20260615T090000Z\r\nDTEND:20260615T100000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n" +
	"--b1--\r\n"

func TestParseMeetingInvite(t *testing.T) {
	inv, ok := parseMeetingInvite([]byte(inviteRaw))
	if !ok || !inv.IsInvite {
		t.Fatalf("expected an invite, got ok=%v inv=%+v", ok, inv)
	}
	if inv.UID != "kickoff-1" || inv.Summary != "Project kickoff" || inv.Location != "Room 5" {
		t.Errorf("invite fields wrong: %+v", inv)
	}
	if inv.Organizer != "organizer@test.com" {
		t.Errorf("organizer wrong: %q", inv.Organizer)
	}
	if inv.Start != "2026-06-15T09:00:00Z" {
		t.Errorf("start wrong: %q", inv.Start)
	}
}

func TestParseMeetingInvite_NotAnInvite(t *testing.T) {
	plain := "From: a@test.com\r\nTo: b@test.com\r\nSubject: hi\r\nContent-Type: text/plain\r\n\r\njust text\r\n"
	if _, ok := parseMeetingInvite([]byte(plain)); ok {
		t.Error("plain message should not parse as an invite")
	}
}

func TestHandleMailRSVP_AcceptAddsToCalendar(t *testing.T) {
	server, database, _ := helperSetupAccount(t)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close account db: %v", err)
		}
	})

	mailDB, err := storage.OpenDatabase(t.TempDir() + "/mail.db")
	if err != nil {
		t.Fatalf("open mail db: %v", err)
	}
	t.Cleanup(func() {
		if err := mailDB.Close(); err != nil {
			t.Errorf("close mail db: %v", err)
		}
	})
	msgStore, err := storage.NewMessageStore(t.TempDir() + "/messages")
	if err != nil {
		t.Fatalf("create message store: %v", err)
	}
	t.Cleanup(func() {
		if err := msgStore.Close(); err != nil {
			t.Errorf("close message store: %v", err)
		}
	})

	const user = "admin@test.com"
	if err := mailDB.CreateMailbox(user, "INBOX"); err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	msgID, err := msgStore.StoreMessage(user, []byte(inviteRaw))
	if err != nil {
		t.Fatalf("store message: %v", err)
	}
	uid, err := mailDB.GetNextUID(user, "INBOX")
	if err != nil {
		t.Fatalf("next uid: %v", err)
	}
	if err := mailDB.StoreMessageMetadata(user, "INBOX", uid, &storage.MessageMetadata{MessageID: msgID, UID: uid, Subject: "Project kickoff"}); err != nil {
		t.Fatalf("store metadata: %v", err)
	}

	server.mailHandler = NewMailHandler()
	server.mailHandler.SetStorage(msgStore, mailDB)
	server.calendarHandler = NewCalendarHandler(t.TempDir())

	// GET invite info reports it is an invite.
	rec := httptest.NewRecorder()
	server.handleMailInvite(rec, reqAsUser(http.MethodGet, "/api/v1/mail/invite?id="+msgID, ""))
	if !strings.Contains(rec.Body.String(), `"isInvite":true`) {
		t.Fatalf("expected isInvite true, got %s", rec.Body.String())
	}

	// Accept adds it to the calendar.
	rec = httptest.NewRecorder()
	server.handleMailRSVP(rec, reqAsUser(http.MethodPost, "/api/v1/mail/rsvp", `{"id":"`+msgID+`","response":"accept"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.calendarHandler.handleCalendarEvents(rec, reqAsUser(http.MethodGet, "/api/v1/calendar/events", ""))
	if !strings.Contains(rec.Body.String(), "Project kickoff") {
		t.Fatalf("expected the accepted event on the calendar, got %s", rec.Body.String())
	}
}
