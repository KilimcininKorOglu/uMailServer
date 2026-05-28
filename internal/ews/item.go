// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements item operations: CreateItem, GetItem,
// UpdateItem, DeleteItem, SendItem, MoveItem, CopyItem, and attachment ops.
package ews

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/mail"
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
		XMLName xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
		Item    []MessageTypeNew `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	SavedItemFolderID struct {
		DistinguishedFolderID *string `xml:"Id,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SavedItemFolderId,omitempty"`
	SaveItemToFolder *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SaveItemToFolder,attr"`
}

// MessageTypeNew is a message item in a CreateItem request.
type MessageTypeNew struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	Subject      string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	Body         *BodyType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body,omitempty"`
	ToRecipients RawRecipients `xml:"http://schemas.microsoft.com/exchange/services/2006/types ToRecipients,omitempty"`
	CcRecipients RawRecipients `xml:"http://schemas.microsoft.com/exchange/services/2006/types CcRecipients,omitempty"`
	BccRecipients RawRecipients `xml:"http://schemas.microsoft.com/exchange/services/2006/types BccRecipients,omitempty"`
	From         *FromAddressType `xml:"http://schemas.microsoft.com/exchange/services/2006/types From,omitempty"`
	IsDraft      bool   `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsDraft,attr"`
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
	XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Items        ItemsContainer    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
}

// ItemsContainer wraps items in response messages.
type ItemsContainer struct {
	XMLName xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
	Items   []MessageTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
}

// MessageTypeResponse is a message item in responses (read/fetched).
type MessageTypeResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Message"`
	ItemID  ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	ParentFolderID FolderIdComponents `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	Subject       string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject,omitempty"`
	DateTimeReceived string `xml:"http://schemas.microsoft.com/exchange/services/2006/types DateTimeReceived,omitempty"`
	Size           int    `xml:"http://schemas.microsoft.com/exchange/services/2006/types Size,omitempty"`
	Body           BodyTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body"`
	ToRecipients  []MailboxTypeResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types ToRecipients,omitempty"`
}

// MailboxTypeResponse is a mailbox entry in responses.
type MailboxTypeResponse struct {
	XMLName     xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
	EmailAddress string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress,omitempty"`
	Name        string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
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
	ID      string  `xml:"Id,attr"`
	CK      string  `xml:"ChangeKey,attr,omitempty"`
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
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorItemResponseXML("CreateItem", ErrErrorInternalServer, "mailbox not found")
	}

	// Determine target folder: Sent Items by default, or SavedItemFolderId.
	var folderID semcore.FolderId
	targetRole := "drafts"
	if req.SaveItemToFolder != nil && *req.SaveItemToFolder {
		if req.SavedItemFolderID.DistinguishedFolderID != nil {
			role, ok := DistinguishedFolderIDs[*req.SavedItemFolderID.DistinguishedFolderID]
			if ok {
				fld, err := s.identity.GetFolderByMailbox(mailboxKey, role)
				if err == nil {
					folderID = fld.FolderID
				}
				targetRole = role
			}
		}
	}

	if folderID.IsZero() {
		fld, err := s.identity.GetFolderByMailbox(mailboxKey, targetRole)
		if err == nil {
			folderID = fld.FolderID
		}
	}
	if folderID.IsZero() {
		folderID, err = s.identity.EnsureFolderId(mailboxKey, targetRole, targetRole)
		if err != nil {
			return s.errorItemResponseXML("CreateItem", ErrErrorInternalServer, "failed to ensure folder: "+err.Error())
		}
	}

	msgs := make([]ItemResponseMessageType, 0, len(req.Items.Item))
	for range req.Items.Item {
		item := &req.Items.Item[0] // safe: we process one at a time
		msg := s.createItemInFolder(ctx, mboxID, mailboxKey, folderID, item)
		msgs = append(msgs, msg)
	}

	resp := CreateItemResponse{}
	resp.Msgs.Messages = msgs
	result := buildResponseEnvelope(resp)
	return result
}

