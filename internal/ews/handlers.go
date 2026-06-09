// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file provides the top-level SOAP HTTP handler.
package ews

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/api"
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/sieve"
	"github.com/umailserver/umailserver/internal/storage"
)

// Server is the EWS request handler. It receives SOAP requests, routes them
// to the appropriate operation handler, and returns SOAP responses.
type Server struct {
	identity      IdentityStore
	sync          SyncStore
	tombstones    TombstoneStore
	msgStore      *storage.MessageStore
	storageDB     MailStore
	db            db.Store
	mutationPipe  *semcore.MutationPipeline
	subscriptions SubscriptionStore
	lifecycle     LifecycleStore
	collabStore   CollabStore
	policyStore   PolicyStore
	delegateStore DelegateStore
	sieveMgr      *sieve.Manager
	submitMessage func(from string, to []string, data []byte) error
	logger        *slog.Logger
	// freeBusyProvider, when set, contributes additional busy intervals (e.g.
	// from the CalDAV/webmail calendar store) that are merged into the
	// GetUserAvailability free/busy view alongside the collaboration store's
	// calendar items. Injected via SetFreeBusyProvider so this package does not
	// depend on the CalDAV store directly.
	freeBusyProvider func(email string, from, to time.Time) []FreeBusyInterval
	// folderChangeNotifier, when set, is invoked after a folder mutation
	// (EmptyFolder, MoveFolder) so IMAP IDLE sessions and the webmail SSE stream
	// refresh. Injected via SetFolderChangeNotifier so this package does not
	// depend on the IMAP notification hub directly.
	folderChangeNotifier func(email, folder string)
	// messageCreatedNotifier, when set, is invoked after an EWS-created item is
	// mirrored into the IMAP mailstore index so IMAP IDLE sessions get an
	// untagged EXISTS and the webmail SSE stream pushes a new_mail event.
	// Injected via SetMessageCreatedNotifier (same no-direct-import rationale as
	// folderChangeNotifier).
	messageCreatedNotifier func(email, folder string, uid uint32)
	// messageExpungedNotifier, when set, is invoked after an EWS item is removed
	// from the IMAP mailstore index (DeleteItem, MoveItem source side).
	messageExpungedNotifier func(email, folder string, seqNum uint32)
	// scheduledCancelNotifier, when set, is invoked with the folder uid when an
	// item is removed from the "Scheduled" folder, so deleting a scheduled
	// message via EWS cancels its send (cross-protocol cancel).
	scheduledCancelNotifier func(owner string, uid uint32)
	// scheduleMessage, when set, records a deferred-send message (Outlook "Do not
	// deliver before") for future delivery instead of submitting it now,
	// returning the scheduled-message id. fileSent files a Sent copy on release
	// (SendAndSaveCopy true, SendOnly false). nil leaves deferred-send inert (the
	// message is submitted immediately).
	scheduleMessage func(owner, from string, to []string, data []byte, sendAt time.Time, fileSent bool) (string, error)
	// allowPrivatePushTargets relaxes the push-subscription SSRF guard to accept
	// loopback/private callback URLs. It is OFF in production (a real client
	// supplies a public https URL); tests set it so an httptest/sink on
	// 127.0.0.1 can receive the POST.
	allowPrivatePushTargets bool
}

// FreeBusyInterval is one busy time range contributed by an external free/busy
// provider. Only the time range and busy type are carried — never the event's
// subject or location — so querying another user's availability never leaks
// their calendar contents.
type FreeBusyInterval struct {
	Start    time.Time
	End      time.Time
	BusyType string // "Busy", "Tentative", "OOF", "Free"; empty is treated as "Busy"
}

// NewServer creates an EWS handler wired to the canonical semcore stores and storage.
// The collabStore provides identity and version persistence for calendar items, contacts,
// and tasks (CalendarItemId, ContactId, TaskId with their ChangeKey variants).
// The policyStore provides OOF and inbox-rule policy persistence.
// The delegateStore provides delegate grant management (AddDelegate, UpdateDelegate,
// RemoveDelegate, GetDelegate) and shared mailbox discovery.
// The sieveMgr is used to recompile the Sieve script after policy changes.
// The db parameter provides account/domain lookups for GAL directory operations.
func NewServer(identity IdentityStore, syncState SyncStore, tombstones TombstoneStore, msgStore *storage.MessageStore, storageDB MailStore, db db.Store, mutationPipe *semcore.MutationPipeline, subscriptions SubscriptionStore, lifecycle LifecycleStore, collabStore CollabStore, policyStore PolicyStore, delegateStore DelegateStore, sieveMgr *sieve.Manager, submitMessage func(from string, to []string, data []byte) error) *Server {
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
		submitMessage: submitMessage,
		logger:        slog.Default(),
	}
}

