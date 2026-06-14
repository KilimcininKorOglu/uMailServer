package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// stubResponder records the meeting responses applied to it; failOn fails the
// matching request id.
type stubResponder struct {
	calls  []string // "<action>:<requestID>"
	calID  string
	failOn string
}

func (r *stubResponder) Respond(_, _, requestID, response string) (string, error) {
	if requestID == r.failOn {
		return "", errMutator
	}
	r.calls = append(r.calls, response+":"+requestID)
	if response == "decline" {
		return "", nil
	}
	return r.calID, nil
}

// doMeetingResponse sends a MeetingResponse with one Request and returns the
// decoded Result.
func doMeetingResponse(t *testing.T, s *Server, userResponse, collectionID, requestID string) *wbxml.Element {
	t.Helper()
	body, err := wbxml.Marshal(&wbxml.Element{Page: wbxml.PageMeetingResponse, Name: "MeetingResponse", Children: []*wbxml.Element{
		{Page: wbxml.PageMeetingResponse, Name: "Request", Children: []*wbxml.Element{
			{Page: wbxml.PageMeetingResponse, Name: "UserResponse", Text: userResponse},
			{Page: wbxml.PageMeetingResponse, Name: "CollectionId", Text: collectionID},
			{Page: wbxml.PageMeetingResponse, Name: "RequestId", Text: requestID},
		}},
	}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=MeetingResponse&DeviceId=DEV1", bytes.NewReader(body))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("MeetingResponse status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return resp.Sub("Result")
}

// TestMeetingResponseAccept verifies an accept (UserResponse 1) applies through
// the responder and returns the resulting CalendarId with success status.
func TestMeetingResponseAccept(t *testing.T) {
	resp := &stubResponder{calID: "evt-cal-1"}
	s := NewServer(allowAuth)
	s.SetMeetingResponder(resp)

	result := doMeetingResponse(t, s, "1", "INBOX", "blob1")
	if result == nil || result.Sub("Status").Text != meetingRespStatusSuccess {
		t.Fatalf("Result status = %v, want 1", result)
	}
	if result.Sub("RequestId").Text != "blob1" {
		t.Fatalf("Result RequestId = %q, want blob1", result.Sub("RequestId").Text)
	}
	if result.Sub("CalendarId").Text != "evt-cal-1" {
		t.Fatalf("Result CalendarId = %q, want evt-cal-1", result.Sub("CalendarId").Text)
	}
	if len(resp.calls) != 1 || resp.calls[0] != "accept:blob1" {
		t.Fatalf("responder calls = %v, want [accept:blob1]", resp.calls)
	}
}

// TestMeetingResponseDecline verifies a decline (UserResponse 3) applies as a
// decline and returns success with no CalendarId (nothing is on the calendar).
func TestMeetingResponseDecline(t *testing.T) {
	resp := &stubResponder{calID: "should-not-appear"}
	s := NewServer(allowAuth)
	s.SetMeetingResponder(resp)

	result := doMeetingResponse(t, s, "3", "INBOX", "blob9")
	if result.Sub("Status").Text != meetingRespStatusSuccess {
		t.Fatalf("decline status = %v, want 1", result.Sub("Status"))
	}
	if result.Sub("CalendarId") != nil {
		t.Fatalf("decline must not return a CalendarId")
	}
	if len(resp.calls) != 1 || resp.calls[0] != "decline:blob9" {
		t.Fatalf("responder calls = %v, want [decline:blob9]", resp.calls)
	}
}

// TestMeetingResponseInvalid verifies an unrecognized UserResponse is rejected
// per-request with the invalid status, not applied.
func TestMeetingResponseInvalid(t *testing.T) {
	resp := &stubResponder{}
	s := NewServer(allowAuth)
	s.SetMeetingResponder(resp)

	result := doMeetingResponse(t, s, "9", "INBOX", "blobx")
	if result.Sub("Status").Text != meetingRespStatusInvalid {
		t.Fatalf("invalid UserResponse status = %v, want 2", result.Sub("Status"))
	}
	if len(resp.calls) != 0 {
		t.Fatalf("invalid response must not be applied: %v", resp.calls)
	}
}

// TestInviteEventFromMIME verifies the iMIP extractor pulls the event out of a
// meeting-request email's text/calendar part — the source for a MeetingResponse.
func TestInviteEventFromMIME(t *testing.T) {
	raw := []byte("From: org@x.test\r\nTo: u@x.test\r\nSubject: Invite\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=bnd\r\n\r\n" +
		"--bnd\r\nContent-Type: text/plain\r\n\r\nYou are invited.\r\n" +
		"--bnd\r\nContent-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\n" +
		"UID:inv-42\r\nSUMMARY:Project kickoff\r\nLOCATION:Room 1\r\n" +
		"DTSTART:20260925T150000Z\r\nDTEND:20260925T160000Z\r\nORGANIZER:mailto:org@x.test\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n--bnd--\r\n")
	inv, ok := InviteEventFromMIME(raw)
	if !ok {
		t.Fatalf("expected a meeting invite to be extracted")
	}
	if inv.UID != "inv-42" || inv.Subject != "Project kickoff" || inv.Location != "Room 1" {
		t.Fatalf("invite fields wrong: %+v", inv)
	}
	if inv.Start.UTC().Format(compactDateTime) != "20260925T150000Z" {
		t.Fatalf("invite start wrong: %v", inv.Start)
	}

	if _, ok := InviteEventFromMIME([]byte("From: a@x\r\nSubject: plain\r\n\r\nno calendar here\r\n")); ok {
		t.Fatalf("a non-invite message must not be treated as an invite")
	}
}
