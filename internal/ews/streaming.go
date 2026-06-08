// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements StreamingSubscription delivery via the
// GetStreamingEvents operation: a long-lived HTTP response that streams a
// series of complete SOAP envelopes (one per frame) as lifecycle events occur,
// keeping the connection alive with periodic StatusEvent heartbeats and closing
// it with a ConnectionStatus=Closed frame at the connection timeout. The event
// source is the same lifecycle/watermark model GetEvents (pull) reads from.
package ews

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// GetStreamingEventsRequest is the EWS GetStreamingEvents operation request.
type GetStreamingEventsRequest struct {
	XMLName         xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetStreamingEvents"`
	SubscriptionIDs struct {
		IDs []string `xml:"http://schemas.microsoft.com/exchange/services/2006/types SubscriptionId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscriptionIds"`
	ConnectionTimeout int `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ConnectionTimeout"`
}

// streamSub is one validated streaming subscription with its live event cursor.
type streamSub struct {
	id     string
	mboxID semcore.MailboxId
	cursor uint64 // highest lifecycle seq already delivered on this connection
}

// handleGetStreamingEvents holds the HTTP connection open and streams
// notification frames. Unlike the buffered handlers it writes directly to w, so
// HandleHTTP dispatches it before the normal []byte response path.
func (s *Server) handleGetStreamingEvents(w http.ResponseWriter, r *http.Request, body []byte) {
	ctx := r.Context()

	var req GetStreamingEventsRequest
	if err := decodeRequest(body, &req); err != nil {
		writeSOAPError(w, http.StatusBadRequest, ErrErrorInvalidOperation, "malformed request: "+err.Error())
		return
	}
	mboxID, _, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		writeSOAPError(w, http.StatusOK, errCode, "could not resolve mailbox")
		return
	}
	if s.subscriptions == nil || s.lifecycle == nil {
		writeSOAPError(w, http.StatusOK, ErrErrorNotImplemented, "subscription store not available")
		return
	}

	// Validate every subscription id up front (all errors must precede the 200
	// + first flush, since a streamed response cannot change status afterwards).
	var subs []streamSub
	for _, id := range req.SubscriptionIDs.IDs {
		sub, err := s.subscriptions.GetSubscription(semcore.SubscriptionId{ID: id})
		if err != nil {
			if err == semcore.ErrSubscriptionDrained {
				writeSOAPError(w, http.StatusOK, ErrErrorSubscriptionDrained, "subscription was invalidated by server restart; please subscribe again")
				return
			}
			writeSOAPError(w, http.StatusOK, ErrErrorInvalidSubscription, "subscription not found or expired: "+id)
			return
		}
		if !sub.MailboxID.Equal(mboxID) {
			writeSOAPError(w, http.StatusOK, ErrErrorAccessDenied, "subscription belongs to a different mailbox")
			return
		}
		// Start from the subscription's last delivered seq, or from "now" (the
		// current highest seq) so a fresh stream does not replay all history.
		cursor := sub.LastSeq
		if cursor == 0 {
			if hs, herr := s.lifecycle.HighestSequence(mboxID); herr == nil {
				cursor = hs
			}
		}
		subs = append(subs, streamSub{id: id, mboxID: mboxID, cursor: cursor})
	}
	if len(subs) == 0 {
		writeSOAPError(w, http.StatusOK, ErrErrorInvalidSubscription, "no subscription ids supplied")
		return
	}

	// ConnectionTimeout is in MINUTES (1..30); default 30.
	timeoutMin := req.ConnectionTimeout
	if timeoutMin < 1 {
		timeoutMin = 30
	}
	if timeoutMin > 30 {
		timeoutMin = 30
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeSOAPError(w, http.StatusOK, ErrErrorInternalServer, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Clear the server's write deadline (the primary http.Server sets a 30s
	// WriteTimeout); without this the long-lived stream is force-closed
	// mid-frame. Mirrors internal/websocket/sse.go.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{}) //nolint:errcheck // best-effort; some writers don't support it
	}

	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	deadline := time.NewTimer(time.Duration(timeoutMin) * time.Minute)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			// Final frame: tell the client the connection is closing cleanly.
			_, _ = w.Write(s.buildStreamingFrame(subs[0].id, "", "Closed", nil)) //nolint:errcheck
			flusher.Flush()
			return
		case <-poll.C:
			if !s.streamPollOnce(w, flusher, subs) {
				return
			}
		case <-heartbeat.C:
			// Keep-alive StatusEvent (also renews the subscriptions so an idle
			// stream's subscription does not expire under it).
			for i := range subs {
				wm := watermarkString(subs[i].cursor)
				_, _ = w.Write(s.buildStreamingFrame(subs[i].id, wm, "OK", []NotificationEventType{{EventType: "StatusEvent"}})) //nolint:errcheck
				//nolint:errcheck
				_ = s.subscriptions.RenewSubscription(semcore.SubscriptionId{ID: subs[i].id})
			}
			flusher.Flush()
		}
	}
}

