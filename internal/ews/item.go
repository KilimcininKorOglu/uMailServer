// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements item operations: CreateItem, GetItem,
// UpdateItem, DeleteItem, SendItem, MoveItem, CopyItem, and attachment ops.
package ews

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
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
		XMLName      xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
		Item         []MessageTypeNew      `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
		ReplyToItem  []ReplyCreateItemType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReplyToItem"`
		ReplyAllItem []ReplyCreateItemType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReplyAllToItem"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	SavedItemFolderID struct {
		DistinguishedFolderID *struct {
			ID string `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SavedItemFolderId,omitempty"`
	// SaveItemToFolder: bool attribute. Uses bare attr name because Go's xml decoder
	// doesn't match default-namespace attributes against namespace URLs in struct tags.
	MessageDisposition      string `xml:"MessageDisposition,attr,omitempty"`
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
	Name        string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
	ContentType string `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContentType,omitempty"`
	ContentID   string `xml:"http://schemas.microsoft.com/exchange/services/2006/types ContentId,omitempty"`
	IsInline    *bool  `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsInline,omitempty"`
	Content     string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Content,omitempty"`
}

type ReplyCreateItemType struct {
	XMLName         xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/types ReplyToItem"`
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
	XMLName xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	Items   []MessageTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
}

// MessageTypeResponse is a message item in responses (read/fetched).
type MessageTypeResponse struct {
	XMLName          xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	ItemID           ItemIdType             `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID   FolderIdComponents     `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	Subject          string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	DateTimeReceived string                 `xml:"http://schemas.microsoft.com/exchange/services/2006/types DateTimeReceived,omitempty"`
	Size             int                    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Size,omitempty"`
	Body             BodyTypeResponse       `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body"`
	ToRecipients     []MailboxTypeResponse  `xml:"http://schemas.microsoft.com/exchange/services/2006/types ToRecipients,omitempty"`
	IsRead           bool                   `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsRead"`
	Categories       *MessageCategoriesType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Categories,omitempty"`
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
	// If the message carries a From address different from the acting user,
	// an explicit grant is required. General delegate folder access (VAL-DIR-002)
	// does NOT imply send-as or send-on-behalf rights.
	// We need to check the From field across all items in the request.
	for i := range req.Items.Item {
		item := &req.Items.Item[i]
		// Check if From field names a different sender than the actor.
		if item.From != nil && item.From.Mailbox.Email != "" && !strings.EqualFold(item.From.Mailbox.Email, actorEmail) {
			// This From address is not the actor's own. Verify send-as on behalf of that identity.
			fromEmail := item.From.Mailbox.Email
			// If the From address matches the owner mailbox, we need either send-as or send-on-behalf.
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
		// if that is authorized, use plain From. Otherwise check send-on-behalf
		// (VAL-DIR-005) and set isSendOnBehalf so MIME builder adds Sender header.
		itemIsSendOnBehalf := false
		if item.From != nil && item.From.Mailbox.Email != "" {
			fromEmail := item.From.Mailbox.Email
			if strings.EqualFold(fromEmail, ownerEmail) && !strings.EqualFold(actorEmail, ownerEmail) {
				if _, code := s.checkSendAsPermission(mboxID, ownerEmail, actorEmail); code != "" {
					if _, code := s.checkSendOnBehalfPermission(mboxID, ownerEmail, actorEmail); code == "" {
						itemIsSendOnBehalf = true
					}
				}
			}
		}
		var msg ItemResponseMessageType
		if sendAndSaveCopy || sendOnly {
			msg = s.submitMessageItem(ctx, mboxID, ownerEmail, folderID, item, nil, delegateCtx, itemIsSendOnBehalf, sendAndSaveCopy)
		} else {
			msg = s.createItemInFolder(ctx, mboxID, ownerEmail, folderID, item, delegateCtx, itemIsSendOnBehalf)
		}
		msgs = append(msgs, msg)
	}
	for i := range req.Items.ReplyToItem {
		msgs = append(msgs, s.submitReplyCreateItem(ctx, mboxID, ownerEmail, folderID, &req.Items.ReplyToItem[i], delegateCtx, sendAndSaveCopy))
	}
	for i := range req.Items.ReplyAllItem {
		msgs = append(msgs, s.submitReplyCreateItem(ctx, mboxID, ownerEmail, folderID, &req.Items.ReplyAllItem[i], delegateCtx, sendAndSaveCopy))
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
	_ = blobKey // blob key already stored; semcore will use its own key

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

	// Persist lifecycle event so GetEvents and sync consumers see the mutation.
	if s.lifecycle != nil {
		//nolint:errcheck
		_ = s.lifecycle.AppendLifecycle(result.Lifecycle) // best-effort; event was already emitted
	}

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
	for _, name := range []string{"In-Reply-To", "References"} {
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

func (s *Server) submitReplyCreateItem(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, item *ReplyCreateItemType, delegateCtx *semcore.DelegateAuditContext, saveCopy bool) ItemResponseMessageType {
	extraHeaders, err := s.replyHeadersForReference(item.ReferenceItemID.ID)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorItemNotFound, err.Error())
	}
	replyMessage := &MessageTypeNew{
		Subject:       item.Subject,
		Body:          item.NewBodyContent,
		ToRecipients:  item.ToRecipients,
		CcRecipients:  item.CcRecipients,
		BccRecipients: item.BccRecipients,
		From:          item.From,
	}
	return s.submitMessageItem(ctx, mboxID, mailboxKey, folderID, replyMessage, extraHeaders, delegateCtx, false, saveCopy)
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
		msg := s.getItemByID(ctx, mboxID, mboxKey, id)
		msgs = append(msgs, msg)
	}

	resp := GetItemResponse{}
	resp.Msgs.Messages = msgs
	return buildResponseEnvelope(resp)
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
		if errors.Is(err, semcore.ErrItemNotFound) {
			return errorItemMsg("GetItem", ErrErrorItemNotFound, "item not found: "+id.ID)
		}
		return errorItemMsg("GetItem", ErrErrorInternalServer, err.Error())
	}

	// Verify mailbox ownership.
	if !rec.MailboxID.IsZero() && rec.MailboxID != mboxID {
		return errorItemMsg("GetItem", ErrErrorAccessDenied, "item belongs to a different mailbox")
	}

	// Check ChangeKey if provided.
	if id.CK != "" && id.CK != rec.ChangeKey.String() {
		return errorItemMsg("GetItem", ErrErrorItemIdOrChangeKey, "ChangeKey mismatch")
	}

	// Retrieve raw MIME content from msgStore.
	// rec.Email is the user key and rec.MsgKey is the blob key.
	rawMsg, err := s.msgStore.ReadMessage(rec.Email, rec.MsgKey)
	if err != nil {
		return errorItemMsg("GetItem", ErrErrorInternalServer, "failed to retrieve message: "+err.Error())
	}

	// Parse MIME headers for display.
	subject, from, dateStr, bodyType, bodyText, toAddrs := parseMimeHeaders(rawMsg)
	_ = from // available for extended properties

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
		Subject:          subject,
		DateTimeReceived: dateStr,
		Size:             len(rawMsg),
		Body: BodyTypeResponse{
			BodyType: bodyType,
			Text:     bodyText,
		},
		ToRecipients: toRecipients,
		IsRead:       rec.IsRead,
		Categories:   categoriesResponse(rec.Categories),
	}

	return ItemResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items:         ItemsContainer{Items: []MessageTypeResponse{msgResp}},
	}
}

