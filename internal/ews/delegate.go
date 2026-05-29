// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file provides delegate management SOAP types and handlers.
//
// Delegate operations (GetDelegate, AddDelegate, UpdateDelegate, RemoveDelegate)
// project the canonical semcore DelegateStore through the EWS wire contract,
// satisfying VAL-DIR-001 (shared mailbox discovery requires explicit grant),
// VAL-DIR-002 (concrete rights enforcement per action), VAL-DIR-003 (grant/revoke
// is authoritative), VAL-DIR-013 (EWS delegate-management round-trips admin state),
// and VAL-DIR-014 (delegated meeting identity and audit context).
//
// Delegate permissions are separate from RFC 4314 IMAP ACLs. A delegate grant
// is an Exchange-semantic capability that controls cross-user mailbox access
// through EWS and Outlook surfaces, not raw folder visibility.
package ews

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/umailserver/umailserver/internal/api"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// xmlEscape returns an XML-escaped version of s.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// ---------------------------------------------------------------------------
// Delegate SOAP types
// ---------------------------------------------------------------------------

// DelegateFolderPermissionLevelType is the EWS delegate folder permission level.
type DelegateFolderPermissionLevelType string

const (
	DelegateFolderPermissionLevelNone     DelegateFolderPermissionLevelType = "None"
	DelegateFolderPermissionLevelReviewer DelegateFolderPermissionLevelType = "Reviewer"
	DelegateFolderPermissionLevelAuthor   DelegateFolderPermissionLevelType = "Author"
	DelegateFolderPermissionLevelCustom   DelegateFolderPermissionLevelType = "Custom"
	DelegateFolderPermissionLevelDelegate DelegateFolderPermissionLevelType = "Delegate"
)

// DelegatePermissionsType is the EWS delegate permissions per folder type.
type DelegatePermissionsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegatePermissions"`

	CalendarFolderPermissionLevel *DelegateFolderPermissionLevelType `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarFolderPermissionLevel,omitempty"`
	TasksFolderPermissionLevel    *DelegateFolderPermissionLevelType `xml:"http://schemas.microsoft.com/exchange/services/2006/types TasksFolderPermissionLevel,omitempty"`
	InboxFolderPermissionLevel    *DelegateFolderPermissionLevelType `xml:"http://schemas.microsoft.com/exchange/services/2006/types InboxFolderPermissionLevel,omitempty"`
	ContactsFolderPermissionLevel *DelegateFolderPermissionLevelType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContactsFolderPermissionLevel,omitempty"`
	NotesFolderPermissionLevel    *DelegateFolderPermissionLevelType `xml:"http://schemas.microsoft.com/exchange/services/2006/types NotesFolderPermissionLevel,omitempty"`
	JournalFolderPermissionLevel  *DelegateFolderPermissionLevelType `xml:"http://schemas.microsoft.com/exchange/services/2006/types JournalFolderPermissionLevel,omitempty"`
}

// UserIdType identifies a user in EWS delegate requests.
type UserIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types UserId"`

	// PrimarySmtpAddress is the primary SMTP email address of the user.
	PrimarySmtpAddress string `xml:"http://schemas.microsoft.com/exchange/services/2006/types PrimarySmtpAddress,omitempty"`
	// SID is the security identifier (not used by uMailServer).
	SID string `xml:"http://schemas.microsoft.com/exchange/services/2006/types SID,omitempty"`
	// DisplayName is the display name (optional).
	DisplayName string `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName,omitempty"`
	// DistinguishedUser distinguishes Anonymous/Default accounts.
	DistinguishedUser string `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedUser,omitempty"`
}

// ArrayOfUserIdType holds a list of user IDs.
type ArrayOfUserIdType struct {
	XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/types UserIds"`
	Users   []UserIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types UserId,omitempty"`
}

