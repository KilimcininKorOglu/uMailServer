// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements item operations: CreateItem, GetItem,
// UpdateItem, DeleteItem, SendItem, MoveItem, CopyItem, and attachment ops.
package ews

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// CreateItem
// ---------------------------------------------------------------------------

// CreateItemRequest is the EWS CreateItem operation request.
type CreateItemRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateItem"`
	Items   struct {
		XMLName               xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
		Item                  []MessageTypeNew             `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
		CalendarItem          []CreateItemCalendarItemType `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItem"`
		Contact               []CreateItemContactType      `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contact"`
		Task                  []CreateItemTaskType         `xml:"http://schemas.microsoft.com/exchange/services/2006/types Task"`
		ReplyToItem           []ReplyCreateItemType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReplyToItem"`
		ReplyAllItem          []ReplyCreateItemType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReplyAllToItem"`
		AcceptItem            []MeetingReplyType           `xml:"http://schemas.microsoft.com/exchange/services/2006/types AcceptItem"`
		TentativelyAcceptItem []MeetingReplyType           `xml:"http://schemas.microsoft.com/exchange/services/2006/types TentativelyAcceptItem"`
		DeclineItem           []MeetingReplyType           `xml:"http://schemas.microsoft.com/exchange/services/2006/types DeclineItem"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	SavedItemFolderID struct {
		DistinguishedFolderID *struct {
			ID string `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SavedItemFolderId,omitempty"`
	// SaveItemToFolder: bool attribute. Uses bare attr name because Go's xml decoder
	// doesn't match default-namespace attributes against namespace URLs in struct tags.
	MessageDisposition      string `xml:"MessageDisposition,attr,omitempty"`
	SendMeetingInvitations  string `xml:"SendMeetingInvitations,attr,omitempty"`
	SaveItemToFolder        *bool  `xml:"SaveItemToFolder,attr"`
	SaveItemToFolderElement *bool  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SaveItemToFolder,omitempty"`
	// DelegateMailbox is a uMailServer EWS extension. When an authenticated
	// delegate acts on behalf of an owner, this namespaced child element specifies
	// the owner's email so the permission check uses the owner's mailbox instead
	// of the delegate's own mailbox.
	DelegateMailbox string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DelegateMailbox,omitempty"`
}

// MessageTypeNew is a message item in a CreateItem request.
type MessageTypeNew struct {
	XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	ItemClass     string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemClass,omitempty"`
	Subject       string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Body          *BodyType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body,omitempty"`
	ToRecipients  RawRecipients    `xml:"http://schemas.microsoft.com/exchange/services/2006/types ToRecipients,omitempty"`
	CcRecipients  RawRecipients    `xml:"http://schemas.microsoft.com/exchange/services/2006/types CcRecipients,omitempty"`
	BccRecipients RawRecipients    `xml:"http://schemas.microsoft.com/exchange/services/2006/types BccRecipients,omitempty"`
	From          *FromAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types From,omitempty"`
	Sender        *FromAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Sender,omitempty"`
	IsDraft       bool             `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsDraft,attr"`
	Attachments   *AttachmentsType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Attachments,omitempty"`
}

type AttachmentsType struct {
	FileAttachments []FileAttachmentType `xml:"http://schemas.microsoft.com/exchange/services/2006/types FileAttachment,omitempty"`
}

type FileAttachmentType struct {
	// AttachmentID must be emitted on read responses (GetItem/GetAttachment) so
	// EWS clients treat the attachment as already-created. Without it, exchangelib
	// parses attachment_id=None and tries to upload the attachment server-side
	// while constructing the item, failing with "Parent item ... must have an account".
	AttachmentID *AttachmentIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId,omitempty"`
	Name         string            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
	ContentType  string            `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContentType,omitempty"`
	ContentID    string            `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContentId,omitempty"`
	IsInline     *bool             `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsInline,omitempty"`
	Content      string            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Content,omitempty"`
}

// AttachmentIdType is the <t:AttachmentId Id="..."/> element. The Id is a
// self-describing token (see makeAttachmentID) that encodes the parent item ID
// and the attachment's index within that item's MIME, so attachment content can
// be resolved on a later GetAttachment without a separate persisted identity.
type AttachmentIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
	ID      string   `xml:"Id,attr"`
}

// makeAttachmentID builds a self-describing AttachmentId from the parent item ID
// and the attachment index. Item IDs are opaque hex tokens and never contain the
// "~att~" separator, so parseAttachmentID round-trips safely.
func makeAttachmentID(parentItemID string, index int) string {
	return fmt.Sprintf("%s~att~%d", parentItemID, index)
}

// parseAttachmentID decodes a token produced by makeAttachmentID. ok is false for
// any token that was not produced by this server.
func parseAttachmentID(raw string) (parentItemID string, index int, ok bool) {
	i := strings.LastIndex(raw, "~att~")
	if i < 0 {
		return "", 0, false
	}
	parentItemID = raw[:i]
	idx, err := strconv.Atoi(raw[i+len("~att~"):])
	if err != nil || parentItemID == "" || idx < 0 {
		return "", 0, false
	}
	return parentItemID, idx, true
}

// CreateItemCalendarItemType is a minimal calendar item for CreateItem requests.
// Cannot reuse CalendarItemTypeNew from collab.go due to XMLName conflicts with
// AttendeesType when embedded in the same Items container.
type CreateItemCalendarItemType struct {
	XMLName    xml.Name        `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItem"`
	Subject    string          `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Body       *BodyType       `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body,omitempty"`
	Start      string          `xml:"http://schemas.microsoft.com/exchange/services/2006/types Start,omitempty"`
	End        string          `xml:"http://schemas.microsoft.com/exchange/services/2006/types End,omitempty"`
	Location   string          `xml:"http://schemas.microsoft.com/exchange/services/2006/types Location,omitempty"`
	Recurrence *RecurrenceType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Recurrence,omitempty"`
	// RequiredAttendees/OptionalAttendees use a type WITHOUT a pinned XMLName so
	// the decoder accepts both element names; a type whose XMLName is fixed to
	// "Attendees" would refuse to unmarshal into a differently-named element.
	RequiredAttendees *CreateAttendeesType   `xml:"http://schemas.microsoft.com/exchange/services/2006/types RequiredAttendees,omitempty"`
	OptionalAttendees *CreateAttendeesType   `xml:"http://schemas.microsoft.com/exchange/services/2006/types OptionalAttendees,omitempty"`
	Categories        *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
}

// CreateAttendeesType is an attendee list with no fixed XMLName, so it can back
// both the RequiredAttendees and OptionalAttendees elements on a CreateItem.
type CreateAttendeesType struct {
	Attendee []AttendeeType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Attendee"`
}

// CreateItemContactType is a minimal contact for CreateItem requests.
// Cannot reuse ContactTypeNew from collab.go due to XMLName conflicts with
// PhysicalAddressType when embedded in the same Items container.
type CreateItemContactType struct {
	XMLName        xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contact"`
	DisplayName    string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName,omitempty"`
	FullName       string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types FullName,omitempty"`
	GivenName      string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types GivenName,omitempty"`
	Surname        string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Surname,omitempty"`
	EmailAddresses *EmailAddressesType    `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddresses,omitempty"`
	Body           *BodyType              `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body,omitempty"`
	Categories     *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
}

// CreateItemTaskType is a minimal task for CreateItem requests.
// Cannot reuse TaskTypeNew from collab.go due to potential XMLName conflicts
// when embedded in the same Items container.
type CreateItemTaskType struct {
	XMLName    xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/types Task"`
	Subject    string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Body       *BodyType              `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body,omitempty"`
	DueDate    string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types DueDate,omitempty"`
	Status     string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Status,omitempty"`
	Categories *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
}

// ReplyCreateItemType is shared by the ReplyToItem and ReplyAllToItem create
// elements. It intentionally has no XMLName field: Go's decoder would otherwise
// reject a <ReplyAllToItem> element when the type pins XMLName to "ReplyToItem".
// The element name is supplied by the enclosing slice field tag instead.
type ReplyCreateItemType struct {
	Subject         string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	NewBodyContent  *BodyType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types NewBodyContent,omitempty"`
	ToRecipients    RawRecipients    `xml:"http://schemas.microsoft.com/exchange/services/2006/types ToRecipients,omitempty"`
	CcRecipients    RawRecipients    `xml:"http://schemas.microsoft.com/exchange/services/2006/types CcRecipients,omitempty"`
	BccRecipients   RawRecipients    `xml:"http://schemas.microsoft.com/exchange/services/2006/types BccRecipients,omitempty"`
	From            *FromAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types From,omitempty"`
	ReferenceItemID struct {
		ID string `xml:"Id,attr"`
		CK string `xml:"ChangeKey,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReferenceItemId"`
}

// RawRecipients holds raw XML for recipient lists (To/Cc/Bcc).
// We use RawMailboxes + manual unmarshaling to avoid the XML naming conflict
// between EmailAddressType (used in From) and the <Mailbox> wrapper
// expected in To/Cc/Bcc recipient lists.
type RawRecipients struct {
	RawMailboxes []byte `xml:",innerxml"`
}

// Recipients returns the parsed To/Cc/Bcc email addresses.
func (r *RawRecipients) Recipients() []EmailAddressType {
	if len(r.RawMailboxes) == 0 {
		return nil
	}
	envelope := []byte(`<root xmlns:t="` + EWSTypesNS + `">`)
	envelope = append(envelope, r.RawMailboxes...)
	envelope = append(envelope, []byte(`</root>`)...)

	var mailboxes struct {
		Items []EmailAddressType `xml:"Mailbox"`
	}
	if err := xml.Unmarshal(envelope, &mailboxes); err != nil {
		return nil
	}
	return mailboxes.Items
}

// FromAddressType is the type used for the t:From and t:Sender elements.
// It wraps EmailAddressType so the XML tag name (From/Sender) doesn't
// conflict with EmailAddressType.XMLName (Mailbox) in staticcheck analysis.
type FromAddressType struct {
	Mailbox EmailAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
}

// BodyType represents the message body.
type BodyType struct {
	Body     string `xml:",chardata"`
	BodyType string `xml:"BodyType,attr"`
}

// EmailAddressType represents an email address in requests.
// It is used inside wrapper elements (ToRecipients, CcRecipients, BccRecipients)
// where Go's XML unmarshaler matches individual items by their email/name children.
// Do NOT set XMLName on this type: it would make Go expect a <Mailbox> outer
// element when used in slices, breaking the <ToRecipients><Mailbox>... pattern.
type EmailAddressType struct {
	Email string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress"`
	Name  string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
}

// CreateItemResponse is the EWS CreateItem operation response.
type CreateItemResponse struct {
	XMLName xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateItemResponse"`
	Msgs    CreateItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// CreateItemResponseMessages wraps CreateItem response messages.
type CreateItemResponseMessages struct {
	Messages []ItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateItemResponseMessage"`
}

// ItemResponseMessageType is one item's result in a response.
type ItemResponseMessageType struct {
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Items         ItemsContainer   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
}

// ItemsContainer wraps items in response messages.
type ItemsContainer struct {
	XMLName         xml.Name                 `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	Items           []MessageTypeResponse    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	MeetingRequests []MeetingRequestResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types MeetingRequest"`
}

// MessageTypeResponse is a message item in responses (read/fetched).
type MessageTypeResponse struct {
	XMLName          xml.Name                    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	ItemID           ItemIdType                  `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID   FolderIdComponents          `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	ItemClass        string                      `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemClass,omitempty"`
	Subject          string                      `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	DateTimeReceived string                      `xml:"http://schemas.microsoft.com/exchange/services/2006/types DateTimeReceived,omitempty"`
	Size             int                         `xml:"http://schemas.microsoft.com/exchange/services/2006/types Size,omitempty"`
	Body             BodyTypeResponse            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body"`
	From             *RecipientResponse          `xml:"http://schemas.microsoft.com/exchange/services/2006/types From,omitempty"`
	Sender           *RecipientResponse          `xml:"http://schemas.microsoft.com/exchange/services/2006/types Sender,omitempty"`
	ToRecipients     *RecipientsResponse         `xml:"http://schemas.microsoft.com/exchange/services/2006/types ToRecipients,omitempty"`
	CcRecipients     *RecipientsResponse         `xml:"http://schemas.microsoft.com/exchange/services/2006/types CcRecipients,omitempty"`
	IsRead           bool                        `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsRead"`
	Categories       *MessageCategoriesType      `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
	Attachments      *AttachmentsType            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Attachments,omitempty"`
	ConversationID   *ConversationIdType         `xml:"http://schemas.microsoft.com/exchange/services/2006/types ConversationId,omitempty"`
	InternetHeaders  *InternetMessageHeadersType `xml:"http://schemas.microsoft.com/exchange/services/2006/types InternetMessageHeaders,omitempty"`
	// isMeetingRequest is an internal marker (never serialized) flagging that
	// FindItem should surface this item as a MeetingRequest element so clients
	// expose accept/decline on it.
	isMeetingRequest bool `xml:"-"`
}

// MeetingRequestResponse is a meeting-request item rendered in responses. It
// mirrors the message fields a client needs to accept/decline, under the
// MeetingRequest element so exchangelib instantiates a MeetingRequest.
type MeetingRequestResponse struct {
	XMLName          xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/types MeetingRequest"`
	ItemID           ItemIdType         `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID   FolderIdComponents `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	ItemClass        string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemClass,omitempty"`
	Subject          string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	DateTimeReceived string             `xml:"http://schemas.microsoft.com/exchange/services/2006/types DateTimeReceived,omitempty"`
	IsRead           bool               `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsRead"`
}

// toMeetingRequestResponse projects a message response onto the MeetingRequest
// shape, preserving the identity and subject the client needs to respond.
func toMeetingRequestResponse(m MessageTypeResponse) MeetingRequestResponse {
	return MeetingRequestResponse{
		ItemID:           m.ItemID,
		ParentFolderID:   m.ParentFolderID,
		ItemClass:        "IPM.Schedule.Meeting.Request",
		Subject:          m.Subject,
		DateTimeReceived: m.DateTimeReceived,
		IsRead:           m.IsRead,
	}
}

// ConversationIdType is the EWS ConversationId element (an ItemId-shaped token
// carrying only an Id attribute) used to group messages into a thread.
type ConversationIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ConversationId"`
	ID      string   `xml:"Id,attr"`
}

// InternetMessageHeadersType is the EWS InternetMessageHeaders collection,
// surfacing the message's RFC 5322 header fields (item:InternetMessageHeaders).
type InternetMessageHeadersType struct {
	XMLName xml.Name                    `xml:"http://schemas.microsoft.com/exchange/services/2006/types InternetMessageHeaders"`
	Headers []InternetMessageHeaderType `xml:"http://schemas.microsoft.com/exchange/services/2006/types InternetMessageHeader"`
}

// InternetMessageHeaderType is a single header field; HeaderName is an
// attribute and the field value is the element text.
type InternetMessageHeaderType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types InternetMessageHeader"`
	Name    string   `xml:"HeaderName,attr"`
	Value   string   `xml:",chardata"`
}

