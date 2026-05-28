// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file provides the OOF (out-of-office) and inbox-rule
// operation handlers that project the canonical semcore policy model through
// the EWS wire contract.
//
// OOF operations (GetUserOofSettings, SetUserOofSettings) expose the canonical
// OOFPolicy through EWS, satisfying VAL-COLLAB-007 (OOF scheduling is
// authoritative and time-bounded), VAL-COLLAB-008 (OOF suppression prevents
// loops and spam), and VAL-COLLAB-013 (OOF audience and domain policy are
// enforced consistently).
//
// Inbox-rule operations (GetInboxRules, UpdateInboxRules) expose the canonical
// Rule list through EWS, satisfying VAL-COLLAB-009 (inbox rules execute in
// deterministic order with explicit parity boundaries) and VAL-COLLAB-014
// (rule edits take effect on the next message).
//
// The BoltPolicyStore is the authoritative persistence layer for both OOF
// policies and inbox rules. Policy compilation to Sieve (via semcore
// CompilePolicyToSieve) produces the runtime execution artifact, but the
// canonical state is always the semcore policy.
package ews

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// OOF (Out-of-Office) EWS Types
// ---------------------------------------------------------------------------

// OofState is the OOF enabled state.
type OofState string

const (
	OofStateDisabled  OofState = "Disabled"
	OofStateEnabled   OofState = "Enabled"
	OofStateScheduled OofState = "Scheduled"
)

// ExternalAudience is who receives external OOF replies.
type ExternalAudience string

const (
	ExternalAudienceNone  ExternalAudience = "None"
	ExternalAudienceKnown ExternalAudience = "Known"
	ExternalAudienceAll  ExternalAudience = "All"
)

// Duration represents a time window for OOF.
type Duration struct {
	StartTime string `xml:"http://schemas.microsoft.com/exchange/services/2006/types StartTime"` // EWS dateTime
	EndTime   string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EndTime"`   // EWS dateTime
}

// ReplyBody holds the OOF reply text.
type ReplyBody struct {
	Message string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	Lang    string `xml:"xml:lang,attr,omitempty"`
}

// UserOofSettings is the complete OOF settings blob.
type UserOofSettings struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types UserOofSettings"`

	OofState        OofState  `xml:"http://schemas.microsoft.com/exchange/services/2006/types OofState"`
	ExternalAudience ExternalAudience `xml:"http://schemas.microsoft.com/exchange/services/2006/types ExternalAudience"`
	Duration        *Duration `xml:"http://schemas.microsoft.com/exchange/services/2006/types Duration,omitempty"`
	InternalReply   *ReplyBody `xml:"http://schemas.microsoft.com/exchange/services/2006/types InternalReply,omitempty"`
	ExternalReply   *ReplyBody `xml:"http://schemas.microsoft.com/exchange/services/2006/types ExternalReply,omitempty"`
}

// ---------------------------------------------------------------------------
// OOF Request / Response types
// ---------------------------------------------------------------------------

// GetUserOofSettingsRequest is the EWS GetUserOofSettings operation request.
type GetUserOofSettingsRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserOofSettingsRequest"`
	Mailbox struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
		Email  string  `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
}

// GetUserOofSettingsResponse is the EWS GetUserOofSettings operation response.
type GetUserOofSettingsResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserOofSettingsResponse"`

	ResponseMessage    ResponseMessageType   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	OofSettings       *UserOofSettings     `xml:"http://schemas.microsoft.com/exchange/services/2006/types OofSettings,omitempty"` //nolint:staticcheck
	AllowExternalOof  ExternalAudience     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AllowExternalOof,omitempty"`
}

// SetUserOofSettingsRequest is the EWS SetUserOofSettings operation request.
type SetUserOofSettingsRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SetUserOofSettingsRequest"`
	Mailbox struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
		Email  string  `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
	UserOofSettings *UserOofSettings `xml:"http://schemas.microsoft.com/exchange/services/2006/types UserOofSettings"`
}

// SetUserOofSettingsResponse is the EWS SetUserOofSettings operation response.
type SetUserOofSettingsResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SetUserOofSettingsResponse"`

	ResponseMessage ResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
}

// ---------------------------------------------------------------------------
// Inbox Rules EWS Types
// ---------------------------------------------------------------------------

// RulePredicatesType represents rule conditions in EWS.
type RulePredicatesType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types RulePredicates"`

	//nolint:staticcheck // SA5008: ArrayOfStringsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	Categories                *ArrayOfStringsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
	//nolint:staticcheck // SA5008: ArrayOfStringsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	ContainsBodyStrings      *ArrayOfStringsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContainsBodyStrings,omitempty"`
	//nolint:staticcheck // SA5008: ArrayOfStringsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	ContainsHeaderStrings    *ArrayOfStringsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContainsHeaderStrings,omitempty"`
	//nolint:staticcheck // SA5008: ArrayOfStringsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	ContainsRecipientStrings *ArrayOfStringsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContainsRecipientStrings,omitempty"`
	//nolint:staticcheck // SA5008: ArrayOfStringsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	ContainsSenderStrings    *ArrayOfStringsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContainsSenderStrings,omitempty"`
	//nolint:staticcheck // SA5008: ArrayOfStringsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	ContainsSubjectOrBody    *ArrayOfStringsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContainsSubjectOrBodyStrings,omitempty"`
	//nolint:staticcheck // SA5008: ArrayOfStringsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	ContainsSubjectStrings   *ArrayOfStringsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContainsSubjectStrings,omitempty"`
	HasAttachments           *bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types HasAttachments,omitempty"`
	Importance               string               `xml:"http://schemas.microsoft.com/exchange/services/2006/types Importance,omitempty"`
	IsAutomaticForward       *bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsAutomaticForward,omitempty"`
	IsAutomaticReply         *bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsAutomaticReply,omitempty"`
	IsReadReceipt            *bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsReadReceipt,omitempty"`
	NotSentToMe              *bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types NotSentToMe,omitempty"`
	SentCcMe                 *bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types SentCcMe,omitempty"`
	SentOnlyToMe             *bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types SentOnlyToMe,omitempty"`
	SentToMe                 *bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types SentToMe,omitempty"`
	SentToOrCcMe             *bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types SentToOrCcMe,omitempty"`
	SentToAddresses          *SentToAddressesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types SentToAddresses,omitempty"`
	WithinDateRange          *RulePredicateDateRange    `xml:"http://schemas.microsoft.com/exchange/services/2006/types WithinDateRange,omitempty"`
	WithinSizeRange          *RulePredicateSizeRange   `xml:"http://schemas.microsoft.com/exchange/services/2006/types WithinSizeRange,omitempty"`
}