// DelegateUserType represents a delegate user in EWS requests and responses.
type DelegateUserType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegateUser"`

	UserId              *UserIdType              `xml:"http://schemas.microsoft.com/exchange/services/2006/types UserId"`
	DelegatePermissions *DelegatePermissionsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegatePermissions,omitempty"`
	// ReceiveCopiesOfMeetingMessages: if true, delegate receives copies of meeting requests.
	ReceiveCopiesOfMeetingMessages *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReceiveCopiesOfMeetingMessages,omitempty"`
	// ViewPrivateItems: if true, delegate can see private calendar items.
	ViewPrivateItems *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types ViewPrivateItems,omitempty"`
	// CanSendAs grants the delegate permission to send as the owner without "on behalf of".
	// VAL-DIR-004.
	CanSendAs *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types CanSendAs,omitempty"`
	// CanSendOnBehalf grants the delegate permission to send on behalf of the owner
	// with "on behalf of" semantics where Sender identifies the delegate.
	// VAL-DIR-005.
	CanSendOnBehalf *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/types CanSendOnBehalf,omitempty"`
}

// ArrayOfDelegateUserType holds a list of delegate users.
type ArrayOfDelegateUserType struct {
	XMLName xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegateUsers"`
	Users   []DelegateUserType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegateUser,omitempty"`
}

// ---------------------------------------------------------------------------
// Delegate request types
// ---------------------------------------------------------------------------

// BaseDelegateType is the abstract base for all delegate SOAP operations.
// It contains the target mailbox and optional user IDs.
type BaseDelegateType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages BaseDelegateType"`

	Mailbox struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
		Email   string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`

	// UserIds: list of user IDs to get or remove.
	UserIds *ArrayOfUserIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types UserIds,omitempty"`
}

// GetDelegateType is the EWS GetDelegate operation request.
type GetDelegateType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetDelegate"`
	BaseDelegateType

	// IncludePermissions: if true, include per-folder permission levels.
	IncludePermissions bool `xml:"http://schemas.microsoft.com/exchange/services/2006/messages IncludePermissions,omitempty"`
}

// AddDelegateType is the EWS AddDelegate operation request.
type AddDelegateType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AddDelegate"`
	BaseDelegateType

	// DelegateUsers: list of delegates to add.
	DelegateUsers *ArrayOfDelegateUserType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegateUsers,omitempty"`
	// DeliverMeetingRequests: how meeting requests are delivered.
	DeliverMeetingRequests string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeliverMeetingRequests,omitempty"`
}

// UpdateDelegateType is the EWS UpdateDelegate operation request.
type UpdateDelegateType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateDelegate"`
	BaseDelegateType

	// DelegateUsers: list of delegates to update.
	DelegateUsers *ArrayOfDelegateUserType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegateUsers,omitempty"`
	// DeliverMeetingRequests: how meeting requests are delivered.
	DeliverMeetingRequests string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeliverMeetingRequests,omitempty"`
}

// RemoveDelegateType is the EWS RemoveDelegate operation request.
type RemoveDelegateType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RemoveDelegate"`
	BaseDelegateType
}

// ---------------------------------------------------------------------------
// Delegate response types
// ---------------------------------------------------------------------------

// BaseDelegateResponseMessageType is the abstract base for delegate response messages.
type BaseDelegateResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages BaseDelegateResponseMessageType"`

	ResponseMessageType
	// DelegateUsers: included in response to report the added/updated delegate state.
	DelegateUsers *ArrayOfDelegateUserType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegateUsers,omitempty"`
}

// DelegateUserResponseMessageType is the response message for a single delegate user.
type DelegateUserResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DelegateUserResponseMessageType"`

	ResponseMessageType
	DelegateUser *DelegateUserType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegateUser,omitempty"`
}

// ArrayOfDelegateUserResponseMessageType holds response messages per delegate.
type ArrayOfDelegateUserResponseMessageType struct {
	XMLName  xml.Name                          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DelegateUserResponseMessages"`
	Messages []DelegateUserResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DelegateUserResponseMessageType,omitempty"`
}

// GetDelegateResponseMessageType is the GetDelegate operation response.
type GetDelegateResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetDelegateResponseMessageType"`

	ResponseMessageType
	DelegateUsers          *ArrayOfDelegateUserType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DelegateUsers,omitempty"`
	DeliverMeetingRequests string                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeliverMeetingRequests,omitempty"`
}

// AddDelegateResponseMessageType is the AddDelegate operation response.
type AddDelegateResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AddDelegateResponseMessageType"`

	ResponseMessageType
	DelegateUserResponseMessages *ArrayOfDelegateUserResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DelegateUserResponseMessages,omitempty"`
}

// UpdateDelegateResponseMessageType is the UpdateDelegate operation response.
type UpdateDelegateResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateDelegateResponseMessageType"`

	ResponseMessageType
	DelegateUserResponseMessages *ArrayOfDelegateUserResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DelegateUserResponseMessages,omitempty"`
}