// parseInternetHeaders extracts the ordered RFC 5322 header fields from a raw
// message, unfolding continuation lines. Order and duplicates are preserved so
// the EWS InternetMessageHeaders collection mirrors the wire message.
func parseInternetHeaders(data []byte) []InternetMessageHeaderType {
	if len(data) == 0 {
		return nil
	}
	// Isolate the header block (everything before the first blank line).
	block := data
	if idx := bytes.Index(data, []byte("\r\n\r\n")); idx >= 0 {
		block = data[:idx]
	} else if idx := bytes.Index(data, []byte("\n\n")); idx >= 0 {
		block = data[:idx]
	}

	rawLines := strings.Split(strings.ReplaceAll(string(block), "\r\n", "\n"), "\n")
	var headers []InternetMessageHeaderType
	for _, line := range rawLines {
		if line == "" {
			continue
		}
		// Folded continuation: append to the previous header's value.
		if (line[0] == ' ' || line[0] == '\t') && len(headers) > 0 {
			headers[len(headers)-1].Value += " " + strings.TrimSpace(line)
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		value := strings.TrimSpace(line[colon+1:])
		headers = append(headers, InternetMessageHeaderType{Name: name, Value: value})
	}
	return headers
}

type MessageCategoriesType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories"`
	Strings []string `xml:"http://schemas.microsoft.com/exchange/services/2006/types String,omitempty"`
}

// MailboxTypeResponse is a mailbox entry in responses.
type MailboxTypeResponse struct {
	XMLName      xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
	EmailAddress string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress,omitempty"`
	Name         string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
}

// RecipientResponse wraps a single mailbox under a recipient element such as
// From or Sender. exchangelib maps <t:From> to an item's author and <t:Sender>
// to its sender; without these elements those attributes resolve to None.
type RecipientResponse struct {
	Mailbox MailboxTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
}

// RecipientsResponse wraps a list of mailboxes under a recipient-collection
// element such as ToRecipients or CcRecipients, producing the EWS structure
// <t:ToRecipients><t:Mailbox>…</t:Mailbox></t:ToRecipients>. A bare
// []MailboxTypeResponse would serialize the Mailbox elements without the
// wrapping element (the element's pinned XMLName overrides the field tag), which
// clients cannot parse — leaving reply-all unable to recover the recipient set.
type RecipientsResponse struct {
	Mailboxes []MailboxTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
}

// recipientsWrap returns a RecipientsResponse for the given mailboxes, or nil
// when empty so the (omitempty) container element is dropped entirely.
func recipientsWrap(addrs []MailboxTypeResponse) *RecipientsResponse {
	if len(addrs) == 0 {
		return nil
	}
	return &RecipientsResponse{Mailboxes: addrs}
}

// recipientsFromHeader parses an address-list header (To, Cc, …) from a raw MIME
// message into mailbox responses, preserving display names when present.
func recipientsFromHeader(rawMsg []byte, header string) []MailboxTypeResponse {
	v := rawHeaderValue(rawMsg, header)
	if strings.TrimSpace(v) == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(v)
	if err != nil {
		return nil
	}
	out := make([]MailboxTypeResponse, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, MailboxTypeResponse{EmailAddress: a.Address, Name: a.Name})
	}
	return out
}

// mailboxFromHeader parses an RFC 5322 address header value ("Name <addr>" or a
// bare address) into a RecipientResponse, or returns nil when no address is
// present. Used to surface the From / Sender headers in item responses.
func mailboxFromHeader(header string) *RecipientResponse {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if addr, err := mail.ParseAddress(header); err == nil {
		return &RecipientResponse{Mailbox: MailboxTypeResponse{EmailAddress: addr.Address, Name: addr.Name}}
	}
	return &RecipientResponse{Mailbox: MailboxTypeResponse{EmailAddress: header}}
}

// stripSubaddress removes an RFC 5233 "+detail" suffix from the local part of an
// email address, returning the bare-mailbox address. "a+x@d" -> "a@d".
func stripSubaddress(email string) string {
	at := strings.IndexByte(email, '@')
	local, domain := email, ""
	if at >= 0 {
		local, domain = email[:at], email[at:]
	}
	if i := strings.IndexByte(local, '+'); i > 0 {
		local = local[:i]
	}
	return local + domain
}

// sameMailboxOwner reports whether two addresses resolve to the same mailbox
// owner, treating an RFC 5233 "+detail" subaddress as the same identity as its
// bare address (so a user may send as their own subaddress without a send-as
// grant).
func sameMailboxOwner(a, b string) bool {
	return strings.EqualFold(stripSubaddress(a), stripSubaddress(b))
}

// rawHeaderValue returns a single header value from a raw MIME message, or "".
func rawHeaderValue(rawMsg []byte, name string) string {
	msg, err := mail.ReadMessage(bytes.NewReader(rawMsg))
	if err != nil {
		return ""
	}
	return msg.Header.Get(name)
}

// BodyTypeResponse represents the message body in EWS responses.
type BodyTypeResponse struct {
	XMLName     xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body"`
	BodyType    string   `xml:"BodyType,attr"`
	Text        string   `xml:",chardata"`
	IsTruncated bool     `xml:"IsTruncated,attr,omitempty"`
}

// ItemIdType is the EWS ItemId element used in responses.
type ItemIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ID      string   `xml:"Id,attr"`
	CK      string   `xml:"ChangeKey,attr,omitempty"`
}

// handleCreateItem processes an EWS CreateItem SOAP request.
func (s *Server) handleCreateItem(ctx context.Context, body []byte) []byte {
	var req CreateItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("CreateItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("CreateItem", errCode, "could not resolve mailbox")
	}

	// mboxKey is "e:alice@local.test" but folder/msgStore use raw email.
	// For delegate operations, SavedItemFolderId.DelegateMailbox may specify the
	// target owner's mailbox so the permission check uses the correct owner.
	ownerEmail := strings.TrimPrefix(mboxKey, "e:")
	if req.SaveItemToFolder != nil && *req.SaveItemToFolder && req.DelegateMailbox != "" {
		ownerEmail = req.DelegateMailbox
	}

	mboxID, err := s.identity.GetMailboxIDByEmail(ownerEmail)
	if err != nil {
		return s.errorItemResponseXML("CreateItem", ErrErrorInternalServer, "mailbox not found")
	}

	// Delegate enforcement (VAL-DIR-002): check write permission for non-owners.
	actorEmail := s.getActingEmail(ctx)
	if msg, code := s.checkDelegatePermission(mboxID, ownerEmail, actorEmail, "write"); code != "" {
		return s.errorItemResponseXML("CreateItem", code, msg)
	}

	// VAL-DIR-004 / VAL-DIR-005: send-as and send-on-behalf authorization.
	// Sending mail as another identity requires an explicit grant; general
	// delegate folder access (VAL-DIR-002) does NOT imply send rights. The
	// represented From is either an explicit client From or — when a delegate
	// sends (SendAndSaveCopy/SendOnly) from a mailbox that is not their own —
	// the targeted owner mailbox, since clients (e.g. exchangelib) often omit
	// the From element and rely on the server to stamp the owner identity.
	isSend := strings.EqualFold(req.MessageDisposition, "SendAndSaveCopy") ||
		strings.EqualFold(req.MessageDisposition, "SendOnly")
	for i := range req.Items.Item {
		item := &req.Items.Item[i]
		fromEmail := ""
		if item.From != nil && item.From.Mailbox.Email != "" {
			fromEmail = item.From.Mailbox.Email
		} else if isSend && !strings.EqualFold(ownerEmail, actorEmail) {
			fromEmail = ownerEmail
		}
		if fromEmail == "" || sameMailboxOwner(fromEmail, actorEmail) {
			continue
		}
		// The represented From is not the actor's own identity.
		if strings.EqualFold(fromEmail, ownerEmail) {
			// Delegate sending as owner: require send-as OR send-on-behalf.
			if _, code := s.checkSendAsPermission(mboxID, ownerEmail, actorEmail); code == "" {
				// send-as authorized
			} else if _, code = s.checkSendOnBehalfPermission(mboxID, ownerEmail, actorEmail); code == "" {
				// send-on-behalf authorized
			} else {
				return s.errorItemResponseXML("CreateItem", code, "send-as/send-on-behalf requires explicit authorization for "+actorEmail+" on "+fromEmail)
			}
		} else {
			// From address is neither the actor's nor the owner's — denied.
			return s.errorItemResponseXML("CreateItem", ErrErrorSendDenied,
				"From address "+fromEmail+" is not authorized for "+actorEmail)
		}
	}

	// Build delegate audit context for lifecycle emission (VAL-DIR-014).
	delegateCtx := s.buildDelegateAuditContext(ctx, mboxID, ownerEmail)

	// Determine target folder: Sent Items by default, or SavedItemFolderId.
	var folderID semcore.FolderId
	sendAndSaveCopy := strings.EqualFold(req.MessageDisposition, "SendAndSaveCopy")
	sendOnly := strings.EqualFold(req.MessageDisposition, "SendOnly")
	targetRole := "drafts"
	if sendAndSaveCopy {
		targetRole = "sent"
	}
	saveItemToFolder := req.SaveItemToFolder != nil && *req.SaveItemToFolder
	if req.SaveItemToFolderElement != nil {
		saveItemToFolder = *req.SaveItemToFolderElement
	}
	if saveItemToFolder {
		if req.SavedItemFolderID.DistinguishedFolderID != nil {
			role, ok := DistinguishedFolderIDs[req.SavedItemFolderID.DistinguishedFolderID.ID]
			if ok {
				fld, err := s.identity.GetFolderByMailbox(ownerEmail, role)
				if err == nil {
					folderID = fld.FolderID
				}
				targetRole = role
			}
		}
	}

	if folderID.IsZero() {
		fld, err := s.identity.GetFolderByMailbox(ownerEmail, targetRole)
		if err == nil {
			folderID = fld.FolderID
		}
	}
	if folderID.IsZero() {
		folderID, err = s.identity.EnsureFolderId(ownerEmail, targetRole, targetRole)
		if err != nil {
			return s.errorItemResponseXML("CreateItem", ErrErrorInternalServer, "failed to ensure folder: "+err.Error())
		}
	}

	msgs := make([]ItemResponseMessageType, 0, len(req.Items.Item)+len(req.Items.ReplyToItem)+len(req.Items.ReplyAllItem))
	for i := range req.Items.Item {
		item := &req.Items.Item[i]
		// Detect which mode each item uses. Check send-as first (VAL-DIR-004);
		// if that is authorized, use plain From=owner. Otherwise check
		// send-on-behalf (VAL-DIR-005) and set isSendOnBehalf so the MIME builder
		// adds a Sender header naming the acting delegate (RFC 5322 §3.6.2).
		// The represented From is an explicit client From, or the targeted owner
		// mailbox when a delegate sends and omits the From element.
		itemIsSendOnBehalf := false
		fromEmail := ownerEmail
		if item.From != nil && item.From.Mailbox.Email != "" {
			fromEmail = item.From.Mailbox.Email
		}
		// A delegate (actor != owner) preparing a message that represents the owner
		// resolves its mode from the grant: send-as keeps From=owner only;
		// send-on-behalf additionally stamps Sender=actor. This is decided at item
		// creation (drafts included) so a draft later dispatched via SendItem already
		// carries the correct headers.
		if strings.EqualFold(fromEmail, ownerEmail) && !strings.EqualFold(actorEmail, ownerEmail) {
			if _, code := s.checkSendAsPermission(mboxID, ownerEmail, actorEmail); code != "" {
				if _, code := s.checkSendOnBehalfPermission(mboxID, ownerEmail, actorEmail); code == "" {
					itemIsSendOnBehalf = true
					// Stamp the acting delegate as Sender when the client omitted it.
					if item.Sender == nil || item.Sender.Mailbox.Email == "" {
						item.Sender = &FromAddressType{Mailbox: EmailAddressType{Email: actorEmail}}
					}
				}
			}
		}
		var msg ItemResponseMessageType
		switch {
		case sendAndSaveCopy || sendOnly:
			msg = s.submitMessageItem(ctx, mboxID, ownerEmail, folderID, item, nil, delegateCtx, itemIsSendOnBehalf, sendAndSaveCopy)
		case isStickyNoteClass(item.ItemClass):
			// Outlook notes are IPM.StickyNote messages in the Notes folder
			// (Exchange-faithful). Store them in the notes folder, not the
			// CreateItem default (drafts), regardless of SavedItemFolderId.
			msg = s.createNoteItem(ctx, mboxID, ownerEmail, req, item, delegateCtx)
		default:
			msg = s.createItemInFolder(ctx, mboxID, ownerEmail, folderID, item, delegateCtx, itemIsSendOnBehalf)
		}
		msgs = append(msgs, msg)
	}
	for i := range req.Items.ReplyToItem {
		msgs = append(msgs, s.submitReplyCreateItem(ctx, mboxID, ownerEmail, folderID, &req.Items.ReplyToItem[i], delegateCtx, sendAndSaveCopy, false))
	}
	for i := range req.Items.ReplyAllItem {
		msgs = append(msgs, s.submitReplyCreateItem(ctx, mboxID, ownerEmail, folderID, &req.Items.ReplyAllItem[i], delegateCtx, sendAndSaveCopy, true))
	}

	// Process collab items (CalendarItem, Contact, Task) via the collaboration store.
	// These are sent by exchangelib's CalendarItem.save(), Contact.save(), Task.save()
	// through the standard CreateItem SOAP operation.
	// NOTE: We resolve the target folder directly here, NOT from the handler's folderID
	// variable, because the handler defaults to "drafts" when the SaveItemToFolder
	// attribute is not set (exchangelib does not set this attribute on CreateItem).
	if s.collabStore != nil {
		for i := range req.Items.CalendarItem {
			calFolderID := s.resolveCollabTargetFolder(ownerEmail, req, "calendar")
			calItem := createItemCalendarToCollabType(&req.Items.CalendarItem[i])
			calMsg := s.createCalendarItemInFolder(ctx, mboxID, ownerEmail, calFolderID, calItem, delegateCtx)
			itemMsg := collabItemMsgToItemMsg(calMsg.ResponseClass, calMsg.ResponseCode, calMsg.Items)
			msgs = append(msgs, itemMsg)
			// When the organizer invites attendees, deliver meeting-request
			// messages to each attendee so they can accept/decline.
			if calMsg.ResponseClass == "Success" && meetingInvitationsRequested(req.SendMeetingInvitations) {
				s.sendMeetingRequests(ownerEmail, &req.Items.CalendarItem[i])
			}
		}
		// Meeting responses (Accept / TentativelyAccept / Decline) reference an
		// inbound meeting request and update the responder's calendar.
		for i := range req.Items.AcceptItem {
			msgs = append(msgs, s.handleMeetingReply(ctx, mboxID, ownerEmail, &req.Items.AcceptItem[i], meetingResponseAccept))
		}
		for i := range req.Items.TentativelyAcceptItem {
			msgs = append(msgs, s.handleMeetingReply(ctx, mboxID, ownerEmail, &req.Items.TentativelyAcceptItem[i], meetingResponseTentative))
		}
		for i := range req.Items.DeclineItem {
			msgs = append(msgs, s.handleMeetingReply(ctx, mboxID, ownerEmail, &req.Items.DeclineItem[i], meetingResponseDecline))
		}
		for i := range req.Items.Contact {
			ctFolderID := s.resolveCollabTargetFolder(ownerEmail, req, "contacts")
			ctItem := createItemContactToCollabType(&req.Items.Contact[i])
			ctMsg := s.createContactInFolder(ctx, mboxID, ownerEmail, ctFolderID, ctItem)
			itemMsg := collabItemMsgToItemMsg(ctMsg.ResponseClass, ctMsg.ResponseCode, ctMsg.Items)
			msgs = append(msgs, itemMsg)
		}
		for i := range req.Items.Task {
			tkFolderID := s.resolveCollabTargetFolder(ownerEmail, req, "tasks")
			tkItem := createItemTaskToCollabType(&req.Items.Task[i])
			tkMsg := s.createTaskInFolder(ctx, mboxID, ownerEmail, tkFolderID, tkItem)
			itemMsg := collabItemMsgToItemMsg(tkMsg.ResponseClass, tkMsg.ResponseCode, tkMsg.Items)
			msgs = append(msgs, itemMsg)
		}
	}

	if len(msgs) == 0 && (sendAndSaveCopy || sendOnly) {
		msgs = append(msgs, ItemResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
		})
	}

	resp := CreateItemResponse{}
	resp.Msgs.Messages = msgs
	result := buildResponseEnvelope(resp)
	return result
}

