package activesync

import (
	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// MeetingResponse status codes (MS-ASCMD 2.2.3.119.1): 1 = success, 2 = an
// invalid meeting request, 4 = a server error applying the response.
const (
	meetingRespStatusSuccess = "1"
	meetingRespStatusInvalid = "2"
	meetingRespStatusError   = "4"
)

// MeetingResponder applies a client's response to a meeting invitation. response
// is "accept", "tentative" or "decline"; collectionID/requestID identify the
// invitation item (the meeting-request email). It returns the resulting calendar
// item's server id (empty for a decline, which adds nothing to the calendar).
// Implementations converge on the canonical calendar store, so a response made
// on a phone is reflected over every surface.
type MeetingResponder interface {
	Respond(email, collectionID, requestID, response string) (calendarID string, err error)
}

// meetingResult is one decoded MeetingResponse outcome to emit.
type meetingResult struct {
	requestID  string
	status     string
	calendarID string
}

// handleMeetingResponse answers the MeetingResponse command (MS-ASCMD): for each
// Request the client sends — a UserResponse (1 accept, 2 tentative, 3 decline)
// against a meeting-request item (CollectionId + RequestId) — it applies the
// response to the canonical calendar and returns a Result with the per-request
// Status and, on accept/tentative, the CalendarId of the resulting event.
func (s *Server) handleMeetingResponse(ctx *Context) ([]byte, error) {
	root, err := wbxml.Unmarshal(ctx.Body)
	if err != nil {
		return nil, err
	}
	var results []meetingResult
	for _, req := range root.Children {
		if req.Name != "Request" {
			continue
		}
		requestID := textOf(req.Sub("RequestId"))
		collectionID := textOf(req.Sub("CollectionId"))
		action := userResponseAction(textOf(req.Sub("UserResponse")))
		if requestID == "" || action == "" {
			results = append(results, meetingResult{requestID: requestID, status: meetingRespStatusInvalid})
			continue
		}
		if s.meetings == nil {
			results = append(results, meetingResult{requestID: requestID, status: meetingRespStatusError})
			continue
		}
		calID, rerr := s.meetings.Respond(ctx.Email, collectionID, requestID, action)
		if rerr != nil {
			s.logger.Warn("activesync meetingresponse failed", "email", ctx.Email, "request", requestID, "error", rerr)
			results = append(results, meetingResult{requestID: requestID, status: meetingRespStatusError})
			continue
		}
		results = append(results, meetingResult{requestID: requestID, status: meetingRespStatusSuccess, calendarID: calID})
	}
	return marshalMeetingResponse(results)
}

// userResponseAction maps the MS-ASCMD UserResponse value to a response action,
// or "" for an unrecognized value.
func userResponseAction(v string) string {
	switch v {
	case "1":
		return "accept"
	case "2":
		return "tentative"
	case "3":
		return "decline"
	default:
		return ""
	}
}

// marshalMeetingResponse builds the MeetingResponse response: one Result per
// request carrying its RequestId, Status and (when the meeting was added) the
// CalendarId of the calendar item.
func marshalMeetingResponse(results []meetingResult) ([]byte, error) {
	root := &wbxml.Element{Page: wbxml.PageMeetingResponse, Name: "MeetingResponse"}
	for _, r := range results {
		result := &wbxml.Element{Page: wbxml.PageMeetingResponse, Name: "Result", Children: []*wbxml.Element{
			{Page: wbxml.PageMeetingResponse, Name: "RequestId", Text: r.requestID},
			{Page: wbxml.PageMeetingResponse, Name: "Status", Text: r.status},
		}}
		if r.calendarID != "" {
			result.Children = append(result.Children, &wbxml.Element{Page: wbxml.PageMeetingResponse, Name: "CalendarId", Text: r.calendarID})
		}
		root.Children = append(root.Children, result)
	}
	return wbxml.Marshal(root)
}
