// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements PushSubscription delivery: the server POSTs
// a SendNotification SOAP envelope to the client-supplied callback URL for each
// batch of lifecycle events. The callback URL is SSRF-guarded (shared with the
// outbound-webhook guard) at Subscribe time and again at every send. The owning
// poll/leader loop lives in internal/server (which holds the subscription store,
// the lifecycle cursor, and the cluster leader gate); this file is the EWS-shaped
// delivery primitive that loop calls.
package ews

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/webhook"
)

// pushHTTPClient is the shared client for outbound push notifications. The
// 30-second timeout matches the webhook/alert outbound clients.
var pushHTTPClient = &http.Client{Timeout: 30 * time.Second}

// pushURLAllowed reports whether a push callback URL is a permitted target.
// Production defers to the shared SSRF guard (http/https, never
// loopback/private/link-local, DNS-rebind resistant); tests may relax it to
// accept a local sink via SetAllowPrivatePushTargets.
func (s *Server) pushURLAllowed(rawURL string) bool {
	if strings.TrimSpace(rawURL) == "" {
		return false
	}
	if s.allowPrivatePushTargets {
		u, err := url.Parse(rawURL)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
	}
	ok, _ := webhook.ValidateOutboundURL(rawURL)
	return ok
}

// SendNotificationResponse is the client's reply to a pushed SendNotification.
// SubscriptionStatus is "OK" (keep delivering) or "Unsubscribe" (stop and drop
// the subscription). A non-2xx HTTP status or a parse failure is treated as a
// transient error: the caller leaves the cursor unadvanced and retries.
type SendNotificationResponse struct {
	SubscriptionStatus string
}

// DeliverPushNotification builds a SendNotification envelope for the events and
// POSTs it to the subscription's callback URL. It returns remove=true when the
// client asks to Unsubscribe (or the URL is no longer permitted), and a non-nil
// error on a transient failure (network error / non-2xx) so the caller can
// retry without advancing the cursor. A nil error with remove=false means the
// batch was delivered and the cursor may advance.
func (s *Server) DeliverPushNotification(sub *semcore.Subscription, events []semcore.Lifecycle) (remove bool, err error) {
	if sub == nil {
		return false, nil
	}
	// Re-validate at send time (defends against DNS rebinding since Subscribe).
	if !s.pushURLAllowed(sub.PushURL) {
		// A target that is no longer permitted will never succeed; drop it.
		return true, nil
	}

	notes := make([]NotificationEventType, 0, len(events))
	for i := range events {
		if ev := s.lifecycleToNotificationEvent(&events[i]); ev != nil {
			notes = append(notes, *ev)
		}
	}
	if len(notes) == 0 {
		return false, nil
	}

	envelope := BuildSendNotificationEnvelope(sub.ID.ID, watermarkString(sub.LastSeq), notes)
	req, rerr := http.NewRequest(http.MethodPost, sub.PushURL, bytes.NewReader(envelope))
	if rerr != nil {
		return false, rerr
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	resp, derr := pushHTTPClient.Do(req)
	if derr != nil {
		return false, derr
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck

	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, e := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if e != nil || len(body) > 64<<10 {
			break
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, &pushDeliveryError{status: resp.StatusCode}
	}
	// Honor an explicit Unsubscribe in the client's SubscriptionStatus.
	if status := parseSubscriptionStatus(body); strings.EqualFold(status, "Unsubscribe") {
		return true, nil
	}
	return false, nil
}

type pushDeliveryError struct{ status int }

func (e *pushDeliveryError) Error() string {
	return "push delivery: non-2xx status " + http.StatusText(e.status)
}

// parseSubscriptionStatus extracts a <SubscriptionStatus> value from the client
// reply (namespace-agnostic; the element local name is matched).
func parseSubscriptionStatus(body []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "SubscriptionStatus" {
			var v string
			if dec.DecodeElement(&v, &se) == nil {
				return strings.TrimSpace(v)
			}
			return ""
		}
	}
}

// BuildSendNotificationEnvelope renders the SOAP envelope the server POSTs to a
// push subscription's callback URL: an <m:SendNotification> carrying one
// <t:Notification> with the named event elements (CreatedEvent/…). Reuses the
// streaming event renderer so push and streaming emit identical event XML.
func BuildSendNotificationEnvelope(subID, prevWatermark string, events []NotificationEventType) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:SendNotification><m:ResponseMessages>`)
	b.WriteString(`<m:SendNotificationResponseMessage ResponseClass="Success">`)
	b.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
	b.WriteString(`<m:Notification>`)
	b.WriteString(`<t:SubscriptionId>` + xmlEscape(subID) + `</t:SubscriptionId>`)
	if prevWatermark != "" {
		b.WriteString(`<t:PreviousWatermark>` + xmlEscape(prevWatermark) + `</t:PreviousWatermark>`)
	}
	b.WriteString(`<t:MoreEvents>false</t:MoreEvents>`)
	for _, ev := range events {
		b.WriteString(renderStreamingEvent(ev, prevWatermark))
	}
	b.WriteString(`</m:Notification>`)
	b.WriteString(`</m:SendNotificationResponseMessage>`)
	b.WriteString(`</m:ResponseMessages></m:SendNotification>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}