// collabItemMsgToItemMsg converts a collab response message (CalendarItem, Contact, or Task)
// to a standard ItemResponseMessageType. It extracts the ItemId from any collab-specific
// container type (CalendarItemsContainer, ContactsItemsContainer, TasksItemsContainer)
// using the shared parentFields pattern (each has Items []struct{ ItemID ... }).
func collabItemMsgToItemMsg(responseClass string, responseCode ResponseCodeType, items any) ItemResponseMessageType {
	itemMsg := ItemResponseMessageType{
		ResponseClass: responseClass,
		ResponseCode:  responseCode,
		Items:         ItemsContainer{Items: []MessageTypeResponse{}},
	}
	if items == nil {
		return itemMsg
	}
	// Use reflection to extract ItemID/ChangeKey from the first item.
	itemsVal := reflect.ValueOf(items)
	if itemsVal.Kind() == reflect.Ptr {
		itemsVal = itemsVal.Elem()
	}
	if itemsVal.Kind() == reflect.Struct {
		fld := itemsVal.FieldByName("Items")
		if fld.IsValid() && fld.Kind() == reflect.Slice && fld.Len() > 0 {
			first := fld.Index(0)
			if first.Kind() == reflect.Ptr {
				first = first.Elem()
			}
			idField := first.FieldByName("ItemID")
			if idField.IsValid() {
				id := idField.FieldByName("ID").String()
				ck := idField.FieldByName("CK").String()
				itemMsg.Items.Items = append(itemMsg.Items.Items, MessageTypeResponse{
					ItemID: ItemIdType{ID: id, CK: ck},
				})
			}
		}
	}
	return itemMsg
}

// resolveCollabTargetFolder determines the target folder for a collab item
// (calendar, contact, or task). It first checks SavedItemFolderId, then falls
// back to the folder for the given role, creating it if necessary.
func (s *Server) resolveCollabTargetFolder(ownerEmail string, req CreateItemRequest, role string) semcore.FolderId {
	// Try SavedItemFolderId first (exchangelib sends DistinguishedFolderId).
	if req.SavedItemFolderID.DistinguishedFolderID != nil {
		if rid, ok := DistinguishedFolderIDs[req.SavedItemFolderID.DistinguishedFolderID.ID]; ok && rid == role {
			if fld, err := s.identity.GetFolderByMailbox(ownerEmail, role); err == nil {
				return fld.FolderID
			}
		}
	}
	// Fall back to GetFolderByMailbox.
	if fld, err := s.identity.GetFolderByMailbox(ownerEmail, role); err == nil {
		return fld.FolderID
	}
	// Create the folder if it doesn't exist.
	if fld, err := s.identity.EnsureFolderId(ownerEmail, role, role); err == nil {
		return fld
	}
	return semcore.FolderId{}
}

// createItemCalendarToCollabType converts a minimal CreateItem calendar type
// to the full CalendarItemTypeNew expected by createCalendarItemInFolder.
func createItemCalendarToCollabType(src *CreateItemCalendarItemType) *CalendarItemTypeNew {
	return &CalendarItemTypeNew{
		Subject:    src.Subject,
		Body:       src.Body,
		Start:      src.Start,
		End:        src.End,
		Location:   src.Location,
		Recurrence: src.Recurrence,
		Categories: src.Categories,
	}
}

// createItemContactToCollabType converts a minimal CreateItem contact type
// to the full ContactTypeNew expected by createContactInFolder.
func createItemContactToCollabType(src *CreateItemContactType) *ContactTypeNew {
	// vCard FN is the formatted/display name. exchangelib sends DisplayName, not
	// FullName, so fall back to DisplayName when FullName is absent.
	fullName := src.FullName
	if fullName == "" {
		fullName = src.DisplayName
	}
	return &ContactTypeNew{
		FullName:       fullName,
		GivenName:      src.GivenName,
		Surname:        src.Surname,
		EmailAddresses: src.EmailAddresses,
		Categories:     src.Categories,
	}
}

// createItemTaskToCollabType converts a minimal CreateItem task type
// to the full TaskTypeNew expected by createTaskInFolder.
func createItemTaskToCollabType(src *CreateItemTaskType) *TaskTypeNew {
	return &TaskTypeNew{
		Subject:    src.Subject,
		Body:       src.Body,
		DueDate:    src.DueDate,
		Status:     src.Status,
		Categories: src.Categories,
	}
}

// createItemInFolder creates a message item in the target folder.
func (s *Server) createItemInFolder(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, item *MessageTypeNew, delegateCtx *semcore.DelegateAuditContext, isSendOnBehalf bool) ItemResponseMessageType {
	if folderID.IsZero() {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "no target folder")
	}

	// Build RFC 5322 MIME from the EWS item.
	// isSendOnBehalf controls whether Sender header is included (VAL-DIR-005).
	rawMsg := buildMimeMessageWithHeaders(item, mailboxKey, isSendOnBehalf, nil)
	if rawMsg == nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "failed to build message")
	}
	return s.createRawItemInFolder(ctx, mboxID, mailboxKey, folderID, item.Subject, rawMsg, delegateCtx)
}

// isStickyNoteClass reports whether an EWS ItemClass identifies an Outlook note.
// Matches "IPM.StickyNote" and any subclass ("IPM.StickyNote.*"), mirroring
// Exchange's the item-class prefix match routing for notes.
func isStickyNoteClass(itemClass string) bool {
	c := strings.ToLower(strings.TrimSpace(itemClass))
	return c == "ipm.stickynote" || strings.HasPrefix(c, "ipm.stickynote.")
}

// createNoteItem stores an Outlook note (IPM.StickyNote). Notes are ordinary
// messages tagged with an X-Message-Class header, kept in the mailbox's Notes
// folder — the same model Exchange uses (a note is a message in an IPF.StickyNote
// folder, with no dedicated EWS element). The stored class round-trips back as
// ItemClass on GetItem/FindItem.
func (s *Server) createNoteItem(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, req CreateItemRequest, item *MessageTypeNew, delegateCtx *semcore.DelegateAuditContext) ItemResponseMessageType {
	folderID := s.resolveCollabTargetFolder(mailboxKey, req, "notes")
	if folderID.IsZero() {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "failed to resolve notes folder")
	}
	rawMsg := buildMimeMessageWithHeaders(item, mailboxKey, false, map[string]string{"X-Message-Class": "IPM.StickyNote"})
	if rawMsg == nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "failed to build note")
	}
	resp := s.createRawItemInFolder(ctx, mboxID, mailboxKey, folderID, item.Subject, rawMsg, delegateCtx)
	if resp.ResponseClass == "Success" && len(resp.Items.Items) > 0 {
		resp.Items.Items[0].ItemClass = "IPM.StickyNote"
	}
	return resp
}

func (s *Server) createRawItemInFolder(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, subject string, rawMsg []byte, delegateCtx *semcore.DelegateAuditContext) ItemResponseMessageType {
	if folderID.IsZero() {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "no target folder")
	}

	// Store raw MIME blob.
	// mailboxKey is raw email (e.g. "alice@local.test").
	blobKey, err := s.msgStore.StoreMessage(mailboxKey, rawMsg)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "failed to store message: "+err.Error())
	}

	// Perform canonical mutation: assigns ItemId, ChangeKey, ConversationId.
	// DelegateAuditContext threads the delegate actor through to lifecycle (VAL-DIR-014).
	in := &semcore.MutationInput{
		MailboxID:            mboxID,
		FolderID:             folderID,
		RawMessage:           rawMsg,
		InternalDate:         time.Now(),
		Actor:                mailboxKey,
		Source:               semcore.MutationSourceEWS,
		Email:                mailboxKey,
		DelegateAuditContext: delegateCtx,
	}
	result, err := s.mutationPipe.MutateItem(in)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "mutation failed: "+err.Error())
	}
	if result == nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "mutation returned nil result")
	}
	// Persist lifecycle event so GetEvents and sync consumers see the mutation.
	if s.lifecycle != nil {
		//nolint:errcheck
		_ = s.lifecycle.AppendLifecycle(result.Lifecycle) // best-effort; event was already emitted
	}

	// Mirror the item into the IMAP mailstore index so IMAP/POP3/JMAP/webmail —
	// which read from that index, not the semcore identity store — see this
	// EWS-created item (cross-protocol integrity). Best-effort; the semcore
	// write above is the canonical record.
	s.mirrorCreateToMailstore(mailboxKey, folderID, rawMsg, blobKey)

	msgResp := MessageTypeResponse{
		ItemID: ItemIdType{
			ID: result.ItemID.String(),
			CK: result.ChangeKey.String(),
		},
		ParentFolderID:   FolderIdComponents{ID: folderID.String()},
		Subject:          subject,
		DateTimeReceived: FormatEWSDateTime(result.Lifecycle.At),
		Size:             len(rawMsg),
	}

	return ItemResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items:         ItemsContainer{Items: []MessageTypeResponse{msgResp}},
	}
}

func buildMimeMessageWithHeaders(item *MessageTypeNew, defaultFrom string, isSendOnBehalf bool, extraHeaders map[string]string) []byte {
	var buf bytes.Buffer
	now := time.Now().UTC().Format(time.RFC1123Z)

	buf.WriteString("Date: " + now + "\r\n")

	// VAL-DIR-004 / VAL-DIR-005: From header sets the represented identity.
	// For send-on-behalf (VAL-DIR-005), Sender distinguishes the acting delegate.
	if item.From != nil && item.From.Mailbox.Email != "" {
		buf.WriteString("From: ")
		if item.From.Mailbox.Name != "" {
			buf.WriteString(item.From.Mailbox.Name + " <" + item.From.Mailbox.Email + ">")
		} else {
			buf.WriteString(item.From.Mailbox.Email)
		}
		buf.WriteString("\r\n")
	} else if defaultFrom != "" {
		buf.WriteString("From: " + defaultFrom + "\r\n")
	}

	// VAL-DIR-005: send-on-behalf preserves represented identity distinctly.
	// When a delegate with send-on-behalf permission sends mail, the Sender header
	// identifies the delegate while From identifies the owner.
	if isSendOnBehalf && item.Sender != nil && item.Sender.Mailbox.Email != "" {
		buf.WriteString("Sender: ")
		if item.Sender.Mailbox.Name != "" {
			buf.WriteString(item.Sender.Mailbox.Name + " <" + item.Sender.Mailbox.Email + ">")
		} else {
			buf.WriteString(item.Sender.Mailbox.Email)
		}
		buf.WriteString("\r\n")
	}

	if len(item.ToRecipients.Recipients()) > 0 {
		buf.WriteString("To: ")
		addrs := make([]string, 0, len(item.ToRecipients.Recipients()))
		for _, r := range item.ToRecipients.Recipients() {
			if r.Email != "" {
				if r.Name != "" {
					addrs = append(addrs, r.Name+" <"+r.Email+">")
				} else {
					addrs = append(addrs, r.Email)
				}
			}
		}
		buf.WriteString(strings.Join(addrs, ", "))
		buf.WriteString("\r\n")
	}

	if len(item.CcRecipients.Recipients()) > 0 {
		buf.WriteString("Cc: ")
		addrs := make([]string, 0, len(item.CcRecipients.Recipients()))
		for _, r := range item.CcRecipients.Recipients() {
			if r.Email != "" {
				if r.Name != "" {
					addrs = append(addrs, r.Name+" <"+r.Email+">")
				} else {
					addrs = append(addrs, r.Email)
				}
			}
		}
		buf.WriteString(strings.Join(addrs, ", "))
		buf.WriteString("\r\n")
	}

	if len(item.BccRecipients.Recipients()) > 0 {
		buf.WriteString("Bcc: ")
		addrs := make([]string, 0, len(item.BccRecipients.Recipients()))
		for _, r := range item.BccRecipients.Recipients() {
			if r.Email != "" {
				if r.Name != "" {
					addrs = append(addrs, r.Name+" <"+r.Email+">")
				} else {
					addrs = append(addrs, r.Email)
				}
			}
		}
		buf.WriteString(strings.Join(addrs, ", "))
		buf.WriteString("\r\n")
	}

	if item.Subject != "" {
		buf.WriteString("Subject: " + item.Subject + "\r\n")
	}
	for _, name := range []string{"In-Reply-To", "References", "X-Message-Class"} {
		if value := strings.TrimSpace(extraHeaders[name]); value != "" {
			buf.WriteString(name + ": " + value + "\r\n")
		}
	}

	buf.WriteString("MIME-Version: 1.0\r\n")

	var attachments []FileAttachmentType
	if item.Attachments != nil {
		attachments = item.Attachments.FileAttachments
	}
	if len(attachments) > 0 {
		boundary := "umail-" + generateID()
		buf.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n")
		buf.WriteString("Message-ID: <" + generateMessageID() + ">\r\n")
		buf.WriteString("\r\n")
		buf.WriteString("--" + boundary + "\r\n")
		if item.Body != nil && item.Body.BodyType == "HTML" {
			buf.WriteString("Content-Type: text/html; charset=utf-8\r\n")
		} else {
			buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		}
		buf.WriteString("Content-Transfer-Encoding: 7bit\r\n")
		buf.WriteString("\r\n")
		if item.Body != nil {
			if item.Body.BodyType == "HTML" {
				buf.WriteString("<html><body>" + item.Body.Body + "</body></html>")
			} else {
				buf.WriteString(item.Body.Body)
			}
		}
		buf.WriteString("\r\n")

		for _, att := range attachments {
			content, err := base64.StdEncoding.DecodeString(strings.TrimSpace(att.Content))
			if err != nil {
				content = []byte(att.Content)
			}
			contentType := att.ContentType
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			disposition := "attachment"
			if att.IsInline != nil && *att.IsInline {
				disposition = "inline"
			}
			buf.WriteString("--" + boundary + "\r\n")
			buf.WriteString("Content-Type: " + contentType)
			if att.Name != "" {
				buf.WriteString("; name=\"" + att.Name + "\"")
			}
			buf.WriteString("\r\n")
			buf.WriteString("Content-Transfer-Encoding: base64\r\n")
			buf.WriteString("Content-Disposition: " + disposition)
			if att.Name != "" {
				buf.WriteString("; filename=\"" + att.Name + "\"")
			}
			buf.WriteString("\r\n")
			if att.ContentID != "" {
				buf.WriteString("Content-ID: <" + strings.Trim(att.ContentID, "<>") + ">\r\n")
			}
			buf.WriteString("\r\n")
			encoded := base64.StdEncoding.EncodeToString(content)
			for len(encoded) > 76 {
				buf.WriteString(encoded[:76] + "\r\n")
				encoded = encoded[76:]
			}
			if encoded != "" {
				buf.WriteString(encoded + "\r\n")
			}
		}
		buf.WriteString("--" + boundary + "--\r\n")
		return buf.Bytes()
	}

	if item.Body != nil && item.Body.BodyType == "HTML" {
		buf.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	} else {
		buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	}
	buf.WriteString("Content-Transfer-Encoding: 7bit\r\n")
	buf.WriteString("Message-ID: <" + generateMessageID() + ">\r\n")
	buf.WriteString("\r\n")

	if item.Body != nil {
		if item.Body.BodyType == "HTML" {
			buf.WriteString("<html><body>" + item.Body.Body + "</body></html>")
		} else {
			buf.WriteString(item.Body.Body)
		}
	}

	return buf.Bytes()
}

