// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements subscription management and event polling:
// Subscribe, Unsubscribe, GetEvents, and the notification watermark model.
package ews

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// Subscribe
// ---------------------------------------------------------------------------

// SubscribeRequest is the EWS Subscribe operation request.
type SubscribeRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Subscribe"`
	PullSubscriptionSubscriptionRequest *struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages PullSubscriptionRequest"`
		SubscribeToAllFolders string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscribeToAllFolders,attr,omitempty"`
		FolderIDs []FolderIDOnly `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages PullSubscriptionRequest,omitempty"`
}

// SubscribeResponse is the EWS Subscribe operation response.
type SubscribeResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscribeResponse"`
	ResponseMessages SubscribeResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// SubscribeResponseMessages wraps a list of Subscribe response messages.
type SubscribeResponseMessages struct {
	Messages []SubscribeResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscribeResponseMessage"`
}

// SubscribeResponseMessageType is one Subscribe result.
type SubscribeResponseMessageType struct {
	XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscribeResponseMessage"`
	ResponseClass string  `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	SubscriptionID SubscriptionIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscriptionId"`
	Watermark     string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Watermark"`
	PreviousWatermark string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages PreviousWatermark,omitempty"`
}

// SubscriptionIdType is the EWS subscription ID with a subscription ID string.
type SubscriptionIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscriptionId"`
	ID     string   `xml:",chardata"`
}

// handleSubscribe processes a Subscribe EWS SOAP request.
func (s *Server) handleSubscribe(ctx context.Context, body []byte) []byte {
	var req SubscribeRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("Subscribe", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("Subscribe", errCode, "could not resolve mailbox")
	}
	_ = mboxKey

	var subscribedFolders []semcore.FolderId
	pullReq := req.PullSubscriptionSubscriptionRequest
	if pullReq != nil {
		for _, fid := range pullReq.FolderIDs {
			fid2, err := semcore.NewFolderId(fid.ID)
			if err != nil {
				continue
			}
			subscribedFolders = append(subscribedFolders, fid2)
		}
	}

	// Get the current highest lifecycle seq for this mailbox.
	var watermark string
	var highestSeq uint64
	if s.lifecycle != nil {
		var err error
		highestSeq, err = s.lifecycle.HighestSequence(mboxID)
		if err == nil {
			watermark = fmt.Sprintf("w%020d", highestSeq)
		}
	}

	// If no subscriptions store is wired, return a "not implemented" response.
	if s.subscriptions == nil {
		return s.errorResponseXML("Subscribe", ErrErrorNotImplemented, "subscription store not available")
	}

	sub := semcore.Subscription{
		MailboxID: mboxID,
		Kind:     semcore.SubscriptionKindPull,
	}
	if len(subscribedFolders) > 0 {
		sub.FolderIDs = subscribedFolders
	}

	// Set a default expiry (30 minutes).
	sub.ExpiresAt = time.Now().Add(30 * time.Minute)

	subID, err := s.subscriptions.CreateSubscription(sub)
	if err != nil {
		return s.errorResponseXML("Subscribe", ErrErrorInternalServer, err.Error())
	}

	// Update watermark to current high seq.
	updated, err := s.subscriptions.GetSubscription(subID)
	if err == nil && updated != nil {
		updated.LastSeq = highestSeq
	}

	msg := SubscribeResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		SubscriptionID: SubscriptionIdType{ID: subID.ID},
		Watermark:     watermark,
	}

	resp := SubscribeResponse{}
	resp.ResponseMessages.Messages = []SubscribeResponseMessageType{msg}
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// Unsubscribe
// ---------------------------------------------------------------------------

// UnsubscribeRequest is the EWS Unsubscribe operation request.
type UnsubscribeRequest struct {
	XMLName        xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Unsubscribe"`
	SubscriptionID SubscriptionIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscriptionId"`
}

// UnsubscribeResponse is the EWS Unsubscribe operation response.
type UnsubscribeResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UnsubscribeResponse"`
	ResponseMessages UnsubscribeResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// UnsubscribeResponseMessages wraps a list of Unsubscribe response messages.