// createItemInFolder creates a message item in the target folder.
func (s *Server) createItemInFolder(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, item *MessageTypeNew) ItemResponseMessageType {
	if folderID.IsZero() {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "no target folder")
	}

	// Build RFC 5322 MIME from the EWS item.
	rawMsg := buildMimeMessage(item)
	if rawMsg == nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "failed to build message")
	}

	// Store raw MIME blob.
	// mailboxKey is raw email (e.g. "alice@local.test").
	blobKey, err := s.msgStore.StoreMessage(mailboxKey, rawMsg)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "failed to store message: "+err.Error())
	}
	_ = blobKey // blob key already stored; semcore will use its own key

	// Perform canonical mutation: assigns ItemId, ChangeKey, ConversationId.
	in := &semcore.MutationInput{
		MailboxID:     mboxID,
		FolderID:      folderID,
		RawMessage:    rawMsg,
		InternalDate: time.Now(),
		Actor:         mailboxKey,
		Source:        semcore.MutationSourceEWS,
		Email:         mailboxKey,
	}
	result, err := s.mutationPipe.MutateItem(in)
	if err != nil {
		return errorItemMsg("CreateItem", ErrErrorInternalServer, "mutation failed: "+err.Error())
	}

	msgResp := MessageTypeResponse{
		ItemID: ItemIdType{
			ID: result.ItemID.String(),
			CK: result.ChangeKey.String(),
		},
		ParentFolderID:   FolderIdComponents{ID: folderID.String()},
		Subject:         item.Subject,
		DateTimeReceived: FormatEWSDateTime(result.Lifecycle.At),
		Size:            len(rawMsg),
	}

	return ItemResponseMessageType{
		ResponseClass: "Success",
		ResponseCode:  ResponseCodeType{Value: ErrNoError},
		Items:         ItemsContainer{Items: []MessageTypeResponse{msgResp}},
	}
}