// RemoveDelegateResponseMessageType is the RemoveDelegate operation response.
type RemoveDelegateResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RemoveDelegateResponseMessageType"`

	ResponseMessageType
}

// ---------------------------------------------------------------------------
// Delegate SOAP operation handlers
// ---------------------------------------------------------------------------

// handleGetDelegate implements the EWS GetDelegate operation.
// Satisfies VAL-DIR-001 (shared mailbox discovery requires explicit grant),
// VAL-DIR-002 (concrete rights enforcement), and VAL-DIR-013 (EWS round-trips admin state).
func (s *Server) handleGetDelegate(ctx context.Context, body []byte) []byte {
	var req GetDelegateType
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetDelegate", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	// Resolve the owner mailbox.
	email := req.Mailbox.Email
	if email == "" {
		return s.errorResponseXML("GetDelegate", ErrErrorMailboxNotFound, "Mailbox element is required")
	}

	ownerID, err := semcore.NewMailboxId(email)
	if err != nil {
		return s.errorResponseXML("GetDelegate", ErrErrorInvalidId, err.Error())
	}

	// Verify the authenticated user is the owner or an admin.
	authUser, _ := ctx.Value(api.ContextKeyEmail).(string) //nolint:errcheck
	if authUser != email {
		isAdmin, _ := ctx.Value("isAdmin").(bool) //nolint:errcheck
		if !isAdmin {
			return s.errorResponseXML("GetDelegate", ErrErrorAccessDenied, "not authorized to view delegates for this mailbox")
		}
	}

	// Use delegate store.
	if s.delegateStore == nil {
		return s.errorResponseXML("GetDelegate", ErrErrorInternalServer, "delegate store not available")
	}

	delegates, err := s.delegateStore.ListDelegates(ownerID)
	if err != nil {
		return s.errorResponseXML("GetDelegate", ErrErrorInternalServer, "failed to list delegates: "+err.Error())
	}

	// Filter to specific users if requested.
	var filtered []*semcore.DelegateUser
	if req.UserIds != nil && len(req.UserIds.Users) > 0 {
		want := make(map[string]bool)
		for _, u := range req.UserIds.Users {
			want[u.PrimarySmtpAddress] = true
		}
		for _, d := range delegates {
			if want[d.DelegateEmail] {
				filtered = append(filtered, d)
			}
		}
	} else {
		filtered = delegates
	}

	// Build response.
	users := make([]DelegateUserType, 0, len(filtered))
	meetingDelivery := string(semcore.DeliverDelegatesAndMe)
	for _, d := range filtered {
		eu := delegateUserToEWS(d, req.IncludePermissions)
		users = append(users, eu)
		if meetingDelivery == "" {
			meetingDelivery = string(d.DeliverRequests)
		}
	}

	var resp DelegateUserType
	if len(users) > 0 {
		resp = users[0]
	}
	_ = resp // reserved for future use

	// Build full response with response messages.
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	buf.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	buf.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	buf.Write(svBytes)
	buf.WriteString(`</soap:Header>`)
	buf.WriteString(`<soap:Body>`)
	buf.WriteString(`<m:GetDelegateResponse>`)
	buf.WriteString(`<m:ResponseMessages>`)
	buf.WriteString(`<m:DelegateUserResponseMessageType ResponseClass="Success">`)
	buf.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
	if len(users) > 0 {
		for _, u := range users {
			buf.WriteString(`<m:DelegateUser>`)
			if u.UserId != nil {
				buf.WriteString(`<t:UserId>`)
				if u.UserId.PrimarySmtpAddress != "" {
					buf.WriteString(`<t:PrimarySmtpAddress>` + xmlEscape(u.UserId.PrimarySmtpAddress) + `</t:PrimarySmtpAddress>`)
				}
				buf.WriteString(`</t:UserId>`)
			}
			if u.DelegatePermissions != nil && req.IncludePermissions {
				buf.WriteString(`<t:DelegatePermissions>`)
				if u.DelegatePermissions.CalendarFolderPermissionLevel != nil {
					buf.WriteString(`<t:CalendarFolderPermissionLevel>` + string(*u.DelegatePermissions.CalendarFolderPermissionLevel) + `</t:CalendarFolderPermissionLevel>`)
				}
				if u.DelegatePermissions.InboxFolderPermissionLevel != nil {
					buf.WriteString(`<t:InboxFolderPermissionLevel>` + string(*u.DelegatePermissions.InboxFolderPermissionLevel) + `</t:InboxFolderPermissionLevel>`)
				}
				buf.WriteString(`</t:DelegatePermissions>`)
			}
			if u.ReceiveCopiesOfMeetingMessages != nil {
				if *u.ReceiveCopiesOfMeetingMessages {
					buf.WriteString(`<t:ReceiveCopiesOfMeetingMessages>true</t:ReceiveCopiesOfMeetingMessages>`)
				} else {
					buf.WriteString(`<t:ReceiveCopiesOfMeetingMessages>false</t:ReceiveCopiesOfMeetingMessages>`)
				}
			}
			if u.ViewPrivateItems != nil {
				if *u.ViewPrivateItems {
					buf.WriteString(`<t:ViewPrivateItems>true</t:ViewPrivateItems>`)
				} else {
					buf.WriteString(`<t:ViewPrivateItems>false</t:ViewPrivateItems>`)
				}
			}
			if u.CanSendAs != nil {
				if *u.CanSendAs {
					buf.WriteString(`<t:CanSendAs>true</t:CanSendAs>`)
				} else {
					buf.WriteString(`<t:CanSendAs>false</t:CanSendAs>`)
				}
			}
			if u.CanSendOnBehalf != nil {
				if *u.CanSendOnBehalf {
					buf.WriteString(`<t:CanSendOnBehalf>true</t:CanSendOnBehalf>`)
				} else {
					buf.WriteString(`<t:CanSendOnBehalf>false</t:CanSendOnBehalf>`)
				}
			}
			buf.WriteString(`</m:DelegateUser>`)
		}
	}
	buf.WriteString(`</m:DelegateUserResponseMessageType>`)
	buf.WriteString(`</m:ResponseMessages>`)
	if len(users) > 0 {
		buf.WriteString(`<m:DeliverMeetingRequests>` + xmlEscape(meetingDelivery) + `</m:DeliverMeetingRequests>`)
	}
	buf.WriteString(`</m:GetDelegateResponse>`)
	buf.WriteString(`</soap:Body>`)
	buf.WriteString(`</soap:Envelope>`)
	return buf.Bytes()
}

