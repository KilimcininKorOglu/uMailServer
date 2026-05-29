// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file provides the top-level SOAP HTTP handler.
package ews

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/sieve"
	"github.com/umailserver/umailserver/internal/storage"
)

// Server is the EWS request handler. It receives SOAP requests, routes them
// to the appropriate operation handler, and returns SOAP responses.
type Server struct {
	identity      *semcore.BoltIdentityStore
	sync          *semcore.BoltSyncStateStore
	tombstones    *semcore.BoltTombstoneStore
	msgStore      *storage.MessageStore
	storageDB     *storage.Database
	db            *db.DB
	mutationPipe  *semcore.MutationPipeline
	subscriptions *semcore.BoltSubscriptionStore
	lifecycle     *semcore.BoltLifecycleStore
	collabStore   *semcore.BoltCollaborationStore
	policyStore   *semcore.BoltPolicyStore
	delegateStore *semcore.BoltDelegateStore
	sieveMgr      *sieve.Manager
	logger        *slog.Logger
}

// NewServer creates an EWS handler wired to the canonical semcore stores and storage.
// The collabStore provides identity and version persistence for calendar items, contacts,
// and tasks (CalendarItemId, ContactId, TaskId with their ChangeKey variants).
// The policyStore provides OOF and inbox-rule policy persistence.
// The delegateStore provides delegate grant management (AddDelegate, UpdateDelegate,
// RemoveDelegate, GetDelegate) and shared mailbox discovery.
// The sieveMgr is used to recompile the Sieve script after policy changes.
// The db parameter provides account/domain lookups for GAL directory operations.
func NewServer(identity *semcore.BoltIdentityStore, syncState *semcore.BoltSyncStateStore, tombstones *semcore.BoltTombstoneStore, msgStore *storage.MessageStore, storageDB *storage.Database, db *db.DB, mutationPipe *semcore.MutationPipeline, subscriptions *semcore.BoltSubscriptionStore, lifecycle *semcore.BoltLifecycleStore, collabStore *semcore.BoltCollaborationStore, policyStore *semcore.BoltPolicyStore, delegateStore *semcore.BoltDelegateStore, sieveMgr *sieve.Manager) *Server {
	return &Server{
		identity:      identity,
		sync:          syncState,
		tombstones:    tombstones,
		msgStore:      msgStore,
		storageDB:     storageDB,
		db:            db,
		mutationPipe:  mutationPipe,
		subscriptions: subscriptions,
		lifecycle:     lifecycle,
		collabStore:   collabStore,
		policyStore:   policyStore,
		delegateStore: delegateStore,
		sieveMgr:      sieveMgr,
		logger:        slog.Default(),
	}
}

// SetLogger sets the logger for the EWS server.
func (s *Server) SetLogger(logger *slog.Logger) {
	s.logger = logger
}

// HandleHTTP is the http.Handler entry point for /EWS/Exchange.asmx.
// ServeHTTP implements http.Handler for use with api.SetEWSHandler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.HandleHTTP(w, r)
}

