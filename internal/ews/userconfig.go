package ews

import (
	"context"
	"encoding/xml"
	"strings"

	"github.com/umailserver/umailserver/internal/db"
)

// EWS UserConfiguration (Create/Get/Update/Delete) persists per-mailbox client
// configuration objects (FAI), such as OWA/Outlook roaming settings. The payload
// (Dictionary, XmlData, BinaryData) is stored opaquely and replayed verbatim on
// Get, which is sufficient for client round-tripping.

const userConfigBucketPrefix = "ewsuserconfig:"

// userConfigNameRef is the UserConfigurationName (config name + owning folder).
type userConfigNameRef struct {
	Name                  string `xml:"Name,attr"`
	DistinguishedFolderID *struct {
		ID string `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	FolderID *struct {
		ID string `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// folderKey returns a stable string identifying the owning folder.
func (n userConfigNameRef) folderKey() string {
	switch {
	case n.DistinguishedFolderID != nil:
		return "d:" + n.DistinguishedFolderID.ID
	case n.FolderID != nil:
		return "f:" + n.FolderID.ID
	default:
		return "d:root"
	}
}

// storedUserConfig is the persisted UserConfiguration payload.
type storedUserConfig struct {
	Dictionary string `json:"dictionary,omitempty"` // raw inner XML of <Dictionary>
	XMLData    string `json:"xml_data,omitempty"`   // base64
	BinaryData string `json:"binary_data,omitempty"` // base64
}

// userConfigPayload captures the data fields of a UserConfiguration element.
type userConfigPayload struct {
	Name       userConfigNameRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types UserConfigurationName"`
	Dictionary struct {
		Inner string `xml:",innerxml"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Dictionary"`
	XMLData    string `xml:"http://schemas.microsoft.com/exchange/services/2006/types XmlData"`
	BinaryData string `xml:"http://schemas.microsoft.com/exchange/services/2006/types BinaryData"`
}

// CreateUserConfigurationRequest is the EWS CreateUserConfiguration request.
type CreateUserConfigurationRequest struct {
	XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateUserConfiguration"`
	Configuration userConfigPayload `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UserConfiguration"`
}

// UpdateUserConfigurationRequest is the EWS UpdateUserConfiguration request.
type UpdateUserConfigurationRequest struct {
	XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateUserConfiguration"`
	Configuration userConfigPayload `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UserConfiguration"`
}

// GetUserConfigurationRequest is the EWS GetUserConfiguration request.
type GetUserConfigurationRequest struct {
	XMLName xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserConfiguration"`
	Name    userConfigNameRef `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UserConfigurationName"`
}

// DeleteUserConfigurationRequest is the EWS DeleteUserConfiguration request.
type DeleteUserConfigurationRequest struct {
	XMLName xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteUserConfiguration"`
	Name    userConfigNameRef `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UserConfigurationName"`
}

func (s *Server) userConfigKey(mailboxKey string, n userConfigNameRef) string {
	return userConfigBucketPrefix + mailboxKey + ":" + n.folderKey() + ":" + n.Name
}

func (s *Server) handleCreateUserConfiguration(ctx context.Context, body []byte) []byte {
	return s.putUserConfiguration(ctx, body, "CreateUserConfiguration")
}

func (s *Server) handleUpdateUserConfiguration(ctx context.Context, body []byte) []byte {
	return s.putUserConfiguration(ctx, body, "UpdateUserConfiguration")
}

// putUserConfiguration handles both Create and Update (upsert semantics).
func (s *Server) putUserConfiguration(ctx context.Context, body []byte, op string) []byte {
	var cfg userConfigPayload
	if op == "CreateUserConfiguration" {
		var req CreateUserConfigurationRequest
		if err := decodeRequest(body, &req); err != nil {
			return s.errorResponseXML(op, ErrErrorInvalidOperation, "malformed request: "+err.Error())
		}
		cfg = req.Configuration
	} else {
		var req UpdateUserConfigurationRequest
		if err := decodeRequest(body, &req); err != nil {
			return s.errorResponseXML(op, ErrErrorInvalidOperation, "malformed request: "+err.Error())
		}
		cfg = req.Configuration
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML(op, errCode, "could not resolve mailbox")
	}
	if s.db == nil {
		return s.errorResponseXML(op, ErrErrorInternalServer, "configuration store not available")
	}
	if cfg.Name.Name == "" {
		return s.errorResponseXML(op, ErrErrorInvalidOperation, "UserConfigurationName is required")
	}

	stored := storedUserConfig{
		Dictionary: strings.TrimSpace(cfg.Dictionary.Inner),
		XMLData:    strings.TrimSpace(cfg.XMLData),
		BinaryData: strings.TrimSpace(cfg.BinaryData),
	}
	key := s.userConfigKey(strings.TrimPrefix(mboxKey, "e:"), cfg.Name)
	if err := s.db.Put(db.BucketPreferences, key, stored); err != nil {
		return s.errorResponseXML(op, ErrErrorInternalServer, "failed to persist configuration")
	}

	return s.userConfigSimpleResponse(op + "Response")
}

func (s *Server) handleGetUserConfiguration(ctx context.Context, body []byte) []byte {
	var req GetUserConfigurationRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetUserConfiguration", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}
	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("GetUserConfiguration", errCode, "could not resolve mailbox")
	}
	if s.db == nil {
		return s.errorResponseXML("GetUserConfiguration", ErrErrorInternalServer, "configuration store not available")
	}

	var stored storedUserConfig
	key := s.userConfigKey(strings.TrimPrefix(mboxKey, "e:"), req.Name)
	if err := s.db.Get(db.BucketPreferences, key, &stored); err != nil {
		return s.errorResponseXML("GetUserConfiguration", ErrErrorItemNotFound, "user configuration not found")
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:GetUserConfigurationResponse><m:ResponseMessages><m:GetUserConfigurationResponseMessage ResponseClass="Success">`)
	b.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
	b.WriteString(`<m:UserConfiguration><t:UserConfigurationName Name="` + xmlEscape(req.Name.Name) + `">`)
	switch {
	case req.Name.DistinguishedFolderID != nil:
		b.WriteString(`<t:DistinguishedFolderId Id="` + xmlEscape(req.Name.DistinguishedFolderID.ID) + `"/>`)
	case req.Name.FolderID != nil:
		b.WriteString(`<t:FolderId Id="` + xmlEscape(req.Name.FolderID.ID) + `"/>`)
	}
	b.WriteString(`</t:UserConfigurationName>`)
	if stored.Dictionary != "" {
		b.WriteString(`<t:Dictionary>` + stored.Dictionary + `</t:Dictionary>`)
	}
	if stored.XMLData != "" {
		b.WriteString(`<t:XmlData>` + stored.XMLData + `</t:XmlData>`)
	}
	if stored.BinaryData != "" {
		b.WriteString(`<t:BinaryData>` + stored.BinaryData + `</t:BinaryData>`)
	}
	b.WriteString(`</m:UserConfiguration></m:GetUserConfigurationResponseMessage></m:ResponseMessages></m:GetUserConfigurationResponse>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}

func (s *Server) handleDeleteUserConfiguration(ctx context.Context, body []byte) []byte {
	var req DeleteUserConfigurationRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("DeleteUserConfiguration", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}
	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("DeleteUserConfiguration", errCode, "could not resolve mailbox")
	}
	if s.db == nil {
		return s.errorResponseXML("DeleteUserConfiguration", ErrErrorInternalServer, "configuration store not available")
	}
	key := s.userConfigKey(strings.TrimPrefix(mboxKey, "e:"), req.Name)
	if err := s.db.Delete(db.BucketPreferences, key); err != nil {
		return s.errorResponseXML("DeleteUserConfiguration", ErrErrorInternalServer, "failed to delete configuration")
	}
	return s.userConfigSimpleResponse("DeleteUserConfigurationResponse")
}

// userConfigSimpleResponse builds a Success response for Create/Update/Delete.
func (s *Server) userConfigSimpleResponse(responseElem string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:` + responseElem + `><m:ResponseMessages><m:ResponseMessage ResponseClass="Success">`)
	b.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
	b.WriteString(`</m:ResponseMessage></m:ResponseMessages></m:` + responseElem + `>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}