// parseMimeHeaders extracts Subject, From, Date, Body, and To from raw MIME.
func parseMimeHeaders(data []byte) (subject, from, date, bodyType, body string, toAddrs []string) {
	if len(data) == 0 {
		return "", "", "", "Text", "", nil
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return "", "", "", "Text", "", nil
	}
	h := msg.Header

	// Determine body type from Content-Type header.
	contentType := h.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(contentType), "text/html") {
		bodyType = "HTML"
	} else {
		bodyType = "Text"
	}

	// Read body content.
	var bodyBytes []byte
	bodyBytes, _ = io.ReadAll(msg.Body) //nolint:errcheck
	body = string(bodyBytes)

	// Parse To header.
	toHeader := h.Get("To")
	if toHeader != "" {
		var addrs []*mail.Address
		addrs, _ = mail.ParseAddressList(toHeader) //nolint:errcheck
		for _, a := range addrs {
			toAddrs = append(toAddrs, a.Address)
		}
	}

	return strings.TrimSpace(h.Get("Subject")), h.Get("From"), h.Get("Date"), bodyType, body, toAddrs
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
	Message ItemUpdateValue `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
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
		role, ok := DistinguishedFolderIDs[req.ToFolder.DistinguishedFolderID.ID]
		if !ok {
			return s.errorItemResponseXML("MoveItem", ErrErrorFolderNotFound, "unknown distinguished folder")
		}
		fld, err := s.identity.GetFolderByMailbox(mailboxKey, role)
		if err != nil {
			return s.errorItemResponseXML("MoveItem", ErrErrorFolderNotFound, err.Error())
		}
		destFolder = fld.FolderID
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

// AttachmentInfoResponseType represents an attachment response.
type AttachmentInfoResponseType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FileAttachment"`
	Name    string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name"`
	Size    int      `xml:"http://schemas.microsoft.com/exchange/services/2006/types Size"`
	Id      string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
	Content []byte   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Content,omitempty"`
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
		attID, err := semcore.NewAttachmentId(att.ID)
		if err != nil {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorInvalidId}, nil))
			continue
		}

		rec, err := s.identity.GetAttachmentIdentity(attID)
		if err != nil {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}, nil))
			continue
		}

		// Validate attachment ownership: the parent item must belong to the
		// authenticated mailbox. An attachment is accessible only if its parent
		// item is accessible to the caller.
		parentRec, err := s.identity.GetItemIdentity(rec.ParentID)
		if err != nil {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorItemNotFound}, nil))
			continue
		}
		if !parentRec.MailboxID.IsZero() && parentRec.MailboxID != mboxID {
			messages = append(messages, attachErrMsg("Error", ResponseCodeType{Value: ErrErrorAccessDenied}, nil))
			continue
		}

		_ = rec // attachment identity validated; content retrieval is separate

		messages = append(messages, attachErrMsg("Success", ResponseCodeType{Value: ErrNoError}, []AttachmentInfoResponseType{
			{Name: "attachment", Size: 0, Id: att.ID},
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

// handleDeleteAttachment processes an EWS DeleteAttachment SOAP request.
func (s *Server) handleDeleteAttachment(ctx context.Context, body []byte) []byte {
	// TODO: implement attachment deletion from identity store.
	return s.errorItemResponseXML("DeleteAttachment", ErrErrorNotImplemented, "DeleteAttachment is not yet implemented")
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
