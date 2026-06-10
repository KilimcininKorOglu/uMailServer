package ews

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"strings"
)

// GetUserPhotoRequest is the EWS GetUserPhoto SOAP request. SizeRequested and
// TypeRequested are accepted but ignored — we serve the stored photo as-is
// (matching Exchange's behavior: no server-side resizing).
type GetUserPhotoRequest struct {
	XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserPhoto"`
	Email         string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Email"`
	SizeRequested string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SizeRequested"`
}

// handleGetUserPhoto serves a user's profile photo from the account's stored
// avatar as base64 PictureData. The response shape mirrors Exchange:
// m:GetUserPhotoResponse with HasChanged (always true) and PictureData.
func (s *Server) handleGetUserPhoto(_ context.Context, body []byte) []byte {
	var req GetUserPhotoRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetUserPhoto", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}
	email := strings.TrimSpace(req.Email)
	if email == "" {
		return s.errorResponseXML("GetUserPhoto", ErrErrorInvalidOperation, "Email is required")
	}
	if s.db == nil {
		return s.errorResponseXML("GetUserPhoto", ErrErrorItemNotFound, "directory unavailable")
	}
	localPart, domain, ok := strings.Cut(email, "@")
	if !ok || localPart == "" || domain == "" {
		return s.errorResponseXML("GetUserPhoto", ErrErrorInvalidOperation, "invalid email address")
	}
	acc, err := s.db.GetAccount(domain, localPart)
	if err != nil || acc == nil || len(acc.Avatar) == 0 {
		return s.errorResponseXML("GetUserPhoto", ErrErrorItemNotFound, "no photo for user")
	}

	picture := base64.StdEncoding.EncodeToString(acc.Avatar)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:GetUserPhotoResponse ResponseClass="Success">`)
	b.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
	b.WriteString(`<m:HasChanged>true</m:HasChanged>`)
	b.WriteString(`<m:PictureData>` + picture + `</m:PictureData>`)
	b.WriteString(`</m:GetUserPhotoResponse>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}