// generateMessageID generates a unique Message-ID.
func generateMessageID() string {
	return fmt.Sprintf("%d.%d@umailserver.local", time.Now().UnixNano(), time.Now().UnixNano()%1000000)
}

func (s *Server) submitMessageItem(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, item *MessageTypeNew, extraHeaders map[string]string, delegateCtx *semcore.DelegateAuditContext, isSendOnBehalf bool, saveCopy bool) ItemResponseMessageType {
	rawMsg := buildMimeMessageWithHeaders(item, mailboxKey, isSendOnBehalf, extraHeaders)
	if rawMsg == nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "failed to build message")
	}

	from, recipients, sanitized, err := prepareMessageForSubmission(rawMsg)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorInvalidOperation, err.Error())
	}
	if err := s.submitOutboundMessage(from, recipients, sanitized); err != nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, err.Error())
	}

	if !saveCopy {
		return ItemResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
		}
	}
	return s.createRawItemInFolder(ctx, mboxID, mailboxKey, folderID, item.Subject, rawMsg, delegateCtx)
}

func (s *Server) submitReplyCreateItem(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, item *ReplyCreateItemType, delegateCtx *semcore.DelegateAuditContext, saveCopy, replyAll bool) ItemResponseMessageType {
	extraHeaders, err := s.replyHeadersForReference(item.ReferenceItemID.ID)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorItemNotFound, err.Error())
	}
	replyMessage := &MessageTypeNew{
		Subject: item.Subject,
		Body:    item.NewBodyContent,
		From:    item.From,
	}
	// Recipients: a Reply/ReplyAll request normally omits recipients and relies
	// on the server to derive them from the referenced item. Honor any the
	// client did supply; otherwise compute them (sender for Reply, sender +
	// original To/Cc minus self for ReplyAll).
	if len(item.ToRecipients.Recipients()) > 0 || len(item.CcRecipients.Recipients()) > 0 {
		replyMessage.ToRecipients = item.ToRecipients
		replyMessage.CcRecipients = item.CcRecipients
		replyMessage.BccRecipients = item.BccRecipients
	} else {
		to, cc := s.deriveReplyRecipients(item.ReferenceItemID.ID, mailboxKey, replyAll)
		replyMessage.ToRecipients = RawRecipients{RawMailboxes: recipientsToRawXML(to)}
		replyMessage.CcRecipients = RawRecipients{RawMailboxes: recipientsToRawXML(cc)}
	}
	return s.submitMessageItem(ctx, mboxID, mailboxKey, folderID, replyMessage, extraHeaders, delegateCtx, false, saveCopy)
}

// deriveReplyRecipients computes the recipients for a server-side Reply or
// ReplyAll from the referenced message. Reply targets the original sender;
// ReplyAll additionally carbon-copies the original To and Cc recipients,
// excluding the replying mailbox itself and any duplicate of the sender.
func (s *Server) deriveReplyRecipients(referenceItemID, selfEmail string, replyAll bool) (to, cc []EmailAddressType) {
	id, err := semcore.NewItemId(referenceItemID)
	if err != nil {
		return nil, nil
	}
	rec, err := s.identity.GetItemIdentity(id)
	if err != nil {
		return nil, nil
	}
	rawMsg, err := s.msgStore.ReadMessage(rec.Email, rec.MsgKey)
	if err != nil {
		return nil, nil
	}
	msg, err := mail.ReadMessage(bytes.NewReader(rawMsg))
	if err != nil {
		return nil, nil
	}

	seen := map[string]bool{strings.ToLower(strings.TrimSpace(selfEmail)): true}

	// The reply goes to the original sender (Reply-To wins over From).
	sender := firstAddress(msg.Header.Get("Reply-To"))
	if sender == "" {
		sender = firstAddress(msg.Header.Get("From"))
	}
	if sender != "" && !seen[strings.ToLower(sender)] {
		to = append(to, EmailAddressType{Email: sender})
		seen[strings.ToLower(sender)] = true
	}

	if !replyAll {
		return to, nil
	}

	for _, hdr := range []string{"To", "Cc"} {
		addrs, _ := mail.ParseAddressList(msg.Header.Get(hdr)) //nolint:errcheck
		for _, a := range addrs {
			key := strings.ToLower(a.Address)
			if a.Address == "" || seen[key] {
				continue
			}
			seen[key] = true
			cc = append(cc, EmailAddressType{Email: a.Address})
		}
	}
	return to, cc
}

// firstAddress returns the first email address parsed from an address-list
// header value, or "" if none can be parsed.
func firstAddress(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	addrs, err := mail.ParseAddressList(header)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	return addrs[0].Address
}

// recipientsToRawXML serializes EWS Mailbox elements as the inner XML expected
// by RawRecipients.Recipients(), so derived recipients flow through the same
// parsing path as client-supplied ones.
func recipientsToRawXML(addrs []EmailAddressType) []byte {
	if len(addrs) == 0 {
		return nil
	}
	var b strings.Builder
	for _, a := range addrs {
		if a.Email == "" {
			continue
		}
		b.WriteString(`<t:Mailbox><t:EmailAddress>`)
		b.WriteString(xmlEsc(a.Email))
		b.WriteString(`</t:EmailAddress>`)
		if a.Name != "" {
			b.WriteString(`<t:Name>` + xmlEsc(a.Name) + `</t:Name>`)
		}
		b.WriteString(`</t:Mailbox>`)
	}
	if b.Len() == 0 {
		return nil
	}
	return []byte(b.String())
}

func (s *Server) replyHeadersForReference(itemID string) (map[string]string, error) {
	id, err := semcore.NewItemId(itemID)
	if err != nil {
		return nil, fmt.Errorf("invalid reference item id: %w", err)
	}
	rec, err := s.identity.GetItemIdentity(id)
	if err != nil {
		return nil, err
	}
	rawMsg, err := s.msgStore.ReadMessage(rec.Email, rec.MsgKey)
	if err != nil {
		return nil, err
	}
	msg, err := mail.ReadMessage(bytes.NewReader(rawMsg))
	if err != nil {
		return nil, err
	}
	messageID := strings.TrimSpace(msg.Header.Get("Message-ID"))
	if messageID == "" {
		return nil, nil
	}
	references := strings.TrimSpace(msg.Header.Get("References"))
	if references != "" {
		references = references + " " + messageID
	} else {
		references = messageID
	}
	return map[string]string{
		"In-Reply-To": messageID,
		"References":  references,
	}, nil
}

// ---------------------------------------------------------------------------
// GetItem
// ---------------------------------------------------------------------------

// GetItemRequest is the EWS GetItem operation request.
type GetItemRequest struct {
	XMLName      xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetItem"`
	ItemShapeDef ItemShapeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemShape"`
	ItemIDs      ItemIdsType   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// ItemShapeType defines the item properties to return in a GetItem response.
// It mirrors ItemResponseShape but is a distinct type so the Go XML unmarshaler
// doesn't see a conflict between the field's xml tag name (ItemShape) and
// ItemResponseShape.XMLName (ItemResponseShape).
type ItemShapeType struct {
	BaseShape            string                    `xml:"http://schemas.microsoft.com/exchange/services/2006/types BaseShape,omitempty"`
	AdditionalProperties *AdditionalPropertiesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types AdditionalProperties,omitempty"`
}

// ItemIdsType is a list of item IDs.

// ItemIdsType is a list of item IDs.
type ItemIdsType struct {
	XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	Item    []ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
}

// GetItemResponse is the EWS GetItem operation response.
type GetItemResponse struct {
	XMLName xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetItemResponse"`
	Msgs    GetItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetItemResponseMessages wraps GetItem response messages.
type GetItemResponseMessages struct {
	Messages []ItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetItemResponseMessage"`
}

// handleGetItem processes an EWS GetItem SOAP request.
func (s *Server) handleGetItem(ctx context.Context, body []byte) []byte {
	var req GetItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("GetItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("GetItem", errCode, "could not resolve mailbox")
	}

	msgs := make([]ItemResponseMessageType, 0, len(req.ItemIDs.Item))
	for _, id := range req.ItemIDs.Item {
		// Check collab store first for proper XML typing.
		if s.collabStore != nil {
			if resp := s.getCollabGetItemResponse(ctx, mboxKey, id); resp != nil {
				return resp
			}
		}
		msg := s.getItemByID(ctx, mboxID, mboxKey, id)
		msgs = append(msgs, msg)
	}

	resp := GetItemResponse{}
	resp.Msgs.Messages = msgs
	return buildResponseEnvelope(resp)
}

// getCollabGetItemResponse checks if the item ID corresponds to a collaboration
// store item (calendar, contact, or task). If found, it builds a proper
// GetItemResponse with the correct XML element type (CalendarItem, Contact, or
// Task) instead of the generic Message element. Returns nil if not found.
func (s *Server) getCollabGetItemResponse(ctx context.Context, mboxKey string, id ItemIdType) []byte {
	_ = ctx
	email := strings.TrimPrefix(mboxKey, "e:")
	raw := id.ID

	// Try contact.
	if ctID, err := semcore.NewContactId(raw); err == nil {
		if rec, err := s.collabStore.GetContactByID(ctID); err == nil {
			return buildContactGetItemFromRec(rec)
		}
	}
	// Try calendar.
	if calID, err := semcore.NewCalendarItemId(raw); err == nil {
		if rec, err := s.collabStore.GetCalendarItemByID(calID); err == nil {
			return buildCalendarGetItemFromRec(rec)
		}
	}
	// Try task.
	if tkID, err := semcore.NewTaskId(raw); err == nil {
		if rec, err := s.collabStore.GetTaskByID(tkID); err == nil {
			return buildTaskGetItemFromRec(rec)
		}
	}
	_ = email
	return nil
}

// icalComponent returns the body of the first iCalendar component named comp
// (e.g. "VEVENT") — the lines between BEGIN:comp and the matching END:comp.
// Properties MUST be read from the right component: a timezone-anchored event
// carries a VTIMEZONE whose own DTSTART/RRULE would otherwise be picked up by a
// whole-document scan (the VTIMEZONE precedes the VEVENT), so a weekly event in
// America/New_York would mis-report the zone's DST rule. Falls back to the full
// data when the component is absent.
func icalComponent(data, comp string) string {
	begin, end := "BEGIN:"+comp, "END:"+comp
	lines := strings.Split(data, "\n")
	start := -1
	for i, ln := range lines {
		trimmed := strings.TrimRight(ln, "\r")
		if start < 0 {
			if strings.EqualFold(trimmed, begin) {
				start = i + 1
			}
			continue
		}
		if strings.EqualFold(trimmed, end) {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return data
}

// extractDirProp returns the value of the first iCalendar/vCard content line
// whose property name matches name (case-insensitive). Property parameters
// (after ';') and folding are handled minimally, which is sufficient for the
// single-line properties uMailServer emits.
func extractDirProp(data, name string) string {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := line[:colon]
		if semi := strings.IndexByte(key, ';'); semi >= 0 {
			key = key[:semi]
		}
		if strings.EqualFold(key, name) {
			return line[colon+1:]
		}
	}
	return ""
}

// extractDirPropParam returns a named parameter (e.g. "TZID") of a directory
// property line, or "" when absent.
func extractDirPropParam(data, name, param string) string {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := line[:colon]
		semi := strings.IndexByte(key, ';')
		base := key
		if semi >= 0 {
			base = key[:semi]
		}
		if !strings.EqualFold(base, name) {
			continue
		}
		if semi >= 0 {
			for _, p := range strings.Split(key[semi+1:], ";") {
				if eq := strings.IndexByte(p, '='); eq > 0 && strings.EqualFold(strings.TrimSpace(p[:eq]), param) {
					return strings.TrimSpace(p[eq+1:])
				}
			}
		}
		return ""
	}
	return ""
}

// icalEventInstant reads a date-time property (DTSTART/DTEND/DUE) from raw iCal
// and returns the correct UTC instant, honoring a TZID parameter. Treating a
// civil-local "DTSTART;TZID=America/New_York:..." value as UTC would shift the
// event by the zone's offset, so the TZID is resolved via the IANA database.
func icalEventInstant(data, prop string) time.Time {
	val := extractDirProp(data, prop)
	if val == "" {
		return time.Time{}
	}
	if strings.HasSuffix(val, "Z") {
		if t, err := time.Parse("20060102T150405", strings.TrimSuffix(val, "Z")); err == nil {
			return t.UTC()
		}
		return time.Time{}
	}
	if tzid := extractDirPropParam(data, prop, "TZID"); tzid != "" && !strings.EqualFold(tzid, "UTC") {
		if loc, err := time.LoadLocation(tzid); err == nil {
			if t, err := time.ParseInLocation("20060102T150405", val, loc); err == nil {
				return t.UTC()
			}
		}
	}
	if t, err := time.Parse("20060102T150405", val); err == nil {
		return t
	}
	return time.Time{}
}

// icalToEWSDateTime converts an iCalendar DTSTART/DTEND value to the EWS
// date-time wire format. Empty input yields empty output.
func icalToEWSDateTime(v string) string {
	if v == "" {
		return ""
	}
	t, _ := parseICalDateTimeTZ(v)
	if t.IsZero() {
		return ""
	}
	return FormatEWSDateTime(t)
}

// icalStatusToEWS maps an iCalendar VTODO STATUS to the EWS task:Status value.
func icalStatusToEWS(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "COMPLETED":
		return "Completed"
	case "IN-PROCESS":
		return "InProgress"
	case "CANCELLED": //nolint:misspell // RFC 5545 spells the iCal STATUS value "CANCELLED".
		return "Deferred"
	case "NEEDS-ACTION", "":
		return "NotStarted"
	default:
		return "NotStarted"
	}
}

// icalBusyToEWS maps an iCalendar X-MICROSOFT-CDO-BUSYSTATUS value to the EWS
// LegacyFreeBusyType used in free/busy responses. Unknown/empty defaults to Busy.
func icalBusyToEWS(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "FREE":
		return "Free"
	case "TENTATIVE":
		return "Tentative"
	case "OOF":
		return "OOF"
	case "BUSY", "":
		return "Busy"
	default:
		return "Busy"
	}
}

// collabGetItemEnvelope wraps a typed collab item element in a full
// GetItemResponse SOAP envelope. itemXML is the serialized <t:Contact>,
// <t:CalendarItem>, or <t:Task> element.
func collabGetItemEnvelope(itemXML string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:GetItemResponse><m:ResponseMessages><m:GetItemResponseMessage ResponseClass="Success">`)
	b.WriteString(`<m:ResponseCode>NoError</m:ResponseCode><m:Items>`)
	b.WriteString(itemXML)
	b.WriteString(`</m:Items></m:GetItemResponseMessage></m:ResponseMessages></m:GetItemResponse>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return b.Bytes()
}

// buildContactGetItemFromRec projects a stored contact's vCard into an EWS
// <t:Contact> GetItem response.
func buildContactGetItemFromRec(rec *semcore.StoredContactIdentity) []byte {
	var b bytes.Buffer
	b.WriteString(`<t:Contact>`)
	b.WriteString(`<t:ItemId Id="` + xmlEsc(rec.ID.String()) + `" ChangeKey="` + xmlEsc(rec.ChangeKey.String()) + `"/>`)
	if fn := extractDirProp(rec.RawData, "FN"); fn != "" {
		b.WriteString(`<t:Subject>` + xmlEsc(fn) + `</t:Subject>`)
		b.WriteString(`<t:DisplayName>` + xmlEsc(fn) + `</t:DisplayName>`)
	}
	// N: Surname;Given;Additional;Prefix;Suffix
	if n := extractDirProp(rec.RawData, "N"); n != "" {
		parts := strings.Split(n, ";")
		if len(parts) > 0 && parts[0] != "" {
			b.WriteString(`<t:Surname>` + xmlEsc(parts[0]) + `</t:Surname>`)
		}
		if len(parts) > 1 && parts[1] != "" {
			b.WriteString(`<t:GivenName>` + xmlEsc(parts[1]) + `</t:GivenName>`)
		}
	}
	b.WriteString(`</t:Contact>`)
	return collabGetItemEnvelope(b.String())
}

