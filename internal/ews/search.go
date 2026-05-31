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
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Restriction"`
	// SearchFilter is embedded anonymously so the decoder matches the
	// Restriction's direct child (IsEqualTo, Contains, And, ...) against the
	// promoted filter fields. A `,any`-tagged named field instead matched the
	// filter's children one level too deep, leaving every field nil.
	SearchFilter
}

// SearchFilter is a disjunction (OR) or conjunction (AND) of search conditions.
// Only one of the fields is populated at a time based on the XML element name.
type SearchFilter struct {
	And       *SearchFilter     `xml:"http://schemas.microsoft.com/exchange/services/2006/types And"`
	Or        *SearchFilter     `xml:"http://schemas.microsoft.com/exchange/services/2006/types Or"`
	Not       *SearchFilter     `xml:"http://schemas.microsoft.com/exchange/services/2006/types Not"`
	IsEqualTo *ComparisonFilter `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsEqualTo"`
	Contains  *ContainsFilter   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contains"`
	Exists    *ExistsFilter     `xml:"http://schemas.microsoft.com/exchange/services/2006/types Exists"`
	// Relational comparisons.
	IsGreaterThan          *ComparisonFilter `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsGreaterThan"`
	IsLessThan             *ComparisonFilter `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsLessThan"`
	IsGreaterThanOrEqualTo *ComparisonFilter `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsGreaterThanOrEqualTo"`
	IsLessThanOrEqualTo    *ComparisonFilter `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsLessThanOrEqualTo"`
}

// ContainsFilter represents the EWS Contains element. Per the EWS schema (and the
// Exchange reference implementation, the MS-OXWSCDATA schema the contains-match builder/the constant loader),
// the field is a <t:FieldURI FieldURI="..."/> element and the constant is a
// <t:Constant Value="..."/> element with the value carried as an attribute.
type ContainsFilter struct {
	FieldURI         *FieldURI         `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
	Constant         ContainsConstType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Constant"`
	ContainmentMode  string            `xml:"ContainmentMode,attr"`
	ContainmentCompr string            `xml:"ContainmentComparison,attr"`
}

// ContainsConstType is the Constant value in a Contains filter (Value attribute).
type ContainsConstType struct {
	Value string `xml:"Value,attr"`
}

// ExistsFilter represents the EWS Exists element.
type ExistsFilter struct {
	Path FieldURI `xml:"http://schemas.microsoft.com/exchange/services/2006/types Path"`
}

// ComparisonFilter is a common comparison type (IsEqualTo, IsGreaterThan, etc.).
// EWS XML: <t:IsEqualTo><t:FieldURI FieldURI="..."/><t:FieldURIOrConstant><t:Constant Value="..."/></t:FieldURIOrConstant></t:IsEqualTo>
type ComparisonFilter struct {
	FieldURI           *FieldURI           `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
	FieldURIOrConstant *FieldURIOrConstant `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURIOrConstant"`
}

// FieldURIOrConstant represents either a field URI or a constant value.
// The Constant's Value is an XML ATTRIBUTE (<t:Constant Value="..."/>), not a
// child element; tagging it as an element left it permanently empty and made
// IsEqualTo comparisons always fail.
type FieldURIOrConstant struct {
	FieldURI *FieldURI `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
	Constant *struct {
		Value string `xml:"Value,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Constant"`
}

// SortOrderContainer wraps field orders for ordering.
type SortOrderContainer struct {
	XMLName xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SortOrder"`
	Fields  []SortByField `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldOrder"`
}

// SortByField is one sort field with optional direction.
// EWS XML: <t:FieldOrder Order="Ascending"><t:FieldURI FieldURI="item:Subject"/></t:FieldOrder>
// The Order is an attribute of FieldOrder; the property is a nested FieldURI element.
type SortByField struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldOrder"`
	Order    string   `xml:"Order,attr"` // Ascending | Descending
	FieldURI struct {
		URI string `xml:"FieldURI,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
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
	XMLName         xml.Name                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Items"`
	Items           []MessageTypeResponse    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	MeetingRequests []MeetingRequestResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types MeetingRequest"`
}