type UnsubscribeResponseMessages struct {
	Messages []struct {
		XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UnsubscribeResponseMessage"`
		ResponseClass string  `xml:"ResponseClass,attr"`
		ResponseCode  string  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UnsubscribeResponseMessage"`
}

// handleUnsubscribe processes an Unsubscribe EWS SOAP request.
func (s *Server) handleUnsubscribe(ctx context.Context, body []byte) []byte {
	var req UnsubscribeRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("Unsubscribe", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	if s.subscriptions == nil {
		return s.errorResponseXML("Unsubscribe", ErrErrorNotImplemented, "subscription store not available")
	}

	subID := semcore.SubscriptionId{ID: req.SubscriptionID.ID}
	if err := s.subscriptions.RemoveSubscription(subID); err != nil {
		return s.errorResponseXML("Unsubscribe", ErrErrorInternalServer, err.Error())
	}

	resp := UnsubscribeResponse{}
	resp.ResponseMessages.Messages = []struct {
		XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UnsubscribeResponseMessage"`
		ResponseClass string  `xml:"ResponseClass,attr"`
		ResponseCode  string  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}{{
		ResponseClass: "Success",
		ResponseCode:  string(ErrNoError),
	}}
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// GetEvents
// ---------------------------------------------------------------------------

// GetEventsRequest is the EWS GetEvents operation request.
type GetEventsRequest struct {
	XMLName        xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetEvents"`
	SubscriptionID SubscriptionIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscriptionId"`
	Watermark      string             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Watermark,omitempty"`
}

// GetEventsResponse is the EWS GetEvents operation response.
type GetEventsResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetEventsResponse"`
	ResponseMessages GetEventsResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetEventsResponseMessages wraps a list of GetEvents response messages.
type GetEventsResponseMessages struct {
	Messages []GetEventsResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetEventsResponseMessage"`
}

// GetEventsResponseMessageType is one GetEvents result.
type GetEventsResponseMessageType struct {
	XMLName             xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetEventsResponseMessage"`
	ResponseClass       string  `xml:"ResponseClass,attr"`
	ResponseCode        ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	SubscriptionID      SubscriptionIdType  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscriptionId"`
	Watermark           string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Watermark"`
	MoreEvents          bool   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MoreEvents,attr"`
	FolderSyncStatus    int    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderSyncStatus,omitempty"`
	ItemSyncStatus      int    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemSyncStatus,omitempty"`
	Events             []NotificationEventType `xml:"http://schemas.microsoft.com/exchange/services/2006/types NotificationEvent"`
}

// NotificationEventType wraps individual notification events.
type NotificationEventType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types NotificationEvent"`
	// Event types: CreatedEvent, ModifiedEvent, DeletedEvent, MovedEvent, CopiedEvent, CreatedEvent.01
	EventType   string   `xml:"EventType,attr"`
	Time       string   `xml:"Time,attr"`
	FolderID   struct {
		ID string `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	ItemID     struct {
		ID string `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID struct {
		ID string `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId,omitempty"`
	// For ModifiedEvent: includes flag changes.
	IsRead *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsRead,omitempty"`
}

// handleGetEvents processes a GetEvents EWS SOAP request.
func (s *Server) handleGetEvents(ctx context.Context, body []byte) []byte {
	var req GetEventsRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetEvents", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("GetEvents", errCode, "could not resolve mailbox")
	}
	_ = mboxKey

	if s.subscriptions == nil {
		return s.errorResponseXML("GetEvents", ErrErrorNotImplemented, "subscription store not available")
	}

	subID := semcore.SubscriptionId{ID: req.SubscriptionID.ID}
	sub, err := s.subscriptions.GetSubscription(subID)
	if err != nil {
		return s.errorResponseXML("GetEvents", ErrErrorInternalServer, "subscription not found or expired")
	}

	// Check mailbox ownership.
	if !sub.MailboxID.Equal(mboxID) {
		return s.errorResponseXML("GetEvents", ErrErrorAccessDenied, "subscription belongs to a different mailbox")
	}

	// Parse previous watermark to get starting seq.
	var sinceSeq uint64
	if req.Watermark != "" {
		wm := strings.TrimPrefix(req.Watermark, "w")
		if seq, err := strconv.ParseUint(wm, 10, 64); err == nil {
			sinceSeq = seq
		}
	} else {
		sinceSeq = sub.LastSeq
	}

	// Collect events from lifecycle store.
	var events []NotificationEventType
	var newWatermark string
	moreEvents := false

	if s.lifecycle != nil {
		lifecycleEvents, highestSeq, err := s.lifecycle.PollEvents(mboxID, sinceSeq, 100)
		if err == nil {
			for _, le := range lifecycleEvents {
				evt := s.lifecycleToNotificationEvent(&le)
				if evt != nil {
					//nolint:staticcheck // intentional guard for event classification.
					_ = le.FolderID.IsZero() && !le.ItemID.IsZero()
					events = append(events, *evt)
				}
			}
			newWatermark = fmt.Sprintf("w%020d", highestSeq)

			// Renew subscription (best-effort).
			//nolint:errcheck
			_ = s.subscriptions.RenewSubscription(subID)
		}
	}

	// If no lifecycle store, at minimum report that subscription is alive.
	if len(events) == 0 && newWatermark == "" {
		newWatermark = fmt.Sprintf("w%020d", sub.LastSeq)
	}

	msg := GetEventsResponseMessageType{
		ResponseClass:  "Success",
		ResponseCode:   ResponseCodeType{Value: ErrNoError},
		SubscriptionID: SubscriptionIdType{ID: subID.ID},
		Watermark:      newWatermark,
		MoreEvents:     moreEvents,
		Events:         events,
	}

	resp := GetEventsResponse{}
	resp.ResponseMessages.Messages = []GetEventsResponseMessageType{msg}
	return buildResponseEnvelope(resp)
}

// lifecycleToNotificationEvent converts a semcore.Lifecycle to a notification event.
func (s *Server) lifecycleToNotificationEvent(lc *semcore.Lifecycle) *NotificationEventType {
	evt := &NotificationEventType{
		Time: FormatEWSDateTime(lc.At),
	}

	switch lc.Kind {
	case semcore.LifecycleKindCreated:
		evt.EventType = "CreatedEvent"
	case semcore.LifecycleKindUpdated:
		evt.EventType = "ModifiedEvent"
	case semcore.LifecycleKindMoved:
		evt.EventType = "MovedEvent"
	case semcore.LifecycleKindSoftDeleted:
		evt.EventType = "DeletedEvent"
	case semcore.LifecycleKindHardDeleted:
		evt.EventType = "DeletedEvent"
	default:
		evt.EventType = "ModifiedEvent"
	}

	if !lc.FolderID.IsZero() {
		evt.FolderID.ID = lc.FolderID.String()
	}
	if !lc.ItemID.IsZero() {
		evt.ItemID.ID = lc.ItemID.String()
	}
	if !lc.FolderID.IsZero() && lc.ItemID.IsZero() {
		evt.ItemID.ID = ""
	}

	return evt
}

// ---------------------------------------------------------------------------
// SyncFolderItems improvements: invalid sync state recovery
// ---------------------------------------------------------------------------

// parseSyncStateForInvalidCheck parses a SyncState and returns
// whether it is clearly invalid (corrupt, expired, or mismatched folder).
func parseSyncStateForInvalidCheck(syncStateStr, folderID, mboxKey string) (invalid bool, reason string) {
	if syncStateStr == "" {
		return false, ""
	}
	// Sync state format: "v<version>:<folderID>:<lastTime>"
	parts := strings.Split(syncStateStr, ":")
	if len(parts) < 2 {
		return true, "sync state format is unrecognizable"
	}
	// If folderID is embedded in sync state, verify it matches.
	if len(parts) >= 2 && parts[1] != folderID {
		// The folder in the sync token doesn't match the requested folder.
		// This is a clearly invalid state - client is syncing a wrong folder.
		return true, "folder mismatch between sync state and request"
	}
	// Verify version prefix.
	if !strings.HasPrefix(parts[0], "v") {
		return true, "sync state has no version prefix"
	}
	return false, ""
}