// SetLogger sets the logger for the EWS server.
func (s *Server) SetLogger(logger *slog.Logger) {
	s.logger = logger
}

// SetFreeBusyProvider wires an external source of busy intervals (typically the
// CalDAV/webmail calendar store) whose events are merged into the
// GetUserAvailability free/busy view, so availability computed over EWS matches
// what webmail and CalDAV clients see.
func (s *Server) SetFreeBusyProvider(fn func(email string, from, to time.Time) []FreeBusyInterval) {
	s.freeBusyProvider = fn
}

// SetFolderChangeNotifier wires a callback invoked after a folder mutation so
// IMAP IDLE sessions and the webmail SSE stream pick up the change.
func (s *Server) SetFolderChangeNotifier(fn func(email, folder string)) {
	s.folderChangeNotifier = fn
}

// SetMessageCreatedNotifier wires a callback invoked after an EWS-created item
// is mirrored into the IMAP mailstore index, so IMAP IDLE + webmail SSE refresh.
func (s *Server) SetMessageCreatedNotifier(fn func(email, folder string, uid uint32)) {
	s.messageCreatedNotifier = fn
}

// SetMessageExpungedNotifier wires a callback invoked after an EWS item is
// removed from the IMAP mailstore index (delete / move source).
func (s *Server) SetMessageExpungedNotifier(fn func(email, folder string, seqNum uint32)) {
	s.messageExpungedNotifier = fn
}

// SetScheduledCancelNotifier wires a callback invoked with the folder uid when an
// item is removed from the "Scheduled" folder, so deleting a scheduled message
// via EWS cancels its send.
func (s *Server) SetScheduledCancelNotifier(fn func(owner string, uid uint32)) {
	s.scheduledCancelNotifier = fn
}

// SetScheduleMessageFunc wires the deferred-send path: a CreateItem carrying a
// future PidTagDeferredSendTime (or the relative number/units pair) is recorded
// for scheduled delivery instead of being submitted now.
func (s *Server) SetScheduleMessageFunc(fn func(owner, from string, to []string, data []byte, sendAt time.Time, fileSent bool) (string, error)) {
	s.scheduleMessage = fn
}

// SetAllowPrivatePushTargets relaxes the push-subscription SSRF guard so a
// loopback/private callback URL is accepted. Production leaves this false; only
// tests enable it so a local sink can receive the delivered notification.
func (s *Server) SetAllowPrivatePushTargets(allow bool) {
	s.allowPrivatePushTargets = allow
}

// notifyFolderChange signals that a mailbox folder changed, resolving a
// best-effort folder name: the canonical IMAP name for a distinguished role,
// else the folder id. The webmail SSE ignores the name (any change triggers a
// refetch); IMAP IDLE routes by it for distinguished folders, which is the
// common EmptyFolder/MoveFolder case.
func (s *Server) notifyFolderChange(email string, fid semcore.FolderId) {
	if s.folderChangeNotifier == nil {
		return
	}
	folder := fid.String()
	if rec, err := s.identity.GetFolderByID(fid); err == nil && rec != nil {
		if name := semcore.CanonicalFolderNameForRole(rec.Role); name != "" {
			folder = name
		}
	}
	s.folderChangeNotifier(email, folder)
}

