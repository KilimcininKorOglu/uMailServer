package ews

import (
	"strings"
	"testing"
)

// TestGetStreamingEventsRequest_Parse proves the request decodes from a
// prefixed SOAP envelope: the SubscriptionIds list and the ConnectionTimeout
// (minutes) must be recovered, so the handler can validate subscriptions and
// bound the connection.
func TestGetStreamingEventsRequest_Parse(t *testing.T) {
	env := `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"` +
		` xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types"` +
		` xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages">` +
		`<soap:Body><m:GetStreamingEvents>` +
		`<m:SubscriptionIds><t:SubscriptionId>SUB-1</t:SubscriptionId><t:SubscriptionId>SUB-2</t:SubscriptionId></m:SubscriptionIds>` +
		`<m:ConnectionTimeout>5</m:ConnectionTimeout>` +
		`</m:GetStreamingEvents></soap:Body></soap:Envelope>`

	var req GetStreamingEventsRequest
	if err := decodeRequest([]byte(env), &req); err != nil {
		t.Fatalf("decodeRequest: %v", err)
	}
	if len(req.SubscriptionIDs.IDs) != 2 || req.SubscriptionIDs.IDs[0] != "SUB-1" || req.SubscriptionIDs.IDs[1] != "SUB-2" {
		t.Errorf("SubscriptionIds = %v, want [SUB-1 SUB-2]", req.SubscriptionIDs.IDs)
	}
	if req.ConnectionTimeout != 5 {
		t.Errorf("ConnectionTimeout = %d, want 5", req.ConnectionTimeout)
	}
}

// TestBuildStreamingFrame_Events verifies a notification frame is a complete
// SOAP envelope carrying ConnectionStatus=OK, the subscription id, and named
// event elements with their ids — the shape exchangelib's DocumentYielder reads.
func TestBuildStreamingFrame_Events(t *testing.T) {
	srv := &Server{}
	events := []NotificationEventType{
		{EventType: "CreatedEvent", Time: "2026-06-08T12:00:00Z"},
	}
	events[0].ItemID.ID = "ITEM-9"
	events[0].FolderID.ID = "FOLDER-3"

	frame := string(srv.buildStreamingFrame("SUB-1", "w00000000000000000007", "OK", events))

	for _, want := range []string{
		`<?xml version="1.0" encoding="utf-8"?>`,
		"<soap:Envelope", "</soap:Envelope>",
		"<m:GetStreamingEventsResponse>", "</m:GetStreamingEventsResponse>",
		"<m:ConnectionStatus>OK</m:ConnectionStatus>",
		"<t:SubscriptionId>SUB-1</t:SubscriptionId>",
		"<t:PreviousWatermark>w00000000000000000007</t:PreviousWatermark>",
		"<t:CreatedEvent>", "</t:CreatedEvent>",
		`<t:ItemId Id="ITEM-9"/>`,
		`<t:ParentFolderId Id="FOLDER-3"/>`,
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q:\n%s", want, frame)
		}
	}
}

// TestBuildStreamingFrame_Closed verifies the terminal frame announces
// ConnectionStatus=Closed and carries no Notifications, so the client stops.
func TestBuildStreamingFrame_Closed(t *testing.T) {
	srv := &Server{}
	frame := string(srv.buildStreamingFrame("SUB-1", "", "Closed", nil))
	if !strings.Contains(frame, "<m:ConnectionStatus>Closed</m:ConnectionStatus>") {
		t.Errorf("closed frame missing ConnectionStatus=Closed:\n%s", frame)
	}
	if strings.Contains(frame, "<m:Notifications>") {
		t.Errorf("closed frame should carry no Notifications:\n%s", frame)
	}
}

// TestRenderStreamingEvent covers the three element shapes: an item event
// (ItemId + ParentFolderId), a folder event (FolderId only), and the StatusEvent
// keep-alive (Watermark only).
func TestRenderStreamingEvent(t *testing.T) {
	item := NotificationEventType{EventType: "NewMailEvent", Time: "T"}
	item.ItemID.ID = "I1"
	item.FolderID.ID = "F1"
	got := renderStreamingEvent(item, "w1")
	for _, w := range []string{"<t:NewMailEvent>", "<t:Watermark>w1</t:Watermark>", "<t:TimeStamp>T</t:TimeStamp>", `<t:ItemId Id="I1"/>`, `<t:ParentFolderId Id="F1"/>`} {
		if !strings.Contains(got, w) {
			t.Errorf("item event missing %q: %s", w, got)
		}
	}

	folder := NotificationEventType{EventType: "ModifiedEvent"}
	folder.FolderID.ID = "F2"
	got = renderStreamingEvent(folder, "w2")
	if !strings.Contains(got, `<t:FolderId Id="F2"/>`) || strings.Contains(got, "<t:ItemId") {
		t.Errorf("folder event shape wrong: %s", got)
	}

	status := renderStreamingEvent(NotificationEventType{EventType: "StatusEvent"}, "w3")
	if !strings.Contains(status, "<t:StatusEvent><t:Watermark>w3</t:Watermark></t:StatusEvent>") {
		t.Errorf("status event shape wrong: %s", status)
	}
}

// TestWatermarkString verifies the 20-digit zero-padded "w"-prefixed format
// GetEvents and the streaming frames share.
func TestWatermarkString(t *testing.T) {
	if got := watermarkString(7); got != "w00000000000000000007" {
		t.Errorf("watermarkString(7) = %q", got)
	}
	if got := watermarkString(0); got != "w00000000000000000000" {
		t.Errorf("watermarkString(0) = %q", got)
	}
}
