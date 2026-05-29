// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements FindItem with field-based search
// restrictions, ordering, and paging — the core interactive mailbox browse
// surface for EWS clients.
package ews

import (
	"context"
	"encoding/xml"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// FindItem
// ---------------------------------------------------------------------------

// FindItemRequest is the EWS FindItem operation request.
type FindItemRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindItem"`
	// ItemShape defines which properties to return.
	ItemShape ItemShapeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemShape"`
	// IndexedPageFolderView: optional paging.
	IndexedPage struct {
		MaxEntriesReturned string `xml:"MaxEntriesReturned,attr"`
		Offset             string `xml:"Offset,attr"`
		BasePoint          string `xml:"BasePoint,attr"` // Beginning | End
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages IndexedPageFolderView,omitempty"`
	// FractionalPageFolderView: optional fractional paging.
	FractionalPage struct {
		Numerator   string `xml:"Numerator,attr"`
		Denominator string `xml:"Denominator,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FractionalPageFolderView,omitempty"`
	// Restrictions: optional field-based filter.
	Restrictions *RestrictionContainer `xml:"Restriction,omitempty"`
	// SortOrder: optional result ordering.
	SortOrder *SortOrderContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SortOrder,omitempty"`
	// ParentFolderIds: which folders to search.
	ParentFolderIDs FolderIDsForSearch `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ParentFolderIds"`
}

// FolderIDsForSearch mirrors FolderIDsType but for search targets.
type FolderIDsForSearch struct {
	XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ParentFolderIds"`
	Distinguished []struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
		ID      string   `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	Folder []struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		ID      string   `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// RestrictionContainer wraps the t:Restriction element.
type RestrictionContainer struct {
	XMLName      xml.Name     `xml:"Restriction"`
	SearchFilter SearchFilter `xml:",any"` // supported filter types
}

// SearchFilter is a disjunction (OR) or conjunction (AND) of search conditions.
// Only one of the fields is populated at a time based on the XML element name.
type SearchFilter struct {
	And       *SearchFilter     `xml:"And"`
	Or        *SearchFilter     `xml:"Or"`
	Not       *SearchFilter     `xml:"Not"`
	IsEqualTo *ComparisonFilter `xml:"IsEqualTo"`
	Contains  *ContainsFilter   `xml:"Contains"`
	Exists    *ExistsFilter     `xml:"Exists"`
	// Relational comparisons.
	IsGreaterThan          *ComparisonFilter `xml:"IsGreaterThan"`
	IsLessThan             *ComparisonFilter `xml:"IsLessThan"`
	IsGreaterThanOrEqualTo *ComparisonFilter `xml:"IsGreaterThanOrEqualTo"`
	IsLessThanOrEqualTo    *ComparisonFilter `xml:"IsLessThanOrEqualTo"`
}

// ContainsFilter represents the EWS Contains element.
type ContainsFilter struct {
	Path      ContainsPathType  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Path"`
	Constant  ContainsConstType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Constant"`
	Traversal string            `xml:"Traversal,attr"` //string, item, shallow, deep
}

// ContainsPathType is the Path in a Contains filter: a Path element with a uri attribute.
type ContainsPathType struct {
	URI string `xml:"uri,attr"`
}

// ContainsConstType is the Constant value in a Contains filter.
type ContainsConstType struct {
	Value string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Value"`
}

// ExistsFilter represents the EWS Exists element.
type ExistsFilter struct {
	Path FieldURI `xml:"http://schemas.microsoft.com/exchange/services/2006/types Path"`
}

// ComparisonFilter is a common comparison type (IsEqualTo, IsGreaterThan, etc.).
type ComparisonFilter struct {
	Path     ComparisonPathType  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Path"`
	Constant ComparisonConstType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Constant"`
}

// ComparisonPathType is the Path in a comparison filter: a FieldURI with uri attribute.
type ComparisonPathType struct {
	URI string `xml:"uri,attr"`
}

// ComparisonConstType is the Constant value in a comparison filter.
type ComparisonConstType struct {
	Value string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Value"`
}

// FieldURIOrConstant represents either a field URI or a constant value.
type FieldURIOrConstant struct {
	FieldURI *FieldURI `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
	Constant *struct {
		Value string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Value"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Constant"`
}

// SortOrderContainer wraps field URIs for ordering.
type SortOrderContainer struct {
	XMLName xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SortOrder"`
	Fields  []SortByField `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
}

// SortByField is one sort field with optional direction.
type SortByField struct {
	FieldURI struct {
		URI string `xml:"uri,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
	Order string `xml:"Order,attr"` // Ascending | Descending
}

// FindItemResponse is the EWS FindItem operation response.
type FindItemResponse struct {
	XMLName          xml.Name                 `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindItemResponse"`
	ResponseMessages FindItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// FindItemResponseMessages wraps FindItem response messages.
type FindItemResponseMessages struct {
	Messages []FindItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindItemResponseMessage"`
}

// FindItemResponseMessageType is one FindItem response.
type FindItemResponseMessageType struct {
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	RootFolder    *RootFolderType  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RootFolder"`
}

type SearchItemsContainer struct {
	XMLName xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/types Items"`
	Items   []MessageTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
}

// RootFolderType wraps the paged result set.
type RootFolderType struct {
	XMLName             xml.Name             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RootFolder"`
	TotalItems          int                  `xml:"TotalItemsInView,attr"`
	TotalItemsResponse  int                  `xml:"TotalItemsInResponse,attr"`
	IncludesLastItem    bool                 `xml:"IncludesLastItemInRange,attr"`
	IndexedPagingOffset string               `xml:"IndexedPagingOffset,attr,omitempty"`
	Items               SearchItemsContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/types Items"`
}

// handleFindItem processes an EWS FindItem SOAP request.
func (s *Server) handleFindItem(ctx context.Context, body []byte) []byte {
	var req FindItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("FindItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("FindItem", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	// Resolve target folder(s).
	var folderIDs []semcore.FolderId
	if len(req.ParentFolderIDs.Distinguished) > 0 {
		for _, d := range req.ParentFolderIDs.Distinguished {
			role, ok := DistinguishedFolderIDs[d.ID]
			if !ok {
				return s.errorResponseXML("FindItem", ErrErrorFolderNotFound, "unknown distinguished folder: "+d.ID)
			}
			fld, err := s.identity.GetFolderByMailbox(mailboxKey, role)
			if err != nil {
				return s.errorResponseXML("FindItem", ErrErrorFolderNotFound, err.Error())
			}
			folderIDs = append(folderIDs, fld.FolderID)
		}
	} else if len(req.ParentFolderIDs.Folder) > 0 {
		for _, f := range req.ParentFolderIDs.Folder {
			fid, err := semcore.NewFolderId(f.ID)
			if err != nil {
				return s.errorResponseXML("FindItem", ErrErrorInvalidId, err.Error())
			}
			folderIDs = append(folderIDs, fid)
		}
	}
	if len(folderIDs) == 0 {
		return s.errorResponseXML("FindItem", ErrErrorFolderNotFound, "no target folder specified")
	}

	// Collect all items from all target folders.
	var allItems []MessageTypeResponse
	for _, fid := range folderIDs {
		items, err := s.collectFolderItems(mailboxKey, fid, req.Restrictions)
		if err != nil {
			return s.errorResponseXML("FindItem", ErrErrorInternalServer, err.Error())
		}
		allItems = append(allItems, items...)
	}

	// Apply sorting if specified.
	if req.SortOrder != nil && len(req.SortOrder.Fields) > 0 {
		sortFindItemResults(allItems, req.SortOrder.Fields)
	}

	// Apply paging.
	total := len(allItems)
	pageOffset := 0
	maxEntries := 0
	includesLast := true

	if req.IndexedPage.MaxEntriesReturned != "" {
		maxEntries, _ = strconv.Atoi(req.IndexedPage.MaxEntriesReturned)
	}
	if req.IndexedPage.Offset != "" {
		pageOffset, _ = strconv.Atoi(req.IndexedPage.Offset)
	}

	if maxEntries > 0 {
		if pageOffset >= total {
			allItems = nil
		} else {
			end := pageOffset + maxEntries
			if end > total {
				end = total
			}
			allItems = allItems[pageOffset:end]
			includesLast = (pageOffset+maxEntries >= total)
		}
	}

	resp := FindItemResponse{}
	resp.ResponseMessages.Messages = []FindItemResponseMessageType{{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		RootFolder: &RootFolderType{
			TotalItems:         total,
			TotalItemsResponse: total,
			IncludesLastItem:   includesLast,
			Items:              SearchItemsContainer{Items: allItems},
		},
	}}
	if maxEntries > 0 && !includesLast {
		nextOffset := pageOffset + len(allItems)
		resp.ResponseMessages.Messages[0].RootFolder.IndexedPagingOffset = strconv.Itoa(nextOffset)
	}
	return buildResponseEnvelope(resp)
}

// collectFolderItems retrieves items from one folder, optionally filtered.
// For mail folders (inbox, drafts, sent, etc.) it queries the identity store + msgStore.
// For collaboration folders (calendar, contacts, tasks) it queries the collab store.
func (s *Server) collectFolderItems(mailboxKey string, folderID semcore.FolderId, restriction *RestrictionContainer) ([]MessageTypeResponse, error) {
	// Check if this is a collaboration folder (calendar, contacts, tasks).
	// Collaboration items are stored in the collab store, not the identity store.
	// We detect the folder type by looking at the role stored on the folder record.
	folderRec, err := s.identity.GetFolderByID(folderID)
	isCollabFolder := false
	if err == nil && folderRec != nil {
		role := folderRec.Role
		isCollabFolder = role == "calendar" || role == "contacts" || role == "tasks"
	}

	var results []MessageTypeResponse

	if isCollabFolder {
		// Query collaboration store items.
		// Calendar items.
		calItems, err := s.collabStore.ListCalendarItemsByFolder(folderID)
		if err == nil {
			for _, rec := range calItems {
				item := s.collabCalendarItemToResponse(rec, folderID)
				if item != nil {
					results = append(results, *item)
				}
			}
		}
		// Contact items.
		contactItems, err := s.collabStore.ListContactsByFolder(folderID)
		if err == nil {
			for _, rec := range contactItems {
				item := s.collabContactItemToResponse(rec, folderID)
				if item != nil {
					results = append(results, *item)
				}
			}
		}
		// Task items.
		taskItems, err := s.collabStore.ListTasksByFolder(folderID)
		if err == nil {
			for _, rec := range taskItems {
				item := s.collabTaskItemToResponse(rec, folderID)
				if item != nil {
					results = append(results, *item)
				}
			}
		}
	} else {
		// Standard mail folder: query identity store + msgStore.
		items, err := s.identity.ListItemIdentitiesByFolder(folderID)
		if err != nil {
			return nil, err
		}

		for _, rec := range items {
			// Retrieve raw MIME content.
			rawMsg, err := s.msgStore.ReadMessage(rec.Email, rec.MsgKey)
			if err != nil {
				continue
			}

			subject, _, dateStr, bodyType, bodyText, toAddrs := parseMimeHeaders(rawMsg)

			toRecipients := make([]MailboxTypeResponse, 0, len(toAddrs))
			for _, addr := range toAddrs {
				toRecipients = append(toRecipients, MailboxTypeResponse{EmailAddress: addr})
			}

			msgResp := MessageTypeResponse{
				ItemID: ItemIdType{
					ID: rec.ItemID.String(),
					CK: rec.ChangeKey.String(),
				},
				ParentFolderID:   FolderIdComponents{ID: folderID.String()},
				Subject:          subject,
				DateTimeReceived: dateStr,
				Size:             len(rawMsg),
				Body: BodyTypeResponse{
					BodyType: bodyType,
					Text:     truncateBody(bodyText, 100),
				},
				ToRecipients: toRecipients,
			}

			// Apply restriction filter if present.
			if restriction != nil {
				headers := parseMimeHeadersForFilter(rawMsg)
				if !evaluateRestriction(restriction, headers, subject, dateStr, len(rawMsg) > 0) {
					continue
				}
			}

			results = append(results, msgResp)
		}
	}

	return results, nil
}

// parseMimeHeadersForFilter extracts fields needed for restriction evaluation.
func parseMimeHeadersForFilter(data []byte) filterFields {
	fields := filterFields{}
	if len(data) == 0 {
		return fields
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return fields
	}
	h := msg.Header
	fields.From = h.Get("From")
	fields.Subject = h.Get("Subject")
	fields.Date = h.Get("Date")
	fields.HasAttachment = strings.Contains(h.Get("Content-Type"), "multipart")
	return fields
}

// filterFields holds the parsed headers needed for restriction evaluation.
type filterFields struct {
	From          string
	Subject       string
	Date          string
	HasAttachment bool
	// body content checked separately
}

// evaluateRestriction returns true if the item matches the restriction tree.
func evaluateRestriction(r *RestrictionContainer, fields filterFields, subject, dateStr string, hasContent bool) bool {
	if r == nil {
		return true
	}
	return evalFilter(r.SearchFilter, fields, subject, dateStr, hasContent)
}

// evalFilter evaluates a search filter recursively.
func evalFilter(f SearchFilter, fields filterFields, subject, dateStr string, hasContent bool) bool {
	if f.And != nil {
		return evalFilter(*f.And, fields, subject, dateStr, hasContent)
	}
	if f.Or != nil {
		return evalFilter(*f.Or, fields, subject, dateStr, hasContent)
	}
	if f.Not != nil {
		return !evalFilter(*f.Not, fields, subject, dateStr, hasContent)
	}
	if f.IsEqualTo != nil {
		return evalComparison(*f.IsEqualTo, fields, subject, dateStr, hasContent, "equal")
	}
	if f.IsGreaterThan != nil {
		return evalComparison(*f.IsGreaterThan, fields, subject, dateStr, hasContent, "gt")
	}
	if f.IsLessThan != nil {
		return evalComparison(*f.IsLessThan, fields, subject, dateStr, hasContent, "lt")
	}
	if f.IsGreaterThanOrEqualTo != nil {
		return evalComparison(*f.IsGreaterThanOrEqualTo, fields, subject, dateStr, hasContent, "gte")
	}
	if f.IsLessThanOrEqualTo != nil {
		return evalComparison(*f.IsLessThanOrEqualTo, fields, subject, dateStr, hasContent, "lte")
	}
	if f.Contains != nil {
		return evalContains(*f.Contains, fields, subject, hasContent)
	}
	if f.Exists != nil {
		return evalExists(*f.Exists, fields)
	}
	return true
}

// evalComparison evaluates a comparison filter (IsEqualTo, IsGreaterThan, etc.).
func evalComparison(c ComparisonFilter, fields filterFields, subject, dateStr string, hasContent bool, op string) bool {
	if c.Path.URI == "" || c.Constant.Value == "" {
		return false
	}
	uri := c.Path.URI
	constVal := c.Constant.Value

	// Map field URI to comparison value.
	var fieldValue string
	var fieldInt int64

	switch uri {
	case "message:From":
		fieldValue = fields.From
	case "message:Subject":
		fieldValue = subject
	case "message:DateTimeReceived":
		fieldValue = dateStr
		// Parse for comparison.
		if t, parseErr := time.Parse(time.RFC1123Z, dateStr); parseErr == nil {
			fieldInt = t.Unix()
		}
		if constVal != "" {
			if t, parseErr := time.Parse(time.RFC1123Z, constVal); parseErr == nil {
				constInt := t.Unix()
				return compareInt(fieldInt, constInt, op)
			}
		}
	default:
		return false
	}

	switch op {
	case "equal":
		return strings.EqualFold(fieldValue, constVal)
	case "gt":
		return fieldValue > constVal
	case "lt":
		return fieldValue < constVal
	case "gte":
		return fieldValue >= constVal
	case "lte":
		return fieldValue <= constVal
	}
	return false
}

// compareInt compares two int64 values.
func compareInt(a, b int64, op string) bool {
	switch op {
	case "gt":
		return a > b
	case "lt":
		return a < b
	case "gte":
		return a >= b
	case "lte":
		return a <= b
	}
	return false
}

// evalContains evaluates a Contains filter.
func evalContains(c ContainsFilter, fields filterFields, subject string, hasContent bool) bool {
	if c.Path.URI == "" || c.Constant.Value == "" {
		return false
	}
	uri := c.Path.URI
	constVal := c.Constant.Value

	switch uri {
	case "message:Subject":
		return strings.Contains(strings.ToLower(subject), strings.ToLower(constVal))
	case "message:From":
		return strings.Contains(strings.ToLower(fields.From), strings.ToLower(constVal))
	default:
		return false
	}
}

// evalExists evaluates an Exists filter.
func evalExists(e ExistsFilter, fields filterFields) bool {
	if e.Path.URI == "" {
		return false
	}
	uri := e.Path.URI
	switch uri {
	case "message:From":
		return fields.From != ""
	case "message:Subject":
		return fields.Subject != ""
	default:
		return false
	}
}

// sortFindItemResults sorts FindItem results by the specified sort order.
func sortFindItemResults(items []MessageTypeResponse, fields []SortByField) {
	if len(items) == 0 || len(fields) == 0 {
		return
	}

	sort.SliceStable(items, func(i, j int) bool {
		for _, f := range fields {
			uri := f.FieldURI.URI
			ascending := f.Order != "Descending"

			var cmp int
			switch uri {
			case "message:DateTimeReceived":
				cmp = strings.Compare(items[i].DateTimeReceived, items[j].DateTimeReceived)
			case "message:Subject":
				cmp = strings.Compare(items[i].Subject, items[j].Subject)
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

// collabCalendarItemToResponse converts a StoredCalendarItemIdentity to a MessageTypeResponse.
// Returns nil if conversion fails.
func (s *Server) collabCalendarItemToResponse(rec semcore.StoredCalendarItemIdentity, folderID semcore.FolderId) *MessageTypeResponse {
	// For calendar items, we don't have MIME blobs in the msgStore.
	// Return a minimal response with the identity info.
	return &MessageTypeResponse{
		ItemID: ItemIdType{
			ID: rec.ID.String(),
			CK: rec.ChangeKey.String(),
		},
		ParentFolderID: FolderIdComponents{ID: folderID.String()},
		Subject:        rec.IcalUID, // UID as subject placeholder
	}
}

// collabContactItemToResponse converts a StoredContactIdentity to a MessageTypeResponse.
// Returns nil if conversion fails.
func (s *Server) collabContactItemToResponse(rec semcore.StoredContactIdentity, folderID semcore.FolderId) *MessageTypeResponse {
	return &MessageTypeResponse{
		ItemID: ItemIdType{
			ID: rec.ID.String(),
			CK: rec.ChangeKey.String(),
		},
		ParentFolderID: FolderIdComponents{ID: folderID.String()},
		Subject:        rec.IcalUID, // UID as subject placeholder
	}
}

// collabTaskItemToResponse converts a StoredTaskIdentity to a MessageTypeResponse.
// Returns nil if conversion fails.
func (s *Server) collabTaskItemToResponse(rec semcore.StoredTaskIdentity, folderID semcore.FolderId) *MessageTypeResponse {
	return &MessageTypeResponse{
		ItemID: ItemIdType{
			ID: rec.ID.String(),
			CK: rec.ChangeKey.String(),
		},
		ParentFolderID: FolderIdComponents{ID: folderID.String()},
		Subject:        rec.IcalUID, // UID as subject placeholder
	}
}

// truncateBody returns a truncated body preview.
func truncateBody(body string, maxLen int) string {
	if len(body) <= maxLen {
		return body
	}
	return body[:maxLen] + "..."
}
