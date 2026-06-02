package ews

import (
	"context"
	"encoding/xml"
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
)

// GetMailTipsRequest is the EWS GetMailTips request (MS-OXWMT). Only the
// recipient list is needed to compute the tips we support.
type GetMailTipsRequest struct {
	XMLName    xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetMailTips"`
	Recipients struct {
		Mailbox []struct {
			EmailAddress string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Recipients"`
}

// handleGetMailTips returns mail tips for each requested recipient. The
// out-of-office tip is populated from the recipient's stored OOF policy; the
// remaining tips are reported with safe defaults so Outlook/OWA compose-time
// mail tips work.
func (s *Server) handleGetMailTips(_ context.Context, body []byte) []byte {
	var req GetMailTipsRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetMailTips", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:GetMailTipsResponse><m:ResponseCode>NoError</m:ResponseCode><m:ResponseMessages>`)
	for _, mb := range req.Recipients.Mailbox {
		addr := strings.TrimSpace(mb.EmailAddress)
		b.WriteString(`<m:MailTipsResponseMessageType ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode>`)
		b.WriteString(`<m:MailTips>`)
		b.WriteString(`<t:RecipientAddress><t:EmailAddress>` + xmlEscape(addr) + `</t:EmailAddress><t:RoutingType>SMTP</t:RoutingType></t:RecipientAddress>`)
		b.WriteString(`<t:PendingMailTips/>`)
		if oof := s.recipientOOF(addr); oof != nil && oof.OofState != OofStateDisabled {
			b.WriteString(`<t:OutOfOffice><t:ReplyBody>`)
			msg := ""
			if oof.InternalReply != nil {
				msg = oof.InternalReply.Message
			}
			b.WriteString(`<t:Message>` + xmlEscape(msg) + `</t:Message></t:ReplyBody>`)
			if oof.Duration != nil && (oof.Duration.StartTime != "" || oof.Duration.EndTime != "") {
				b.WriteString(`<t:Duration><t:StartTime>` + oof.Duration.StartTime + `</t:StartTime><t:EndTime>` + oof.Duration.EndTime + `</t:EndTime></t:Duration>`)
			}
			b.WriteString(`</t:OutOfOffice>`)
		}
		b.WriteString(`<t:MailboxFull>false</t:MailboxFull>`)
		b.WriteString(`<t:TotalMemberCount>0</t:TotalMemberCount><t:ExternalMemberCount>0</t:ExternalMemberCount>`)
		b.WriteString(`<t:MaxMessageSize>0</t:MaxMessageSize><t:DeliveryRestricted>false</t:DeliveryRestricted>`)
		b.WriteString(`<t:IsModerated>false</t:IsModerated><t:InvalidRecipient>false</t:InvalidRecipient>`)
		b.WriteString(`</m:MailTips></m:MailTipsResponseMessageType>`)
	}
	b.WriteString(`</m:ResponseMessages></m:GetMailTipsResponse>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}

// recipientOOF reads a recipient's stored out-of-office settings, or nil when no
// policy exists / stores are unavailable.
func (s *Server) recipientOOF(email string) *UserOofSettings {
	if s.policyStore == nil || s.identity == nil || email == "" {
		return nil
	}
	mboxID, err := s.identity.EnsureMailboxId(email)
	if err != nil {
		return nil
	}
	oofID, err := semcore.NewOOFId(mboxID.String())
	if err != nil {
		return nil
	}
	policy, err := s.policyStore.GetOOF(oofID)
	if err != nil {
		return nil
	}
	return oofPolicyToEWS(policy)
}