// HandleHTTP is the http.Handler entry point for /EWS/Exchange.asmx.
func (s *Server) HandleHTTP(w http.ResponseWriter, r *http.Request) {
	// EWS only accepts POST.
	if r.Method != http.MethodPost {
		writeSOAPError(w, http.StatusMethodNotAllowed, ErrErrorInvalidOperation, "EWS requires POST")
		return
	}

	// Read the full SOAP body.
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 64<<20))
	if err != nil {
		writeSOAPError(w, http.StatusBadRequest, ErrErrorInternalServer, "failed to read request body")
		return
	}

	// Determine the operation name and extract the soap:Body content.
	op, soapBody := parseSOAPOperation(body)
	ctx := r.Context()

	var response []byte
	switch op {
	case "GetFolder":
		response = s.handleGetFolder(ctx, soapBody)
	case "FindFolder":
		response = s.handleFindFolder(ctx, soapBody)
	case "CreateFolder":
		response = s.handleCreateFolder(ctx, soapBody)
	case "UpdateFolder":
		response = s.handleUpdateFolder(ctx, soapBody)
	case "DeleteFolder":
		response = s.handleDeleteFolder(ctx, soapBody)
	case "SyncFolderHierarchy":
		response = s.handleSyncFolderHierarchy(ctx, soapBody)
	case "SyncFolderItems":
		response = s.handleSyncFolderItems(ctx, soapBody)
	case "FindItem":
		response = s.handleFindItem(ctx, soapBody)
	case "FindConversation":
		response = s.handleFindConversation(ctx, soapBody)
	case "CreateItem":
		response = s.handleCreateItem(ctx, soapBody)
	case "GetItem":
		response = s.handleGetItem(ctx, soapBody)
	case "UpdateItem":
		response = s.handleUpdateItem(ctx, soapBody)
	case "DeleteItem":
		response = s.handleDeleteItem(ctx, soapBody)
	case "SendItem":
		response = s.handleSendItem(ctx, soapBody)
	case "MoveItem":
		response = s.handleMoveItem(ctx, soapBody)
	case "CopyItem":
		response = s.handleCopyItem(ctx, soapBody)
	case "GetAttachment":
		response = s.handleGetAttachment(ctx, soapBody)
	case "DeleteAttachment":
		response = s.handleDeleteAttachment(ctx, soapBody)
	case "Subscribe":
		response = s.handleSubscribe(ctx, soapBody)
	case "Unsubscribe":
		response = s.handleUnsubscribe(ctx, soapBody)
	case "GetEvents":
		response = s.handleGetEvents(ctx, soapBody)
	case "CreateCalendarItem":
		response = s.handleCreateCalendarItem(ctx, soapBody)
	case "GetCalendarItem":
		response = s.handleGetCalendarItem(ctx, soapBody)
	case "UpdateCalendarItem":
		response = s.handleUpdateCalendarItem(ctx, soapBody)
	case "DeleteCalendarItem":
		response = s.handleDeleteCalendarItem(ctx, soapBody)
	case "CreateContact":
		response = s.handleCreateContact(ctx, soapBody)
	case "GetContact":
		response = s.handleGetContact(ctx, soapBody)
	case "UpdateContact":
		response = s.handleUpdateContact(ctx, soapBody)
	case "DeleteContact":
		response = s.handleDeleteContact(ctx, soapBody)
	case "CreateTask":
		response = s.handleCreateTask(ctx, soapBody)
	case "GetTask":
		response = s.handleGetTask(ctx, soapBody)
	case "UpdateTask":
		response = s.handleUpdateTask(ctx, soapBody)
	case "DeleteTask":
		response = s.handleDeleteTask(ctx, soapBody)
	case "GetUserOofSettings":
		response = s.handleGetUserOofSettings(ctx, soapBody)
	case "SetUserOofSettings":
		response = s.handleSetUserOofSettings(ctx, soapBody)
	case "GetInboxRules":
		response = s.handleGetInboxRules(ctx, soapBody)
	case "UpdateInboxRules":
		response = s.handleUpdateInboxRules(ctx, soapBody)
	case "GetDelegate":
		response = s.handleGetDelegate(ctx, soapBody)
	case "AddDelegate":
		response = s.handleAddDelegate(ctx, soapBody)
	case "UpdateDelegate":
		response = s.handleUpdateDelegate(ctx, soapBody)
	case "RemoveDelegate":
		response = s.handleRemoveDelegate(ctx, soapBody)
	case "ResolveNames":
		response = s.handleResolveNames(ctx, soapBody)
	case "ResolveNamesRequest":
		response = s.handleResolveNames(ctx, soapBody)
	case "GetUserAvailability", "GetUserAvailabilityRequest":
		response = s.handleGetUserAvailability(ctx, soapBody)
	case "GetRoomLists", "GetRoomListsRequest":
		response = s.handleGetRoomLists(ctx, soapBody)
	case "GetRooms", "GetRoomsRequest":
		response = s.handleGetRooms(ctx, soapBody)
	default:
		response = s.errorResponseXML(op, ErrErrorNotImplemented, fmt.Sprintf("operation %q not implemented", op))
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("X-AutoDiscovery", "1")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
}