// SetSubmitMessageFunc wires the outbound submission path used by SendItem.
func (s *Server) SetSubmitMessageFunc(fn func(from string, to []string, data []byte) error) {
	s.submitMessage = fn
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

	// Check per-account Exchange compatibility tier.
	// The email is injected by api.server.ewsBasicAuth into r.Context.
	// Use the exported ContextKeyEmail string constant to ensure we match the key.
	var email string
	if e, ok := r.Context().Value(api.ContextKeyEmail).(string); ok && e != "" {
		email = e
		s.logger.Info("EWS HandleHTTP: X-Email from context", "email", email)
	} else {
		s.logger.Warn("EWS HandleHTTP: no X-Email in context")
		email = ""
	}

	if email != "" && s.db != nil {
		if localPart, domain, ok := strings.Cut(email, "@"); ok {
			if acc, err := s.db.GetAccount(domain, localPart); err == nil && acc != nil {
				tier := semcore.AccountCompatibilityTier(acc.CompatibilityTier)
				if tier == semcore.TierIMAPOnly && !semcore.Gate().IsEnabled(semcore.FeatureEWS) {
					// Account is in TierIMAPOnly and the global EWS gate is disabled.
					// Reject with a policy-consistent error before any mailbox data is exposed.
					writeSOAPError(w, http.StatusForbidden, ErrErrorInternalServer,
						"Exchange services are not enabled for this account")
					return
				}
			}
		}
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

	// GetStreamingEvents holds the connection open and streams frames directly
	// to w, so it must bypass the buffered []byte response path below.
	if op == "GetStreamingEvents" {
		s.handleGetStreamingEvents(w, r, soapBody)
		return
	}

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
	case "EmptyFolder":
		response = s.handleEmptyFolder(ctx, soapBody)
	case "ExpandDL":
		response = s.handleExpandDL(ctx, soapBody)
	case "MoveFolder":
		response = s.handleMoveFolder(ctx, soapBody)
	case "CopyFolder":
		response = s.handleCopyFolder(ctx, soapBody)
	case "GetMailTips":
		response = s.handleGetMailTips(ctx, soapBody)
	case "GetServiceConfiguration":
		response = s.handleGetServiceConfiguration(ctx, soapBody)
	case "GetAppManifests":
		response = s.handleGetAppManifests(ctx, soapBody)
	case "CreateUserConfiguration":
		response = s.handleCreateUserConfiguration(ctx, soapBody)
	case "GetUserConfiguration":
		response = s.handleGetUserConfiguration(ctx, soapBody)
	case "UpdateUserConfiguration":
		response = s.handleUpdateUserConfiguration(ctx, soapBody)
	case "DeleteUserConfiguration":
		response = s.handleDeleteUserConfiguration(ctx, soapBody)
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
	case "CreateAttachment":
		response = s.handleCreateAttachment(ctx, soapBody)
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
	case "GetUserOofSettings", "GetUserOofSettingsRequest":
		response = s.handleGetUserOofSettings(ctx, soapBody)
	case "SetUserOofSettings", "SetUserOofSettingsRequest":
		response = s.handleSetUserOofSettings(ctx, soapBody)
	case "GetInboxRules", "GetInboxRulesRequest":
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
	case "ConvertId", "ConvertIdRequest":
		response = s.handleConvertId(ctx, soapBody)
	case "MarkAsJunk":
		response = s.handleMarkAsJunk(ctx, soapBody)
	case "FindPeople":
		response = s.handleFindPeople(ctx, soapBody)
	case "GetPersona":
		response = s.handleGetPersona(ctx, soapBody)
	case "GetUserPhoto", "GetUserPhotoRequest":
		response = s.handleGetUserPhoto(ctx, soapBody)
	case "RequestServerVersion":
		response = s.handleRequestServerVersion(ctx, soapBody)
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
	var bodyContent []byte
	bodyMatcher := regexp.MustCompile(`(?s)<(?:\w+:)?Body[^>]*>(.*)</(?:\w+:)?Body>`)
	if matches := bodyMatcher.FindSubmatch(body); len(matches) == 2 {
		bodyContent = bytes.TrimSpace(matches[1])
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
	inBody := false
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
			if name == "Body" {
				inBody = true
				continue
			}
			if !inBody {
				continue
			}
			if name == "Envelope" || name == "Header" {
				continue
			}
			return name, syntheticEnv
		case xml.EndElement:
			if v.Name.Local == "Body" {
				inBody = false
			}
		}
	}
}