// ArrayOfStringsType holds a list of strings.
type ArrayOfStringsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ArrayOfStrings"`
	Strings []string `xml:"http://schemas.microsoft.com/exchange/services/2006/types String,omitempty"`
}

// MailboxArrayType holds a list of mailboxes (reused for multiple EWS elements).
type MailboxArrayType struct {
	XMLName   xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/types ArrayOfEmailAddresses"`
	Addresses []MailboxType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox,omitempty"`
}

// RulePredicateDateRange is date range for rule predicate.
type RulePredicateDateRange struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types WithinDateRange"`
	StartDate string `xml:"http://schemas.microsoft.com/exchange/services/2006/types StartDateTime,omitempty"`
	EndDate   string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EndDateTime,omitempty"`
}

// RulePredicateSizeRange is size range for rule predicate.
type RulePredicateSizeRange struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types WithinSizeRange"`
	MinSize string `xml:"http://schemas.microsoft.com/exchange/services/2006/types MinimumSize,omitempty"`
	MaxSize string `xml:"http://schemas.microsoft.com/exchange/services/2006/types MaximumSize,omitempty"`
}

// ForwardAsAttachmentToRecipientsType is the ForwardAsAttachmentToRecipients element.
type ForwardAsAttachmentToRecipientsType struct {
	XMLName   xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/types ForwardAsAttachmentToRecipients"`
	Addresses []MailboxType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox,omitempty"`
}

// ForwardToRecipientsType is the ForwardToRecipients element.
type ForwardToRecipientsType struct {
	XMLName   xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/types ForwardToRecipients"`
	Addresses []MailboxType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox,omitempty"`
}

// RedirectToRecipientsType is the RedirectToRecipients element.
type RedirectToRecipientsType struct {
	XMLName   xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/types RedirectToRecipients"`
	Addresses []MailboxType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox,omitempty"`
}

// SentToAddressesType is the SentToAddresses element.
type SentToAddressesType struct {
	XMLName   xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/types SentToAddresses"`
	Addresses []MailboxType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox,omitempty"`
}

// ConditionsType wraps RulePredicatesType with XMLName "Conditions".
type ConditionsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Conditions"`
	*RulePredicatesType
}

// ExceptionsType wraps RulePredicatesType with XMLName "Exceptions".
type ExceptionsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Exceptions"`
	*RulePredicatesType
}

// RuleActionsType represents rule actions in EWS.
type RuleActionsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleActions"`

	//nolint:staticcheck // SA5008: ArrayOfStringsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	AssignCategories      *ArrayOfStringsType               `xml:"http://schemas.microsoft.com/exchange/services/2006/types AssignCategories,omitempty"`
	//nolint:staticcheck // SA5008: TargetFolderIdType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	CopyToFolder         *TargetFolderIdType             `xml:"http://schemas.microsoft.com/exchange/services/2006/types CopyToFolder,omitempty"`
	Delete               *bool                             `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete,omitempty"`
	ForwardAsAttachment  *ForwardAsAttachmentToRecipientsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ForwardAsAttachmentToRecipients,omitempty"`
	ForwardTo            *ForwardToRecipientsType         `xml:"http://schemas.microsoft.com/exchange/services/2006/types ForwardToRecipients,omitempty"`
	MarkAsRead           *bool                             `xml:"http://schemas.microsoft.com/exchange/services/2006/types MarkAsRead,omitempty"`
	MarkImportance       string                             `xml:"http://schemas.microsoft.com/exchange/services/2006/types MarkImportance,omitempty"`
	//nolint:staticcheck // SA5008: TargetFolderIdType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	MoveToFolder         *TargetFolderIdType             `xml:"http://schemas.microsoft.com/exchange/services/2006/types MoveToFolder,omitempty"`
	PermanentDelete      *bool                             `xml:"http://schemas.microsoft.com/exchange/services/2006/types PermanentDelete,omitempty"`
	RedirectTo           *RedirectToRecipientsType         `xml:"http://schemas.microsoft.com/exchange/services/2006/types RedirectToRecipients,omitempty"`
	ServerReplyWithMessage *string                         `xml:"http://schemas.microsoft.com/exchange/services/2006/types ServerReplyWithMessage,omitempty"`
	StopProcessingRules  *bool                             `xml:"http://schemas.microsoft.com/exchange/services/2006/types StopProcessingRules,omitempty"`
}