// parseSOAPOperation extracts the EWS operation name and returns a synthetic SOAP envelope
// with proper namespace declarations. Handlers should unmarshal from this synthetic envelope
// using xml.Decoder.DecodeElement so that namespace prefixes (m:, t:) are properly resolved.
func parseSOAPOperation(body []byte) (string, []byte) {
	// Extract soap:Body content using simple string manipulation.
	// Handle both full SOAP envelopes and bare EWS operation XML.
	bodyStart := bytes.Index(body, []byte("<soap:Body>"))
	bodyEnd := bytes.Index(body, []byte("</soap:Body>"))
	var bodyContent []byte
	if bodyStart != -1 && bodyEnd != -1 {
		// Full SOAP envelope: extract body content.
		bodyContent = bytes.TrimSpace(body[bodyStart+len("<soap:Body>") : bodyEnd])
	} else {
		// Bare EWS message (no SOAP envelope): use the entire body.
		bodyContent = bytes.TrimSpace(body)
	}

	// Rewrite EWS message elements: add m: prefix to EWS message elements that don't already
	// have an m: or t: prefix or an explicit xmlns declaration. The synthetic envelope uses
	// xmlns="messages_NS" as the default namespace so that:
	// - Elements with m: prefix get messages_NS (from synthetic xmlns:m)
	// - Elements with t: prefix get types_NS (from synthetic xmlns:t)
	// - Elements with explicit xmlns="..." keep their declared namespace
	// - Bare elements (no prefix, no xmlns) inherit the default = messages_NS
	// This allows Go's xml decoder to correctly resolve namespace URLs from struct tags.
	bodyContent = rewriteEWSMessagePrefix(bodyContent)

	// Wrap bodyContent in a synthetic envelope with proper namespace declarations.
	// Use xmlns="messages_NS" as the DEFAULT namespace so bare elements inherit it.
	// Also declare xmlns:m and xmlns:t for prefixed elements.
	syntheticEnv := []byte(`<?xml version="1.0" encoding="utf-8"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns="` + EWSMessagesNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `"><soap:Body>`)
	syntheticEnv = append(syntheticEnv, bodyContent...)
	syntheticEnv = append(syntheticEnv, []byte(`</soap:Body></soap:Envelope>`)...)

	decoder := xml.NewDecoder(bytes.NewReader(syntheticEnv))
	for {
		tok, err := decoder.Token()
		if err != nil {
			return "", syntheticEnv
		}
		if tok == nil {
			return "", syntheticEnv
		}
		switch v := tok.(type) {
		case xml.StartElement:
			name := v.Name.Local
			// Skip SOAP envelope wrappers.
			if name == "Envelope" || name == "Header" || name == "Body" {
				continue
			}
			return name, syntheticEnv
		}
	}
}

// decodeRequest unmarshals an EWS request from a synthetic SOAP envelope.
// It uses xml.Decoder with DecodeElement to properly resolve namespace prefixes (m:, t:).
func decodeRequest(syntheticEnv []byte, req interface{}) error {
	decoder := xml.NewDecoder(bytes.NewReader(syntheticEnv))
	for {
		tok, err := decoder.Token()
		if err != nil {
			return err
		}
		if tok == nil {
			return fmt.Errorf("no element found in request")
		}
		switch elem := tok.(type) {
		case xml.StartElement:
			// Skip SOAP envelope wrappers.
			if elem.Name.Local == "Envelope" || elem.Name.Local == "Header" || elem.Name.Local == "Body" {
				continue
			}
			return decoder.DecodeElement(req, &elem)
		}
	}
}