// handleAddDelegate implements the EWS AddDelegate operation.
// Satisfies VAL-DIR-003 (grant is authoritative) and VAL-DIR-013.
func (s *Server) handleAddDelegate(ctx context.Context, body []byte) []byte {
	var req AddDelegateType
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("AddDelegate", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	email := req.Mailbox.Email
	if email == "" {
		return s.errorResponseXML("AddDelegate", ErrErrorMailboxNotFound, "Mailbox element is required")
	}

	ownerID, err := semcore.NewMailboxId(email)
	if err != nil {
		return s.errorResponseXML("AddDelegate", ErrErrorInvalidId, err.Error())
	}

	// Only owner or admin can add delegates.
	authUser, _ := ctx.Value(api.ContextKeyEmail).(string) //nolint:errcheck
	isAdmin, _ := ctx.Value("isAdmin").(bool)              //nolint:errcheck
	if authUser != email && !isAdmin {
		return s.errorResponseXML("AddDelegate", ErrErrorAccessDenied, "not authorized to add delegates for this mailbox")
	}

	if s.delegateStore == nil {
		return s.errorResponseXML("AddDelegate", ErrErrorInternalServer, "delegate store not available")
	}

	if req.DelegateUsers == nil || len(req.DelegateUsers.Users) == 0 {
		return s.errorResponseXML("AddDelegate", ErrErrorInvalidOperation, "DelegateUsers element is required")
	}

	meetingDelivery := semcore.DeliverDelegatesAndMe
	switch req.DeliverMeetingRequests {
	case "DelegatesOnly":
		meetingDelivery = semcore.DeliverDelegatesOnly
	case "DelegatesAndSendInformationToMe":
		meetingDelivery = semcore.DeliverDelegatesAndSendInfoToMe
	}

	// Process each delegate user.
	messages := make([]DelegateUserResponseMessageType, 0, len(req.DelegateUsers.Users))
	for _, eu := range req.DelegateUsers.Users {
		msg := s.addSingleDelegate(ctx, ownerID, email, authUser, eu, meetingDelivery)
		messages = append(messages, msg)
	}

	// Build response.
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	buf.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	buf.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	buf.Write(svBytes)
	buf.WriteString(`</soap:Header>`)
	buf.WriteString(`<soap:Body>`)
	buf.WriteString(`<m:AddDelegateResponse>`)
	buf.WriteString(`<m:ResponseMessages>`)
	for _, msg := range messages {
		class := "Success"
		code := string(ErrNoError)
		if msg.ResponseCode != ErrNoError {
			class = "Error"
			code = string(msg.ResponseCode)
		}
		buf.WriteString(`<m:AddDelegateResponseMessageType ResponseClass="` + class + `">`)
		buf.WriteString(`<m:ResponseCode>` + code + `</m:ResponseCode>`)
		if msg.DelegateUser != nil {
			buf.WriteString(`<m:DelegateUser>`)
			if msg.DelegateUser.UserId != nil && msg.DelegateUser.UserId.PrimarySmtpAddress != "" {
				buf.WriteString(`<t:UserId><t:PrimarySmtpAddress>` + xmlEscape(msg.DelegateUser.UserId.PrimarySmtpAddress) + `</t:PrimarySmtpAddress></t:UserId>`)
			}
			buf.WriteString(`</m:DelegateUser>`)
		}
		buf.WriteString(`</m:AddDelegateResponseMessageType>`)
	}
	buf.WriteString(`</m:ResponseMessages>`)
	buf.WriteString(`</m:AddDelegateResponse>`)
	buf.WriteString(`</soap:Body>`)
	buf.WriteString(`</soap:Envelope>`)
	return buf.Bytes()
}

// addSingleDelegate processes one delegate user from an AddDelegate request.
func (s *Server) addSingleDelegate(ctx context.Context, ownerID semcore.MailboxId, ownerEmail, grantedBy string, eu DelegateUserType, meetingDelivery semcore.DeliverMeetingRequests) DelegateUserResponseMessageType {
	if eu.UserId == nil || eu.UserId.PrimarySmtpAddress == "" {
		return DelegateUserResponseMessageType{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInvalidDelegateUserId,
				ErrorMessage:  "PrimarySmtpAddress is required",
			},
		}
	}

	// Check if delegate already exists.
	existing, _ := s.delegateStore.GetDelegateForUser(ownerID, eu.UserId.PrimarySmtpAddress) //nolint:errcheck
	if existing != nil {
		return DelegateUserResponseMessageType{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorDelegateAlreadyExists,
				ErrorMessage:  "Delegate already exists for this mailbox",
			},
		}
	}

	// Build delegate user record.
	perms := permissionsFromEWS(eu.DelegatePermissions)
	if !perms.HasAccess() {
		// Default to Author on inbox if no permissions specified.
		perms.Inbox = semcore.DelegateFolderPermissionAuthor
	}

	viewPrivate := false
	if eu.ViewPrivateItems != nil {
		viewPrivate = *eu.ViewPrivateItems
	}
	receiveCopies := true
	if eu.ReceiveCopiesOfMeetingMessages != nil {
		receiveCopies = *eu.ReceiveCopiesOfMeetingMessages
	}
	canSendAs := false
	if eu.CanSendAs != nil {
		canSendAs = *eu.CanSendAs
	}
	canSendOnBehalf := false
	if eu.CanSendOnBehalf != nil {
		canSendOnBehalf = *eu.CanSendOnBehalf
	}

	delegate := &semcore.DelegateUser{
		OwnerID:          ownerID,
		DelegateEmail:    eu.UserId.PrimarySmtpAddress,
		DelegateUserID:   eu.UserId.PrimarySmtpAddress,
		Permissions:      perms,
		ViewPrivateItems: viewPrivate,
		ReceiveCopies:    receiveCopies,
		DeliverRequests:  meetingDelivery,
		GrantedBy:        grantedBy,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
		CanSendAs:        canSendAs,
		CanSendOnBehalf:  canSendOnBehalf,
	}

	_, err := s.delegateStore.PutDelegate(delegate)
	if err != nil {
		return DelegateUserResponseMessageType{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
				ErrorMessage:  err.Error(),
			},
		}
	}

	// Return success response.
	respUser := delegateUserToEWS(delegate, true)
	return DelegateUserResponseMessageType{
		ResponseMessageType: ResponseMessageType{
			ResponseClass: ResponseClassSuccess,
			ResponseCode:  ErrNoError,
		},
		DelegateUser: &DelegateUserType{
			UserId:                         &UserIdType{PrimarySmtpAddress: eu.UserId.PrimarySmtpAddress},
			DelegatePermissions:            respUser.DelegatePermissions,
			ReceiveCopiesOfMeetingMessages: &receiveCopies,
			ViewPrivateItems:               &viewPrivate,
		},
	}
}