// TargetFolderIdType holds a folder ID reference for rule actions.
type TargetFolderIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types TargetFolderId"`
	Folder  *struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
		ID     string   `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId,omitempty"`
}

// RuleType is the EWS representation of a inbox rule.
type RuleType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Rule"`

	RuleID         string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleId,omitempty"`
	DisplayName    string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName"`
	Priority       int              `xml:"http://schemas.microsoft.com/exchange/services/2006/types Priority"`
	IsEnabled      bool             `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsEnabled"`
	IsNotSupported *bool           `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsNotSupported,omitempty"`
	IsInError      *bool           `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsInError,omitempty"`
	Conditions     *ConditionsType  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Conditions,omitempty"`
	Exceptions     *ExceptionsType  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Exceptions,omitempty"`
	//nolint:staticcheck // SA5008: RuleActionsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	Actions        *RuleActionsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Actions,omitempty"`
}

// ArrayOfRulesType holds a list of rules.
type ArrayOfRulesType struct {
	XMLName xml.Name  `xml:"http://schemas.microsoft.com/exchange/services/2006/types ArrayOfRules"`
	Rules   []RuleType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Rule,omitempty"`
}

// ---------------------------------------------------------------------------
// Inbox Rules Request / Response types
// ---------------------------------------------------------------------------

// GetInboxRulesRequest is the EWS GetInboxRules operation request.
type GetInboxRulesRequest struct {
	XMLName           xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetInboxRules"`
	MailboxSmtpAddress string  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MailboxSmtpAddress,omitempty"`
}

// GetInboxRulesResponse is the EWS GetInboxRules operation response.
type GetInboxRulesResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetInboxRulesResponse"`

	ResponseMessageType
	OutlookRuleBlobExists *bool           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages OutlookRuleBlobExists,omitempty"`
	//nolint:staticcheck // SA5008: ArrayOfRulesType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	InboxRules           *ArrayOfRulesType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages InboxRules,omitempty"`
}

// UpdateInboxRulesRequest is the EWS UpdateInboxRules operation request.
type UpdateInboxRulesRequest struct {
	XMLName             xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateInboxRules"`
	MailboxSmtpAddress   string  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MailboxSmtpAddress,omitempty"`
	RemoveOutlookRuleBlob *bool  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RemoveOutlookRuleBlob,omitempty"`
	Operations          *ArrayOfRuleOperationsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleOperations"`
}

// ArrayOfRuleOperationsType holds the list of rule operations.
type ArrayOfRuleOperationsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleOperations"`
	// choice of CreateRuleOperation, SetRuleOperation, DeleteRuleOperation
	Operations []RuleOperationType `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleOperation,omitempty"`
}

// RuleOperationType is the interface for rule operations.
type RuleOperationType interface {
	isRuleOperation()
}

// CreateRuleOperationType creates a new rule.
type CreateRuleOperationType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types CreateRuleOperation"`
	Rule    RuleType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Rule"`
}

func (CreateRuleOperationType) isRuleOperation() {}

// SetRuleOperationType updates an existing rule.
type SetRuleOperationType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types SetRuleOperation"`
	Rule    RuleType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Rule"`
}

func (SetRuleOperationType) isRuleOperation() {}

// DeleteRuleOperationType deletes an existing rule.
type DeleteRuleOperationType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DeleteRuleOperation"`
	RuleID  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleId"`
}

func (DeleteRuleOperationType) isRuleOperation() {}

// UpdateInboxRulesResponse is the EWS UpdateInboxRules operation response.
type UpdateInboxRulesResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateInboxRulesResponse"`

	ResponseMessageType
	//nolint:staticcheck // SA5008: ArrayOfRuleOperationErrorsType.XMLName conflicts with element name but Go XML encoder uses tag element name.
	RuleOperationErrors *ArrayOfRuleOperationErrorsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RuleOperationErrors,omitempty"`
}

// ArrayOfRuleOperationErrorsType holds rule operation errors.
type ArrayOfRuleOperationErrorsType struct {
	XMLName xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/types ArrayOfRuleOperationErrors"`
	Errors  []RuleOperationErrorType `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleOperationError,omitempty"`
}

// RuleOperationErrorType describes an error for a specific rule operation.
type RuleOperationErrorType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleOperationError"`

	RuleID     string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types RuleId,omitempty"`
	ErrorCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types ErrorCode"`
	ErrorMessage string `xml:"http://schemas.microsoft.com/exchange/services/2006/types ErrorMessage,omitempty"`
	FieldURI   string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI,omitempty"`
}

// ---------------------------------------------------------------------------
// Response message type
// ---------------------------------------------------------------------------

// ResponseMessageType is the EWS response message wrapper.
type ResponseMessageType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`

	ResponseClass    string     `xml:"ResponseClass,attr,omitempty"`
	ResponseCode     ErrorCode  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	ErrorMessage     string     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ErrorMessage,omitempty"`
	DescriptiveLinkKey *int     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DescriptiveLinkKey,omitempty"`
}

// ResponseClass constants.
const (
	ResponseClassSuccess = "Success"
	ResponseClassWarning = "Warning"
	ResponseClassError   = "Error"
)

// ---------------------------------------------------------------------------
// EWS OOF operation handlers
// ---------------------------------------------------------------------------

