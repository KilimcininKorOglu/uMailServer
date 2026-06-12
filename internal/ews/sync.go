// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements SyncFolderItems — the incremental item
// sync surface for EWS clients. It surfaces real mailbox mutations as item
// deltas and manages durable sync-state continuation tokens.
package ews

import (
	"context"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// SyncFolderItems
// ---------------------------------------------------------------------------

// SyncFolderItemsRequest is the EWS SyncFolderItems operation request.
type SyncFolderItemsRequest struct {
	XMLName            xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderItems"`
	SyncState          string        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncState,omitempty"`
	SyncScope          string        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncScope,attr,omitempty"` // NormalItems | NormalAndAssociatedItems
	ItemShape          ItemShapeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemShape"`
	MaxChangesReturned string        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MaxChangesReturned,attr,omitempty"`
	// FolderId: the root folder to sync under.
	SyncFolderId struct {
		DistinguishedFolderID *DistinguishedIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
		FolderID              *FolderIdAttrType    `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderId"`
}

// DistinguishedIdType is a distinguished folder ID with an Id attribute.
type DistinguishedIdType struct {
	ID string `xml:"Id,attr"`
}

// FolderIdAttrType is a folder ID with an Id attribute.
type FolderIdAttrType struct {
	ID string `xml:"Id,attr"`
}

// SyncFolderItemsResponse is the EWS SyncFolderItems operation response.
type SyncFolderItemsResponse struct {
	XMLName          xml.Name                        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderItemsResponse"`
	ResponseMessages SyncFolderItemsResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// SyncFolderItemsResponseMessages wraps SyncFolderItems response messages.
type SyncFolderItemsResponseMessages struct {
	Messages []SyncFolderItemsResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderItemsResponseMessage"`
}

// SyncFolderItemsResponseMessageType is one SyncFolderItems response. Per the
// EWS schema the body is SyncState, then IncludesLastItemInRange (a child
// element, not an attribute), then the <Changes> container — Outlook reads
// IncludesLastItemInRange to know the sync is complete and walks <Changes> (not
// <Items>) for the item deltas, so both the element shape and the container name
// are load-bearing.
type SyncFolderItemsResponseMessageType struct {
	XMLName       xml.Name                  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderItemsResponseMessage"`
	ResponseClass string                    `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	SyncState     string                    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncState"`
	IncludesLast  bool                      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages IncludesLastItemInRange"`
	Changes       *SyncFolderItemsContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Changes"`
}

// SyncFolderItemsContainer wraps sync item changes under <Changes>.
type SyncFolderItemsContainer struct {
	XMLName   xml.Name                 `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Changes"`
	Creates   []SyncFolderItemCreate   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Create"`
	Updates   []SyncFolderItemUpdate   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Update"`
	Deletes   []SyncFolderItemDelete   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
	ReadFlags []SyncFolderItemReadFlag `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReadFlagChange"`
}

// SyncFolderItemCreate wraps a created item in sync response.
type SyncFolderItemCreate struct {
	XMLName xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Create"`
	Item    MessageTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
}

// SyncFolderItemUpdate wraps an updated item in sync response.
type SyncFolderItemUpdate struct {
	XMLName xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Update"`
	Item    MessageTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
}

// SyncFolderItemDelete wraps a deleted item in sync response.
type SyncFolderItemDelete struct {
	XMLName xml.Name   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
	ItemID  ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
}

// SyncFolderItemReadFlag wraps a read flag change in sync response.
type SyncFolderItemReadFlag struct {
	XMLName xml.Name   `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReadFlagChange"`
	ItemID  ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	IsRead  bool       `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsRead,attr"`
}

// handleSyncFolderItems processes an EWS SyncFolderItems SOAP request.
func (s *Server) handleSyncFolderItems(ctx context.Context, body []byte) []byte {
	var req SyncFolderItemsRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("SyncFolderItems", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("SyncFolderItems", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorResponseXML("SyncFolderItems", ErrErrorMailboxNotFound, err.Error())
	}

	// Resolve target folder.
	var folderID semcore.FolderId
	if req.SyncFolderId.DistinguishedFolderID != nil {
		role, ok := DistinguishedFolderIDs[req.SyncFolderId.DistinguishedFolderID.ID]
		if !ok {
			return s.errorResponseXML("SyncFolderItems", ErrErrorFolderNotFound, "unknown distinguished folder: "+req.SyncFolderId.DistinguishedFolderID.ID)
		}
		fld, err := s.identity.GetFolderByMailbox(mailboxKey, role)
		if err != nil {
			return s.errorResponseXML("SyncFolderItems", ErrErrorFolderNotFound, err.Error())
		}
		folderID = fld.FolderID
	} else if req.SyncFolderId.FolderID != nil && req.SyncFolderId.FolderID.ID != "" {
		var err error
		folderID, err = semcore.NewFolderId(req.SyncFolderId.FolderID.ID)
		if err != nil {
			return s.errorResponseXML("SyncFolderItems", ErrErrorInvalidId, err.Error())
		}
	}

	if folderID.IsZero() {
		return s.errorResponseXML("SyncFolderItems", ErrErrorFolderNotFound, "no target folder specified")
	}

	// Build client ID for sync state.
	clientID := "ews:" + mailboxKey + ":" + folderID.String()

	// Parse existing sync state.
	// Format: "v<version>:<folderID>:<lastTime>:<cursor>" where cursor is the last
	// ItemId paged to the client. The cursor walks the ItemId-sorted folder so each
	// call returns the NEXT page of items instead of re-sending the same batch.
	var syncVersion uint64
	var lastSyncTime int64
	var cursor string
	if req.SyncState != "" {
		parts := strings.Split(req.SyncState, ":")
		if len(parts) >= 2 {
			if strings.HasPrefix(parts[0], "v") {
				syncVersion, _ = strconv.ParseUint(strings.TrimPrefix(parts[0], "v"), 10, 64)
			}
			if len(parts) >= 3 {
				lastSyncTime, _ = strconv.ParseInt(parts[2], 10, 64)
			}
			if len(parts) >= 4 {
				cursor = parts[3]
			}
		}
	}

	// Collect all items in the folder.
	items, err := s.identity.ListItemIdentitiesByFolder(folderID)
	if err != nil {
		return s.errorResponseXML("SyncFolderItems", ErrErrorInternalServer, err.Error())
	}

	// Sort by ItemID string for stable ordering.
	sort.Slice(items, func(i, j int) bool {
		return items[i].ItemID.String() < items[j].ItemID.String()
	})

	// Limit results.
	maxChanges := 100
	if req.MaxChangesReturned != "" {
		if mc, err := strconv.Atoi(req.MaxChangesReturned); err == nil && mc > 0 {
			maxChanges = mc
		}
	}

	// Page through the ItemId-sorted folder from the cursor: every item past the
	// cursor is new to the client, so each page is a batch of Creates and the
	// cursor advances to the last item paged. A page cut short by maxChanges means
	// more remain (IncludesLastItemInRange=false) and the client resumes from the
	// new cursor; a page that runs to the end completes the range.
	var creates []SyncFolderItemCreate
	var deletes []SyncFolderItemDelete
	var readFlags []SyncFolderItemReadFlag
	lastID := cursor
	hitLimit := false

	for _, rec := range items {
		id := rec.ItemID.String()
		if cursor != "" && id <= cursor {
			continue // already paged to the client
		}
		if len(creates) >= maxChanges {
			hitLimit = true
			break
		}

		// Retrieve raw MIME.
		rawMsg, err := s.msgStore.ReadMessage(rec.Email, rec.MsgKey)
		if err != nil {
			continue
		}

		subject, from, dateStr, bodyType, bodyText, toAddrs := parseMimeHeaders(rawMsg)

		toRecipients := make([]MailboxTypeResponse, 0, len(toAddrs))
		for _, addr := range toAddrs {
			toRecipients = append(toRecipients, MailboxTypeResponse{EmailAddress: addr})
		}

		msgResp := MessageTypeResponse{
			ItemID: ItemIdType{
				ID: rec.ItemID.String(),
				CK: rec.ChangeKey.String(),
			},
			ParentFolderID: FolderIdComponents{ID: folderID.String()},
			ItemClass:      "IPM.Note",
			Subject:        subject,
			Body: BodyTypeResponse{
				BodyType: bodyType,
				Text:     truncateBody(bodyText, 100),
			},
			DateTimeReceived: dateStr,
			Size:             len(rawMsg),
			Sender:           mailboxFromHeader(rawHeaderValue(rawMsg, "Sender")),
			ToRecipients:     recipientsWrap(toRecipients),
			CcRecipients:     recipientsWrap(recipientsFromHeader(rawMsg, "Cc")),
			From:             mailboxFromHeader(from),
			IsRead:           rec.IsRead,
		}

		creates = append(creates, SyncFolderItemCreate{Item: msgResp})
		lastID = id
	}

	// Collect tombstone deletions since last sync.
	if req.SyncState != "" && lastSyncTime > 0 {
		tombs, err := s.tombstones.ListTombstonesSince(mboxID, folderID, timeFromUnix(lastSyncTime))
		if err == nil {
			for _, t := range tombs {
				if t.IsItemLevel() && len(deletes) < maxChanges {
					deletes = append(deletes, SyncFolderItemDelete{
						ItemID: ItemIdType{ID: t.ItemID.String()},
					})
				}
			}
		}
	}

	// Advance sync state, carrying the new cursor so the next call resumes paging.
	newSyncState := fmt.Sprintf("v%d:%s:%d:%s", syncVersion+1, folderID.String(), time.Now().Unix(), lastID)
	_ = s.sync.PutSyncState(mboxID, folderID, clientID, newSyncState)

	// The range is complete once a page runs to the end rather than being cut
	// short by maxChanges.
	includesLast := !hitLimit

	var container *SyncFolderItemsContainer
	if len(creates)+len(deletes)+len(readFlags) > 0 {
		container = &SyncFolderItemsContainer{
			Creates:   creates,
			Deletes:   deletes,
			ReadFlags: readFlags,
		}
	}

	resp := SyncFolderItemsResponse{}
	resp.ResponseMessages.Messages = []SyncFolderItemsResponseMessageType{{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		SyncState:     newSyncState,
		IncludesLast:  includesLast,
		Changes:       container,
	}}
	return buildResponseEnvelope(resp)
}

// timeFromUnix converts a Unix timestamp (int64) to time.Time.
func timeFromUnix(ts int64) time.Time {
	if ts == 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}