// handleUpdateDelegate implements the EWS UpdateDelegate operation.
func (s *Server) handleUpdateDelegate(ctx context.Context, body []byte) []byte {
	var req UpdateDelegateType
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("UpdateDelegate", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	email := req.Mailbox.Email
	if email == "" {
		return s.errorResponseXML("UpdateDelegate", ErrErrorMailboxNotFound, "Mailbox element is required")
	}

	ownerID, err := semcore.NewMailboxId(email)
	if err != nil {
		return s.errorResponseXML("UpdateDelegate", ErrErrorInvalidId, err.Error())
	}

	authUser, _ := ctx.Value(api.ContextKeyEmail).(string) //nolint:errcheck
	isAdmin, _ := ctx.Value("isAdmin").(bool)              //nolint:errcheck
	if authUser != email && !isAdmin {
		return s.errorResponseXML("UpdateDelegate", ErrErrorAccessDenied, "not authorized to update delegates for this mailbox")
	}

	if s.delegateStore == nil {
		return s.errorResponseXML("UpdateDelegate", ErrErrorInternalServer, "delegate store not available")
	}

	if req.DelegateUsers == nil || len(req.DelegateUsers.Users) == 0 {
		return s.errorResponseXML("UpdateDelegate", ErrErrorInvalidOperation, "DelegateUsers element is required")
	}

	meetingDelivery := semcore.DeliverDelegatesAndMe
	switch req.DeliverMeetingRequests {
	case "DelegatesOnly":
		meetingDelivery = semcore.DeliverDelegatesOnly
	case "DelegatesAndSendInformationToMe":
		meetingDelivery = semcore.DeliverDelegatesAndSendInfoToMe
	}

	messages := make([]DelegateUserResponseMessageType, 0, len(req.DelegateUsers.Users))
	for _, eu := range req.DelegateUsers.Users {
		msg := s.updateSingleDelegate(ctx, ownerID, email, authUser, eu, meetingDelivery)
		messages = append(messages, msg)
	}

	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	buf.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	buf.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	buf.Write(svBytes)
	buf.WriteString(`</soap:Header>`)
	buf.WriteString(`<soap:Body>`)
	buf.WriteString(`<m:UpdateDelegateResponse>`)
	buf.WriteString(`<m:ResponseMessages>`)
	for _, msg := range messages {
		class := "Success"
		code := string(ErrNoError)
		if msg.ResponseCode != ErrNoError {
			class = "Error"
			code = string(msg.ResponseCode)
		}
		buf.WriteString(`<m:UpdateDelegateResponseMessageType ResponseClass="` + class + `">`)
		buf.WriteString(`<m:ResponseCode>` + code + `</m:ResponseCode>`)
		buf.WriteString(`</m:UpdateDelegateResponseMessageType>`)
	}
	buf.WriteString(`</m:ResponseMessages>`)
	buf.WriteString(`</m:UpdateDelegateResponse>`)
	buf.WriteString(`</soap:Body>`)
	buf.WriteString(`</soap:Envelope>`)
	return buf.Bytes()
}

// updateSingleDelegate processes one delegate user from an UpdateDelegate request.
func (s *Server) updateSingleDelegate(ctx context.Context, ownerID semcore.MailboxId, ownerEmail, grantedBy string, eu DelegateUserType, meetingDelivery semcore.DeliverMeetingRequests) DelegateUserResponseMessageType {
	if eu.UserId == nil || eu.UserId.PrimarySmtpAddress == "" {
		return DelegateUserResponseMessageType{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInvalidDelegateUserId,
				ErrorMessage:  "PrimarySmtpAddress is required",
			},
		}
	}

	existing, err := s.delegateStore.GetDelegateForUser(ownerID, eu.UserId.PrimarySmtpAddress)
	if err != nil {
		if errors.Is(err, fmt.Errorf("no delegate grant")) || err.Error() == "no delegate grant for "+eu.UserId.PrimarySmtpAddress+" on "+ownerID.String() {
			return DelegateUserResponseMessageType{
				ResponseMessageType: ResponseMessageType{
					ResponseClass: ResponseClassError,
					ResponseCode:  ErrErrorNotDelegate,
					ErrorMessage:  "Delegate does not exist",
				},
			}
		}
		return DelegateUserResponseMessageType{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
				ErrorMessage:  err.Error(),
			},
		}
	}

	// Update fields that were provided.
	if eu.DelegatePermissions != nil {
		existing.Permissions = permissionsFromEWS(eu.DelegatePermissions)
	}
	if eu.ViewPrivateItems != nil {
		existing.ViewPrivateItems = *eu.ViewPrivateItems
	}
	if eu.ReceiveCopiesOfMeetingMessages != nil {
		existing.ReceiveCopies = *eu.ReceiveCopiesOfMeetingMessages
	}
	if eu.CanSendAs != nil {
		existing.CanSendAs = *eu.CanSendAs
	}
	if eu.CanSendOnBehalf != nil {
		existing.CanSendOnBehalf = *eu.CanSendOnBehalf
	}
	existing.DeliverRequests = meetingDelivery
	existing.UpdatedAt = time.Now()

	if _, err := s.delegateStore.PutDelegate(existing); err != nil {
		return DelegateUserResponseMessageType{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
				ErrorMessage:  err.Error(),
			},
		}
	}

	return DelegateUserResponseMessageType{
		ResponseMessageType: ResponseMessageType{
			ResponseClass: ResponseClassSuccess,
			ResponseCode:  ErrNoError,
		},
	}
}