// handleGetUserOofSettings implements the EWS GetUserOofSettings operation.
// Satisfies VAL-COLLAB-007 (OOF scheduling is authoritative and time-bounded),
// VAL-COLLAB-008 (OOF suppression), and VAL-COLLAB-013 (OOF audience policy).
func (s *Server) handleGetUserOofSettings(ctx context.Context, soapBody []byte) []byte {
	var req GetUserOofSettingsRequest
	if err := decodeRequest(soapBody, &req); err != nil {
		return s.errorResponseXML("GetUserOofSettings", ErrErrorInternalServer, "failed to parse request: "+err.Error())
	}

	email := req.Mailbox.Email
	if email == "" {
		// Try to get from authenticated context
		if u, ok := ctx.Value("user").(string); ok && u != "" {
			email = u
		}
	}

	mailboxID, err := s.resolveMailboxByEmail(ctx, email)
	if err != nil {
		resp := GetUserOofSettingsResponse{
			ResponseMessage: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorMailboxNotFound,
			},
		}
		return buildResponseEnvelope(resp)
	}

	// Load OOF policy from BoltPolicyStore
	if s.policyStore == nil {
		resp := GetUserOofSettingsResponse{
			ResponseMessage: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
				ErrorMessage: "policy store not available",
			},
		}
		return buildResponseEnvelope(resp)
	}

	oofID, err := semcore.NewOOFId(mailboxID.String())
	if err != nil {
		resp := GetUserOofSettingsResponse{
			ResponseMessage: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
			},
		}
		return buildResponseEnvelope(resp)
	}

	policy, err := s.policyStore.GetOOF(oofID)
	if err != nil {
		// No OOF policy exists — return a default disabled response.
		// This is not an error; it just means no OOF has been configured.
		resp := GetUserOofSettingsResponse{
			ResponseMessage: ResponseMessageType{
				ResponseClass: ResponseClassSuccess,
				ResponseCode:  ErrNoError,
			},
			OofSettings: &UserOofSettings{
				OofState:        OofStateDisabled,
				ExternalAudience: ExternalAudienceNone,
			},
			AllowExternalOof: ExternalAudienceNone,
		}
		return buildResponseEnvelope(resp)
	}

	// Map semcore OOFPolicy to EWS UserOofSettings
	oofSettings := oofPolicyToEWS(policy)
	resp := GetUserOofSettingsResponse{
		ResponseMessage: ResponseMessageType{
			ResponseClass: ResponseClassSuccess,
			ResponseCode:  ErrNoError,
		},
		OofSettings:      oofSettings,
		AllowExternalOof: externalAudienceFromOOF(policy.Audience),
	}
	return buildResponseEnvelope(resp)
}

// handleSetUserOofSettings implements the EWS SetUserOofSettings operation.
// Satisfies VAL-COLLAB-007, VAL-COLLAB-008, and VAL-COLLAB-013.
func (s *Server) handleSetUserOofSettings(ctx context.Context, soapBody []byte) []byte {
	var req SetUserOofSettingsRequest
	if err := decodeRequest(soapBody, &req); err != nil {
		return s.errorResponseXML("SetUserOofSettings", ErrErrorInternalServer, "failed to parse request: "+err.Error())
	}

	email := req.Mailbox.Email
	if email == "" {
		if u, ok := ctx.Value("user").(string); ok && u != "" {
			email = u
		}
	}

	mailboxID, err := s.resolveMailboxByEmail(ctx, email)
	if err != nil {
		resp := SetUserOofSettingsResponse{
			ResponseMessage: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorMailboxNotFound,
			},
		}
		return buildResponseEnvelope(resp)
	}

	// Build OOFPolicy from EWS request
	policy, err := oofPolicyFromEWS(ctx, mailboxID, req.UserOofSettings)
	if err != nil {
		resp := SetUserOofSettingsResponse{
			ResponseMessage: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
				ErrorMessage:  err.Error(),
			},
		}
		return buildResponseEnvelope(resp)
	}

	// Persist to BoltPolicyStore
	if err := s.policyStore.PutOOF(policy); err != nil {
		resp := SetUserOofSettingsResponse{
			ResponseMessage: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
				ErrorMessage:  "failed to save OOF policy",
			},
		}
		return buildResponseEnvelope(resp)
	}

	// If OOF is enabled, recompile and activate Sieve script
	if policy.Enabled {
		if err := s.recompileSieveForMailbox(ctx, mailboxID); err != nil {
			// Log but don't fail the write — the policy is saved
			s.logger.Warn("failed to recompile sieve after OOF update", "mailbox", mailboxID, "error", err)
		}
	}

	resp := SetUserOofSettingsResponse{
		ResponseMessage: ResponseMessageType{
			ResponseClass: ResponseClassSuccess,
			ResponseCode:  ErrNoError,
		},
	}
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// EWS Inbox Rules operation handlers
// ---------------------------------------------------------------------------

