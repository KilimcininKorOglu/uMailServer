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

// userConfigOwnerName derives the (owner, name) identity the store keys an EWS
// UserConfiguration by: owner is the mailbox key (without the "e:" prefix) and
// name combines the folder key with the configuration name.
func userConfigOwnerName(mboxKey string, n userConfigNameRef) (owner, name string) {
	return strings.TrimPrefix(mboxKey, "e:"), n.folderKey() + ":" + n.Name
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

	blob := &db.UserConfigBlob{
		Dictionary: strings.TrimSpace(cfg.Dictionary.Inner),
		XMLData:    strings.TrimSpace(cfg.XMLData),
		BinaryData: strings.TrimSpace(cfg.BinaryData),
	}
	owner, name := userConfigOwnerName(mboxKey, cfg.Name)
	if err := s.db.PutUserConfig(owner, name, blob); err != nil {
		return s.errorResponseXML(op, ErrErrorInternalServer, "failed to persist configuration")
	}

	// Signature bridge: an Outlook/OWA write of OWA.UserOptions carries the
	// signature in its dictionary; mirror it onto the canonical signature store
	// so webmail (/api/v1/signature) and EWS share ONE signature.
	if strings.EqualFold(cfg.Name.Name, owaUserOptionsName) {
		if sig, ok := owaSignatureFromDict(blob.Dictionary); ok {
			//nolint:errcheck // best-effort: the opaque config is already persisted
			s.db.PutSignature(owner, sig)
		}
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

	owner, name := userConfigOwnerName(mboxKey, req.Name)
	stored, err := s.db.GetUserConfig(owner, name)
	isOWAOptions := strings.EqualFold(req.Name.Name, owaUserOptionsName)
	isRules := strings.EqualFold(req.Name.Name, "Rules")
	// Signature bridge: surface the canonical webmail signature through OWA's
	// options config so an EWS/Outlook client reads the same signature webmail
	// set. The bridge is minimally invasive — it only engages when there is a
	// canonical signature to share (or the stored config already carries one), so
	// a signature-less OWA.UserOptions still round-trips (or 404s) verbatim.
	sig := ""
	if isOWAOptions {
		sig, _ = s.db.GetSignature(owner) //nolint:errcheck // empty signature is a valid (no-signature) state
	}
	if err != nil {
		switch {
		case isRules:
			// Outlook for Mac probes the Rules config before populating its
			// Server Rules list. An ItemNotFound error makes it abandon rules and
			// never call GetInboxRules; returning an empty Success
			// UserConfiguration lets it proceed to the rule listing path (the
			// same way it accepts an empty CategoryList probe and initializes the
			// feature instead of giving up).
			stored = &db.UserConfigBlob{}
		case isOWAOptions && sig != "":
			stored = &db.UserConfigBlob{} // synthesize so the signature can be injected
		default:
			// A well-formed GetUserConfigurationResponse error (not the bare
			// generic ResponseMessage) so Outlook parses the not-found envelope
			// cleanly instead of choking on a malformed body.
			return userConfigNotFoundResponse()
		}
	}
	if isOWAOptions {
		if _, hadSig := owaSignatureFromDict(stored.Dictionary); sig != "" || hadSig {
			stored.Dictionary = emitOWADictionaryWithSignature(parseOWADictionary(stored.Dictionary), sig)
		}
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

// userConfigNotFoundResponse builds a schema-correct GetUserConfigurationResponse
// carrying an ItemNotFound error, wrapped in the GetUserConfigurationResponse >
// ResponseMessages > GetUserConfigurationResponseMessage envelope Outlook expects
// (the generic errorResponseXML emits a bare ResponseMessage with no wrapper).
func userConfigNotFoundResponse() []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:GetUserConfigurationResponse><m:ResponseMessages><m:GetUserConfigurationResponseMessage ResponseClass="Error">`)
	b.WriteString(`<m:ResponseCode>` + string(ErrErrorItemNotFound) + `</m:ResponseCode>`)
	b.WriteString(`<m:MessageText>user configuration not found</m:MessageText>`)
	b.WriteString(`</m:GetUserConfigurationResponseMessage></m:ResponseMessages></m:GetUserConfigurationResponse>`)
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
	owner, name := userConfigOwnerName(mboxKey, req.Name)
	if err := s.db.DeleteUserConfig(owner, name); err != nil {
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