// rewriteEWSMessagePrefix adds the m: prefix to bare EWS message elements (those without m: or t: prefix
// and without an explicit xmlns declaration). This is needed because Go's xml decoder requires elements
// to have a namespace (either via prefix or explicit xmlns declaration) for struct tag matching with
// namespace URLs. The synthetic envelope uses xmlns="messages_NS" as the default namespace so that:
// - Elements with m: prefix get messages_NS (from synthetic xmlns:m)
// - Elements with t: prefix get types_NS (from synthetic xmlns:t)
// - Elements with explicit xmlns="..." keep their declared namespace
// - Bare elements (no prefix, no xmlns) inherit the default = messages_NS
func rewriteEWSMessagePrefix(data []byte) []byte {
	// EWS message element names that may need m: prefix (in EWSMessagesNS).
	msgElements := []string{
		// Top-level operations
		"GetFolder", "FindFolder", "CreateFolder", "UpdateFolder", "DeleteFolder",
		"SyncFolderHierarchy", "SyncFolderItems", "GetItem", "UpdateItem", "DeleteItem",
		"CreateItem", "SendItem", "MoveItem", "CopyItem", "MarkAllItemsAsRead",
		// Child elements in EWSMessagesNS
		"FolderShape", "FolderIds", "ParentFolderIds", "DistinguishedFolderId",
		"SyncState", "Folders", "RootFolder", "Changes", "Create", "Update", "Delete",
		"ResponseMessages", "Items", "ItemId", "ParentItemId", "OldItemId", "NewItemId",
		"AdditionalProperties", "BaseShape", "DeleteType", "MoveType",
		"AffectedTaskOccurrences", "MarkAsRead", "SaveItemToFolder", "SavedItemId",
		"ReturnNewItemIds", "ReturnedItems", "CreatedItems", "UpdatedItems",
		"DeletedItems", "ReadFlagChange", "ItemChanges",
		"SyncFolderHierarchyResponse", "SyncFolderItemsResponse",
		"DistinguishedFolderName", "DisplayName",
		// Item operation elements
		"GetItem", "UpdateItem", "DeleteItem", "CreateItem", "SendItem", "MoveItem", "CopyItem",
		"GetAttachment", "DeleteAttachment", "ItemIds", "ItemShape", "ToFolderId",
		"AttachmentIds", "AttachmentId", "FileAttachment", "Mailbox",
		// Subscription elements
		"Subscribe", "Unsubscribe", "GetEvents",
		"PullSubscriptionRequest", "SubscriptionId", "Watermark",
		"NotificationEvent", "SubscribeResponse", "UnsubscribeResponse",
		"GetEventsResponse", "SubscribeResponseMessage", "UnsubscribeResponseMessage",
		"GetEventsResponseMessage",
		// Calendar item operation elements
		"CreateCalendarItem", "GetCalendarItem", "UpdateCalendarItem", "DeleteCalendarItem",
		"CalendarItem", "CalendarItemId", "Recurrence", "CalendarFolder",
		"RecurrenceId", "ModifiedOccurrence", "DeletedOccurrence",
		// Contact operation elements
		"CreateContact", "GetContact", "UpdateContact", "DeleteContact",
		"Contact", "ContactId", "ContactsFolder",
		// Task operation elements
		"CreateTask", "GetTask", "UpdateTask", "DeleteTask",
		"Task", "TaskId", "TasksFolder",
		// Delegate operation elements
		"GetDelegate", "AddDelegate", "UpdateDelegate", "RemoveDelegate",
		"DelegateUser", "DelegateUsers", "DelegatePermissions",
		"DeliverMeetingRequests", "DelegateUserResponseMessageType",
		"GetDelegateResponse", "AddDelegateResponse", "UpdateDelegateResponse",
		"RemoveDelegateResponse", "GetDelegateResponseMessage",
		"AddDelegateResponseMessage", "UpdateDelegateResponseMessage",
		"RemoveDelegateResponseMessage",
		"DelegateFolderPermissionLevel",
	}

	// Build a map for fast lookup.
	msgSet := make(map[string]bool)
	for _, e := range msgElements {
		msgSet[e] = true
	}

	// Track which elements were rewritten (opened with m: prefix) for closing tag matching.
	type rewrittenElem struct {
		name string
	}
	var rewritten []rewrittenElem

	// Process the data byte-by-byte.
	result := make([]byte, 0, len(data)*2)
	i := 0
	for i < len(data) {
		if data[i] != '<' {
			result = append(result, data[i])
			i++
			continue
		}

		// Check for closing tag </Name> or </m:Name> or </t:Name>.
		if i+1 < len(data) && data[i+1] == '/' {
			// Find the end of the closing tag.
			j := i + 2
			// Skip prefix if present.
			hasPrefix := false
			if j+1 < len(data) && data[j] == 'm' && data[j+1] == ':' {
				hasPrefix = true
				j += 2
			} else if j+1 < len(data) && data[j] == 't' && data[j+1] == ':' {
				hasPrefix = true
				j += 2
			}
			end := bytes.Index(data[j:], []byte{'>'})
			if end == -1 {
				result = append(result, data[i:]...)
				break
			}
			name := string(data[j : j+end])
			// Only add m: prefix if this element was rewritten (opening tag had m: prefix added).
			// Find if this name was rewritten.
			wasRewritten := false
			for k := len(rewritten) - 1; k >= 0; k-- {
				if rewritten[k].name == name {
					wasRewritten = true
					break
				}
			}
			if !hasPrefix && wasRewritten && msgSet[name] {
				result = append(result, []byte("</m:"+name+">")...)
				// Remove from rewritten stack.
				for k := len(rewritten) - 1; k >= 0; k-- {
					if rewritten[k].name == name {
						rewritten = rewritten[:k]
						break
					}
				}
			} else {
				result = append(result, data[i:j+end+1]...)
			}
			i = j + end + 1
			continue
		}

		// Find the end of the opening tag.
		tagStart := i + 1
		selfClose := false
		tagEnd := -1
		for j := tagStart; j < len(data); j++ {
			if data[j] == '>' {
				tagEnd = j
				break
			}
			if data[j] == '/' && j+1 < len(data) && data[j+1] == '>' {
				selfClose = true
				tagEnd = j
				break
			}
		}
		if tagEnd == -1 {
			result = append(result, data[i:]...)
			break
		}

		tag := data[tagStart:tagEnd]
		// Extract element name (before any whitespace or '/').
		nameEnd := 0
		for nameEnd < len(tag) && tag[nameEnd] != ' ' && tag[nameEnd] != '\t' && tag[nameEnd] != '/' {
			nameEnd++
		}
		elemName := string(tag[:nameEnd])

		// Check if this is an EWS message element that needs m: prefix.
		if msgSet[elemName] {
			// Check for existing prefix (m: or t:) or xmlns declaration.
			hasMPrefix := bytes.HasPrefix(tag, []byte("m:"))
			hasTPrefix := bytes.HasPrefix(tag, []byte("t:"))
			hasXmlns := bytes.Contains(tag, []byte(`xmlns`))

			if !hasMPrefix && !hasTPrefix && !hasXmlns {
				// Bare element without namespace - add m: prefix.
				result = append(result, '<', 'm', ':')
				result = append(result, tag...)
				if selfClose {
					result = append(result, '/', '>')
				} else {
					result = append(result, '>')
					rewritten = append(rewritten, rewrittenElem{name: elemName})
				}
			} else {
				// Already has namespace (prefix or xmlns) - copy as-is.
				result = append(result, '<')
				result = append(result, tag...)
				if selfClose {
					result = append(result, '/', '>')
				} else {
					result = append(result, '>')
				}
			}
		} else {
			// Not an EWS message element - copy as-is.
			result = append(result, '<')
			result = append(result, tag...)
			if selfClose {
				result = append(result, '/', '>')
			} else {
				result = append(result, '>')
			}
		}

		i = tagEnd + 1
		if selfClose {
			i++
		}
	}

	return result
}