// handleGetInboxRules implements the EWS GetInboxRules operation.
// Satisfies VAL-COLLAB-009 (inbox rules execute in deterministic order
// with explicit parity boundaries) and VAL-COLLAB-014 (rule edits take
// effect on the next message).
func (s *Server) handleGetInboxRules(ctx context.Context, soapBody []byte) []byte {
	var req GetInboxRulesRequest
	if err := decodeRequest(soapBody, &req); err != nil {
		return s.errorResponseXML("GetInboxRules", ErrErrorInternalServer, "failed to parse request: "+err.Error())
	}

	email := req.MailboxSmtpAddress
	if email == "" {
		if u, ok := ctx.Value("user").(string); ok && u != "" {
			email = u
		}
	}

	mailboxID, err := s.resolveMailboxByEmail(ctx, email)
	if err != nil {
		resp := GetInboxRulesResponse{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorMailboxNotFound,
			},
		}
		return buildResponseEnvelope(resp)
	}

	// Load rules from BoltPolicyStore
	if s.policyStore == nil {
		resp := GetInboxRulesResponse{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
				ErrorMessage: "policy store not available",
			},
		}
		return buildResponseEnvelope(resp)
	}

	rules, err := s.policyStore.ListRules(mailboxID)
	if err != nil {
		resp := GetInboxRulesResponse{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
			},
		}
		return buildResponseEnvelope(resp)
	}

	// Map semcore Rules to EWS RuleType list
	ewsRules := make([]RuleType, 0, len(rules))
	for _, rule := range rules {
		ewsRules = append(ewsRules, ruleToEWS(rule))
	}

	resp := GetInboxRulesResponse{
		ResponseMessageType: ResponseMessageType{
			ResponseClass: ResponseClassSuccess,
			ResponseCode:  ErrNoError,
		},
		OutlookRuleBlobExists: boolPtr(false),
	}
	if len(ewsRules) > 0 {
		resp.InboxRules = &ArrayOfRulesType{Rules: ewsRules}
	}
	return buildResponseEnvelope(resp)
}

// handleUpdateInboxRules implements the EWS UpdateInboxRules operation.
// Satisfies VAL-COLLAB-009 and VAL-COLLAB-014.
func (s *Server) handleUpdateInboxRules(ctx context.Context, soapBody []byte) []byte {
	var req UpdateInboxRulesRequest
	if err := decodeRequest(soapBody, &req); err != nil {
		return s.errorResponseXML("UpdateInboxRules", ErrErrorInternalServer, "failed to parse request: "+err.Error())
	}

	email := req.MailboxSmtpAddress
	if email == "" {
		if u, ok := ctx.Value("user").(string); ok && u != "" {
			email = u
		}
	}

	mailboxID, err := s.resolveMailboxByEmail(ctx, email)
	if err != nil {
		resp := UpdateInboxRulesResponse{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorMailboxNotFound,
			},
		}
		return buildResponseEnvelope(resp)
	}

	if req.Operations == nil || len(req.Operations.Operations) == 0 {
		resp := UpdateInboxRulesResponse{
			ResponseMessageType: ResponseMessageType{
				ResponseClass: ResponseClassError,
				ResponseCode:  ErrErrorInternalServer,
				ErrorMessage:  "no operations provided",
			},
		}
		return buildResponseEnvelope(resp)
	}

	// Process each operation in order
	var opErrors []RuleOperationErrorType
	for i, op := range req.Operations.Operations {
		if err := s.applyRuleOperation(ctx, mailboxID, op); err != nil {
			opErrors = append(opErrors, RuleOperationErrorType{
				ErrorCode:    "ErrorInternalServerError",
				ErrorMessage: err.Error(),
			})
			// For now, collect errors but continue processing remaining operations
			_ = i // suppress unused warning
		}
	}

	// Recompile Sieve script for the mailbox after any mutation
	if err := s.recompileSieveForMailbox(ctx, mailboxID); err != nil {
		s.logger.Warn("failed to recompile sieve after rules update", "mailbox", mailboxID, "error", err)
	}

	resp := UpdateInboxRulesResponse{
		ResponseMessageType: ResponseMessageType{
			ResponseClass: ResponseClassSuccess,
			ResponseCode:  ErrNoError,
		},
	}
	if len(opErrors) > 0 {
		resp.RuleOperationErrors = &ArrayOfRuleOperationErrorsType{Errors: opErrors}
	}
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// Helper: resolve mailbox ID from email address
// ---------------------------------------------------------------------------

// resolveMailboxByEmail resolves a MailboxId from an SMTP email address.
func (s *Server) resolveMailboxByEmail(ctx context.Context, email string) (semcore.MailboxId, error) {
	if email == "" {
		return semcore.MailboxId{}, errors.New("empty email")
	}
	mailboxID, err := semcore.NewMailboxId(email)
	if err != nil {
		return semcore.MailboxId{}, err
	}
	return mailboxID, nil
}

// ---------------------------------------------------------------------------
// Helper: recompile Sieve for a mailbox from canonical policy
// ---------------------------------------------------------------------------

// recompileSieveForMailbox recompiles the Sieve script for a mailbox from
// its canonical policy state (rules + OOF) and stores it in the sieve manager.
func (s *Server) recompileSieveForMailbox(ctx context.Context, mailboxID semcore.MailboxId) error {
	if s.sieveMgr == nil {
		return nil
	}

	rules, err := s.policyStore.ListRules(mailboxID)
	if err != nil && !errors.Is(err, nil) {
		return err
	}

	var oofPolicy *semcore.OOFPolicy
	oofID, err := semcore.NewOOFId(mailboxID.String())
	if err == nil {
		oofPolicy, _ = s.policyStore.GetOOF(oofID) //nolint:errcheck
	}

	script := semcore.CompilePolicyToSieve(rules, oofPolicy)
	if err := s.sieveMgr.StoreScript(mailboxID.String(), "active", script); err != nil {
		return err
	}
	return s.sieveMgr.SetActiveScriptByName(mailboxID.String(), "active")
}

// ---------------------------------------------------------------------------
// Apply rule operations
// ---------------------------------------------------------------------------