// buildCalendarGetItemFromRec projects a stored calendar item's iCalendar into
// an EWS <t:CalendarItem> GetItem response.
func buildCalendarGetItemFromRec(rec *semcore.StoredCalendarItemIdentity) []byte {
	// Scope all reads to the VEVENT so a timezone-anchored event's VTIMEZONE
	// (which carries its own DTSTART/RRULE for the DST transitions) cannot leak
	// into Start/End or the recurrence pattern.
	ev := icalComponent(rec.RawData, "VEVENT")
	var b bytes.Buffer
	b.WriteString(`<t:CalendarItem>`)
	b.WriteString(`<t:ItemId Id="` + xmlEsc(rec.ID.String()) + `" ChangeKey="` + xmlEsc(rec.ChangeKey.String()) + `"/>`)
	if subj := extractDirProp(ev, "SUMMARY"); subj != "" {
		b.WriteString(`<t:Subject>` + xmlEsc(subj) + `</t:Subject>`)
	}
	if t := icalEventInstant(ev, "DTSTART"); !t.IsZero() {
		b.WriteString(`<t:Start>` + FormatEWSDateTime(t) + `</t:Start>`)
	}
	if t := icalEventInstant(ev, "DTEND"); !t.IsZero() {
		b.WriteString(`<t:End>` + FormatEWSDateTime(t) + `</t:End>`)
	}
	if loc := extractDirProp(ev, "LOCATION"); loc != "" {
		b.WriteString(`<t:Location>` + xmlEsc(loc) + `</t:Location>`)
	}
	b.WriteString(`<t:UID>` + xmlEsc(rec.IcalUID) + `</t:UID>`)
	// A recurring event carries its full <t:Recurrence> (pattern + boundary) so
	// the client renders the whole series, not just the master occurrence. The
	// pattern is derived from the stored RRULE anchored to the event's CIVIL-local
	// start (its DTSTART;TZID), so weekday/day-of-month/month are the wall-clock
	// ones, not the UTC instant's.
	if rrule := extractDirProp(ev, "RRULE"); rrule != "" {
		b.WriteString(`<t:IsRecurring>true</t:IsRecurring>`)
		localStart := icalEventInstant(ev, "DTSTART")
		if tzid := extractDirPropParam(ev, "DTSTART", "TZID"); tzid != "" {
			if loc, err := time.LoadLocation(tzid); err == nil {
				localStart = localStart.In(loc)
			}
		}
		b.WriteString(ewsRecurrenceXML(rrule, localStart))
	}
	b.WriteString(`</t:CalendarItem>`)
	return collabGetItemEnvelope(b.String())
}

// buildTaskGetItemFromRec projects a stored task's iCalendar VTODO into an EWS
// <t:Task> GetItem response.
func buildTaskGetItemFromRec(rec *semcore.StoredTaskIdentity) []byte {
	var b bytes.Buffer
	b.WriteString(`<t:Task>`)
	b.WriteString(`<t:ItemId Id="` + xmlEsc(rec.ID.String()) + `" ChangeKey="` + xmlEsc(rec.ChangeKey.String()) + `"/>`)
	if subj := extractDirProp(rec.RawData, "SUMMARY"); subj != "" {
		b.WriteString(`<t:Subject>` + xmlEsc(subj) + `</t:Subject>`)
	}
	if due := icalToEWSDateTime(extractDirProp(rec.RawData, "DUE")); due != "" {
		b.WriteString(`<t:DueDate>` + due + `</t:DueDate>`)
	}
	b.WriteString(`<t:Status>` + icalStatusToEWS(extractDirProp(rec.RawData, "STATUS")) + `</t:Status>`)
	b.WriteString(`</t:Task>`)
	return collabGetItemEnvelope(b.String())
}

// getItemByID retrieves one item by ItemId.
func (s *Server) getItemByID(ctx context.Context, mboxID semcore.MailboxId, mboxKey string, id ItemIdType) ItemResponseMessageType {
	itemID, err := semcore.NewItemId(id.ID)
	if err != nil {
		return errorItemMsg("GetItem", ErrErrorInvalidId, err.Error())
	}

	// Look up item identity.
	rec, err := s.identity.GetItemIdentity(itemID)
	if err != nil {
		// Not found in identity store — try collaboration store (calendar, contact, task).
		if errors.Is(err, semcore.ErrItemNotFound) && s.collabStore != nil {
			return s.getItemByIDFromCollab(ctx, mboxKey, id)
		}
		return errorItemMsg("GetItem", ErrErrorItemNotFound, "item not found: "+id.ID)
	}

	// Verify mailbox ownership.
	if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
		return errorItemMsg("GetItem", ErrErrorAccessDenied, "item belongs to a different mailbox")
	}

	// ChangeKey is intentionally NOT validated on reads. Standard EWS (and
	// Exchange) resolve GetItem purely by item id; the ChangeKey is an
	// optimistic-concurrency token used only by write operations. Validating
	// it here would reject a client refreshing an item whose ChangeKey was
	// advanced out of band (e.g. after DeleteAttachment).

	// Retrieve raw MIME content from msgStore.
	// rec.Email is the user key and rec.MsgKey is the blob key.
	rawMsg, err := s.msgStore.ReadMessage(rec.Email, rec.MsgKey)
	if err != nil {
		return errorItemMsg("GetItem", ErrErrorInternalServer, "failed to retrieve message: "+err.Error())
	}

	// Parse MIME headers for display.
	subject, from, dateStr, bodyType, bodyText, toAddrs, attachments := parseMimeWithAttachments(rawMsg)

	// Assign a self-describing AttachmentId to each attachment so clients treat
	// them as already-created and can later fetch content via GetAttachment.
	for i := range attachments {
		attachments[i].AttachmentID = &AttachmentIdType{ID: makeAttachmentID(itemID.String(), i)}
	}

	toRecipients := make([]MailboxTypeResponse, 0, len(toAddrs))
	for _, addr := range toAddrs {
		toRecipients = append(toRecipients, MailboxTypeResponse{EmailAddress: addr})
	}

	msgResp := MessageTypeResponse{
		ItemID: ItemIdType{
			ID: itemID.String(),
			CK: rec.ChangeKey.String(),
		},
		ParentFolderID:   FolderIdComponents{ID: rec.FolderID.String()},
		ItemClass:        rawHeaderValue(rawMsg, "X-Message-Class"),
		Subject:          subject,
		DateTimeReceived: dateStr,
		Size:             len(rawMsg),
		Body: BodyTypeResponse{
			BodyType: bodyType,
			Text:     bodyText,
		},
		From:         mailboxFromHeader(from),
		Sender:       mailboxFromHeader(rawHeaderValue(rawMsg, "Sender")),
		ToRecipients: recipientsWrap(toRecipients),
		CcRecipients: recipientsWrap(recipientsFromHeader(rawMsg, "Cc")),
		IsRead:       rec.IsRead,
		Categories:   categoriesResponse(rec.Categories),
	}
	if !rec.ConversationID.IsZero() {
		msgResp.ConversationID = &ConversationIdType{ID: rec.ConversationID.String()}
	}
	hdrs := parseInternetHeaders(rawMsg)
	if len(hdrs) > 0 {
		msgResp.InternetHeaders = &InternetMessageHeadersType{Headers: hdrs}
	}
	if len(attachments) > 0 {
		msgResp.Attachments = &AttachmentsType{FileAttachments: attachments}
	}
	for _, h := range hdrs {
		if strings.EqualFold(h.Name, hdrMeeting) && strings.TrimSpace(h.Value) == "1" {
			msgResp.isMeetingRequest = true
			break
		}
	}

	// A meeting request must be returned under the MeetingRequest element so the
	// client exposes accept/decline; ordinary mail uses Message.
	if msgResp.isMeetingRequest {
		return ItemResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			Items:         ItemsContainer{MeetingRequests: []MeetingRequestResponse{toMeetingRequestResponse(msgResp)}},
		}
	}

	return ItemResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items:         ItemsContainer{Items: []MessageTypeResponse{msgResp}},
	}
}

// getItemByIDFromCollab tries to find the item in the collaboration store
// (calendar, contact, or task) by its ID string. Returns ErrorItemNotFound if
// the ID doesn't match any collab item type.
func (s *Server) getItemByIDFromCollab(ctx context.Context, mboxKey string, id ItemIdType) ItemResponseMessageType {
	email := strings.TrimPrefix(mboxKey, "e:")
	raw := id.ID

	// Try calendar item.
	if calID, err := semcore.NewCalendarItemId(raw); err == nil && s.collabStore != nil {
		if rec, err := s.collabStore.GetCalendarItemByID(calID); err == nil {
			folderID := FolderIdComponents{ID: rec.FolderID.String()}
			return ItemResponseMessageType{
				ResponseClass: "Success",
				ResponseCode:  ResponseCodeType{Value: ErrNoError},
				Items: ItemsContainer{Items: []MessageTypeResponse{{
					ItemID:         ItemIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
					ParentFolderID: folderID,
					Subject:        rec.IcalUID,
				}}},
			}
		}
	}

	// Try contact item.
	if ctID, err := semcore.NewContactId(raw); err == nil && s.collabStore != nil {
		if rec, err := s.collabStore.GetContactByID(ctID); err == nil {
			folderID := FolderIdComponents{ID: rec.FolderID.String()}
			return ItemResponseMessageType{
				ResponseClass: "Success",
				ResponseCode:  ResponseCodeType{Value: ErrNoError},
				Items: ItemsContainer{Items: []MessageTypeResponse{{
					ItemID:         ItemIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
					ParentFolderID: folderID,
					Subject:        rec.IcalUID,
				}}},
			}
		}
	}

	// Try task item.
	if tkID, err := semcore.NewTaskId(raw); err == nil && s.collabStore != nil {
		if rec, err := s.collabStore.GetTaskByID(tkID); err == nil {
			folderID := FolderIdComponents{ID: rec.FolderID.String()}
			return ItemResponseMessageType{
				ResponseClass: "Success",
				ResponseCode:  ResponseCodeType{Value: ErrNoError},
				Items: ItemsContainer{Items: []MessageTypeResponse{{
					ItemID:         ItemIdType{ID: rec.ID.String(), CK: rec.ChangeKey.String()},
					ParentFolderID: folderID,
					Subject:        rec.IcalUID,
				}}},
			}
		}
	}

	// Not found in any store.
	_ = email
	return errorItemMsg("GetItem", ErrErrorItemNotFound, "item not found: "+id.ID)
}

// deleteItemFromCollab tries to delete an item from the collaboration store
// (calendar, contact, or task) by its ID string. Returns true on success.
func (s *Server) deleteItemFromCollab(mailboxKey, raw string) bool {
	_ = mailboxKey
	if s.collabStore == nil {
		return false
	}

	// Try calendar item.
	if calID, err := semcore.NewCalendarItemId(raw); err == nil {
		if rec, err := s.collabStore.GetCalendarItemByID(calID); err == nil {
			ck, ckErr := semcore.NewCalendarChangeKey(rec.ChangeKey.String())
			if ckErr == nil {
				_ = s.collabStore.DeleteCalendarItemIdentity(rec.RawHash, ck) //nolint:errcheck
			}
			return true
		}
	}

	// Try contact item.
	if ctID, err := semcore.NewContactId(raw); err == nil {
		if rec, err := s.collabStore.GetContactByID(ctID); err == nil {
			ck, ckErr := semcore.NewContactChangeKey(rec.ChangeKey.String())
			if ckErr == nil {
				_ = s.collabStore.DeleteContactIdentity(rec.RawHash, ck) //nolint:errcheck
			}
			return true
		}
	}

	// Try task item.
	if tkID, err := semcore.NewTaskId(raw); err == nil {
		if rec, err := s.collabStore.GetTaskByID(tkID); err == nil {
			ck, ckErr := semcore.NewTaskChangeKey(rec.ChangeKey.String())
			if ckErr == nil {
				_ = s.collabStore.DeleteTaskIdentity(rec.RawHash, ck) //nolint:errcheck
			}
			return true
		}
	}

	return false
}

// updateItemInCollab tries to update an item in the collaboration store.
// Handles contacts, calendar items, and tasks. Returns nil if the item is not
// found in any collab store.
func (s *Server) updateItemInCollab(mailboxKey string, ic ItemChangeOp) *ItemResponseMessageType {
	raw := ic.ItemID.ID
	_ = mailboxKey

	if s.collabStore == nil {
		return nil
	}

	collabResult := func(id, ck string) *ItemResponseMessageType {
		return &ItemResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			Items: ItemsContainer{Items: []MessageTypeResponse{{
				ItemID: ItemIdType{ID: id, CK: ck},
			}}},
		}
	}

	// Contact: mutate the stored vCard.
	if ctID, err := semcore.NewContactId(raw); err == nil {
		if rec, err := s.collabStore.GetContactByID(ctID); err == nil {
			data := rec.RawData
			for _, op := range ic.Updates.Ops {
				if op.Contact == nil {
					continue
				}
				if v := op.Contact.DisplayName; v != "" {
					data = setDirProp(data, "FN", v)
				}
				if v := op.Contact.FullName; v != "" {
					data = setDirProp(data, "FN", v)
				}
				if v := op.Contact.Surname; v != "" {
					data = setDirProp(data, "N", v+";;;;")
				}
			}
			oldCK := rec.ChangeKey
			newCK, ckErr := semcore.NewContactChangeKey(generateID())
			if ckErr != nil {
				return collabErrItemMsg(ckErr)
			}
			rec.RawData = data
			rec.ChangeKey = newCK
			if err := s.collabStore.PutContactIdentity(rec.RawHash, rec, oldCK); err != nil {
				return collabErrItemMsg(err)
			}
			return collabResult(rec.ID.String(), newCK.String())
		}
	}

	// Calendar: mutate the stored iCalendar VEVENT.
	if calID, err := semcore.NewCalendarItemId(raw); err == nil {
		if rec, err := s.collabStore.GetCalendarItemByID(calID); err == nil {
			data := rec.RawData
			for _, op := range ic.Updates.Ops {
				if op.CalendarItem == nil {
					continue
				}
				if v := op.CalendarItem.Subject; v != "" {
					data = setDirProp(data, "SUMMARY", v)
				}
				if v := op.CalendarItem.Location; v != "" {
					data = setDirProp(data, "LOCATION", v)
				}
				if v := op.CalendarItem.Start; v != "" {
					if t, perr := ParseEWSDateTime(v); perr == nil {
						data = setDirProp(data, "DTSTART", formatICalDateTime(t))
					}
				}
				if v := op.CalendarItem.End; v != "" {
					if t, perr := ParseEWSDateTime(v); perr == nil {
						data = setDirProp(data, "DTEND", formatICalDateTime(t))
					}
				}
			}
			oldCK := rec.ChangeKey
			newCK, ckErr := semcore.NewCalendarChangeKey(generateID())
			if ckErr != nil {
				return collabErrItemMsg(ckErr)
			}
			rec.RawData = data
			rec.ChangeKey = newCK
			if err := s.collabStore.PutCalendarItemIdentity(rec.RawHash, rec, oldCK); err != nil {
				return collabErrItemMsg(err)
			}
			return collabResult(rec.ID.String(), newCK.String())
		}
	}

	// Task: mutate the stored iCalendar VTODO.
	if tkID, err := semcore.NewTaskId(raw); err == nil {
		if rec, err := s.collabStore.GetTaskByID(tkID); err == nil {
			data := rec.RawData
			for _, op := range ic.Updates.Ops {
				if op.Task == nil {
					continue
				}
				if v := op.Task.Subject; v != "" {
					data = setDirProp(data, "SUMMARY", v)
				}
				if v := op.Task.Status; v != "" {
					data = setDirProp(data, "STATUS", ewsTaskStatusToICal(v))
				}
			}
			oldCK := rec.ChangeKey
			newCK, ckErr := semcore.NewTaskChangeKey(generateID())
			if ckErr != nil {
				return collabErrItemMsg(ckErr)
			}
			rec.RawData = data
			rec.ChangeKey = newCK
			if err := s.collabStore.PutTaskIdentity(rec.RawHash, rec, oldCK); err != nil {
				return collabErrItemMsg(err)
			}
			return collabResult(rec.ID.String(), newCK.String())
		}
	}

	return nil
}