// writeSOAPError writes a SOAP fault with the given HTTP status and EWS error code.
func writeSOAPError(w http.ResponseWriter, status int, code ErrorCode, message string) {
	env := SOAPEnvelope{
		XmlnsSOAP: SOAPEnvelopeNS,
	}
	detail := struct {
		XMLName xml.Name `xml:"detail"`
		Value   struct {
			XMLName      xml.Name  `xml:"ResponseCode"`
			ErrorCode    ErrorCode `xml:"ErrorCode"`
			ErrorMessage string    `xml:"ErrorMessage"`
		} `xml:"ResponseCode"`
	}{
		Value: struct {
			XMLName      xml.Name  `xml:"ResponseCode"`
			ErrorCode    ErrorCode `xml:"ErrorCode"`
			ErrorMessage string    `xml:"ErrorMessage"`
		}{
			XMLName:      xml.Name{Local: "ResponseCode"},
			ErrorCode:    code,
			ErrorMessage: message,
		},
	}
	env.Body = map[string]interface{}{
		"faultcode":   fmt.Sprintf("soap:Server:%s", code),
		"faultstring": message,
		"detail":      detail,
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(env)
}

// buildResponseEnvelope wraps an EWS response struct in a SOAP envelope.
// It uses string concatenation to ensure m:, t: prefixes are preserved correctly.
func buildResponseEnvelope(response interface{}) []byte {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	buf.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	buf.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv)
	buf.Write(svBytes)
	buf.WriteString(`</soap:Header>`)
	buf.WriteString(`<soap:Body>`)
	respBytes, _ := xml.Marshal(response)
	buf.Write(respBytes)
	buf.WriteString(`</soap:Body>`)
	buf.WriteString(`</soap:Envelope>`)
	return buf.Bytes()
}