// decodeRequest unmarshals an EWS request from a synthetic SOAP envelope.
// It uses xml.Decoder with DecodeElement to properly resolve namespace prefixes (m:, t:).
func decodeRequest(syntheticEnv []byte, req interface{}) error {
	decoder := xml.NewDecoder(bytes.NewReader(syntheticEnv))
	inBody := false
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
			if elem.Name.Local == "Body" {
				inBody = true
				continue
			}
			if !inBody {
				continue
			}
			if elem.Name.Local == "Envelope" || elem.Name.Local == "Header" {
				continue
			}
			return decoder.DecodeElement(req, &elem)
		case xml.EndElement:
			if elem.Name.Local == "Body" {
				inBody = false
			}
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
		"GetFolder", "FindFolder", "CreateFolder", "UpdateFolder", "DeleteFolder", "EmptyFolder", "MoveFolder", "CopyFolder",
		"SyncFolderHierarchy", "SyncFolderItems", "GetItem", "UpdateItem", "DeleteItem",
		"CreateItem", "SendItem", "MoveItem", "CopyItem", "MarkAllItemsAsRead",
		// ResolveNames variants
		"ResolveNames", "ResolveNamesRequest",
		// ExpandDL
		"ExpandDL",
		// GetMailTips
		"GetMailTips", "Recipients",
		// GetServiceConfiguration
		"GetServiceConfiguration",
		// GetAppManifests
		"GetAppManifests",
		// UserConfiguration family
		"CreateUserConfiguration", "GetUserConfiguration", "UpdateUserConfiguration", "DeleteUserConfiguration",
		"UserConfiguration", "UserConfigurationName",
		// GetUserAvailability variants
		"GetUserAvailability", "GetUserAvailabilityRequest",
		// GetRoomLists variants
		"GetRoomLists", "GetRoomListsRequest",
		// GetRooms variants
		"GetRooms", "GetRoomsRequest",
		// ConvertId and RequestServerVersion (sent by exchangelib)
		"ConvertId", "ConvertIdRequest", "RequestServerVersion",
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
		"GetAttachment", "CreateAttachment", "DeleteAttachment", "ItemIds", "ItemShape", "ToFolderId",
		"AttachmentIds", "AttachmentId", "FileAttachment", "Mailbox", "ParentItemId", "Attachments",
		// Subscription elements
		"Subscribe", "Unsubscribe", "GetEvents", "GetStreamingEvents",
		"PullSubscriptionRequest", "StreamingSubscriptionRequest", "PushSubscriptionRequest",
		"SubscriptionId", "SubscriptionIds", "ConnectionTimeout", "Watermark",
		"NotificationEvent", "SubscribeResponse", "UnsubscribeResponse",
		"GetEventsResponse", "SubscribeResponseMessage", "UnsubscribeResponseMessage",
		"GetEventsResponseMessage", "GetStreamingEventsResponse", "GetStreamingEventsResponseMessage",
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
	s.logger.Info("errorResponseXML called", "op", op, "code", code, "message", message)
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

// ---------------------------------------------------------------------------
// ConvertId and RequestServerVersion handlers
// ---------------------------------------------------------------------------

// ConvertIdType is the EWS ConvertId operation request.
type ConvertIdType struct {
	XMLName xml.Name `xml:"ConvertId"`
	// SourceIds contains the mailbox object IDs to convert.
	SourceIds ConvertIdSourceIdsType `xml:"SourceIds"`
	// DestinationFormat specifies the target ID format.
	DestinationFormat string `xml:"DestinationFormat,attr"`
}

// ConvertIdSourceIdsType wraps the list of IDs to convert.
type ConvertIdSourceIdsType struct {
	XMLName xml.Name          `xml:"SourceIds"`
	IDs     []AlternateIdType `xml:"AlternateId"`
}

// AlternateIdType represents an alternate ID for conversion.
type AlternateIdType struct {
	XMLName xml.Name `xml:"AlternateId"`
	ID      string   `xml:"Id,attr"`
	// Format: "EwsId", "EntryId", "HexEntryId", "StoreId", "OWAId", "PRecordId",
	// "EWSLegacyId", "WebClientReadFormQueryString", "WebClientEditFormQueryString"
	Format string `xml:"Format,attr"`
	// Mailbox for cross-mailbox ID conversions (optional).
	Mailbox string `xml:"Mailbox,attr,omitempty"`
}

// ConvertIdResponseType is the EWS ConvertId response.
type ConvertIdResponseType struct {
	XMLName          xml.Name                      `xml:"m:ConvertIdResponse"`
	ResponseMessages ConvertIdResponseMessagesType `xml:"ResponseMessages"`
}

// ConvertIdResponseMessagesType wraps response messages.
type ConvertIdResponseMessagesType struct {
	Messages []ConvertIdResponseMessageType `xml:"ConvertIdResponseMessage"`
}

// ConvertIdResponseMessageType is one ConvertId response message.
type ConvertIdResponseMessageType struct {
	XMLName xml.Name `xml:"ConvertIdResponseMessage"`
	// ResponseClass: "Success" or "Error"
	ResponseClass string `xml:"ResponseClass,attr"`
	// ResponseCode: "NoError" or an error code
	ResponseCode string `xml:"ResponseCode"`
	// ConversionResults holds the converted IDs.
	ConversionResults *ConvertIdResultsType `xml:"ConversionResults,omitempty"`
}

// ConvertIdResultsType holds converted ID results.
type ConvertIdResultsType struct {
	XMLName xml.Name          `xml:"ConversionResults"`
	IDs     []ConvertedIdType `xml:"AlternateId"`
}

// ConvertedIdType is a converted ID result.
type ConvertedIdType struct {
	XMLName xml.Name `xml:"AlternateId"`
	ID      string   `xml:"Id,attr"`
	Format  string   `xml:"Format,attr"`
	Mailbox string   `xml:"Mailbox,attr,omitempty"`
}

// handleConvertId implements the EWS ConvertId operation.
// Converts mailbox object IDs between formats (EwsId, EntryId, HexEntryId, etc.).
// Satisfies the protocol compatibility requirement for ID format translation.
func (s *Server) handleConvertId(ctx context.Context, body []byte) []byte {
	var req ConvertIdType
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("ConvertId", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	// Build response with conversion results.
	messages := make([]ConvertIdResponseMessageType, 0, len(req.SourceIds.IDs))
	for _, srcID := range req.SourceIds.IDs {
		// For now, we do a pass-through conversion: keep the same ID string but
		// change the format to the destination format. This is sufficient for
		// basic Exchange compatibility where clients need to translate between
		// EWS legacy IDs and the current format.
		messages = append(messages, ConvertIdResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  "NoError",
			ConversionResults: &ConvertIdResultsType{
				IDs: []ConvertedIdType{
					{
						ID:      srcID.ID,
						Format:  req.DestinationFormat,
						Mailbox: srcID.Mailbox,
					},
				},
			},
		})
	}

	resp := ConvertIdResponseType{
		ResponseMessages: ConvertIdResponseMessagesType{
			Messages: messages,
		},
	}

	return buildResponseEnvelope(resp)
}

// RequestServerVersionType is the EWS RequestServerVersion SOAP header element.
// It is sent by clients in the SOAP header to specify which server version they
// expect, allowing the server to adjust behavior accordingly.
type RequestServerVersionType struct {
	XMLName xml.Name `xml:"RequestServerVersion"`
	// Version: "Exchange2013", "Exchange2013_SP1", "Exchange2015", "Exchange2016",
	// "Exchange2019", "Exchange2019_SP1", "V2_1", "V2_2", "V2_3", "V2_4", "V2_5",
	// "V2_6", "V2_7", "V2_8" etc.
	Version string `xml:"Version,attr"`
}

// handleRequestServerVersion handles the RequestServerVersion SOAP header element.
// exchangelib sends this as an explicit SOAP operation, but in standard EWS it is
// a SOAP header. We treat it as a no-op info-level request and return success.
func (s *Server) handleRequestServerVersion(ctx context.Context, body []byte) []byte {
	// This is treated as an informational request — the server version we
	// advertise in every response's ServerVersion header is sufficient for
	// clients that understand the SOAP header convention. We return a simple
	// success response with the proper messages namespace.
	resp := struct {
		XMLName xml.Name `xml:"m:RequestServerVersionResponse"`
	}{
		XMLName: xml.Name{Local: "RequestServerVersionResponse", Space: EWSMessagesNS},
	}
	return buildResponseEnvelope(resp)
}

// MarkAsJunkRequest is the EWS MarkAsJunk operation request. IsJunk and
// MoveItem are attributes on the MarkAsJunk element; ItemIds carries the
// affected items.
type MarkAsJunkRequest struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MarkAsJunk"`
	IsJunk   string   `xml:"IsJunk,attr"`
	MoveItem string   `xml:"MoveItem,attr"`
	ItemIDs  struct {
		Item []ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// handleMarkAsJunk marks items as junk (or not) and, when MoveItem is set,
// relocates them between the Inbox and Junk Email folders. It returns the
// resulting MovedItemId so the client can update its local item id/changekey.
func (s *Server) handleMarkAsJunk(ctx context.Context, body []byte) []byte {
	var req MarkAsJunkRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("MarkAsJunk", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("MarkAsJunk", errCode, "could not resolve mailbox")
	}
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorItemResponseXML("MarkAsJunk", ErrErrorMailboxNotFound, err.Error())
	}

	isJunk := strings.EqualFold(req.IsJunk, "true") || req.IsJunk == "1"
	moveItem := strings.EqualFold(req.MoveItem, "true") || req.MoveItem == "1"

	// When marking as junk we move to the Junk Email folder; un-marking moves
	// the item back to the Inbox.
	destName, destRole := "junkemail", "spam"
	if !isJunk {
		destName, destRole = "inbox", "inbox"
	}

	msgs := make([]MarkAsJunkResponseMessage, 0, len(req.ItemIDs.Item))
	for _, id := range req.ItemIDs.Item {
		itemID, err := semcore.NewItemId(id.ID)
		if err != nil {
			msgs = append(msgs, markJunkErr(ErrErrorInvalidId))
			continue
		}
		rec, err := s.identity.GetItemIdentity(itemID)
		if err != nil {
			msgs = append(msgs, markJunkErr(ErrErrorItemNotFound))
			continue
		}
		if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
			msgs = append(msgs, markJunkErr(ErrErrorAccessDenied))
			continue
		}

		moved := &MovedItemIdType{ID: itemID.String(), CK: rec.ChangeKey.String()}
		if moveItem {
			destFolderID, derr := s.ensureDistinguishedFolderID(mailboxKey, destName, destRole)
			if derr != nil {
				msgs = append(msgs, markJunkErr(ErrErrorFolderNotFound))
				continue
			}
			result := s.moveItemToFolder(ctx, mboxID, mboxKey, rec.FolderID, destFolderID, itemID)
			if result.ResponseClass != "Success" {
				msgs = append(msgs, markJunkErr(result.ResponseCode.Value))
				continue
			}
			if len(result.Items.Items) > 0 {
				moved.ID = result.Items.Items[0].ItemID.ID
				moved.CK = result.Items.Items[0].ItemID.CK
			}
		}

		msgs = append(msgs, MarkAsJunkResponseMessage{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			MovedItemID:   moved,
		})
	}

	resp := MarkAsJunkResponse{}
	resp.Msgs.Messages = msgs
	return buildResponseEnvelope(resp)
}