// applyRuleOperation applies a single rule operation (create/set/delete).
func (s *Server) applyRuleOperation(ctx context.Context, mailboxID semcore.MailboxId, op RuleOperationType) error {
	switch v := op.(type) {
	case CreateRuleOperationType:
		return s.applyCreateRule(ctx, mailboxID, v.Rule)
	case SetRuleOperationType:
		return s.applySetRule(ctx, mailboxID, v.Rule)
	case DeleteRuleOperationType:
		return s.applyDeleteRule(ctx, mailboxID, v.RuleID)
	default:
		return fmt.Errorf("unknown rule operation type")
	}
}

// applyCreateRule creates a new inbox rule.
func (s *Server) applyCreateRule(ctx context.Context, mailboxID semcore.MailboxId, ewsRule RuleType) error {
	rule, err := ruleFromEWS(ewsRule, mailboxID)
	if err != nil {
		return err
	}

	// Assign a new RuleId
	id, err := semcore.NewRuleId("rule-" + generateID())
	if err != nil {
		return err
	}
	rule.ID = id
	rule.MailboxID = mailboxID

	// Assign priority if not set (append to end)
	if rule.Priority == 0 {
		existing, _ := s.policyStore.ListRules(mailboxID) //nolint:errcheck
		rule.Priority = len(existing) + 1
	}

	return s.policyStore.PutRule(rule)
}

// applySetRule updates an existing inbox rule.
func (s *Server) applySetRule(ctx context.Context, mailboxID semcore.MailboxId, ewsRule RuleType) error {
	if ewsRule.RuleID == "" {
		return errors.New("RuleId is required for update")
	}

	ruleID, err := semcore.NewRuleId(ewsRule.RuleID)
	if err != nil {
		return err
	}

	existing, err := s.policyStore.GetRule(ruleID)
	if err != nil {
		return err
	}

	// Only update fields that are provided in the EWS request
	updated, err := mergeRuleFromEWS(existing, ewsRule)
	if err != nil {
		return err
	}

	return s.policyStore.PutRule(updated)
}

// applyDeleteRule deletes an inbox rule.
func (s *Server) applyDeleteRule(ctx context.Context, mailboxID semcore.MailboxId, ruleIDStr string) error {
	ruleID, err := semcore.NewRuleId(ruleIDStr)
	if err != nil {
		return err
	}
	return s.policyStore.DeleteRule(ruleID)
}

// ---------------------------------------------------------------------------
// Map semcore Rule to EWS RuleType
// ---------------------------------------------------------------------------

// ruleToEWS converts a semcore Rule to an EWS RuleType.
func ruleToEWS(rule *semcore.Rule) RuleType {
	ewsRule := RuleType{
		RuleID:      rule.ID.String(),
		DisplayName: rule.Name,
		Priority:    rule.Priority,
		IsEnabled:   rule.Enabled,
		IsNotSupported: boolPtr(false),
		IsInError:   boolPtr(false),
	}

	// Map conditions
	if len(rule.Conditions) > 0 {
		ewsRule.Conditions = conditionsToEWS(rule.Conditions, rule.MatchAll)
	}

	return ewsRule
}

// conditionsToEWS converts semcore RuleConditions to EWS ConditionsType.
func conditionsToEWS(conds []semcore.RuleCondition, matchAll bool) *ConditionsType {
	pred := &RulePredicatesType{}

	for _, cond := range conds {
		switch cond.Kind {
		case semcore.RuleConditionKindFrom:
			if pred.ContainsSenderStrings == nil {
				pred.ContainsSenderStrings = &ArrayOfStringsType{}
			}
			pred.ContainsSenderStrings.Strings = append(pred.ContainsSenderStrings.Strings, cond.Value)
		case semcore.RuleConditionKindTo:
			if pred.ContainsRecipientStrings == nil {
				pred.ContainsRecipientStrings = &ArrayOfStringsType{}
			}
			pred.ContainsRecipientStrings.Strings = append(pred.ContainsRecipientStrings.Strings, cond.Value)
		case semcore.RuleConditionKindSubject:
			if pred.ContainsSubjectStrings == nil {
				pred.ContainsSubjectStrings = &ArrayOfStringsType{}
			}
			pred.ContainsSubjectStrings.Strings = append(pred.ContainsSubjectStrings.Strings, cond.Value)
		case semcore.RuleConditionKindBody:
			if pred.ContainsBodyStrings == nil {
				pred.ContainsBodyStrings = &ArrayOfStringsType{}
			}
			pred.ContainsBodyStrings.Strings = append(pred.ContainsBodyStrings.Strings, cond.Value)
		case semcore.RuleConditionKindSize:
			// Size is handled separately
			pred.WithinSizeRange = &RulePredicateSizeRange{
				MaxSize: cond.Value,
			}
		}
	}

	// Note: MatchAll (allof vs anyof) is implied by presence of multiple conditions
	// EWS clients use conditions AND semantics when multiple are present
	_ = matchAll

	return &ConditionsType{RulePredicatesType: pred}
}

// ---------------------------------------------------------------------------
// Map EWS RuleType to semcore Rule
// ---------------------------------------------------------------------------

// ruleFromEWS converts an EWS RuleType to a semcore Rule.
func ruleFromEWS(ewsRule RuleType, mailboxID semcore.MailboxId) (*semcore.Rule, error) {
	rule := &semcore.Rule{
		Name:     ewsRule.DisplayName,
		Enabled:  ewsRule.IsEnabled,
		Priority: ewsRule.Priority,
	}

	if ewsRule.RuleID != "" {
		id, err := semcore.NewRuleId(ewsRule.RuleID)
		if err == nil {
			rule.ID = id
		}
	}

	rule.MailboxID = mailboxID

	// Map conditions
	if ewsRule.Conditions != nil {
		rule.Conditions, rule.MatchAll = conditionsFromEWS(ewsRule.Conditions.RulePredicatesType)
	}

	// Map actions
	if ewsRule.Actions != nil {
		rule.Actions = actionsFromEWS(ewsRule.Actions)
	}

	rule.Created = time.Now()
	rule.Modified = time.Now()

	return rule, nil
}