// (s *Server) errorResponseXML builds an EWS SOAP error response for the given operation.
func (s *Server) errorResponseXML(op string, code ErrorCode, message string) []byte {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	buf.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	buf.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv)
	buf.Write(svBytes)
	buf.WriteString(`</soap:Header>`)
	buf.WriteString(`<soap:Body>`)
	buf.WriteString(`<m:` + op + `ResponseMessage ResponseClass="Error">`)
	buf.WriteString(`<m:ResponseCode>` + string(code) + `</m:ResponseCode>`)
	buf.WriteString(`<m:ErrorMessage>` + message + `</m:ErrorMessage>`)
	buf.WriteString(`</m:` + op + `ResponseMessage>`)
	buf.WriteString(`</soap:Body>`)
	buf.WriteString(`</soap:Envelope>`)

	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// Delegate permission enforcement helpers
// ---------------------------------------------------------------------------

// checkDelegatePermission verifies whether the authenticated acting user
// (actorEmail) has sufficient permission to perform the requested action on
// the target mailbox (ownerID). It returns an error message and ErrorCode when
// access is denied, or "" when access is granted.
//
// The permission check is scoped to the specific action:
//   - "read"  — at least Reviewer on the target folder
//   - "write" — at least Author on the target folder
//   - "delete" — Author or Delegate permission on the target folder
//
// When no delegate grant exists for actorEmail on ownerID, the function
// returns ErrErrorAccessDenied, satisfying VAL-DIR-002.
func (s *Server) checkDelegatePermission(ownerID semcore.MailboxId, ownerEmail, actorEmail, action string) (string, ErrorCode) {
	if s.delegateStore == nil {
		return "", ""
	}

	if actorEmail == ownerEmail {
		return "", ""
	}

	delegate, err := s.delegateStore.GetDelegateForUser(ownerID, actorEmail)
	if err != nil {
		return "no delegate permission for " + actorEmail + " on " + ownerEmail, ErrErrorAccessDenied
	}

	switch action {
	case "read":
		if !delegate.CanActAsDelegate() {
			return "delegate " + actorEmail + " has no read access on " + ownerEmail, ErrErrorAccessDenied
		}
	case "write":
		if !delegate.Permissions.CanWriteInbox() {
			return "delegate " + actorEmail + " has no write access on " + ownerEmail, ErrErrorAccessDenied
		}
	case "write_calendar":
		if !delegate.Permissions.CanWriteCalendar() {
			return "delegate " + actorEmail + " has no calendar write access on " + ownerEmail, ErrErrorAccessDenied
		}
	case "delete":
		if !delegate.Permissions.CanWriteInbox() && !delegate.Permissions.CanWriteCalendar() {
			return "delegate " + actorEmail + " has no delete access on " + ownerEmail, ErrErrorAccessDenied
		}
	}

	return "", ""
}

