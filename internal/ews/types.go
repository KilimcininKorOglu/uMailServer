// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. It projects the canonical semcore identity and sync surfaces
// through the EWS wire contract, giving Exchange-style clients a real
// folder/item identity surface without requiring MAPI/HTTP.
//
// This file defines shared EWS SOAP types used across all operation handlers.
package ews

import (
	"encoding/xml"
	"time"
)

// ---------------------------------------------------------------------------
// SOAP namespaces
// ---------------------------------------------------------------------------

const (
	// SOAPEnvelopeNS is the SOAP 1.1 envelope namespace.
	SOAPEnvelopeNS = "http://schemas.xmlsoap.org/soap/envelope/"
	// EWSMessagesNS is the EWS messages namespace.
	EWSMessagesNS = "http://schemas.microsoft.com/exchange/services/2006/messages"
	// EWSTypesNS is the EWS types namespace.
	EWSTypesNS = "http://schemas.microsoft.com/exchange/services/2006/types"
)

// SOAPNamespaces exposes namespace strings for use in XML marshalling.
var SOAPNamespaces = struct {
	EWSMessages string
	EWSTypes    string
}{
	EWSMessages: EWSMessagesNS,
	EWSTypes:    EWSTypesNS,
}

// ---------------------------------------------------------------------------
// SOAP envelope
// ---------------------------------------------------------------------------

// SOAPEnvelope is the top-level SOAP 1.1 message envelope.
type SOAPEnvelope struct {
	XMLName   xml.Name `xml:"soap:Envelope"`
	XmlnsSOAP string   `xml:"xmlns:soap,attr"`
	Header    *SOAPHeader
	Body      interface{}
}

// SOAPHeader is the SOAP header block.
type SOAPHeader struct {
	XMLName        xml.Name `xml:"soap:Header"`
	ServerVersion  *ServerVersion
	PizzaServerVer PizzaServerVersion `xml:"ServerVersion"`
}

// ServerVersion is sent by the server in every response.
type ServerVersion struct {
	XMLName          xml.Name `xml:"ServerVersion"`
	XMLNS            string   `xml:"xmlns,attr"`
	MajorVersion     int      `xml:"MajorVersion,attr"`
	MinorVersion     int      `xml:"MinorVersion,attr"`
	MajorBuildNumber int      `xml:"MajorBuildNumber,attr"`
	MinorBuildNumber int      `xml:"MinorBuildNumber,attr"`
}

// NewServerVersion returns a server version suitable for EWS responses.
func NewServerVersion() *ServerVersion {
	return &ServerVersion{
		XMLNS:            EWSTypesNS,
		MajorVersion:     15,
		MinorVersion:     0,
		MajorBuildNumber: 2251,
		MinorBuildNumber: 53,
	}
}

// PizzaServerVersion is a compatibility placeholder.
type PizzaServerVersion struct{}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// ErrorCode values used in EWS SOAP fault responses.
type ErrorCode string

const (
	ErrNoError                  ErrorCode = "NoError"
	ErrErrorAccessDenied        ErrorCode = "ErrorAccessDenied"
	ErrErrorMailboxNotFound     ErrorCode = "ErrorMailboxNotFound"
	ErrErrorFolderNotFound      ErrorCode = "ErrorFolderNotFound"
	ErrErrorItemNotFound        ErrorCode = "ErrorItemNotFound"
	ErrErrorItemIdOrChangeKey   ErrorCode = "ErrorItemIdOrChangeKey"
	ErrErrorFolderIdOrChangeKey ErrorCode = "ErrorFolderIdOrChangeKey"
	ErrErrorVersionMismatch     ErrorCode = "ErrorVersionMismatch"
	ErrErrorStaleObject         ErrorCode = "ErrorStaleObject"
	ErrErrorSync                ErrorCode = "ErrorSync"
	ErrErrorInternalServer      ErrorCode = "ErrorInternalServerError"
	ErrErrorNotImplemented      ErrorCode = "ErrorNotImplemented"
	ErrErrorInvalidOperation    ErrorCode = "ErrorInvalidOperation"
	ErrErrorInvalidId           ErrorCode = "ErrorInvalidId"
	ErrErrorInvalidChangeKey    ErrorCode = "ErrorInvalidChangeKey"
	ErrErrorChangeKeyRequired   ErrorCode = "ErrorChangeKeyRequired"
	// Send identity error codes (VAL-DIR-004, VAL-DIR-005)
	ErrErrorSendDenied ErrorCode = "ErrorSendDenied"
	// Delegate-specific error codes (VAL-DIR-002, VAL-DIR-003, VAL-DIR-013)
	ErrErrorDelegateAlreadyExists        ErrorCode = "ErrorDelegateAlreadyExists"
	ErrErrorNotDelegate                  ErrorCode = "ErrorNotDelegate"
	ErrErrorInvalidDelegateUserId        ErrorCode = "ErrorInvalidDelegateUserId"
	ErrErrorInvalidDelegatePermission    ErrorCode = "ErrorInvalidDelegatePermission"
	ErrErrorDelegateCannotAddOwner       ErrorCode = "ErrorDelegateCannotAddOwner"
	ErrErrorDelegateNoUser               ErrorCode = "ErrorDelegateNoUser"
	ErrErrorDelegateMissingConfiguration ErrorCode = "ErrorDelegateMissingConfiguration"
	ErrErrorAddDelegatesFailed           ErrorCode = "ErrorAddDelegatesFailed"
	ErrErrorRemoveDelegatesFailed        ErrorCode = "ErrorRemoveDelegatesFailed"
	ErrErrorUpdateDelegatesFailed        ErrorCode = "ErrorUpdateDelegatesFailed"
	ErrErrorDelegateValidationFailed     ErrorCode = "ErrorDelegateValidationFailed"
	// Subscription and sync session error codes (VAL-CROSS-008)
	ErrErrorSubscriptionDrained ErrorCode = "ErrorSubscriptionDrained"
	// Directory resolution.
	ErrErrorNameResolutionNoResults ErrorCode = "ErrorNameResolutionNoResults"
)

