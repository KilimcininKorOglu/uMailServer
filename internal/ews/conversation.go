// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements conversation operations: FindConversation
// and GetConversationItems. These support VAL-MAIL-019 by exposing conversation
// grouping as a first-class EWS browse surface.
package ews

import (
	"context"
	"encoding/xml"
	"sort"
	"strconv"
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// FindConversation
// ---------------------------------------------------------------------------

// convInfo holds conversation grouping metadata during FindConversation.
type convInfo struct {
	id       semcore.ConversationId
	items    []semcore.StoredItemIdentity
	lastTime string
	subject  string
	unread   int
}

// FindConversationRequest is the EWS FindConversation operation request.
type FindConversationRequest struct {
	XMLName     xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindConversation"`
	ItemShape   ItemShapeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemShape"`
	IndexedPage struct {
		MaxEntriesReturned string `xml:"MaxEntriesReturned,attr"`
		Offset             string `xml:"Offset,attr"`
		BasePoint          string `xml:"BasePoint,attr"` // Beginning | End
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages IndexedPageFolderView,omitempty"`
	SortOrder       *SortOrderContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SortOrder,omitempty"`
	ParentFolderIDs FolderIDsForSearch  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ParentFolderIds"`
}

// FindConversationResponse is the EWS FindConversation operation response.
type FindConversationResponse struct {
	XMLName          xml.Name                         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindConversationResponse"`
	ResponseMessages FindConversationResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// FindConversationResponseMessages wraps FindConversation response messages.
type FindConversationResponseMessages struct {
	Messages []FindConversationResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindConversationResponseMessage"`
}

// FindConversationResponseMessageType is one FindConversation response.
type FindConversationResponseMessageType struct {
	XMLName       xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string                  `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Conversations *ConversationsContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Conversations"`
}

// ConversationsContainer wraps the conversations list.
type ConversationsContainer struct {
	XMLName       xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Conversations"`
	Conversations []ConversationType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ConversationType"`
}

// ConversationType is the EWS conversation element in responses.
type ConversationType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ConversationType"`
	Topic   string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Topic"`
	// ConversationKey is the canonical ConversationId.
	ConversationKey string `xml:"http://schemas.microsoft.com/exchange/services/2006/types ConversationId"`
	// UniqueRecipients is the list of unique recipients across all messages
	// in this conversation.
	UniqueRecipients string `xml:"http://schemas.microsoft.com/exchange/services/2006/types UniqueRecipients,omitempty"`
	// GlobalUniqueRecipients for cross-mailbox conversations.
	GlobalUniqueRecipients string `xml:"http://schemas.microsoft.com/exchange/services/2006/types GlobalUniqueRecipients,omitempty"`
	// LastDeliveryTime is the DateTimeReceived of the most recent message.
	LastDeliveryTime string `xml:"http://schemas.microsoft.com/exchange/services/2006/types LastDeliveryTime,omitempty"`
	// TotalCount is the number of messages in this conversation.
	TotalCount int `xml:"http://schemas.microsoft.com/exchange/services/2006/types TotalCount,omitempty"`
	// UnreadCount is the number of unread messages in this conversation.
	UnreadCount int `xml:"http://schemas.microsoft.com/exchange/services/2006/types UnreadCount,omitempty"`
	// ItemIds are the ItemIds of messages in the conversation (latest first).
	ItemIds []ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	// EffectiveWhen last delivery time.
	EffectiveWhen string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EffectiveWhen,omitempty"`
}

// handleFindConversation processes an EWS FindConversation SOAP request.
func (s *Server) handleFindConversation(ctx context.Context, body []byte) []byte {
	var req FindConversationRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("FindConversation", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("FindConversation", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	// Resolve target folder(s) — use Inbox by default if none specified.
	var folderIDs []semcore.FolderId
	if len(req.ParentFolderIDs.Distinguished) > 0 {
		for _, d := range req.ParentFolderIDs.Distinguished {
			role, ok := DistinguishedFolderIDs[d.ID]
			if !ok {
				return s.errorResponseXML("FindConversation", ErrErrorFolderNotFound, "unknown distinguished folder: "+d.ID)
			}
			fld, err := s.identity.GetFolderByMailbox(mailboxKey, role)
			if err != nil {
				return s.errorResponseXML("FindConversation", ErrErrorFolderNotFound, err.Error())
			}
			folderIDs = append(folderIDs, fld.FolderID)
		}
	} else if len(req.ParentFolderIDs.Folder) > 0 {
		for _, f := range req.ParentFolderIDs.Folder {
			fid, err := semcore.NewFolderId(f.ID)
			if err != nil {
				return s.errorResponseXML("FindConversation", ErrErrorInvalidId, err.Error())
			}
			folderIDs = append(folderIDs, fid)
		}
	} else {
		// Default to Inbox.
		fld, err := s.identity.GetFolderByMailbox(mailboxKey, "inbox")
		if err != nil {
			return s.errorResponseXML("FindConversation", ErrErrorFolderNotFound, "no folder specified and Inbox not found")
		}
		folderIDs = append(folderIDs, fld.FolderID)
	}

	// Collect all items from target folders and group by ConversationId.
	convMap := make(map[string]*convInfo)

	for _, fid := range folderIDs {
		items, err := s.identity.ListItemIdentitiesByFolder(fid)
		if err != nil {
			continue
		}
		for _, item := range items {
			cKey := item.ConversationID.String()
			if cKey == "" {
				continue
			}
			// Skip identities whose body is absent in msgStore — the same readable
			// filter FindItem/SyncFolderItems/folderCounts apply — so a conversation's
			// item count never includes an orphaned identity the other EWS surfaces
			// hide (e.g. an identity left behind when the store drifted ahead of
			// msgStore). Reading the body up front both enforces that filter and
			// supplies the subject/date below.
			rawMsg, err := s.msgStore.ReadMessage(item.Email, item.MsgKey)
			if err != nil {
				continue
			}
			if _, exists := convMap[cKey]; !exists {
				convMap[cKey] = &convInfo{id: item.ConversationID}
			}
			convMap[cKey].items = append(convMap[cKey].items, item)

			// Subject and last delivery time come from the message body.
			subject, _, dateStr, _, _, _ := parseMimeHeaders(rawMsg)
			if convMap[cKey].subject == "" && subject != "" {
				convMap[cKey].subject = subject
			}
			if dateStr > convMap[cKey].lastTime {
				convMap[cKey].lastTime = dateStr
			}
		}
	}

	// Sort conversations by last delivery time (descending = newest first).
	var convs []*convInfo
	for _, c := range convMap {
		convs = append(convs, c)
	}
	if req.SortOrder != nil && len(req.SortOrder.Fields) > 0 {
		sortConversationResults(convs, req.SortOrder.Fields)
	} else {
		// Default sort: newest first.
		sort.SliceStable(convs, func(i, j int) bool {
			return convs[i].lastTime > convs[j].lastTime
		})
	}

	// Apply paging.
	total := len(convs)
	pageOffset := 0
	maxEntries := 0
	if req.IndexedPage.MaxEntriesReturned != "" {
		maxEntries, _ = strconv.Atoi(req.IndexedPage.MaxEntriesReturned)
	}
	if req.IndexedPage.Offset != "" {
		pageOffset, _ = strconv.Atoi(req.IndexedPage.Offset)
	}

	if maxEntries > 0 {
		if pageOffset < total {
			end := pageOffset + maxEntries
			if end > total {
				end = total
			}
			convs = convs[pageOffset:end]
		} else {
			convs = nil
		}
	}

	// Build response conversations.
	var responseConvs []ConversationType
	for _, c := range convs {
		itemIDs := make([]ItemIdType, 0, len(c.items))
		for _, item := range c.items {
			itemIDs = append(itemIDs, ItemIdType{
				ID: item.ItemID.String(),
				CK: item.ChangeKey.String(),
			})
		}
		// Use latest subject as conversation topic.
		topic := c.subject
		if topic == "" {
			topic = "(no subject)"
		}
		responseConvs = append(responseConvs, ConversationType{
			Topic:            topic,
			ConversationKey:  c.id.String(),
			LastDeliveryTime: c.lastTime,
			TotalCount:       len(c.items),
			UnreadCount:      c.unread,
			ItemIds:          itemIDs,
		})
	}

	resp := FindConversationResponse{}
	resp.ResponseMessages.Messages = []FindConversationResponseMessageType{{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Conversations: &ConversationsContainer{Conversations: responseConvs},
	}}
	return buildResponseEnvelope(resp)
}

// sortConversationResults sorts FindConversation results by the specified sort order.
func sortConversationResults(convs []*convInfo, fields []SortByField) {
	if len(convs) == 0 || len(fields) == 0 {
		return
	}
	sort.SliceStable(convs, func(i, j int) bool {
		for _, f := range fields {
			uri := f.FieldURI.URI
			ascending := f.Order != "Descending"
			var cmp int
			switch uri {
			case "conversation:LastDeliveryTime":
				cmp = strings.Compare(convs[i].lastTime, convs[j].lastTime)
			case "conversation:Topic":
				cmp = strings.Compare(convs[i].subject, convs[j].subject)
			default:
				continue
			}
			if cmp == 0 {
				continue
			}
			if !ascending {
				cmp = -cmp
			}
			return cmp < 0
		}
		return false
	})
}