// checkSendAsPermission verifies whether the authenticated acting user
// (actorEmail) has send-as permission on the target mailbox (ownerID).
// VAL-DIR-004: send-as is NOT implied by general mailbox access.
// It requires an explicit CanSendAs grant on the delegate-user record.
func (s *Server) checkSendAsPermission(ownerID semcore.MailboxId, ownerEmail, actorEmail string) (string, ErrorCode) {
	if s.delegateStore == nil {
		// No delegate store: only the owner can send-as themselves.
		if actorEmail == ownerEmail {
			return "", ""
		}
		return "send-as requires explicit authorization", ErrErrorSendDenied
	}

	if actorEmail == ownerEmail {
		return "", ""
	}

	delegate, err := s.delegateStore.GetDelegateForUser(ownerID, actorEmail)
	if err != nil {
		return "send-as requires explicit authorization for " + actorEmail + " on " + ownerEmail, ErrErrorSendDenied
	}

	if !delegate.CanSendAs {
		return "send-as requires explicit authorization for " + actorEmail + " on " + ownerEmail, ErrErrorSendDenied
	}

	return "", ""
}

// checkSendOnBehalfPermission verifies whether the authenticated acting user
// (actorEmail) has send-on-behalf permission on the target mailbox (ownerID).
// VAL-DIR-005: send-on-behalf preserves represented identity distinctly
// from send-as. It requires an explicit CanSendOnBehalf grant on the
// delegate-user record. General mailbox access does NOT imply this right.
func (s *Server) checkSendOnBehalfPermission(ownerID semcore.MailboxId, ownerEmail, actorEmail string) (string, ErrorCode) {
	if s.delegateStore == nil {
		// No delegate store: only the owner can send on behalf of themselves.
		if actorEmail == ownerEmail {
			return "", ""
		}
		return "send-on-behalf requires explicit authorization", ErrErrorSendDenied
	}

	if actorEmail == ownerEmail {
		return "", ""
	}

	delegate, err := s.delegateStore.GetDelegateForUser(ownerID, actorEmail)
	if err != nil {
		return "send-on-behalf requires explicit authorization for " + actorEmail + " on " + ownerEmail, ErrErrorSendDenied
	}

	if !delegate.CanSendOnBehalf {
		return "send-on-behalf requires explicit authorization for " + actorEmail + " on " + ownerEmail, ErrErrorSendDenied
	}

	return "", ""
}

// getActingEmail extracts the authenticated user's email from the request context.
func (s *Server) getActingEmail(ctx context.Context) string {
	if email, ok := ctx.Value("X-Email").(string); ok && email != "" {
		return email
	}
	return "unknown"
}

// buildDelegateAuditContext constructs a DelegateAuditContext from the current
// actor and the mailbox owner. It returns nil when the actor is the mailbox owner
// (no delegation in play) or when the delegate store is unavailable.
func (s *Server) buildDelegateAuditContext(ctx context.Context, ownerID semcore.MailboxId, ownerEmail string) *semcore.DelegateAuditContext {
	actorEmail := s.getActingEmail(ctx)
	if actorEmail == ownerEmail || actorEmail == "unknown" {
		return nil
	}
	if s.delegateStore == nil {
		return nil
	}
	delegate, err := s.delegateStore.GetDelegateForUser(ownerID, actorEmail)
	if err != nil {
		return nil
	}
	audit := semcore.NewDelegateAuditContext(ownerID, ownerEmail, actorEmail, delegate.ID)
	return &audit
}
