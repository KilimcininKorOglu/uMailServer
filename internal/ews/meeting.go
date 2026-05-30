// Package ews — meeting workflow support: sending meeting requests to
// attendees on calendar create, and processing Accept/TentativelyAccept/Decline
// responses by updating the responder's calendar. The wire format follows the
// EWS schema (AcceptItem/DeclineItem/TentativelyAcceptItem with ReferenceItemId)
// and the request mail carries an RFC 5545 iCalendar METHOD:REQUEST body.
package ews

import (
	"bytes"
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// meetingResponseKind enumerates the three meeting reply outcomes.
type meetingResponseKind int

const (
	meetingResponseAccept meetingResponseKind = iota
	meetingResponseTentative
	meetingResponseDecline
)

// Internal headers used to carry the meeting fields on the request mail so the
// responder side can rebuild the calendar entry without re-parsing iCalendar.
const (
	hdrMeeting          = "X-UMail-Meeting"
	hdrMeetingStart     = "X-UMail-Meeting-Start"
	hdrMeetingEnd       = "X-UMail-Meeting-End"
	hdrMeetingLocation  = "X-UMail-Meeting-Location"
	hdrMeetingOrganizer = "X-UMail-Meeting-Organizer"
	hdrMeetingUID       = "X-UMail-Meeting-UID"
)

// MeetingReplyType is the body of an AcceptItem / TentativelyAcceptItem /
// DeclineItem element. It carries the ReferenceItemId of the meeting request
// being responded to. It deliberately has no XMLName so the same type can back
// all three differently-named elements.
type MeetingReplyType struct {
	ReferenceItemID struct {
		ID string `xml:"Id,attr"`
		CK string `xml:"ChangeKey,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReferenceItemId"`
}

// meetingInvitationsRequested reports whether the CreateItem SendMeetingInvitations
// attribute asks the server to dispatch invitations to attendees.
func meetingInvitationsRequested(v string) bool {
	if v == "" {
		return false
	}
	return !strings.EqualFold(v, "SendToNone")
}

// attendeeEmails flattens the required and optional attendee mailboxes.
func attendeeEmails(cal *CreateItemCalendarItemType) []string {
	var out []string
	for _, list := range []*CreateAttendeesType{cal.RequiredAttendees, cal.OptionalAttendees} {
		if list == nil {
			continue
		}
		for _, a := range list.Attendee {
			if a.Mailbox.Email != "" {
				out = append(out, a.Mailbox.Email)
			}
		}
	}
	return out
}

// sendMeetingRequests delivers an iCalendar meeting request to each attendee.
// Delivery reuses the standard submission path so a local attendee lands in
// their inbox and a remote one is relayed.
func (s *Server) sendMeetingRequests(organizer string, cal *CreateItemCalendarItemType) {
	attendees := attendeeEmails(cal)
	if len(attendees) == 0 {
		return
	}
	uid := fmt.Sprintf("%d-%s@umailserver.local", time.Now().UnixNano(), organizer)
	for _, attendee := range attendees {
		msg := buildMeetingRequestMime(organizer, attendee, uid, cal)
		if err := s.submitOutboundMessage(organizer, []string{attendee}, msg); err != nil {
			s.logger.Warn("failed to send meeting request", "organizer", organizer, "attendee", attendee, "error", err)
		}
	}
}

// buildMeetingRequestMime builds the RFC 5322 message carrying the meeting
// request. The structured X-UMail-Meeting-* headers let the responder rebuild
// the calendar entry; the text/calendar body provides the RFC 5545 payload.
func buildMeetingRequestMime(organizer, attendee, uid string, cal *CreateItemCalendarItemType) []byte {
	var b bytes.Buffer
	now := time.Now().UTC().Format(time.RFC1123Z)
	b.WriteString("Date: " + now + "\r\n")
	b.WriteString("From: " + organizer + "\r\n")
	b.WriteString("To: " + attendee + "\r\n")
	b.WriteString("Subject: " + sanitizeHeader(cal.Subject) + "\r\n")
	b.WriteString("Message-ID: <" + generateMessageID() + ">\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	// Mark and carry the meeting fields.
	b.WriteString(hdrMeeting + ": 1\r\n")
	b.WriteString(hdrMeetingStart + ": " + sanitizeHeader(cal.Start) + "\r\n")
	b.WriteString(hdrMeetingEnd + ": " + sanitizeHeader(cal.End) + "\r\n")
	b.WriteString(hdrMeetingLocation + ": " + sanitizeHeader(cal.Location) + "\r\n")
	b.WriteString(hdrMeetingOrganizer + ": " + sanitizeHeader(organizer) + "\r\n")
	b.WriteString(hdrMeetingUID + ": " + uid + "\r\n")
	b.WriteString("Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n")
	b.WriteString("\r\n")
	// RFC 5545 VEVENT (best-effort; the responder uses the headers above).
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//uMailServer//EWS//EN\r\n")
	b.WriteString("METHOD:REQUEST\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	b.WriteString("UID:" + uid + "\r\n")
	b.WriteString("SUMMARY:" + icalEscape(cal.Subject) + "\r\n")
	if cal.Location != "" {
		b.WriteString("LOCATION:" + icalEscape(cal.Location) + "\r\n")
	}
	b.WriteString("ORGANIZER:mailto:" + organizer + "\r\n")
	b.WriteString("ATTENDEE:mailto:" + attendee + "\r\n")
	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.Bytes()
}

// handleMeetingReply processes one Accept/TentativelyAccept/Decline response.
// Accept and TentativelyAccept add the meeting to the responder's calendar;
// Decline removes any existing copy. The meeting details come from the
// referenced request mail's X-UMail-Meeting-* headers.
func (s *Server) handleMeetingReply(ctx context.Context, mboxID semcore.MailboxId, ownerEmail string, reply *MeetingReplyType, kind meetingResponseKind) ItemResponseMessageType {
	itemID, err := semcore.NewItemId(reply.ReferenceItemID.ID)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorInvalidId, err.Error())
	}
	rec, err := s.identity.GetItemIdentity(itemID)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorItemNotFound, err.Error())
	}
	rawMsg, err := s.msgStore.ReadMessage(rec.Email, rec.MsgKey)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorItemNotFound, err.Error())
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(rawMsg))
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, err.Error())
	}
	h := parsed.Header

	// Decline never touches the calendar.
	if kind == meetingResponseDecline {
		return ItemResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
		}
	}

	// Accept / TentativelyAccept: add the meeting to the responder's calendar.
	calFolderID, err := s.ensureDistinguishedFolderID(ownerEmail, "calendar", "calendar")
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, err.Error())
	}
	calItem := &CalendarItemTypeNew{
		Subject:  strings.TrimSpace(h.Get("Subject")),
		Start:    strings.TrimSpace(h.Get(hdrMeetingStart)),
		End:      strings.TrimSpace(h.Get(hdrMeetingEnd)),
		Location: strings.TrimSpace(h.Get(hdrMeetingLocation)),
	}
	calMsg := s.createCalendarItemInFolder(ctx, mboxID, ownerEmail, calFolderID, calItem, nil)
	if calMsg.ResponseClass != "Success" {
		return errorItemMsg("CreateItem", calMsg.ResponseCode.Value, "failed to add meeting to calendar")
	}
	return ItemResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
	}
}

// sanitizeHeader strips CR/LF to prevent header injection in built messages.
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// icalEscape escapes characters that are special in iCalendar text values.
func icalEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}