// collabErrItemMsg wraps a collab-store error as an UpdateItem error message.
func collabErrItemMsg(err error) *ItemResponseMessageType {
	msg := errorItemMsg("UpdateItem", ErrErrorInternalServer, err.Error())
	return &msg
}

// setDirProp replaces the value of an iCalendar/vCard content line, or inserts a
// new line before the closing END: line when the property is absent. CRLF line
// endings are preserved.
func setDirProp(data, name, value string) string {
	lines := strings.Split(data, "\n")
	for i, line := range lines {
		l := strings.TrimRight(line, "\r")
		colon := strings.IndexByte(l, ':')
		if colon < 0 {
			continue
		}
		key := l[:colon]
		if semi := strings.IndexByte(key, ';'); semi >= 0 {
			key = key[:semi]
		}
		if strings.EqualFold(key, name) {
			lines[i] = name + ":" + value + "\r"
			return strings.Join(lines, "\n")
		}
	}
	// Not found: insert before the last END: line.
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimRight(lines[i], "\r"), "END:") {
			lines = append(lines[:i], append([]string{name + ":" + value + "\r"}, lines[i:]...)...)
			return strings.Join(lines, "\n")
		}
	}
	return data
}

// ewsTaskStatusToICal maps an EWS task:Status value to its iCalendar VTODO STATUS.
func ewsTaskStatusToICal(s string) string {
	switch s {
	case "Completed":
		return "COMPLETED"
	case "InProgress", "WaitingOnOthers":
		return "IN-PROCESS"
	case "Deferred":
		return "CANCELLED" //nolint:misspell // RFC 5545 spells the iCal STATUS value "CANCELLED".
	default:
		return "NEEDS-ACTION"
	}
}

// parseMimeHeaders extracts Subject, From, Date, Body, and To from raw MIME.
func parseMimeHeaders(data []byte) (subject, from, date, bodyType, body string, toAddrs []string) {
	subject, from, date, bodyType, body, toAddrs, _ = parseMimeWithAttachments(data)
	return
}

// parseMimeWithAttachments extracts all standard headers plus inline/file attachments.
// decodeBase64Lenient decodes base64 content that may contain the line breaks
// and whitespace mandated by MIME (RFC 2045), which the standard base64 decoder
// rejects.
func decodeBase64Lenient(b []byte) ([]byte, error) {
	cleaned := strings.NewReplacer("\r", "", "\n", "", "\t", "", " ", "").Replace(string(b))
	return base64.StdEncoding.DecodeString(cleaned)
}

func parseMimeWithAttachments(data []byte) (subject, from, date, bodyType, body string, toAddrs []string, attachments []FileAttachmentType) {
	if len(data) == 0 {
		return "", "", "", "Text", "", nil, nil
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return "", "", "", "Text", "", nil, nil
	}
	h := msg.Header

	contentType := h.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(contentType), "text/html") {
		bodyType = "HTML"
	} else {
		bodyType = "Text"
	}

	mediaType, params, _ := mime.ParseMediaType(contentType) //nolint:errcheck // malformed Content-Type yields empty mediaType, handled below.
	boundary := params["boundary"]

	var bodyBytes []byte
	bodyBytes, _ = io.ReadAll(msg.Body) //nolint:errcheck

	if boundary != "" && strings.HasPrefix(mediaType, "multipart/") {
		// Multi-part: walk parts to extract body and attachments using
		// Go's standard mime/multipart package.
		mpr := multipart.NewReader(strings.NewReader(string(data)), boundary)
		for {
			part, err := mpr.NextPart()
			if err != nil {
				break
			}
			partContentType := part.Header.Get("Content-Type")
			partMediaType, partParams, _ := mime.ParseMediaType(partContentType) //nolint:errcheck // malformed part Content-Type yields empty values, handled below.
			cd := part.Header.Get("Content-Disposition")
			isInline := false
			if cd != "" {
				// The inline marker is the disposition VALUE ("inline"), not a
				// parameter. mime.ParseMediaType returns it as the media type.
				disp, _, _ := mime.ParseMediaType(cd) //nolint:errcheck // malformed disposition is treated as not-inline.
				if strings.EqualFold(disp, "inline") {
					isInline = true
				}
			}
			partCID := strings.Trim(part.Header.Get("Content-ID"), "<>")
			partName := partParams["name"]
			if partName == "" {
				partName = partParams["filename"]
			}
			partBody, _ := io.ReadAll(part) //nolint:errcheck // partial read still yields usable bytes; the part is best-effort.
			// mime/multipart transparently decodes only quoted-printable; decode
			// base64-encoded parts ourselves so partBody holds the raw bytes.
			if strings.EqualFold(part.Header.Get("Content-Transfer-Encoding"), "base64") {
				if decoded, derr := decodeBase64Lenient(partBody); derr == nil {
					partBody = decoded
				}
			}
			ctLower := strings.ToLower(partMediaType)
			if strings.HasPrefix(ctLower, "text/") && body == "" && !isInline {
				body = string(partBody)
			}
			if isInline || partName != "" || partCID != "" || strings.Contains(ctLower, "attachment") {
				content := base64.StdEncoding.EncodeToString(partBody)
				att := FileAttachmentType{
					Name:        partName,
					ContentType: partMediaType,
					ContentID:   partCID,
					Content:     content,
				}
				if isInline {
					att.IsInline = boolPtr(true)
				}
				attachments = append(attachments, att)
			}
		}
		if body == "" {
			body = string(bodyBytes)
		}
	} else {
		body = string(bodyBytes)
	}

	toHeader := h.Get("To")
	if toHeader != "" {
		var addrs []*mail.Address
		addrs, _ = mail.ParseAddressList(toHeader) //nolint:errcheck
		for _, a := range addrs {
			toAddrs = append(toAddrs, a.Address)
		}
	}

	return strings.TrimSpace(h.Get("Subject")), h.Get("From"), h.Get("Date"), bodyType, body, toAddrs, attachments
}

func prepareMessageForSubmission(data []byte) (from string, recipients []string, sanitized []byte, err error) {
	if len(data) == 0 {
		return "", nil, nil, errors.New("empty message")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return "", nil, nil, fmt.Errorf("parse message: %w", err)
	}

	for _, headerName := range []string{"To", "Cc", "Bcc"} {
		headerValue := msg.Header.Get(headerName)
		if headerValue == "" {
			continue
		}
		addrs, err := mail.ParseAddressList(headerValue)
		if err != nil {
			return "", nil, nil, fmt.Errorf("parse %s header: %w", headerName, err)
		}
		for _, addr := range addrs {
			recipients = append(recipients, addr.Address)
		}
	}
	if len(recipients) == 0 {
		return "", nil, nil, errors.New("message has no recipients")
	}

	fromAddrs, err := mail.ParseAddressList(msg.Header.Get("From"))
	if err != nil {
		return "", nil, nil, fmt.Errorf("parse From header: %w", err)
	}
	if len(fromAddrs) == 0 {
		return "", nil, nil, errors.New("message has no From header")
	}

	return fromAddrs[0].Address, recipients, stripBccHeader(data), nil
}

func (s *Server) submitOutboundMessage(from string, recipients []string, data []byte) error {
	if s.submitMessage != nil {
		return s.submitMessage(from, recipients, data)
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:25", 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck

	client, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck

	if err := client.Hello("localhost"); err != nil {
		return err
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}

	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close() //nolint:errcheck
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func stripBccHeader(data []byte) []byte {
	headerSep := []byte("\r\n\r\n")
	lineSep := []byte("\r\n")
	parts := bytes.SplitN(data, headerSep, 2)
	if len(parts) != 2 {
		headerSep = []byte("\n\n")
		lineSep = []byte("\n")
		parts = bytes.SplitN(data, headerSep, 2)
		if len(parts) != 2 {
			return data
		}
	}

	lines := bytes.Split(parts[0], lineSep)
	filtered := make([][]byte, 0, len(lines))
	skippingBccContinuation := false
	for _, line := range lines {
		if skippingBccContinuation {
			if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				continue
			}
			skippingBccContinuation = false
		}
		if bytes.HasPrefix(bytes.ToLower(line), []byte("bcc:")) {
			skippingBccContinuation = true
			continue
		}
		filtered = append(filtered, line)
	}

	sanitized := bytes.Join(filtered, lineSep)
	sanitized = append(sanitized, headerSep...)
	sanitized = append(sanitized, parts[1]...)
	return sanitized
}

func prependHeader(data []byte, name, value string) []byte {
	if len(data) == 0 {
		return data
	}
	headerSep := []byte("\r\n\r\n")
	lineBreak := "\r\n"
	parts := bytes.SplitN(data, headerSep, 2)
	if len(parts) != 2 {
		headerSep = []byte("\n\n")
		lineBreak = "\n"
		parts = bytes.SplitN(data, headerSep, 2)
		if len(parts) != 2 {
			return data
		}
	}
	headerLine := []byte(name + ": " + value + lineBreak)
	out := make([]byte, 0, len(data)+len(headerLine))
	out = append(out, parts[0]...)
	out = append(out, []byte(lineBreak)...)
	out = append(out, headerLine...)
	out = append(out, headerSep...)
	out = append(out, parts[1]...)
	return out
}

func categoriesResponse(categories []string) *MessageCategoriesType {
	if len(categories) == 0 {
		return nil
	}
	return &MessageCategoriesType{Strings: append([]string(nil), categories...)}
}

// ---------------------------------------------------------------------------
// UpdateItem
// ---------------------------------------------------------------------------

// UpdateItemRequest is the EWS UpdateItem operation request.
type UpdateItemRequest struct {
	XMLName     xml.Name        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateItem"`
	ItemChanges ItemChangesList `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
}

// ItemChangesList wraps the ItemChange list.
type ItemChangesList struct {
	XMLName xml.Name       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
	Changes []ItemChangeOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
}

// ItemChangeOp represents one item change in UpdateItem.
type ItemChangeOp struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
	ItemID  struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
		ID      string   `xml:"Id,attr"`
		CK      string   `xml:"ChangeKey,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	Updates ItemUpdatesOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
}

// ItemUpdatesOp wraps update operations.
type ItemUpdatesOp struct {
	XMLName xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
	Ops     []ItemUpdateField `xml:"http://schemas.microsoft.com/exchange/services/2006/types SetItemField"`
}

// ItemUpdateField is one update operation on an item field.
type ItemUpdateField struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types SetItemField"`
	FieldURI struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
		URI     string   `xml:"FieldURI,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
	Message      ItemUpdateValue     `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	Contact      *ItemUpdateContact  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Contact,omitempty"`
	CalendarItem *ItemUpdateCalendar `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItem,omitempty"`
	Task         *ItemUpdateTask     `xml:"http://schemas.microsoft.com/exchange/services/2006/types Task,omitempty"`
}

type ItemUpdateContact struct {
	FullName    string `xml:"http://schemas.microsoft.com/exchange/services/2006/types FullName,omitempty"`
	DisplayName string `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName,omitempty"`
	GivenName   string `xml:"http://schemas.microsoft.com/exchange/services/2006/types GivenName,omitempty"`
	Surname     string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Surname,omitempty"`
}

type ItemUpdateCalendar struct {
	Subject  string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Location string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Location,omitempty"`
	Start    string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Start,omitempty"`
	End      string `xml:"http://schemas.microsoft.com/exchange/services/2006/types End,omitempty"`
}

type ItemUpdateTask struct {
	Subject         string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Status          string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Status,omitempty"`
	PercentComplete string `xml:"http://schemas.microsoft.com/exchange/services/2006/types PercentComplete,omitempty"`
}

type ItemUpdateValue struct {
	Subject *struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject"`
		Value   string   `xml:",chardata"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject"`
	Body   *BodyType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body"`
	IsRead *struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsRead"`
		Value   bool     `xml:",chardata"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsRead"`
	Categories *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories"`
}

// UpdateItemResponse is the EWS UpdateItem operation response.
type UpdateItemResponse struct {
	XMLName xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateItemResponse"`
	Msgs    UpdateItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// UpdateItemResponseMessages wraps UpdateItem response messages.
type UpdateItemResponseMessages struct {
	Messages []ItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateItemResponseMessage"`
}

// handleUpdateItem processes an EWS UpdateItem SOAP request.
func (s *Server) handleUpdateItem(ctx context.Context, body []byte) []byte {
	var req UpdateItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("UpdateItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("UpdateItem", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorItemResponseXML("UpdateItem", ErrErrorMailboxNotFound, err.Error())
	}

	// Delegate enforcement (VAL-DIR-002): check write permission for non-owners.
	actorEmail := s.getActingEmail(ctx)
	if msg, code := s.checkDelegatePermission(mboxID, mailboxKey, actorEmail, "write"); code != "" {
		return s.errorItemResponseXML("UpdateItem", code, msg)
	}

	// Build delegate audit context for lifecycle emission (VAL-DIR-014).
	delegateCtx := s.buildDelegateAuditContext(ctx, mboxID, mailboxKey)

	msgs := make([]ItemResponseMessageType, 0, len(req.ItemChanges.Changes))
	for _, ic := range req.ItemChanges.Changes {
		itemID, err := semcore.NewItemId(ic.ItemID.ID)
		if err != nil {
			msgs = append(msgs, errorItemMsg("UpdateItem", ErrErrorInvalidId, err.Error()))
			continue
		}

		rec, err := s.identity.GetItemIdentity(itemID)
		if err != nil {
			if errors.Is(err, semcore.ErrItemNotFound) {
				// Try collaboration store (calendar, contact, task) as fallback.
				if s.collabStore != nil {
					if msg := s.updateItemInCollab(mailboxKey, ic); msg != nil {
						msgs = append(msgs, *msg)
						continue
					}
				}
				msgs = append(msgs, errorItemMsg("UpdateItem", ErrErrorItemNotFound, err.Error()))
			} else {
				msgs = append(msgs, errorItemMsg("UpdateItem", ErrErrorInternalServer, err.Error()))
			}
			continue
		}

		if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
			msgs = append(msgs, errorItemMsg("UpdateItem", ErrErrorAccessDenied, "item belongs to a different mailbox"))
			continue
		}

		// Validate ChangeKey if provided.
		if ic.ItemID.CK != "" && ic.ItemID.CK != rec.ChangeKey.String() {
			msgs = append(msgs, errorItemMsg("UpdateItem", ErrErrorItemIdOrChangeKey, "ChangeKey mismatch"))
			continue
		}

		nextIsRead := rec.IsRead
		nextCategories := append([]string(nil), rec.Categories...)
		var updatedIsRead *bool
		var updatedCategories []string
		for _, op := range ic.Updates.Ops {
			switch op.FieldURI.URI {
			case "message:IsRead", "item:IsRead":
				if op.Message.IsRead != nil {
					nextIsRead = op.Message.IsRead.Value
					updatedIsRead = &nextIsRead
				}
			case "item:Categories":
				if op.Message.Categories != nil {
					nextCategories = append([]string(nil), op.Message.Categories.Strings...)
					updatedCategories = nextCategories
				}
			}
		}

		// Advance ChangeKey through update mutation, with delegate audit context (VAL-DIR-014).
		in := &semcore.UpdateInput{
			ItemID:               itemID,
			MailboxID:            mboxID,
			FolderID:             rec.FolderID,
			Actor:                mailboxKey,
			Source:               semcore.MutationSourceEWS,
			DelegateAuditContext: delegateCtx,
		}
		result, err := s.mutationPipe.MutateUpdate(in)
		if err != nil {
			msgs = append(msgs, errorItemMsg("UpdateItem", ErrErrorInternalServer, err.Error()))
			continue
		}
		if updatedIsRead != nil || updatedCategories != nil {
			if err := s.identity.UpdateItemState(itemID, updatedIsRead, updatedCategories); err != nil {
				msgs = append(msgs, errorItemMsg("UpdateItem", ErrErrorInternalServer, err.Error()))
				continue
			}
		}
		// Mirror a read-state change onto the IMAP mailstore \Seen flag so the
		// read state matches across IMAP/JMAP/webmail.
		if updatedIsRead != nil {
			s.mirrorReadFlagToMailstore(rec.Email, rec.FolderID, rec.MsgKey, nextIsRead)
		}

		msgResp := MessageTypeResponse{
			ItemID: ItemIdType{
				ID: itemID.String(),
				CK: result.ChangeKey.String(),
			},
			ParentFolderID: FolderIdComponents{ID: rec.FolderID.String()},
			IsRead:         nextIsRead,
			Categories:     categoriesResponse(nextCategories),
		}
		msgs = append(msgs, ItemResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			Items:         ItemsContainer{Items: []MessageTypeResponse{msgResp}},
		})
	}

	resp := UpdateItemResponse{}
	resp.Msgs.Messages = msgs
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// DeleteItem
// ---------------------------------------------------------------------------