// streamPollOnce polls each subscription for new lifecycle events and writes a
// frame per subscription that has any. Returns false if a write fails (client
// gone) so the caller stops.
func (s *Server) streamPollOnce(w http.ResponseWriter, flusher http.Flusher, subs []streamSub) bool {
	wrote := false
	for i := range subs {
		evts, highestSeq, err := s.lifecycle.PollEvents(subs[i].mboxID, subs[i].cursor, 100)
		if err != nil || len(evts) == 0 {
			continue
		}
		prevWatermark := watermarkString(subs[i].cursor)
		notes := make([]NotificationEventType, 0, len(evts))
		for j := range evts {
			if ev := s.lifecycleToNotificationEvent(&evts[j]); ev != nil {
				notes = append(notes, *ev)
			}
		}
		if _, werr := w.Write(s.buildStreamingFrame(subs[i].id, prevWatermark, "OK", notes)); werr != nil {
			return false
		}
		subs[i].cursor = highestSeq
		//nolint:errcheck
		_ = s.subscriptions.RenewSubscription(semcore.SubscriptionId{ID: subs[i].id})
		wrote = true
	}
	if wrote {
		flusher.Flush()
	}
	return true
}

// watermarkString formats a lifecycle seq as the EWS watermark token.
func watermarkString(seq uint64) string {
	return "w" + leftPad20(strconv.FormatUint(seq, 10))
}

func leftPad20(s string) string {
	if len(s) >= 20 {
		return s
	}
	return strings.Repeat("0", 20-len(s)) + s
}

// buildStreamingFrame renders ONE complete SOAP envelope frame for the streaming
// connection. exchangelib's DocumentYielder reads a sequence of complete
// documents off the wire until it sees ConnectionStatus=Closed.
func (s *Server) buildStreamingFrame(subID, prevWatermark, status string, events []NotificationEventType) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:GetStreamingEventsResponse><m:ResponseMessages>`)
	b.WriteString(`<m:GetStreamingEventsResponseMessage ResponseClass="Success">`)
	b.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
	b.WriteString(`<m:ConnectionStatus>` + status + `</m:ConnectionStatus>`)
	if len(events) > 0 {
		b.WriteString(`<m:Notifications><t:Notification>`)
		b.WriteString(`<t:SubscriptionId>` + xmlEscape(subID) + `</t:SubscriptionId>`)
		if prevWatermark != "" {
			b.WriteString(`<t:PreviousWatermark>` + xmlEscape(prevWatermark) + `</t:PreviousWatermark>`)
		}
		b.WriteString(`<t:MoreEvents>false</t:MoreEvents>`)
		for _, ev := range events {
			b.WriteString(renderStreamingEvent(ev, prevWatermark))
		}
		b.WriteString(`</t:Notification></m:Notifications>`)
	}
	b.WriteString(`</m:GetStreamingEventsResponseMessage>`)
	b.WriteString(`</m:ResponseMessages></m:GetStreamingEventsResponse>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}

// renderStreamingEvent renders one named streaming event element (CreatedEvent,
// ModifiedEvent, …) carrying Watermark/TimeStamp and the object id. A
// StatusEvent carries only a Watermark (the keep-alive heartbeat).
func renderStreamingEvent(ev NotificationEventType, watermark string) string {
	name := ev.EventType
	if name == "" {
		name = "ModifiedEvent"
	}
	var b strings.Builder
	b.WriteString("<t:" + name + ">")
	b.WriteString("<t:Watermark>" + xmlEscape(watermark) + "</t:Watermark>")
	if name == "StatusEvent" {
		b.WriteString("</t:" + name + ">")
		return b.String()
	}
	if ev.Time != "" {
		b.WriteString("<t:TimeStamp>" + xmlEscape(ev.Time) + "</t:TimeStamp>")
	}
	if ev.ItemID.ID != "" {
		b.WriteString(`<t:ItemId Id="` + xmlEscape(ev.ItemID.ID) + `"/>`)
		if ev.FolderID.ID != "" {
			b.WriteString(`<t:ParentFolderId Id="` + xmlEscape(ev.FolderID.ID) + `"/>`)
		}
	} else if ev.FolderID.ID != "" {
		b.WriteString(`<t:FolderId Id="` + xmlEscape(ev.FolderID.ID) + `"/>`)
	}
	b.WriteString("</t:" + name + ">")
	return b.String()
}