// splitSearchItems separates meeting-request items (rendered as MeetingRequest)
// from ordinary messages so each is emitted under the correct element name.
func splitSearchItems(all []MessageTypeResponse) SearchItemsContainer {
	c := SearchItemsContainer{}
	for _, it := range all {
		if it.isMeetingRequest {
			c.MeetingRequests = append(c.MeetingRequests, toMeetingRequestResponse(it))
		} else {
			c.Items = append(c.Items, it)
		}
	}
	return c
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
			Items:              splitSearchItems(allItems),
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

	// collabMatches applies the evaluable parts of a FindItem restriction to a
	// collaboration item. Predicates we can evaluate (Subject) filter the item;
	// predicates over fields not materialized for collab items (e.g. Categories)
	// are treated as matching so they neither over- nor under-select.
	collabMatches := func(item *MessageTypeResponse) bool {
		if item == nil {
			return false
		}
		if restriction == nil {
			return true
		}
		return collabRestrictionMatch(restriction.SearchFilter, item.Subject)
	}

	if isCollabFolder {
		// Query collaboration store items.
		// Calendar items.
		calItems, err := s.collabStore.ListCalendarItemsByFolder(folderID)
		if err == nil {
			for _, rec := range calItems {
				item := s.collabCalendarItemToResponse(rec, folderID)
				if collabMatches(item) {
					results = append(results, *item)
				}
			}
		}
		// Contact items.
		contactItems, err := s.collabStore.ListContactsByFolder(folderID)
		if err == nil {
			for _, rec := range contactItems {
				item := s.collabContactItemToResponse(rec, folderID)
				if collabMatches(item) {
					results = append(results, *item)
				}
			}
		}
		// Task items.
		taskItems, err := s.collabStore.ListTasksByFolder(folderID)
		if err == nil {
			for _, rec := range taskItems {
				item := s.collabTaskItemToResponse(rec, folderID)
				if collabMatches(item) {
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
				ParentFolderID:   FolderIdComponents{ID: folderID.String()},
				Subject:          subject,
				DateTimeReceived: dateStr,
				Size:             len(rawMsg),
				Body: BodyTypeResponse{
					BodyType: bodyType,
					Text:     truncateBody(bodyText, 100),
				},
				From:         mailboxFromHeader(from),
				Sender:       mailboxFromHeader(rawHeaderValue(rawMsg, "Sender")),
				ToRecipients: recipientsWrap(toRecipients),
				CcRecipients: recipientsWrap(recipientsFromHeader(rawMsg, "Cc")),
			}
			if !rec.ConversationID.IsZero() {
				msgResp.ConversationID = &ConversationIdType{ID: rec.ConversationID.String()}
			}
			hdrs := parseInternetHeaders(rawMsg)
			if len(hdrs) > 0 {
				msgResp.InternetHeaders = &InternetMessageHeadersType{Headers: hdrs}
			}
			// Surface delivered meeting requests as MeetingRequest items so
			// clients expose accept/decline on them.
			for _, h := range hdrs {
				if strings.EqualFold(h.Name, hdrMeeting) && strings.TrimSpace(h.Value) == "1" {
					msgResp.isMeetingRequest = true
					break
				}
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
	if c.FieldURI == nil || c.FieldURIOrConstant == nil || c.FieldURIOrConstant.Constant == nil {
		return false
	}
	uri := c.FieldURI.URI
	constVal := c.FieldURIOrConstant.Constant.Value
	if uri == "" || constVal == "" {
		return false
	}

	// Map field URI to comparison value.
	var fieldValue string
	var fieldInt int64

	switch uri {
	case "message:From":
		fieldValue = fields.From
	case "item:Subject", "message:Subject":
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
	if c.FieldURI == nil || c.FieldURI.URI == "" || c.Constant.Value == "" {
		return false
	}
	// Match on the property suffix so both item:* and message:* prefixes work
	// (exchangelib sends item:Subject for the Item-level Subject field).
	uri := c.FieldURI.URI
	if idx := strings.IndexByte(uri, ':'); idx >= 0 {
		uri = uri[idx+1:]
	}
	constVal := c.Constant.Value

	switch uri {
	case "Subject":
		return strings.Contains(strings.ToLower(subject), strings.ToLower(constVal))
	case "From":
		return strings.Contains(strings.ToLower(fields.From), strings.ToLower(constVal))
	default:
		return false
	}
}

// collabRestrictionMatch evaluates a FindItem restriction against a
// collaboration item using only the Subject field, which is the one property
// reliably materialized for calendar/contact/task items. Predicates referencing
// any other field are treated as matching (lenient), so a Subject filter
// narrows the result set while a Categories filter does not wrongly exclude
// items whose categories are not surfaced here.
func collabRestrictionMatch(f SearchFilter, subject string) bool {
	if f.And != nil {
		return collabRestrictionMatch(*f.And, subject)
	}
	if f.Or != nil {
		return collabRestrictionMatch(*f.Or, subject)
	}
	if f.Not != nil {
		return !collabRestrictionMatch(*f.Not, subject)
	}
	if f.IsEqualTo != nil {
		if collabFieldIsSubject(f.IsEqualTo.FieldURI) && f.IsEqualTo.FieldURIOrConstant != nil && f.IsEqualTo.FieldURIOrConstant.Constant != nil {
			return strings.EqualFold(subject, f.IsEqualTo.FieldURIOrConstant.Constant.Value)
		}
		return true // non-subject predicate: not evaluable here, treat as match
	}
	if f.Contains != nil {
		if collabFieldIsSubject(f.Contains.FieldURI) {
			return strings.Contains(strings.ToLower(subject), strings.ToLower(f.Contains.Constant.Value))
		}
		return true
	}
	// AndList / OrList style containers and any other predicate: lenient match.
	return true
}

// collabFieldIsSubject reports whether a field URI addresses the Subject field
// (either the item: or message: prefix).
func collabFieldIsSubject(fu *FieldURI) bool {
	if fu == nil || fu.URI == "" {
		return false
	}
	uri := fu.URI
	if idx := strings.IndexByte(uri, ':'); idx >= 0 {
		uri = uri[idx+1:]
	}
	return uri == "Subject"
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
			ascending := f.Order != "Descending"

			// Match on the property suffix so both item:* and message:* prefixes
			// work. exchangelib sends item:Subject (Subject is an Item-level field),
			// not message:Subject.
			uri := f.FieldURI.URI
			if idx := strings.IndexByte(uri, ':'); idx >= 0 {
				uri = uri[idx+1:]
			}

			var cmp int
			switch uri {
			case "DateTimeReceived":
				cmp = strings.Compare(items[i].DateTimeReceived, items[j].DateTimeReceived)
			case "Subject":
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
	// Surface the real meeting subject (iCal SUMMARY) so subject restrictions
	// match; fall back to the UID only when no SUMMARY was stored.
	subject := extractDirProp(rec.RawData, "SUMMARY")
	if subject == "" {
		subject = rec.IcalUID
	}
	return &MessageTypeResponse{
		ItemID: ItemIdType{
			ID: rec.ID.String(),
			CK: rec.ChangeKey.String(),
		},
		ParentFolderID: FolderIdComponents{ID: folderID.String()},
		Subject:        subject,
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

// ---------------------------------------------------------------------------
// FindPeople
// ---------------------------------------------------------------------------

// FindPeopleRequest is the EWS FindPeople operation request. exchangelib's
// folder.people() sends a single ParentFolderId plus an optional Restriction.
type FindPeopleRequest struct {
	XMLName      xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindPeople"`
	Restriction  *RestrictionContainer `xml:"Restriction,omitempty"`
	ParentFolder struct {
		Distinguished *struct {
			ID string `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
		Folder *struct {
			ID string `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ParentFolderId"`
}

// restrictionConstant extracts the constant value from an IsEqualTo or Contains
// restriction, used to filter personas by display name. Returns "" if absent.
func restrictionConstant(r *RestrictionContainer) string {
	if r == nil {
		return ""
	}
	f := r.SearchFilter
	if f.IsEqualTo != nil && f.IsEqualTo.FieldURIOrConstant != nil && f.IsEqualTo.FieldURIOrConstant.Constant != nil {
		return f.IsEqualTo.FieldURIOrConstant.Constant.Value
	}
	if f.Contains != nil {
		return f.Contains.Constant.Value
	}
	return ""
}

// handleFindPeople implements the EWS FindPeople operation by projecting the
// contacts of the target folder into Persona results.
func (s *Server) handleFindPeople(ctx context.Context, body []byte) []byte {
	var req FindPeopleRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("FindPeople", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}
	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("FindPeople", errCode, "could not resolve mailbox")
	}
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	var folderID semcore.FolderId
	if req.ParentFolder.Distinguished != nil {
		role, ok := DistinguishedFolderIDs[req.ParentFolder.Distinguished.ID]
		if ok {
			if fld, err := s.identity.GetFolderByMailbox(mailboxKey, role); err == nil {
				folderID = fld.FolderID
			}
		}
	} else if req.ParentFolder.Folder != nil {
		folderID, _ = semcore.NewFolderId(req.ParentFolder.Folder.ID) //nolint:errcheck
	}

	nameFilter := restrictionConstant(req.Restriction)

	var personas strings.Builder
	count := 0
	if s.collabStore != nil && !folderID.IsZero() {
		contacts, err := s.collabStore.ListContactsByFolder(folderID)
		if err == nil {
			for _, c := range contacts {
				dn := extractDirProp(c.RawData, "FN")
				if nameFilter != "" && !strings.EqualFold(dn, nameFilter) &&
					!strings.Contains(strings.ToLower(dn), strings.ToLower(nameFilter)) {
					continue
				}
				personas.WriteString(`<t:Persona>`)
				personas.WriteString(`<t:PersonaId Id="` + xmlEsc(c.ID.String()) + `" ChangeKey="` + xmlEsc(c.ChangeKey.String()) + `"/>`)
				if dn != "" {
					personas.WriteString(`<t:DisplayName>` + xmlEsc(dn) + `</t:DisplayName>`)
				}
				personas.WriteString(`</t:Persona>`)
				count++
			}
		}
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:FindPeopleResponse><m:ResponseMessages><m:FindPeopleResponseMessage ResponseClass="Success">`)
	b.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
	b.WriteString(`<m:People>` + personas.String() + `</m:People>`)
	b.WriteString(`<m:TotalNumberOfPeopleInView>` + strconv.Itoa(count) + `</m:TotalNumberOfPeopleInView>`)
	b.WriteString(`<m:FirstMatchingRowIndex>0</m:FirstMatchingRowIndex>`)
	b.WriteString(`<m:FirstLoadedRowIndex>0</m:FirstLoadedRowIndex>`)
	b.WriteString(`</m:FindPeopleResponseMessage></m:ResponseMessages></m:FindPeopleResponse>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}

// GetPersonaRequest is the EWS GetPersona operation request (one PersonaId).
type GetPersonaRequest struct {
	XMLName   xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetPersona"`
	PersonaID struct {
		ID string `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types PersonaId"`
}

// handleGetPersona implements the EWS GetPersona operation, returning the full
// persona for a contact. exchangelib calls this for each persona returned by
// FindPeople. The Persona element is emitted in the messages namespace, which
// is what exchangelib's GetPersona container lookup expects.
func (s *Server) handleGetPersona(ctx context.Context, body []byte) []byte {
	var req GetPersonaRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetPersona", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}
	_, _, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("GetPersona", errCode, "could not resolve mailbox")
	}

	var personaBody string
	if s.collabStore != nil {
		if ctID, err := semcore.NewContactId(req.PersonaID.ID); err == nil {
			if rec, err := s.collabStore.GetContactByID(ctID); err == nil {
				dn := extractDirProp(rec.RawData, "FN")
				var pb strings.Builder
				pb.WriteString(`<t:PersonaId Id="` + xmlEsc(rec.ID.String()) + `" ChangeKey="` + xmlEsc(rec.ChangeKey.String()) + `"/>`)
				if dn != "" {
					pb.WriteString(`<t:DisplayName>` + xmlEsc(dn) + `</t:DisplayName>`)
				}
				if email := extractDirProp(rec.RawData, "EMAIL"); email != "" {
					pb.WriteString(`<t:EmailAddress><t:EmailAddress>` + xmlEsc(email) + `</t:EmailAddress></t:EmailAddress>`)
				}
				personaBody = pb.String()
			}
		}
	}
	if personaBody == "" {
		return s.errorResponseXML("GetPersona", ErrErrorItemNotFound, "persona not found")
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:GetPersonaResponse><m:ResponseMessages><m:GetPersonaResponseMessage ResponseClass="Success">`)
	b.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
	b.WriteString(`<m:Persona>` + personaBody + `</m:Persona>`)
	b.WriteString(`</m:GetPersonaResponseMessage></m:ResponseMessages></m:GetPersonaResponse>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}