// DeleteItemRequest is the EWS DeleteItem operation request.
type DeleteItemRequest struct {
	XMLName    xml.Name    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItem"`
	ItemIDs    ItemIdsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	DeleteType string      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteType,attr"`
}

// DeleteItemResponse is the EWS DeleteItem operation response.
type DeleteItemResponse struct {
	XMLName xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponse"`
	Msgs    DeleteItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// DeleteItemResponseMessages wraps DeleteItem response messages.
type DeleteItemResponseMessages struct {
	Messages []struct {
		XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponseMessage"`
}

// handleDeleteItem processes an EWS DeleteItem SOAP request.
func (s *Server) handleDeleteItem(ctx context.Context, body []byte) []byte {
	var req DeleteItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("DeleteItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("DeleteItem", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorItemResponseXML("DeleteItem", ErrErrorMailboxNotFound, err.Error())
	}

	// Delegate enforcement (VAL-DIR-002): check delete permission for non-owners.
	actorEmail := s.getActingEmail(ctx)
	if msg, code := s.checkDelegatePermission(mboxID, mailboxKey, actorEmail, "delete"); code != "" {
		return s.errorItemResponseXML("DeleteItem", code, msg)
	}

	// Build delegate audit context for lifecycle emission (VAL-DIR-014).
	delegateCtx := s.buildDelegateAuditContext(ctx, mboxID, mailboxKey)

	hardDelete := req.DeleteType == "HardDelete"

	msgs := make([]struct {
		XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}, 0, len(req.ItemIDs.Item))

	for _, id := range req.ItemIDs.Item {
		itemID, err := semcore.NewItemId(id.ID)
		if err != nil {
			msgs = append(msgs, deleteErrMsg("Error", ResponseCodeType{Value: ErrErrorInvalidId}))
			continue
		}

		rec, err := s.identity.GetItemIdentity(itemID)
		if err != nil {
			// Try collaboration store (calendar, contact, task) as fallback.
			if errors.Is(err, semcore.ErrItemNotFound) && s.collabStore != nil {
				if s.deleteItemFromCollab(mailboxKey, id.ID) {
					msgs = append(msgs, deleteErrMsg("Success", ResponseCodeType{Value: ErrNoError}))
					continue
				}
			}
			msgs = append(msgs, deleteErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}))
			continue
		}

		if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
			msgs = append(msgs, deleteErrMsg("Error", ResponseCodeType{Value: ErrErrorAccessDenied}))
			continue
		}

		// Perform canonical delete mutation.
		in := &semcore.DeleteInput{
			ItemID:               itemID,
			MailboxID:            mboxID,
			FolderID:             rec.FolderID,
			Actor:                mailboxKey,
			Source:               semcore.MutationSourceEWS,
			HardDelete:           hardDelete,
			DelegateAuditContext: delegateCtx,
		}
		if err := s.mutationPipe.MutateDelete(in, s.tombstones); err != nil {
			msgs = append(msgs, deleteErrMsg("Error", ResponseCodeType{Value: ErrErrorInternalServer}))
			continue
		}

		// For soft delete, remove the item from identity store so it's inaccessible
		// via normal operations (GetItem etc.) while it remains in the msgStore.
		if !hardDelete {
			_ = s.identity.DeleteItemIdentity(itemID) //nolint:errcheck
		}

		// Remove the item from the IMAP mailstore index too, so it disappears
		// from IMAP/POP3/JMAP/webmail (cross-protocol integrity).
		s.mirrorDeleteFromMailstore(rec.Email, rec.FolderID, rec.MsgKey)

		msgs = append(msgs, deleteErrMsg("Success", ResponseCodeType{Value: ErrNoError}))
	}

	resp := DeleteItemResponse{}
	resp.Msgs.Messages = msgs
	return buildResponseEnvelope(resp)
}

func deleteErrMsg(class string, code ResponseCodeType) struct {
	XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
} {
	return struct {
		XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}{
		ResponseClass: class,
		ResponseCode:  code,
	}
}

// ---------------------------------------------------------------------------
// SendItem
// ---------------------------------------------------------------------------

// SendItemRequest is the EWS SendItem operation request.
type SendItemRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SendItem"`
	ItemIDs struct {
		XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
		Item    []ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	SaveItemToFolder  *bool `xml:"SaveItemToFolder,attr"`
	SavedItemFolderID struct {
		DistinguishedFolderID *string `xml:"Id,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SavedItemFolderId,omitempty"`
	DelegateMailbox string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DelegateMailbox,omitempty"`
}

// SendItemResponse is the EWS SendItem operation response.
type SendItemResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SendItemResponse"`
	Msgs    struct {
		Messages []SendItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SendItemResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

type SendItemResponseMessageType struct {
	XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SendItemResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
}

// handleSendItem processes an EWS SendItem SOAP request.
// SendItem transitions a draft in Drafts to Sent Items.
func (s *Server) handleSendItem(ctx context.Context, body []byte) []byte {
	var req SendItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("SendItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("SendItem", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorItemResponseXML("SendItem", ErrErrorMailboxNotFound, err.Error())
	}

	// Resolve Sent Items folder as destination.
	sentFolder, err := s.identity.GetFolderByMailbox(mailboxKey, "sent")
	if err != nil {
		return s.errorItemResponseXML("SendItem", ErrErrorInternalServer, "could not find Sent Items folder: "+err.Error())
	}

	responses := make([]SendItemResponseMessageType, 0, len(req.ItemIDs.Item))

	for _, id := range req.ItemIDs.Item {
		itemID, err := semcore.NewItemId(id.ID)
		if err != nil {
			responses = append(responses, sendErrMsg(ResponseCodeType{Value: ErrErrorInvalidId}))
			continue
		}

		rec, err := s.identity.GetItemIdentity(itemID)
		if err != nil {
			responses = append(responses, sendErrMsg(ResponseCodeType{Value: ErrErrorItemNotFound}))
			continue
		}

		if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
			responses = append(responses, sendErrMsg(ResponseCodeType{Value: ErrErrorAccessDenied}))
			continue
		}

		rawMsg, err := s.msgStore.ReadMessage(rec.Email, rec.MsgKey)
		if err != nil {
			responses = append(responses, sendErrMsg(ResponseCodeType{Value: ErrErrorInternalServer}))
			continue
		}

		from, recipients, sanitized, err := prepareMessageForSubmission(rawMsg)
		if err != nil {
			s.logger.Error("SendItem preparation failed", "error", err)
			responses = append(responses, sendErrMsg(ResponseCodeType{Value: ErrErrorInvalidOperation}))
			continue
		}

		if err := s.submitOutboundMessage(from, recipients, sanitized); err != nil {
			s.logger.Error("SendItem submission failed", "from", from, "recipients", recipients, "error", err)
			responses = append(responses, sendErrMsg(ResponseCodeType{Value: ErrErrorInternalServer}))
			continue
		}

		// Move from current folder to Sent Items.
		resultMsg := s.moveItemToFolder(ctx, mboxID, mboxKey, rec.FolderID, sentFolder.FolderID, itemID)
		if resultMsg.ResponseClass == "Error" {
			responses = append(responses, SendItemResponseMessageType{
				ResponseClass: "Error",
				ResponseCode:  resultMsg.ResponseCode,
			})
			continue
		}

		responses = append(responses, sendErrMsg(ResponseCodeType{Value: ErrNoError}))
	}

	resp := SendItemResponse{}
	resp.Msgs.Messages = responses
	return buildResponseEnvelope(resp)
}

func sendErrMsg(code ResponseCodeType) SendItemResponseMessageType {
	responseClass := "Success"
	if code.Value != ErrNoError {
		responseClass = "Error"
	}
	return SendItemResponseMessageType{
		ResponseClass: responseClass,
		ResponseCode:  code,
	}
}

// ---------------------------------------------------------------------------
// MoveItem
// ---------------------------------------------------------------------------

// MoveItemRequest is the EWS MoveItem operation request.
type MoveItemRequest struct {
	XMLName  xml.Name       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MoveItem"`
	ToFolder ToFolderIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ToFolderId"`
	ItemIDs  struct {
		XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
		Item    []ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// ToFolderIdType represents the ToFolderId element in MoveItem/CopyItem.
type ToFolderIdType struct {
	XMLName               xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ToFolderId"`
	DistinguishedFolderID *DistFolderIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId,omitempty"`
	FolderID              *string           `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId,attr,omitempty"`
}

// DistFolderIdType represents a DistinguishedFolderId element with Id attribute.
type DistFolderIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	ID      string   `xml:"Id,attr"`
}

// MoveItemResponse is the EWS MoveItem operation response.
type MoveItemResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MoveItemResponse"`
	Msgs    struct {
		Messages []ItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MoveItemResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// handleMoveItem processes an EWS MoveItem SOAP request.
func (s *Server) handleMoveItem(ctx context.Context, body []byte) []byte {
	var req MoveItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("MoveItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("MoveItem", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorItemResponseXML("MoveItem", ErrErrorMailboxNotFound, err.Error())
	}

	// Resolve destination folder.
	var destFolder semcore.FolderId
	if req.ToFolder.DistinguishedFolderID != nil {
		name := req.ToFolder.DistinguishedFolderID.ID
		role, ok := DistinguishedFolderIDs[name]
		if !ok {
			return s.errorItemResponseXML("MoveItem", ErrErrorFolderNotFound, "unknown distinguished folder")
		}
		// Resolve by role, auto-provisioning the distinguished folder identity if
		// it has not been materialized yet (matching resolveDistinguishedFolder /
		// resolveCollabTargetFolder); otherwise a move to an untouched standard
		// folder fails with FolderNotFound.
		if fld, err := s.identity.GetFolderByMailbox(mailboxKey, role); err == nil {
			destFolder = fld.FolderID
		} else if fid, eerr := s.identity.EnsureFolderId(mailboxKey, name, role); eerr == nil {
			destFolder = fid
		} else {
			return s.errorItemResponseXML("MoveItem", ErrErrorFolderNotFound, err.Error())
		}
	} else if req.ToFolder.FolderID != nil {
		destFolder, err = semcore.NewFolderId(*req.ToFolder.FolderID)
		if err != nil {
			return s.errorItemResponseXML("MoveItem", ErrErrorInvalidId, err.Error())
		}
	}

	if destFolder.IsZero() {
		return s.errorItemResponseXML("MoveItem", ErrErrorFolderNotFound, "destination folder required")
	}

	msgs := make([]ItemResponseMessageType, 0, len(req.ItemIDs.Item))
	for _, id := range req.ItemIDs.Item {
		itemID, err := semcore.NewItemId(id.ID)
		if err != nil {
			msgs = append(msgs, errorItemMsg("MoveItem", ErrErrorInvalidId, err.Error()))
			continue
		}

		rec, err := s.identity.GetItemIdentity(itemID)
		if err != nil {
			msgs = append(msgs, errorItemMsg("MoveItem", ErrErrorItemNotFound, err.Error()))
			continue
		}

		if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
			msgs = append(msgs, errorItemMsg("MoveItem", ErrErrorAccessDenied, "item belongs to a different mailbox"))
			continue
		}

		resultMsg := s.moveItemToFolder(ctx, mboxID, mboxKey, rec.FolderID, destFolder, itemID)
		msgs = append(msgs, resultMsg)
	}

	resp := MoveItemResponse{}
	resp.Msgs.Messages = msgs
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// CopyItem
// ---------------------------------------------------------------------------

// CopyItemRequest is the EWS CopyItem operation request.
type CopyItemRequest struct {
	XMLName  xml.Name       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CopyItem"`
	ToFolder ToFolderIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ToFolderId"`
	ItemIDs  struct {
		XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
		Item    []ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// CopyItemResponse is the EWS CopyItem operation response.
type CopyItemResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CopyItemResponse"`
	Msgs    struct {
		Messages []ItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CopyItemResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// handleCopyItem processes an EWS CopyItem SOAP request.
func (s *Server) handleCopyItem(ctx context.Context, body []byte) []byte {
	var req CopyItemRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("CopyItem", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("CopyItem", errCode, "could not resolve mailbox")
	}

	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorItemResponseXML("CopyItem", ErrErrorMailboxNotFound, err.Error())
	}

	// Resolve destination folder.
	var destFolder semcore.FolderId
	if req.ToFolder.DistinguishedFolderID != nil {
		role, ok := DistinguishedFolderIDs[req.ToFolder.DistinguishedFolderID.ID]
		if !ok {
			return s.errorItemResponseXML("CopyItem", ErrErrorFolderNotFound, "unknown distinguished folder")
		}
		fld, err := s.identity.GetFolderByMailbox(mailboxKey, role)
		if err != nil {
			return s.errorItemResponseXML("CopyItem", ErrErrorFolderNotFound, err.Error())
		}
		destFolder = fld.FolderID
	} else if req.ToFolder.FolderID != nil {
		destFolder, err = semcore.NewFolderId(*req.ToFolder.FolderID)
		if err != nil {
			return s.errorItemResponseXML("CopyItem", ErrErrorInvalidId, err.Error())
		}
	}

	if destFolder.IsZero() {
		return s.errorItemResponseXML("CopyItem", ErrErrorFolderNotFound, "destination folder required")
	}

	msgs := make([]ItemResponseMessageType, 0, len(req.ItemIDs.Item))
	for _, id := range req.ItemIDs.Item {
		itemID, err := semcore.NewItemId(id.ID)
		if err != nil {
			msgs = append(msgs, errorItemMsg("CopyItem", ErrErrorInvalidId, err.Error()))
			continue
		}
		rec, err := s.identity.GetItemIdentity(itemID)
		if err != nil {
			msgs = append(msgs, errorItemMsg("CopyItem", ErrErrorItemNotFound, err.Error()))
			continue
		}
		rawMsg, err := s.msgStore.ReadMessage(rec.Email, rec.MsgKey)
		if err != nil {
			msgs = append(msgs, errorItemMsg("CopyItem", ErrErrorInternalServer, err.Error()))
			continue
		}
		copiedRaw := prependHeader(rawMsg, "X-uMailServer-Copy-ID", generateID())
		subject, _, _, _, _, _ := parseMimeHeaders(rawMsg)
		msgs = append(msgs, s.createRawItemInFolder(ctx, mboxID, mailboxKey, destFolder, subject, copiedRaw, nil))
	}

	resp := CopyItemResponse{}
	resp.Msgs.Messages = msgs
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// GetAttachment / DeleteAttachment
// ---------------------------------------------------------------------------

// GetAttachmentRequest is the EWS GetAttachment operation request.
type GetAttachmentRequest struct {
	XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetAttachment"`
	AttachmentIDs struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AttachmentIds"`
		Item    []struct {
			XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
			ID      string   `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AttachmentIds"`
}

// GetAttachmentResponse is the EWS GetAttachment operation response.
type GetAttachmentResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetAttachmentResponse"`
	Msgs    struct {
		Messages []struct {
			XMLName       xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
			ResponseClass string                       `xml:"ResponseClass,attr"`
			ResponseCode  ResponseCodeType             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
			Attachments   []AttachmentInfoResponseType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Attachments"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetAttachmentResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// AttachmentInfoResponseType represents an attachment response. The element
// order follows the EWS FileAttachment schema (AttachmentId first), and the
// Content field carries the raw bytes (Go's XML encoder base64-encodes []byte).
type AttachmentInfoResponseType struct {
	XMLName      xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/types FileAttachment"`
	AttachmentID *AttachmentIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId,omitempty"`
	Name         string            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name"`
	ContentType  string            `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContentType,omitempty"`
	ContentID    string            `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContentId,omitempty"`
	IsInline     *bool             `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsInline,omitempty"`
	Size         int               `xml:"http://schemas.microsoft.com/exchange/services/2006/types Size"`
	Content      []byte            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Content,omitempty"`
}

// handleGetAttachment processes an EWS GetAttachment SOAP request.
func (s *Server) handleGetAttachment(ctx context.Context, body []byte) []byte {
	var req GetAttachmentRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("GetAttachment", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("GetAttachment", errCode, "could not resolve mailbox")
	}

	_ = mboxKey // mboxID used for ownership validation

	messages := make([]struct {
		XMLName       xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string                       `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
		Attachments   []AttachmentInfoResponseType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Attachments"`
	}, 0, len(req.AttachmentIDs.Item))

	for _, att := range req.AttachmentIDs.Item {
		// Decode the self-describing AttachmentId: <parentItemID>~att~<index>.
		parentIDStr, index, ok := parseAttachmentID(att.ID)
		if !ok {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorInvalidId}, nil))
			continue
		}
		parentID, err := semcore.NewItemId(parentIDStr)
		if err != nil {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorInvalidId}, nil))
			continue
		}

		// Validate ownership: the parent item must belong to the authenticated
		// mailbox. An attachment is accessible only if its parent item is.
		parentRec, err := s.identity.GetItemIdentity(parentID)
		if err != nil {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}, nil))
			continue
		}
		if !parentRec.MailboxID.IsZero() && parentRec.MailboxID != mboxID {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorAccessDenied}, nil))
			continue
		}

		// Re-read the parent MIME and extract the requested attachment.
		rawMsg, err := s.msgStore.ReadMessage(parentRec.Email, parentRec.MsgKey)
		if err != nil {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}, nil))
			continue
		}
		_, _, _, _, _, _, attachments := parseMimeWithAttachments(rawMsg)
		if index >= len(attachments) {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}, nil))
			continue
		}
		src := attachments[index]
		content, err := base64.StdEncoding.DecodeString(src.Content)
		if err != nil {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorInternalServer}, nil))
			continue
		}
		messages = append(messages, attachErrMsg("Success", ResponseCodeType{Value: ErrNoError}, []AttachmentInfoResponseType{
			{
				AttachmentID: &AttachmentIdType{ID: att.ID},
				Name:         src.Name,
				ContentType:  src.ContentType,
				ContentID:    src.ContentID,
				IsInline:     src.IsInline,
				Size:         len(content),
				Content:      content,
			},
		}))
	}

	resp := GetAttachmentResponse{}
	resp.Msgs.Messages = messages
	return buildResponseEnvelope(resp)
}