// mergeRuleFromEWS merges fields from an EWS RuleType into an existing semcore Rule.
func mergeRuleFromEWS(existing *semcore.Rule, ewsRule RuleType) (*semcore.Rule, error) {
	if ewsRule.DisplayName != "" {
		existing.Name = ewsRule.DisplayName
	}
	existing.Enabled = ewsRule.IsEnabled
	if ewsRule.Priority > 0 {
		existing.Priority = ewsRule.Priority
	}

	if ewsRule.Conditions != nil {
		existing.Conditions, existing.MatchAll = conditionsFromEWS(ewsRule.Conditions.RulePredicatesType)
	}
	if ewsRule.Actions != nil {
		existing.Actions = actionsFromEWS(ewsRule.Actions)
	}

	existing.Modified = time.Now()
	return existing, nil
}

// conditionsFromEWS converts EWS RulePredicatesType to semcore RuleConditions.
func conditionsFromEWS(pred *RulePredicatesType) ([]semcore.RuleCondition, bool) {
	if pred == nil {
		return nil, true
	}
	var conds []semcore.RuleCondition
	matchAll := true // EWS default is allof (AND)

	if pred.ContainsSenderStrings != nil {
		for _, s := range pred.ContainsSenderStrings.Strings {
			conds = append(conds, semcore.RuleCondition{
				Kind:      semcore.RuleConditionKindFrom,
				MatchType: semcore.RuleMatchTypeContains,
				Value:     s,
			})
		}
	}
	if pred.ContainsRecipientStrings != nil {
		for _, s := range pred.ContainsRecipientStrings.Strings {
			conds = append(conds, semcore.RuleCondition{
				Kind:      semcore.RuleConditionKindTo,
				MatchType: semcore.RuleMatchTypeContains,
				Value:     s,
			})
		}
	}
	if pred.ContainsSubjectStrings != nil {
		for _, s := range pred.ContainsSubjectStrings.Strings {
			conds = append(conds, semcore.RuleCondition{
				Kind:      semcore.RuleConditionKindSubject,
				MatchType: semcore.RuleMatchTypeContains,
				Value:     s,
			})
		}
	}
	if pred.ContainsBodyStrings != nil {
		for _, s := range pred.ContainsBodyStrings.Strings {
			conds = append(conds, semcore.RuleCondition{
				Kind:      semcore.RuleConditionKindBody,
				MatchType: semcore.RuleMatchTypeContains,
				Value:     s,
			})
		}
	}
	if pred.ContainsSubjectOrBody != nil {
		for _, s := range pred.ContainsSubjectOrBody.Strings {
			conds = append(conds, semcore.RuleCondition{
				Kind:      semcore.RuleConditionKindBody,
				MatchType: semcore.RuleMatchTypeContains,
				Value:     s,
			})
		}
	}
	if pred.SentToMe != nil && *pred.SentToMe {
		conds = append(conds, semcore.RuleCondition{
			Kind:      semcore.RuleConditionKindAddress,
			MatchType: semcore.RuleMatchTypeContains,
			Value:     "me",
		})
	}
	if pred.HasAttachments != nil && *pred.HasAttachments {
		conds = append(conds, semcore.RuleCondition{
			Kind:      semcore.RuleConditionKindFlag,
			MatchType: semcore.RuleMatchTypeContains,
			Value:     "attachment",
		})
	}
	if pred.WithinSizeRange != nil {
		conds = append(conds, semcore.RuleCondition{
			Kind:      semcore.RuleConditionKindSize,
			MatchType: semcore.RuleMatchTypeContains,
			Value:     pred.WithinSizeRange.MaxSize,
		})
	}

	return conds, matchAll
}

// actionsFromEWS converts EWS RuleActionsType to semcore RuleActions.
func actionsFromEWS(actions *RuleActionsType) []semcore.RuleAction {
	if actions == nil {
		return nil
	}
	var result []semcore.RuleAction

	if actions.MoveToFolder != nil && actions.MoveToFolder.Folder != nil {
		result = append(result, semcore.RuleAction{
			Kind:   semcore.RuleActionKindMoveToFolder,
			Target: actions.MoveToFolder.Folder.ID,
		})
	}
	if actions.Delete != nil && *actions.Delete {
		result = append(result, semcore.RuleAction{
			Kind: semcore.RuleActionKindDelete,
		})
	}
	if actions.MarkAsRead != nil && *actions.MarkAsRead {
		result = append(result, semcore.RuleAction{
			Kind: semcore.RuleActionKindMarkRead,
		})
	}
	if actions.ForwardTo != nil && len(actions.ForwardTo.Addresses) > 0 {
		for _, addr := range actions.ForwardTo.Addresses {
			result = append(result, semcore.RuleAction{
				Kind:      semcore.RuleActionKindForward,
				ForwardTo: addr.Email,
			})
		}
	}
	if actions.ForwardAsAttachment != nil && len(actions.ForwardAsAttachment.Addresses) > 0 {
		for _, addr := range actions.ForwardAsAttachment.Addresses {
			result = append(result, semcore.RuleAction{
				Kind:      semcore.RuleActionKindForwardAsAttachment,
				ForwardTo: addr.Email,
			})
		}
	}
	if actions.RedirectTo != nil && len(actions.RedirectTo.Addresses) > 0 {
		for _, addr := range actions.RedirectTo.Addresses {
			result = append(result, semcore.RuleAction{
				Kind:      semcore.RuleActionKindRedirect,
				ForwardTo: addr.Email,
			})
		}
	}
	if actions.StopProcessingRules != nil && *actions.StopProcessingRules {
		result = append(result, semcore.RuleAction{
			Kind: semcore.RuleActionKindStop,
		})
	}
	if actions.AssignCategories != nil {
		for _, cat := range actions.AssignCategories.Strings {
			result = append(result, semcore.RuleAction{
				Kind:   semcore.RuleActionKindAddHeader,
				HeaderName:  "X-Category",
				HeaderValue: cat,
			})
		}
	}

	return result
}