// buildMimeMessage constructs RFC 5322 MIME bytes from an EWS Message item.
func buildMimeMessage(item *MessageTypeNew) []byte {
	var buf bytes.Buffer
	now := time.Now().UTC().Format(time.RFC1123Z)

	buf.WriteString("Date: " + now + "\r\n")

	if item.From != nil && item.From.Mailbox.Email != "" {
		buf.WriteString("From: ")
		if item.From.Mailbox.Name != "" {
			buf.WriteString(item.From.Mailbox.Name + " <" + item.From.Mailbox.Email + ">")
		} else {
			buf.WriteString(item.From.Mailbox.Email)
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

	if item.Subject != "" {
		buf.WriteString("Subject: " + item.Subject + "\r\n")
	}

	buf.WriteString("MIME-Version: 1.0\r\n")

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

// ---------------------------------------------------------------------------
// GetItem
// ---------------------------------------------------------------------------

// GetItemRequest is the EWS GetItem operation request.
type GetItemRequest struct {
	XMLName     xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetItem"`
	ItemShapeDef ItemShapeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemShape"`
	ItemIDs    ItemIdsType    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// ItemShapeType defines the item properties to return in a GetItem response.
// It mirrors ItemResponseShape but is a distinct type so the Go XML unmarshaler
// doesn't see a conflict between the field's xml tag name (ItemShape) and
// ItemResponseShape.XMLName (ItemResponseShape).
type ItemShapeType struct {
	BaseShape            string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types BaseShape,omitempty"`
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
	XMLName xml.Name                 `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetItemResponse"`
	Msgs    GetItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetItemResponseMessages wraps GetItem response messages.
type GetItemResponseMessages struct {
	Messages []ItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
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
		DateTimeReceived:  dateStr,
		Size:             len(rawMsg),
		Body: BodyTypeResponse{
			BodyType: bodyType,
			Text:     bodyText,
		},
		ToRecipients: toRecipients,
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

// ---------------------------------------------------------------------------
// UpdateItem
// ---------------------------------------------------------------------------

// UpdateItemRequest is the EWS UpdateItem operation request.
type UpdateItemRequest struct {
	XMLName    xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateItem"`
	ItemChanges ItemChangesList `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
}

// ItemChangesList wraps the ItemChange list.
type ItemChangesList struct {
	XMLName  xml.Name        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemChanges"`
	Changes []ItemChangeOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
}

// ItemChangeOp represents one item change in UpdateItem.
type ItemChangeOp struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemChange"`
	ItemID  struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
		ID     string   `xml:"Id,attr"`
		CK     string   `xml:"ChangeKey,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	Updates ItemUpdatesOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
}

// ItemUpdatesOp wraps update operations.
type ItemUpdatesOp struct {
	XMLName xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
	Ops    []ItemUpdateField `xml:"http://schemas.microsoft.com/exchange/services/2006/types SetItemField"`
}

// ItemUpdateField is one update operation on an item field.
type ItemUpdateField struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types SetItemField"`
	FieldURI struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
		URI     string  `xml:"uri,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
	Item struct {
		Subject *struct {
			XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject"`
			Value   string  `xml:",chardata"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Subject"`
		Body *BodyType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Body"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Item"`
}

// UpdateItemResponse is the EWS UpdateItem operation response.
type UpdateItemResponse struct {
	XMLName xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateItemResponse"`
	Msgs    UpdateItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// UpdateItemResponseMessages wraps UpdateItem response messages.
type UpdateItemResponseMessages struct {
	Messages []ItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
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

	mboxID, err := s.identity.GetMailboxIDByEmail(strings.TrimPrefix(mboxKey, "e:"))
	if err != nil {
		return s.errorItemResponseXML("UpdateItem", ErrErrorMailboxNotFound, err.Error())
	}

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

		// Advance ChangeKey through update mutation.
		in := &semcore.UpdateInput{
			ItemID:    itemID,
			MailboxID:  mboxID,
			FolderID:  rec.FolderID,
			Actor:     mboxKey,
			Source:    semcore.MutationSourceEWS,
		}
		result, err := s.mutationPipe.MutateUpdate(in)
		if err != nil {
			msgs = append(msgs, errorItemMsg("UpdateItem", ErrErrorInternalServer, err.Error()))
			continue
		}

		msgResp := MessageTypeResponse{
			ItemID: ItemIdType{
				ID: itemID.String(),
				CK: result.ChangeKey.String(),
			},
			ParentFolderID: FolderIdComponents{ID: rec.FolderID.String()},
		}
		msgs = append(msgs, ItemResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			Items:        ItemsContainer{Items: []MessageTypeResponse{msgResp}},
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
	XMLName   xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItem"`
	ItemIDs   ItemIdsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	DeleteType string     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteType,attr"`
}

// DeleteItemResponse is the EWS DeleteItem operation response.
type DeleteItemResponse struct {
	XMLName xml.Name                    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponse"`
	Msgs    DeleteItemResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// DeleteItemResponseMessages wraps DeleteItem response messages.
type DeleteItemResponseMessages struct {
	Messages []struct {
		XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponseMessage"`
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

	mboxID, err := s.identity.GetMailboxIDByEmail(strings.TrimPrefix(mboxKey, "e:"))
	if err != nil {
		return s.errorItemResponseXML("DeleteItem", ErrErrorMailboxNotFound, err.Error())
	}

	hardDelete := req.DeleteType == "HardDelete"

	msgs := make([]struct {
		XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponseMessage"`
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
			ItemID:     itemID,
			MailboxID:  mboxID,
			FolderID:   rec.FolderID,
			Actor:      mboxKey,
			Source:     semcore.MutationSourceEWS,
			HardDelete: hardDelete,
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
	XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
} {
	return struct {
		XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteItemResponseMessage"`
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
		Item   []ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
	SaveItemToFolder *bool `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SaveItemToFolder,attr"`
	SavedItemFolderID struct {
		DistinguishedFolderID *string `xml:"Id,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SavedItemFolderId,omitempty"`
}

// SendItemResponse is the EWS SendItem operation response.
type SendItemResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SendItemResponse"`
	Msgs    struct {
		Messages []struct {
			XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
			ResponseClass string           `xml:"ResponseClass,attr"`
			ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SendItemResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
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

	responses := make([]struct {
		XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}, 0, len(req.ItemIDs.Item))

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

		// Move from current folder to Sent Items.
		resultMsg := s.moveItemToFolder(ctx, mboxID, mboxKey, rec.FolderID, sentFolder.FolderID, itemID)
		if resultMsg.ResponseClass == "Error" {
			responses = append(responses, struct {
				XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
				ResponseClass string           `xml:"ResponseClass,attr"`
				ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
			}{
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

func sendErrMsg(code ResponseCodeType) struct {
	XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
	ResponseClass string           `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
} {
	return struct {
		XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string           `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}{
		ResponseClass: "Success",
		ResponseCode:  code,
	}
}

// ---------------------------------------------------------------------------
// MoveItem
// ---------------------------------------------------------------------------

// MoveItemRequest is the EWS MoveItem operation request.
type MoveItemRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MoveItem"`
	ToFolder ToFolderIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ToFolderId"`
	ItemIDs struct {
		XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
		Item   []ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// ToFolderIdType represents the ToFolderId element in MoveItem/CopyItem.
type ToFolderIdType struct {
	XMLName              xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ToFolderId"`
	DistinguishedFolderID *DistFolderIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId,omitempty"`
	FolderID             *string          `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId,attr,omitempty"`
}

// DistFolderIdType represents a DistinguishedFolderId element with Id attribute.
type DistFolderIdType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	ID      string `xml:"Id,attr"`
}

// MoveItemResponse is the EWS MoveItem operation response.
type MoveItemResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MoveItemResponse"`
	Msgs    struct {
		Messages []ItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
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
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CopyItem"`
	ToFolder ToFolderIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ToFolderId"`
	ItemIDs struct {
		XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
		Item   []ItemIdType `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ItemIds"`
}

// CopyItemResponse is the EWS CopyItem operation response.
type CopyItemResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CopyItemResponse"`
	Msgs    struct {
		Messages []ItemResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
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

		// Generate new ItemId and ChangeKey for the copy.
		newItemID, err := semcore.NewItemId(generateID())
		if err != nil {
			msgs = append(msgs, errorItemMsg("CopyItem", ErrErrorInternalServer, err.Error()))
			continue
		}
		newCK, err := semcore.NewChangeKey(generateID())
		if err != nil {
			msgs = append(msgs, errorItemMsg("CopyItem", ErrErrorInternalServer, err.Error()))
			continue
		}

		// Register copy under new identity with same conversation if source had one.
		rec, err := s.identity.GetItemIdentity(itemID)
		if err != nil {
			msgs = append(msgs, errorItemMsg("CopyItem", ErrErrorItemNotFound, err.Error()))
			continue
		}

		copyMsgKey := fmt.Sprintf("copy:%s:%s", itemID.String(), newItemID.String())
		if err := s.identity.PutItemIdentity(copyMsgKey, rec.Email, newItemID, mboxID, destFolder, newCK, rec.ConversationID); err != nil {
			if !errors.Is(err, semcore.ErrIdentityExists) {
				msgs = append(msgs, errorItemMsg("CopyItem", ErrErrorInternalServer, err.Error()))
				continue
			}
		}

		msgResp := MessageTypeResponse{
			ItemID: ItemIdType{
				ID: newItemID.String(),
				CK: newCK.String(),
			},
			ParentFolderID: FolderIdComponents{ID: destFolder.String()},
		}
		msgs = append(msgs, ItemResponseMessageType{
			ResponseClass: "Success",
			ResponseCode:  ResponseCodeType{Value: ErrNoError},
			Items:        ItemsContainer{Items: []MessageTypeResponse{msgResp}},
		})
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
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetAttachment"`
	AttachmentIDs struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AttachmentIds"`
		Item   []struct {
			XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
			ID     string   `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AttachmentIds"`
}

// GetAttachmentResponse is the EWS GetAttachment operation response.
type GetAttachmentResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetAttachmentResponse"`
	Msgs    struct {
		Messages []struct {
			XMLName       xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
			ResponseClass string                      `xml:"ResponseClass,attr"`
			ResponseCode  ResponseCodeType            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
			Attachments   []AttachmentInfoResponseType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Attachments"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetAttachmentResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// AttachmentInfoResponseType represents an attachment response.
type AttachmentInfoResponseType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FileAttachment"`
	Name    string  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name"`
	Size    int     `xml:"http://schemas.microsoft.com/exchange/services/2006/types Size"`
	Id      string  `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
	Content []byte  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Content,omitempty"`
}

// handleGetAttachment processes an EWS GetAttachment SOAP request.
func (s *Server) handleGetAttachment(ctx context.Context, body []byte) []byte {
	var req GetAttachmentRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorItemResponseXML("GetAttachment", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorItemResponseXML("GetAttachment", errCode, "could not resolve mailbox")
	}
	_ = mboxKey // TODO: validate attachment ownership via mailbox scope

	messages := make([]struct {
		XMLName       xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string                      `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
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

		_ = rec // TODO: retrieve attachment content from blob store

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
	ResponseClass string                      `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Attachments   []AttachmentInfoResponseType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Attachments"`
} {
	return struct {
		XMLName       xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessage"`
		ResponseClass string                      `xml:"ResponseClass,attr"`
		ResponseCode  ResponseCodeType            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
		Attachments   []AttachmentInfoResponseType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Attachments"`
	}{
		ResponseClass: class,
		ResponseCode:  code,
		Attachments:   atts,
	}
}

// DeleteAttachmentRequest is the EWS DeleteAttachment operation request.
type DeleteAttachmentRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteAttachment"`
	AttachmentIDs struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages AttachmentIds"`
		Item   []struct {
			XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types AttachmentId"`
			ID     string   `xml:"Id,attr"`
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
		ResponseCode: ResponseCodeType{Value: code},
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