func attachErrMsg(class string, code ResponseCodeType, atts []AttachmentInfoResponseType) struct {
	XMLName       xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string                       `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Attachments   []AttachmentInfoResponseType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Attachments"`
} {
	return struct {
		XMLName       xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string                       `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
		Attachments   []AttachmentInfoResponseType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Attachments"`
	}{
		ResponseClass: class,
		ResponseCode:  code,
		Attachments:   atts,
	}
}

// DeleteAttachmentRequest is the EWS DeleteAttachment operation request.
type DeleteAttachmentRequest struct {
	XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteAttachment"`
	AttachmentIDs struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AttachmentIds"`
		Item    []struct {
			XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
			ID      string   `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AttachmentIds"`
}

// RootItemIdType identifies the parent item whose attachment was deleted.
// exchangelib reads both attributes to update the parent item's id/changekey.
type RootItemIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RootItemId"`
	ID      string   `xml:"RootItemId,attr"`
	CK      string   `xml:"RootItemChangeKey,attr"`
}

// DeleteAttachmentResponseMessageType is one DeleteAttachment response entry.
type DeleteAttachmentResponseMessageType struct {
	XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteAttachmentResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	RootItemID    *RootItemIdType  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RootItemId"`
}

// DeleteAttachmentResponse is the EWS DeleteAttachment operation response.
type DeleteAttachmentResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteAttachmentResponse"`
	Msgs    struct {
		XMLName  xml.Name                              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
		Messages []DeleteAttachmentResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteAttachmentResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

func deleteAttachErrMsg(class string, code ResponseCodeType) DeleteAttachmentResponseMessageType {
	return DeleteAttachmentResponseMessageType{ResponseClass: class, ResponseCode: code}
}

// handleDeleteAttachment processes an EWS DeleteAttachment SOAP request.
// It removes the attachment from the parent message's MIME, re-stores the
// rewritten blob under the same ItemId, advances the ChangeKey, and returns
// the parent's RootItemId so the client can refresh.
func (s *Server) handleDeleteAttachment(ctx context.Context, body []byte) []byte {
	var req DeleteAttachmentRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("DeleteAttachment", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("DeleteAttachment", errCode, "could not resolve mailbox")
	}
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	msgs := make([]DeleteAttachmentResponseMessageType, 0, len(req.AttachmentIDs.Item))
	for _, att := range req.AttachmentIDs.Item {
		parentIDStr, index, ok := parseAttachmentID(att.ID)
		if !ok {
			msgs = append(msgs, deleteAttachErrMsg("Error", ResponseCodeType{Value: ErrErrorInvalidId}))
			continue
		}
		parentID, err := semcore.NewItemId(parentIDStr)
		if err != nil {
			msgs = append(msgs, deleteAttachErrMsg("Error", ResponseCodeType{Value: ErrErrorInvalidId}))
			continue
		}
		parentRec, err := s.identity.GetItemIdentity(parentID)
		if err != nil {
			msgs = append(msgs, deleteAttachErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}))
			continue
		}
		if !parentRec.MailboxID.IsZero() && parentRec.MailboxID != mboxID {
			msgs = append(msgs, deleteAttachErrMsg("Error", ResponseCodeType{Value: ErrErrorAccessDenied}))
			continue
		}

		rawMsg, err := s.msgStore.ReadMessage(parentRec.Email, parentRec.MsgKey)
		if err != nil {
			msgs = append(msgs, deleteAttachErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}))
			continue
		}
		newMsg, removed := removeAttachmentFromMime(rawMsg, index)
		if !removed {
			msgs = append(msgs, deleteAttachErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}))
			continue
		}

		// Persist the rewritten MIME and repoint the item's blob key.
		newKey, err := s.msgStore.StoreMessage(parentRec.Email, newMsg)
		if err != nil {
			msgs = append(msgs, deleteAttachErrMsg("Error", ResponseCodeType{Value: ErrErrorInternalServer}))
			continue
		}
		if err := s.identity.SetItemMsgKey(parentID, newKey); err != nil {
			msgs = append(msgs, deleteAttachErrMsg("Error", ResponseCodeType{Value: ErrErrorInternalServer}))
			continue
		}

		// Advance the parent item's ChangeKey through the canonical mutation.
		delegateCtx := s.buildDelegateAuditContext(ctx, mboxID, mailboxKey)
		upd := &semcore.UpdateInput{
			ItemID:               parentID,
			MailboxID:            mboxID,
			FolderID:             parentRec.FolderID,
			Actor:                mailboxKey,
			Source:               semcore.MutationSourceEWS,
			DelegateAuditContext: delegateCtx,
		}
		result, err := s.mutationPipe.MutateUpdate(upd)
		if err != nil {
			msgs = append(msgs, deleteAttachErrMsg("Error", ResponseCodeType{Value: ErrErrorInternalServer}))
			continue
		}

		msgs = append(msgs, DeleteAttachmentResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			RootItemID: &RootItemIdType{
				ID: parentID.String(),
				CK: result.ChangeKey.String(),
			},
		})
	}

	resp := DeleteAttachmentResponse{}
	resp.Msgs.Messages = msgs
	return buildResponseEnvelope(resp)
}

// removeAttachmentFromMime removes the attachment at dropIndex (counting only
// parts that parseMimeWithAttachments would surface as attachments) from a
// multipart MIME message, preserving every other part and all top-level
// headers byte-for-byte. It returns the rewritten message and whether the
// target attachment was found and removed.
func removeAttachmentFromMime(data []byte, dropIndex int) ([]byte, bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	mediaType, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type")) //nolint:errcheck // non-multipart yields empty boundary, handled below.
	boundary := params["boundary"]
	if boundary == "" || !strings.HasPrefix(mediaType, "multipart/") {
		return nil, false
	}

	hdrEnd := bytes.Index(data, []byte("\r\n\r\n"))
	if hdrEnd < 0 {
		return nil, false
	}
	header := data[:hdrEnd+4]
	mimeBody := data[hdrEnd+4:]

	delim := []byte("--" + boundary)
	segments := bytes.Split(mimeBody, delim)
	if len(segments) < 2 {
		return nil, false
	}

	// segments[0] is the preamble; the final segment beginning with "--" is the
	// closing delimiter remainder. Everything between is a part body.
	kept := make([][]byte, 0, len(segments))
	kept = append(kept, segments[0])
	attIdx := 0
	found := false
	for i := 1; i < len(segments); i++ {
		seg := segments[i]
		if bytes.HasPrefix(seg, []byte("--")) {
			// Closing delimiter and any epilogue; keep verbatim.
			kept = append(kept, seg)
			continue
		}
		if isAttachmentPart(seg) {
			if attIdx == dropIndex {
				attIdx++
				found = true
				continue // drop this part
			}
			attIdx++
		}
		kept = append(kept, seg)
	}
	if !found {
		return nil, false
	}

	newBody := bytes.Join(kept, delim)
	out := make([]byte, 0, len(header)+len(newBody))
	out = append(out, header...)
	out = append(out, newBody...)
	return out, true
}

// isAttachmentPart reports whether a raw multipart segment is an attachment,
// using the exact same predicate as parseMimeWithAttachments so attachment
// indices stay aligned between GetItem and DeleteAttachment.
func isAttachmentPart(seg []byte) bool {
	trimmed := bytes.TrimPrefix(seg, []byte("\r\n"))
	tr := textproto.NewReader(bufio.NewReader(bytes.NewReader(trimmed)))
	partHeader, err := tr.ReadMIMEHeader()
	if err != nil && len(partHeader) == 0 {
		return false
	}
	_, partParams, _ := mime.ParseMediaType(partHeader.Get("Content-Type")) //nolint:errcheck // mirrors parseMimeWithAttachments; malformed values are treated as non-attachment.
	isInline := false
	if cd := partHeader.Get("Content-Disposition"); cd != "" {
		if disp, _, _ := mime.ParseMediaType(cd); strings.EqualFold(disp, "inline") { //nolint:errcheck // malformed disposition is treated as not-inline.
			isInline = true
		}
	}
	partCID := strings.Trim(partHeader.Get("Content-ID"), "<>")
	partName := partParams["name"]
	if partName == "" {
		partName = partParams["filename"]
	}
	ctLower := strings.ToLower(partHeader.Get("Content-Type"))
	return isInline || partName != "" || partCID != "" || strings.Contains(ctLower, "attachment")
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// errorItemMsg builds an error ItemResponseMessageType.
func errorItemMsg(op string, code ErrorCode, message string) ItemResponseMessageType {
	return ItemResponseMessageType{
		ResponseClass: "Error",
		ResponseCode:  ResponseCodeType{Value: code},
	}
}

// s.errorItemResponseXML delegates to the existing errorResponseXML method.
func (s *Server) errorItemResponseXML(op string, code ErrorCode, message string) []byte {
	return s.errorResponseXML(op, code, message)
}

// moveItemToFolder performs a canonical item move from sourceFolder to destFolder.
func (s *Server) moveItemToFolder(ctx context.Context, mboxID semcore.MailboxId, mboxKey string, sourceFolder, destFolder semcore.FolderId, itemID semcore.ItemId) ItemResponseMessageType {
	rec, err := s.identity.GetItemIdentity(itemID)
	if err != nil {
		return errorItemMsg("MoveItem", ErrErrorItemNotFound, err.Error())
	}

	// Perform canonical move mutation.
	in := &semcore.MoveInput{
		ItemID:       itemID,
		MailboxID:    mboxID,
		SourceFolder: sourceFolder,
		DestFolder:   destFolder,
		Actor:        mboxKey,
		Source:       semcore.MutationSourceEWS,
	}
	if err := s.mutationPipe.MutateMove(in); err != nil {
		return errorItemMsg("MoveItem", ErrErrorInternalServer, err.Error())
	}
	if err := s.identity.SetItemFolder(itemID, destFolder); err != nil {
		return errorItemMsg("MoveItem", ErrErrorInternalServer, err.Error())
	}

	// Mirror the move into the IMAP mailstore index (remove from source, add to
	// destination) so IMAP/JMAP/webmail reflect the move.
	s.mirrorMoveInMailstore(rec.Email, sourceFolder, destFolder, rec.MsgKey)

	return ItemResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items: ItemsContainer{Items: []MessageTypeResponse{{
			ItemID: ItemIdType{
				ID: itemID.String(),
				CK: rec.ChangeKey.String(),
			},
			ParentFolderID: FolderIdComponents{ID: destFolder.String()},
		}}},
	}
}

// generateID produces a cryptographically random 16-byte hex token.
func generateID() string {
	b := make([]byte, 16)
	// Use a simple time-based generator since crypto/rand is used in semcore.
	// This mirrors semcore.generateID but duplicated here to avoid import cycles.
	now := time.Now().UnixNano()
	for i := 0; i < 16; i++ {
		b[i] = byte((now >> (i % 8)) & 0xff)
		if i > 0 && i%8 == 0 {
			now = time.Now().UnixNano()
		}
	}
	// Fallback to simple hex encoding of counter.
	hexStr := fmt.Sprintf("%x", now)
	if len(hexStr) < 16 {
		hexStr = fmt.Sprintf("%016x", now)
	}
	return hexStr[:16]
}