// handleRemoveDelegate implements the EWS RemoveDelegate operation.
func (s *Server) handleRemoveDelegate(ctx context.Context, body []byte) []byte {
	var req RemoveDelegateType
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("RemoveDelegate", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	email := req.Mailbox.Email
	if email == "" {
		return s.errorResponseXML("RemoveDelegate", ErrErrorMailboxNotFound, "Mailbox element is required")
	}

	ownerID, err := semcore.NewMailboxId(email)
	if err != nil {
		return s.errorResponseXML("RemoveDelegate", ErrErrorInvalidId, err.Error())
	}

	authUser, _ := ctx.Value(api.ContextKeyEmail).(string) //nolint:errcheck
	isAdmin, _ := ctx.Value("isAdmin").(bool)              //nolint:errcheck
	if authUser != email && !isAdmin {
		return s.errorResponseXML("RemoveDelegate", ErrErrorAccessDenied, "not authorized to remove delegates for this mailbox")
	}

	if s.delegateStore == nil {
		return s.errorResponseXML("RemoveDelegate", ErrErrorInternalServer, "delegate store not available")
	}

	if req.UserIds == nil || len(req.UserIds.Users) == 0 {
		return s.errorResponseXML("RemoveDelegate", ErrErrorInvalidOperation, "UserIds element is required")
	}

	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	buf.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	buf.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	buf.Write(svBytes)
	buf.WriteString(`</soap:Header>`)
	buf.WriteString(`<soap:Body>`)
	buf.WriteString(`<m:RemoveDelegateResponse>`)
	buf.WriteString(`<m:ResponseMessages>`)

	for _, u := range req.UserIds.Users {
		delegateEmail := u.PrimarySmtpAddress
		if delegateEmail == "" {
			buf.WriteString(`<m:RemoveDelegateResponseMessageType ResponseClass="Error">`)
			buf.WriteString(`<m:ResponseCode>` + string(ErrErrorInvalidDelegateUserId) + `</m:ResponseCode>`)
			buf.WriteString(`</m:RemoveDelegateResponseMessageType>`)
			continue
		}

		// Find the delegate grant ID and remove.
		existing, err := s.delegateStore.GetDelegateForUser(ownerID, delegateEmail)
		if err != nil {
			buf.WriteString(`<m:RemoveDelegateResponseMessageType ResponseClass="Error">`)
			buf.WriteString(`<m:ResponseCode>` + string(ErrErrorNotDelegate) + `</m:ResponseCode>`)
			buf.WriteString(`</m:RemoveDelegateResponseMessageType>`)
			continue
		}

		if err := s.delegateStore.RemoveDelegate(existing.ID); err != nil {
			buf.WriteString(`<m:RemoveDelegateResponseMessageType ResponseClass="Error">`)
			buf.WriteString(`<m:ResponseCode>` + string(ErrErrorInternalServer) + `</m:ResponseCode>`)
			buf.WriteString(`</m:RemoveDelegateResponseMessageType>`)
			continue
		}

		buf.WriteString(`<m:RemoveDelegateResponseMessageType ResponseClass="Success">`)
		buf.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
		buf.WriteString(`</m:RemoveDelegateResponseMessageType>`)
	}

	buf.WriteString(`</m:ResponseMessages>`)
	buf.WriteString(`</m:RemoveDelegateResponse>`)
	buf.WriteString(`</soap:Body>`)
	buf.WriteString(`</soap:Envelope>`)
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// Helper: convert semcore delegate to EWS delegate user
// ---------------------------------------------------------------------------

// delegateUserToEWS converts a semcore DelegateUser to an EWS DelegateUserType.
func delegateUserToEWS(d *semcore.DelegateUser, includePermissions bool) DelegateUserType {
	eu := DelegateUserType{
		UserId: &UserIdType{
			PrimarySmtpAddress: d.DelegateEmail,
		},
		ReceiveCopiesOfMeetingMessages: &d.ReceiveCopies,
		ViewPrivateItems:               &d.ViewPrivateItems,
		CanSendAs:                      &d.CanSendAs,
		CanSendOnBehalf:                &d.CanSendOnBehalf,
	}

	if includePermissions {
		perms := &DelegatePermissionsType{}
		if d.Permissions.Calendar != "" {
			level := DelegateFolderPermissionLevelType(d.Permissions.Calendar)
			perms.CalendarFolderPermissionLevel = &level
		}
		if d.Permissions.Inbox != "" {
			level := DelegateFolderPermissionLevelType(d.Permissions.Inbox)
			perms.InboxFolderPermissionLevel = &level
		}
		if d.Permissions.Tasks != "" {
			level := DelegateFolderPermissionLevelType(d.Permissions.Tasks)
			perms.TasksFolderPermissionLevel = &level
		}
		if d.Permissions.Contacts != "" {
			level := DelegateFolderPermissionLevelType(d.Permissions.Contacts)
			perms.ContactsFolderPermissionLevel = &level
		}
		if d.Permissions.Notes != "" {
			level := DelegateFolderPermissionLevelType(d.Permissions.Notes)
			perms.NotesFolderPermissionLevel = &level
		}
		if d.Permissions.Journal != "" {
			level := DelegateFolderPermissionLevelType(d.Permissions.Journal)
			perms.JournalFolderPermissionLevel = &level
		}
		eu.DelegatePermissions = perms
	}

	return eu
}

// ---------------------------------------------------------------------------
// Helper: convert EWS permissions to semcore permissions
// ---------------------------------------------------------------------------

// permissionsFromEWS converts EWS DelegatePermissionsType to semcore DelegateFolderPermissions.
func permissionsFromEWS(ep *DelegatePermissionsType) semcore.DelegateFolderPermissions {
	var perms semcore.DelegateFolderPermissions
	if ep == nil {
		return perms
	}
	if ep.CalendarFolderPermissionLevel != nil {
		perms.Calendar = semcore.DelegateFolderPermissionLevel(*ep.CalendarFolderPermissionLevel)
	}
	if ep.InboxFolderPermissionLevel != nil {
		perms.Inbox = semcore.DelegateFolderPermissionLevel(*ep.InboxFolderPermissionLevel)
	}
	if ep.TasksFolderPermissionLevel != nil {
		perms.Tasks = semcore.DelegateFolderPermissionLevel(*ep.TasksFolderPermissionLevel)
	}
	if ep.ContactsFolderPermissionLevel != nil {
		perms.Contacts = semcore.DelegateFolderPermissionLevel(*ep.ContactsFolderPermissionLevel)
	}
	if ep.NotesFolderPermissionLevel != nil {
		perms.Notes = semcore.DelegateFolderPermissionLevel(*ep.NotesFolderPermissionLevel)
	}
	if ep.JournalFolderPermissionLevel != nil {
		perms.Journal = semcore.DelegateFolderPermissionLevel(*ep.JournalFolderPermissionLevel)
	}
	return perms
}