// ---------------------------------------------------------------------------
// EWS type elements
// ---------------------------------------------------------------------------

// DistinguishedFolderName is an EWS distinguished folder identifier.
type DistinguishedFolderName struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	ID      string   `xml:"Id,attr"`
	Mailbox *MailboxType
}

// MailboxType represents an SMTP mailbox in EWS.
type MailboxType struct {
	XMLName   xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
	Email     string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress"`
	Name      string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
	ItemID    string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId,omitempty"`
	MailboxID string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxId,omitempty"`
}

// DistinguishedFolderIDs maps EWS distinguished folder names to semcore folder roles.
// NOTE: "calendar" and "contacts" are included to support EWS folder operations
// (GetFolder, FindItem, etc.) in addition to the CalDAV/CardDAV storage paths
// used by CreateCalendarItem/CreateContact. The calendar and contacts folders
// are backed by the collaboration store (collabStore), not the standard message store.
var DistinguishedFolderIDs = map[string]string{
	"msgfolderroot": "root",
	"root":          "root",
	"inbox":         "inbox",
	"drafts":        "drafts",
	"sentitems":     "sent",
	"deleteditems":  "trash",
	"junkemail":     "spam",
	"archive":       "archive",
	"outbox":        "outbox",
	"calendar":      "calendar",
	"contacts":      "contacts",
	"tasks":         "tasks",
	"notes":         "notes",
}

// DistinguishedFolderIdType represents a DistinguishedFolderId element that can
// optionally carry a Mailbox child element for delegate targeting.
// When the Mailbox element is present, it indicates the target owner's mailbox
// that the authenticated delegate is acting on behalf of.
type DistinguishedFolderIdType struct {
	XMLName xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	ID      string             `xml:"Id,attr"`
	Mailbox *MailboxTypeSimple `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox,omitempty"`
}

// SavedDistinguishedFolderIdType wraps DistinguishedFolderId for use in
// SavedItemFolderId, avoiding namespace/tag conflicts with the types variant.
type SavedDistinguishedFolderIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DistinguishedFolderId"`
	ID      string   `xml:"Id,attr"`
}

// MailboxTypeSimple holds an email address for delegate mailbox targeting.
type MailboxTypeSimple struct {
	XMLName      xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
	EmailAddress string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress,omitempty"`
}

// FolderResponseShape defines the folder properties to return.
type FolderResponseShape struct {
	// BaseShape: IdOnly, Default, AllProperties.
	BaseShape            string                    `xml:"http://schemas.microsoft.com/exchange/services/2006/types BaseShape,omitempty"`
	AdditionalProperties *AdditionalPropertiesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types AdditionalProperties,omitempty"`
}

// AdditionalPropertiesType specifies additional folder properties to return.
type AdditionalPropertiesType struct {
	XMLName   xml.Name   `xml:"http://schemas.microsoft.com/exchange/services/2006/types AdditionalProperties"`
	FieldURIs []FieldURI `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
}

// FieldURI identifies a property by its URI.
type FieldURI struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
	URI     string   `xml:"FieldURI,attr"`
}

// FolderShape constants for BaseShape.
const (
	FolderIdOnly        = "IdOnly"
	FolderDefault       = "Default"
	FolderAllProperties = "AllProperties"
)

// ItemResponseShape defines the item properties to return.
type ItemResponseShape struct {
	XMLName              xml.Name                  `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemResponseShape"`
	BaseShape            string                    `xml:"http://schemas.microsoft.com/exchange/services/2006/types BaseShape,omitempty"`
	AdditionalProperties *AdditionalPropertiesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types AdditionalProperties,omitempty"`
}

// FolderIdComponents holds FolderId or ParentFolderId attributes.
type FolderIdComponents struct {
	ID string `xml:"Id,attr"`
	CK string `xml:"ChangeKey,attr,omitempty"`
}

// FolderType is the EWS Folder element (IPF.Note by default).
type FolderType struct {
	XMLName          xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
	FolderID         FolderIdComponents `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	ParentFolderID   FolderIdComponents `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	DisplayName      string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName,omitempty"`
	UnreadCount      int                `xml:"http://schemas.microsoft.com/exchange/services/2006/types UnreadCount,omitempty"`
	TotalCount       int                `xml:"http://schemas.microsoft.com/exchange/services/2006/types TotalCount,omitempty"`
	ChildFolderCount int                `xml:"http://schemas.microsoft.com/exchange/services/2006/types ChildFolderCount,omitempty"`
	FolderClass      string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderClass,omitempty"`
	EffectiveRights  struct {
		VALUE int `xml:",chardata"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types EffectiveRights,omitempty"`
}

// ContactsFolderType is a contacts folder.
type ContactsFolderType struct {
	FolderType
}

// CalendarFolderType is a calendar folder.
type CalendarFolderType struct {
	FolderType
}

// TasksFolderType is a tasks folder.
type TasksFolderType struct {
	FolderType
}

// ---------------------------------------------------------------------------
// Timestamps
// ---------------------------------------------------------------------------

// FormatEWSDateTime formats a time as an EWS-compatible string.
func FormatEWSDateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ParseEWSDateTime parses an EWS-compatible datetime string.
func ParseEWSDateTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}
