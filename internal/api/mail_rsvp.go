package api

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/caldav"
)

// meetingInvite is the parsed iMIP (text/calendar) meeting request extracted
// from a message, projected for the webmail RSVP UI.
type meetingInvite struct {
	IsInvite  bool   `json:"isInvite"`
	UID       string `json:"uid,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
	Location  string `json:"location,omitempty"`
	Organizer string `json:"organizer,omitempty"`
}

// decodeBody decodes a MIME part body per its Content-Transfer-Encoding.
func decodeBody(b []byte, cte string) string {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		dec, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r", ""), "\n", ""))
		if err == nil {
			return string(dec)
		}
	case "quoted-printable":
		dec, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(b)))
		if err == nil {
			return string(dec)
		}
	}
	return string(b)
}

// findCalendarPart walks a MIME body (recursing into nested multiparts) and
// returns the first text/calendar part's decoded content.
func findCalendarPart(body io.Reader, mediaType string, params map[string]string) (string, bool) {
	if strings.HasPrefix(mediaType, "text/calendar") {
		b, err := io.ReadAll(body)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	if !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		return "", false
	}
	mr := multipart.NewReader(body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			return "", false
		}
		pmt, pparams, perr := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if perr != nil {
			continue
		}
		if strings.HasPrefix(pmt, "text/calendar") {
			raw, err := io.ReadAll(p)
			if err != nil {
				return "", false
			}
			return decodeBody(raw, p.Header.Get("Content-Transfer-Encoding")), true
		}
		if strings.HasPrefix(pmt, "multipart/") {
			raw, err := io.ReadAll(p)
			if err != nil {
				return "", false
			}
			if ics, ok := findCalendarPart(bytes.NewReader(raw), pmt, pparams); ok {
				return ics, true
			}
		}
	}
}

// parseMeetingInvite extracts a meeting request from a raw RFC822 message.
func parseMeetingInvite(raw []byte) (meetingInvite, bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return meetingInvite{}, false
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		return meetingInvite{}, false
	}
	ics, ok := findCalendarPart(msg.Body, mediaType, params)
	if ok && strings.EqualFold(strings.TrimSpace(msg.Header.Get("Content-Transfer-Encoding")), "base64") && strings.HasPrefix(mediaType, "text/calendar") {
		ics = decodeBody([]byte(ics), "base64")
	}
	if !ok || !strings.Contains(ics, "BEGIN:VEVENT") {
		return meetingInvite{}, false
	}

	// Only treat METHOD:REQUEST (or a present VEVENT with an organizer) as an
	// actionable invite.
	var method, organizer string
	for _, line := range unfoldICS(ics) {
		name, _, value := splitICSLine(line)
		switch name {
		case "METHOD":
			method = strings.ToUpper(value)
		case "ORGANIZER":
			organizer = strings.TrimPrefix(strings.ToLower(value), "mailto:")
		}
	}
	ev, ok := parseICSEvent(ics)
	if !ok {
		return meetingInvite{}, false
	}
	if method != "" && method != "REQUEST" {
		return meetingInvite{}, false
	}
	return meetingInvite{
		IsInvite:  true,
		UID:       ev.UID,
		Summary:   ev.Summary,
		Start:     ev.Start,
		End:       ev.End,
		Location:  ev.Location,
		Organizer: organizer,
	}, true
}

// readInvite locates a message by id and parses it as a meeting invite.
func (s *Server) readInvite(user, id string) (meetingInvite, bool) {
	if s.mailHandler == nil || s.mailHandler.msgStore == nil {
		return meetingInvite{}, false
	}
	_, _, meta, found := s.mailHandler.findMessage(user, id)
	if !found {
		return meetingInvite{}, false
	}
	raw, err := s.mailHandler.msgStore.ReadMessage(user, meta.MessageID)
	if err != nil {
		return meetingInvite{}, false
	}
	return parseMeetingInvite(raw)
}

// handleMailInvite reports whether a message is a meeting invite and returns its
// details for the RSVP UI. GET /api/v1/mail/invite?id=<msgid>.
func (s *Server) handleMailInvite(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		s.sendError(w, http.StatusBadRequest, "message id required")
		return
	}
	invite, ok := s.readInvite(user, id)
	if !ok {
		s.sendJSON(w, http.StatusOK, meetingInvite{IsInvite: false})
		return
	}
	s.sendJSON(w, http.StatusOK, invite)
}

// rsvpRequest is the POST body for responding to an invite.
type rsvpRequest struct {
	ID       string `json:"id"`
	Response string `json:"response"` // accept | tentative | decline
}

// handleMailRSVP responds to a meeting invite. Accept/tentative add the event to
// the user's calendar; decline removes it if present. (The webmail send path is
// local-only, so the organizer is not emailed a reply here.)
func (s *Server) handleMailRSVP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		s.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user, ok := r.Context().Value("user").(string)
	if !ok || user == "" {
		s.sendError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.calendarHandler == nil {
		s.sendError(w, http.StatusServiceUnavailable, "calendar not available")
		return
	}
	var req rsvpRequest
	if err := decodeJSON(r, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Response {
	case "accept", "tentative", "decline":
	default:
		s.sendError(w, http.StatusBadRequest, "response must be accept, tentative, or decline")
		return
	}
	invite, ok := s.readInvite(user, req.ID)
	if !ok {
		s.sendError(w, http.StatusBadRequest, "message is not a meeting invite")
		return
	}

	calID, err := s.calendarHandler.ensureCalendar(user)
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to open calendar")
		return
	}
	store := s.calendarHandler.getStorage()

	if req.Response == "decline" {
		// Remove the event from the calendar if a prior accept added it.
		// DeleteEvent treats a missing event as success, so any error here is
		// a real storage failure.
		if err := store.DeleteEvent(user, calID, invite.UID); err != nil {
			s.sendError(w, http.StatusInternalServerError, "failed to update calendar")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]string{"status": "declined"})
		return
	}

	// Accept/tentative: add (or refresh) the event on the user's calendar.
	summary := invite.Summary
	if req.Response == "tentative" {
		summary = "[Tentative] " + summary
	}
	ics, err := buildICSEvent(CalendarEventDTO{
		UID:      invite.UID,
		Summary:  summary,
		Location: invite.Location,
		Start:    invite.Start,
		End:      invite.End,
	})
	if err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to build calendar event")
		return
	}
	event := &caldav.CalendarEvent{UID: invite.UID, Summary: summary, Created: time.Now(), Modified: time.Now()}
	if err := store.SaveEvent(user, calID, event, ics); err != nil {
		s.sendError(w, http.StatusInternalServerError, "failed to add event to calendar")
		return
	}
	s.sendJSON(w, http.StatusOK, map[string]string{"status": req.Response})
}