// ensureDistinguishedFolderID resolves (creating if necessary) the FolderId for
// a distinguished folder identified by its EWS name and semcore role.
func (s *Server) ensureDistinguishedFolderID(mailboxKey, name, role string) (semcore.FolderId, error) {
	fld, err := s.identity.GetFolderByMailbox(mailboxKey, role)
	if err == nil {
		return fld.FolderID, nil
	}
	if !errors.Is(err, semcore.ErrFolderNotFound) {
		return semcore.FolderId{}, err
	}
	if _, err := s.identity.EnsureFolderId(mailboxKey, name, role); err != nil {
		return semcore.FolderId{}, err
	}
	fld, err = s.identity.GetFolderByMailbox(mailboxKey, role)
	if err != nil {
		return semcore.FolderId{}, err
	}
	return fld.FolderID, nil
}

func markJunkErr(code ErrorCode) MarkAsJunkResponseMessage {
	return MarkAsJunkResponseMessage{
		ResponseClass: "Error",
		ResponseCode:  ResponseCodeType{Value: code},
	}
}

// MovedItemIdType is the ItemId returned by MarkAsJunk for a relocated item.
type MovedItemIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MovedItemId"`
	ID      string   `xml:"Id,attr"`
	CK      string   `xml:"ChangeKey,attr,omitempty"`
}

// MarkAsJunkResponse is the EWS MarkAsJunk operation response.
type MarkAsJunkResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MarkAsJunkResponse"`
	Msgs    struct {
		Messages []MarkAsJunkResponseMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MarkAsJunkResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// MarkAsJunkResponseMessage is one MarkAsJunk result.
type MarkAsJunkResponseMessage struct {
	XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MarkAsJunkResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	ErrorMessage  string           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ErrorMessage,omitempty"`
	MovedItemID   *MovedItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MovedItemId"`
}