// ---------------------------------------------------------------------------
// Map semcore OOFPolicy to EWS UserOofSettings
// ---------------------------------------------------------------------------

// oofPolicyToEWS converts a semcore OOFPolicy to EWS UserOofSettings.
func oofPolicyToEWS(policy *semcore.OOFPolicy) *UserOofSettings {
	settings := &UserOofSettings{
		OofState:        oofStateFromOOF(policy),
		ExternalAudience: externalAudienceFromOOF(policy.Audience),
	}

	// Duration (schedule)
	if !policy.StartTime.IsZero() || !policy.EndTime.IsZero() {
		settings.Duration = &Duration{
			StartTime: FormatEWSDateTime(policy.StartTime),
			EndTime:   FormatEWSDateTime(policy.EndTime),
		}
		if !policy.StartTime.IsZero() || !policy.EndTime.IsZero() {
			if !policy.StartTime.IsZero() && !policy.EndTime.IsZero() {
				settings.OofState = OofStateScheduled
			}
		}
	}

	// Reply content
	if policy.TextBody != "" {
		settings.InternalReply = &ReplyBody{Message: policy.TextBody}
		settings.ExternalReply = &ReplyBody{Message: policy.TextBody}
	}

	return settings
}

// oofStateFromOOF derives EWS OofState from a semcore OOFPolicy.
func oofStateFromOOF(policy *semcore.OOFPolicy) OofState {
	if !policy.Enabled {
		return OofStateDisabled
	}
	if !policy.StartTime.IsZero() || !policy.EndTime.IsZero() {
		return OofStateScheduled
	}
	return OofStateEnabled
}

// externalAudienceFromOOF maps semcore OOFAudience to EWS ExternalAudience.
func externalAudienceFromOOF(aud semcore.OOFAudience) ExternalAudience {
	switch aud {
	case semcore.OOFAudienceInternal:
		return ExternalAudienceNone
	case semcore.OOFAudienceExternal:
		return ExternalAudienceKnown
	case semcore.OOFAudienceEveryone:
		return ExternalAudienceAll
	default:
		return ExternalAudienceNone
	}
}

// oofAudienceFromEWS maps EWS ExternalAudience to semcore OOFAudience.
func oofAudienceFromEWS(ext ExternalAudience) semcore.OOFAudience {
	switch ext {
	case ExternalAudienceNone:
		return semcore.OOFAudienceInternal
	case ExternalAudienceKnown:
		return semcore.OOFAudienceExternal
	case ExternalAudienceAll:
		return semcore.OOFAudienceEveryone
	default:
		return semcore.OOFAudienceInternal
	}
}

// ---------------------------------------------------------------------------
// Map EWS UserOofSettings to semcore OOFPolicy
// ---------------------------------------------------------------------------

// oofPolicyFromEWS converts EWS UserOofSettings to a semcore OOFPolicy.
func oofPolicyFromEWS(ctx context.Context, mailboxID semcore.MailboxId, settings *UserOofSettings) (*semcore.OOFPolicy, error) {
	if settings == nil {
		return nil, errors.New("UserOofSettings is required")
	}

	policy := &semcore.OOFPolicy{
		MailboxID:    mailboxID,
		Audience:     oofAudienceFromEWS(settings.ExternalAudience),
		SendIntervalSeconds: 7 * 24 * 3600, // default 7 days
	}

	// OofState
	switch settings.OofState {
	case OofStateDisabled:
		policy.Enabled = false
	case OofStateEnabled:
		policy.Enabled = true
	case OofStateScheduled:
		policy.Enabled = true
	default:
		policy.Enabled = false
	}

	// Duration (schedule)
	if settings.Duration != nil {
		if settings.Duration.StartTime != "" {
			if t, err := ParseEWSDateTime(settings.Duration.StartTime); err == nil {
				policy.StartTime = t
			}
		}
		if settings.Duration.EndTime != "" {
			if t, err := ParseEWSDateTime(settings.Duration.EndTime); err == nil {
				policy.EndTime = t
			}
		}
	}

	// Reply content
	if settings.InternalReply != nil {
		policy.TextBody = settings.InternalReply.Message
	}
	if settings.ExternalReply != nil && settings.ExternalReply.Message != "" {
		policy.TextBody = settings.ExternalReply.Message
	}

	// External audience mapping
	policy.Audience = oofAudienceFromEWS(settings.ExternalAudience)

	// Assign OOFId == MailboxId
	oofID, err := semcore.NewOOFId(mailboxID.String())
	if err != nil {
		return nil, err
	}
	policy.ID = oofID

	// Set Timezone if provided (default to UTC if not)
	policy.Timezone = "UTC"

	return policy, nil
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

func boolPtr(b bool) *bool { return &b }
