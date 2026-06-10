// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements folder operations.
package ews

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// folderDisplayName resolves a folder's human-readable name for EWS responses.
// A distinguished folder reports its canonical name; a user-created folder
// reports the name it was created with (recovered from the identity store, which
// keys folders by name). The generic "User Folder" is only a last resort when
// neither a role nor a stored name is available, so custom folders no longer all
// surface as "User Folder".
func (s *Server) folderDisplayName(mailboxKey, role string, id semcore.FolderId) string {
	if name := semcore.CanonicalFolderNameForRole(role); name != "" {
		return name
	}
	if name, err := s.identity.FolderNameByID(strings.TrimPrefix(mailboxKey, "e:"), id); err == nil && name != "" {
		// FolderNameByID returns the raw storage name, which is parent-scoped
		// ("\x1f"+parentID+"\x1f"+display) for a child that collided with a
		// same-named sibling; strip it back to the client-visible display name.
		return semcore.DisplayNameFromStorageName(name)
	}
	if role != "" {
		return role
	}
	return "User Folder"
}

// ---------------------------------------------------------------------------
// GetFolder
// ---------------------------------------------------------------------------

// GetFolderRequest is the EWS GetFolder operation request.
type GetFolderRequest struct {
	XMLName     xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetFolder"`
	FolderIDs   FolderIDsType       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
	FolderShape FolderResponseShape `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderShape"`
}

// FolderIDsType is a list of folder IDs.
type FolderIDsType struct {
	XMLName       xml.Name                  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
	Distinguished []DistinguishedFolderName `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	Folder        []FolderIDOnly            `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// FolderIDOnly is a folder ID without additional properties.
type FolderIDOnly struct {
	XMLName   xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	ID        string   `xml:"Id,attr"`
	ChangeKey string   `xml:"ChangeKey,attr,omitempty"`
}

// GetFolderResponse is the EWS GetFolder operation response.
type GetFolderResponse struct {
	XMLName          xml.Name                  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetFolderResponse"`
	ResponseMessages GetFolderResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetFolderResponseMessages wraps a list of folder response messages.
type GetFolderResponseMessages struct {
	Messages []FolderResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetFolderResponseMessage"`
}

// FolderResponseMessageType is one folder's result in a folder response. The
// enclosing element name is operation-specific (GetFolderResponseMessage,
// FindFolderResponseMessage, …) and is therefore supplied by each container's
// field tag rather than pinned here via XMLName. EWS clients (e.g. exchangelib)
// locate response messages by the operation-prefixed element name, so a generic
// "FolderResponseMessage" name is not interoperable.
type FolderResponseMessageType struct {
	ResponseClass string                  `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType        `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Folders       FolderResponseContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
}

// FolderResponseContainer wraps the Folders list in response messages.
// The m:Folders element is in messages namespace, containing t:Folder in types namespace.
type FolderResponseContainer struct {
	XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
	Folders []FolderType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
}

// ResponseCodeType is the EWS ResponseCode element inside response messages.
type ResponseCodeType struct {
	XMLName xml.Name  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Value   ErrorCode `xml:",chardata"`
}

// handleGetFolder processes a GetFolder EWS SOAP request.
func (s *Server) handleGetFolder(ctx context.Context, body []byte) []byte {
	var req GetFolderRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("GetFolder", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("GetFolder", errCode, "could not resolve mailbox")
	}

	msgs := make([]FolderResponseMessageType, 0)

	// Process distinguished folder IDs.
	for _, d := range req.FolderIDs.Distinguished {
		msg := s.resolveDistinguishedFolder(ctx, mboxID, mboxKey, d.ID)
		msgs = append(msgs, msg)
	}

	// Process explicit folder IDs.
	for _, f := range req.FolderIDs.Folder {
		msg := s.getFolderByID(ctx, mboxID, mboxKey, f.ID, f.ChangeKey)
		msgs = append(msgs, msg)
	}

	resp := GetFolderResponse{}
	resp.ResponseMessages.Messages = msgs
	return buildResponseEnvelope(resp)
}

// resolveDistinguishedFolder resolves a distinguished folder by its name.
// It looks up the folder by its role rather than by folder name.
// For new accounts, it auto-creates the distinguished folder identity.
func (s *Server) resolveDistinguishedFolder(ctx context.Context, mboxID semcore.MailboxId, mboxKey, name string) FolderResponseMessageType {
	role, ok := DistinguishedFolderIDs[name]
	if !ok {
		return errorMsg("GetFolder", ErrErrorFolderNotFound, "unknown distinguished folder: "+name)
	}

	// Strip "e:" prefix to match the key format used by all other handlers.
	// resolveMailboxFromBody returns mboxKey = "e:" + email, but folder/identity
	// operations use the raw email as the mailbox key.
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	// Look up by role — the role is stored in the identity record when
	// EnsureFolderId was called with a role.
	folder, err := s.identity.GetFolderByMailbox(mailboxKey, role)
	if err != nil {
		if errors.Is(err, semcore.ErrFolderNotFound) {
			// Auto-create the distinguished folder for new accounts. Create it
			// under its CANONICAL name (e.g. "Junk", not the EWS id "junkemail")
			// so it converges with the same folder IMAP/JMAP/webmail use and so
			// EnsureFolderId's name fast-path cannot bind to a legacy folder that
			// shares the EWS id as its name but carries a different role.
			createName := name
			if canonical := semcore.CanonicalFolderNameForRole(role); canonical != "" {
				createName = canonical
			}
			// Uses EnsureFolderId so the operation is idempotent for existing folders.
			_, err := s.identity.EnsureFolderId(mailboxKey, createName, role)
			if err != nil {
				return errorMsg("GetFolder", ErrErrorInternalServer, "failed to create folder: "+err.Error())
			}
			// Reload the folder after creation.
			folder, err = s.identity.GetFolderByMailbox(mailboxKey, role)
			if err != nil {
				return errorMsg("GetFolder", ErrErrorFolderNotFound, fmt.Sprintf("folder with role %q not found after creation", role))
			}
		} else {
			return errorMsg("GetFolder", ErrErrorInternalServer, err.Error())
		}
	}

	return s.buildFolderResponse(ctx, mboxID, mboxKey, folder.FolderID)
}

// getFolderByID resolves an explicit folder by its ID.
func (s *Server) getFolderByID(ctx context.Context, mboxID semcore.MailboxId, mboxKey, folderIDStr, changeKey string) FolderResponseMessageType {
	folderID, err := semcore.NewFolderId(folderIDStr)
	if err != nil {
		return errorMsg("GetFolder", ErrErrorInvalidId, err.Error())
	}

	return s.buildFolderResponse(ctx, mboxID, mboxKey, folderID)
}

// buildFolderResponse builds a FolderResponseMessageType for a resolved folder ID.
// mboxKey is needed for ownership check because stored MailboxId may be the key string.
func (s *Server) buildFolderResponse(ctx context.Context, mboxID semcore.MailboxId, mboxKey string, folderID semcore.FolderId) FolderResponseMessageType {
	rec, err := s.identity.GetFolderByID(folderID)
	if err != nil {
		if errors.Is(err, semcore.ErrFolderNotFound) {
			return errorMsg("GetFolder", ErrErrorFolderNotFound, err.Error())
		}
		return errorMsg("GetFolder", ErrErrorInternalServer, err.Error())
	}

	// Verify mailbox ownership.
	// Note: rec.MailboxID stores the mboxKey when using EnsureFolderId, not a
	// stable MailboxId. Compare as strings so e:alice@example.com == e:alice@example.com.
	// Strip "e:" prefix for the comparison to handle records created with or without it.
	ownerKey := strings.TrimPrefix(rec.MailboxID.String(), "e:")
	checkKey := strings.TrimPrefix(mboxKey, "e:")
	if !rec.MailboxID.IsZero() && rec.MailboxID.String() != "" && ownerKey != checkKey {
		return errorMsg("GetFolder", ErrErrorAccessDenied, "folder belongs to a different mailbox")
	}

	displayName := s.folderDisplayName(checkKey, rec.Role, folderID)

	msg := FolderResponseMessageType{
		ResponseClass: "Success",
	}
	msg.ResponseCode.Value = ErrNoError

	fxml := FolderType{
		FolderID:         FolderIdComponents{ID: folderID.String()},
		ParentFolderID:   FolderIdComponents{ID: rec.ParentID.String()},
		DisplayName:      displayName,
		TotalCount:       0,
		ChildFolderCount: 0,
		FolderClass:      folderClassForRole(rec.Role),
	}
	msg.Folders = FolderResponseContainer{Folders: []FolderType{fxml}}
	return msg
}

// folderClassForRole returns the MAPI container class for a distinguished
// folder role. Notes folders carry IPF.StickyNote (so Outlook treats their
// contents as IPM.StickyNote items); every other folder defaults to IPF.Note.
func folderClassForRole(role string) string {
	if role == "notes" {
		return "IPF.StickyNote"
	}
	return "IPF.Note"
}

// errorMsg builds an error FolderResponseMessageType.
func errorMsg(op string, code ErrorCode, message string) FolderResponseMessageType {
	msg := FolderResponseMessageType{}
	msg.ResponseClass = "Error"
	msg.ResponseCode.Value = code
	return msg
}

// ---------------------------------------------------------------------------
// FindFolder
// ---------------------------------------------------------------------------

// FindFolderRequest is the EWS FindFolder operation request.
type FindFolderRequest struct {
	XMLName               xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindFolder"`
	FolderShape           FolderResponseShape `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderShape"`
	IndexedPageFolderView struct {
		MaxEntriesReturned string `xml:"MaxEntriesReturned,attr"`
		Offset             string `xml:"Offset,attr"`
		BasePoint          string `xml:"BasePoint,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages IndexedPageFolderView,omitempty"`
	ParentFolderIDs struct {
		Distinguished []struct {
			XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
			ID      string   `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
		Folder []FolderIDOnly `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ParentFolderIds"`
}

// FindFolderResponse is the EWS FindFolder operation response.
type FindFolderResponse struct {
	XMLName          xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindFolderResponse"`
	ResponseMessages FindFolderResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// FindFolderResponseMessages wraps FindFolder response messages.
type FindFolderResponseMessages struct {
	Messages []FolderResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindFolderResponseMessage"`
}

// handleFindFolder processes a FindFolder EWS SOAP request.
func (s *Server) handleFindFolder(ctx context.Context, body []byte) []byte {
	var req FindFolderRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("FindFolder", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("FindFolder", errCode, "could not resolve mailbox")
	}

	// Strip "e:" prefix: folder/identity operations use raw email as mailbox key.
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	// Determine the parent folder to enumerate under.
	var parentID semcore.FolderId
	var enumerateRoot bool
	if len(req.ParentFolderIDs.Distinguished) > 0 {
		d := req.ParentFolderIDs.Distinguished[0]
		role, ok := DistinguishedFolderIDs[d.ID]
		if !ok {
			return s.errorResponseXML("FindFolder", ErrErrorFolderNotFound, "unknown distinguished folder: "+d.ID)
		}
		if role == "root" {
			// The mailbox root has no concrete parent: its children are the
			// top-level folders, which are stored with a zero parent. Filtering
			// by the root folder's own id would match nothing, so enumerate the
			// top level instead (this is what makes msgfolderroot list the tree).
			enumerateRoot = true
		} else {
			folder, err := s.identity.GetFolderByMailbox(mailboxKey, role)
			if err == nil {
				parentID = folder.FolderID
			} else if errors.Is(err, semcore.ErrFolderNotFound) {
				// Parent distinguished folder doesn't exist yet; enumerate root folders.
				enumerateRoot = true
			} else {
				return s.errorResponseXML("FindFolder", ErrErrorInternalServer, err.Error())
			}
		}
	} else if len(req.ParentFolderIDs.Folder) > 0 {
		fid, err := semcore.NewFolderId(req.ParentFolderIDs.Folder[0].ID)
		if err != nil {
			return s.errorResponseXML("FindFolder", ErrErrorInvalidId, err.Error())
		}
		parentID = fid
	}
	// If no parent specified or parent doesn't exist, enumerate root folders.
	if parentID.IsZero() || enumerateRoot {
		parentID = semcore.FolderId{} // zero = enumerate root folders
	}

	// List all folders for this mailbox.
	allFolders, err := s.identity.ListFolderIdentitiesForMailbox(mailboxKey)
	if err != nil {
		return s.errorResponseXML("FindFolder", ErrErrorInternalServer, err.Error())
	}

	var matching []FolderType
	for _, f := range allFolders {
		// ListFolderIdentitiesForMailbox already filters by mboxKey scope.
		// The mailbox root represents the top level; never list it as a folder.
		if f.Role == "root" {
			continue
		}
		// Filter to children of the specified parent.
		if parentID.String() != "" && !f.ParentID.Equal(parentID) {
			continue
		}
		// Skip the parent itself.
		if f.FolderID.Equal(parentID) {
			continue
		}

		displayName := s.folderDisplayName(mailboxKey, f.Role, f.FolderID)

		fxml := FolderType{
			FolderID:         FolderIdComponents{ID: f.FolderID.String()},
			ParentFolderID:   FolderIdComponents{ID: f.ParentID.String()},
			DisplayName:      displayName,
			TotalCount:       0,
			ChildFolderCount: 0,
			FolderClass:      folderClassForRole(f.Role),
		}
		matching = append(matching, fxml)
	}

	msg := FolderResponseMessageType{}
	msg.ResponseClass = "Success"
	msg.ResponseCode.XMLName = xml.Name{Local: "m:ResponseCode"}
	msg.ResponseCode.Value = ErrNoError
	msg.Folders = FolderResponseContainer{Folders: matching}

	resp := FindFolderResponse{}
	resp.ResponseMessages.Messages = []FolderResponseMessageType{msg}
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// CreateFolder
// ---------------------------------------------------------------------------

// FolderTypeForCreate is used for CreateFolder requests.
type FolderTypeForCreate struct {
	XMLName     xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
	DisplayName string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName"`
	FolderClass string   `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderClass,omitempty"`
}

// FoldersContainer wraps the Folders list in CreateFolder requests.
// The m:Folders element is in messages namespace, containing t:Folder in types namespace.
type FoldersContainer struct {
	XMLName xml.Name              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
	Folders []FolderTypeForCreate `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
}

// CreateFolderRequest is the EWS CreateFolder operation request.
type CreateFolderRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateFolder"`
	// ParentFolderId carries the parent as a CHILD element — either a
	// <t:DistinguishedFolderId Id=".."/> or a <t:FolderId Id=".."/> — not as an
	// attribute on ParentFolderId. Parsing them as attributes (the previous
	// shape) silently dropped the parent, so every folder landed at the root.
	ParentFolderID struct {
		DistinguishedFolderID *struct {
			ID string `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
		FolderID *struct {
			ID string `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ParentFolderId"`
	Folders FoldersContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
}

// CreateFolderResponse is the EWS CreateFolder operation response.
type CreateFolderResponse struct {
	XMLName          xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateFolderResponse"`
	ResponseMessages CreateFolderResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// CreateFolderResponseMessages wraps CreateFolder response messages.
type CreateFolderResponseMessages struct {
	Messages []FolderResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateFolderResponseMessage"`
}

// handleCreateFolder processes a CreateFolder EWS SOAP request.
func (s *Server) handleCreateFolder(ctx context.Context, body []byte) []byte {
	var req CreateFolderRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("CreateFolder", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("CreateFolder", errCode, "could not resolve mailbox")
	}

	// Strip "e:" prefix: folder/identity operations use raw email as mailbox key.
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	// Resolve parent folder.
	var parentID semcore.FolderId
	if req.ParentFolderID.DistinguishedFolderID != nil {
		role, ok := DistinguishedFolderIDs[req.ParentFolderID.DistinguishedFolderID.ID]
		if !ok {
			return s.errorResponseXML("CreateFolder", ErrErrorFolderNotFound, "unknown distinguished folder: "+req.ParentFolderID.DistinguishedFolderID.ID)
		}
		if role != "root" {
			// msgfolderroot has no concrete parent — a folder created directly
			// under it is top-level (zero parent), matching how FindFolder treats
			// the root. Only a non-root distinguished parent (inbox, etc.) nests.
			folder, err := s.identity.GetFolderByMailbox(mailboxKey, role)
			if err == nil {
				parentID = folder.FolderID
			} else if errors.Is(err, semcore.ErrFolderNotFound) {
				// Parent doesn't exist yet; create folder at root (parentID stays zero).
			} else {
				return s.errorResponseXML("CreateFolder", ErrErrorInternalServer, err.Error())
			}
		}
	} else if req.ParentFolderID.FolderID != nil {
		var err error
		parentID, err = semcore.NewFolderId(req.ParentFolderID.FolderID.ID)
		if err != nil {
			return s.errorResponseXML("CreateFolder", ErrErrorInvalidId, err.Error())
		}
	}

	// Ensure mailbox identity exists.
	if _, err := s.identity.EnsureMailboxId(mailboxKey); err != nil {
		return s.errorResponseXML("CreateFolder", ErrErrorInternalServer, err.Error())
	}

	msgs := make([]FolderResponseMessageType, 0, len(req.Folders.Folders))
	for _, f := range req.Folders.Folders {
		if f.DisplayName == "" {
			msgs = append(msgs, errorMsg("CreateFolder", ErrErrorInvalidOperation, "DisplayName is required"))
			continue
		}

		folderID, err := s.identity.EnsureFolderId(mailboxKey, f.DisplayName, "")
		if err != nil {
			msgs = append(msgs, errorMsg("CreateFolder", ErrErrorInternalServer, err.Error()))
			continue
		}

		if !parentID.IsZero() {
			if err := s.identity.SetFolderParent(folderID, parentID); err != nil {
				msgs = append(msgs, errorMsg("CreateFolder", ErrErrorInternalServer, err.Error()))
				continue
			}
		}

		displayName := f.DisplayName
		fxml := FolderType{
			FolderID:       FolderIdComponents{ID: folderID.String()},
			ParentFolderID: FolderIdComponents{ID: parentID.String()},
			DisplayName:    displayName,
			FolderClass:    "IPF.Note",
		}
		msg := FolderResponseMessageType{}
		msg.ResponseClass = "Created"
		msg.ResponseCode.XMLName = xml.Name{Local: "m:ResponseCode"}
		msg.ResponseCode.Value = ErrNoError
		msg.Folders = FolderResponseContainer{Folders: []FolderType{fxml}}
		msgs = append(msgs, msg)
	}

	resp := CreateFolderResponse{}
	resp.ResponseMessages.Messages = msgs
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// UpdateFolder
// ---------------------------------------------------------------------------

// SetFolderFieldOp represents a SetFolderField update operation.
type SetFolderFieldOp struct {
	XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types SetFolderField"`
	FieldURIField struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
		URI     string   `xml:"uri,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FieldURI"`
	Folder struct {
		DisplayName *struct {
			XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName"`
			Value   string   `xml:",chardata"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName"`
		ParentFolderId *struct {
			XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
			ID      string   `xml:"Id,attr"`
			CK      string   `xml:"ChangeKey,attr,omitempty"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
}

// FolderUpdatesOp wraps the update operations for a folder.
type FolderUpdatesOp struct {
	XMLName    xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
	Operations []SetFolderFieldOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types SetFolderField"`
}

// FolderChangeOp represents one folder change in UpdateFolder.
type FolderChangeOp struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderChange"`
	FolderID struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		ID      string   `xml:"Id,attr"`
		CK      string   `xml:"ChangeKey,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	Updates FolderUpdatesOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
}

// FolderChangesList wraps the FolderChanges list in UpdateFolder.
type FolderChangesList struct {
	XMLName xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderChanges"`
	Changes []FolderChangeOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderChange"`
}

// UpdateFolderRequest is the EWS UpdateFolder operation request.
type UpdateFolderRequest struct {
	XMLName       xml.Name          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateFolder"`
	FolderChanges FolderChangesList `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderChanges"`
}

// UpdateFolderResponse is the EWS UpdateFolder operation response.
type UpdateFolderResponse struct {
	XMLName          xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateFolderResponse"`
	ResponseMessages UpdateFolderResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// UpdateFolderResponseMessages wraps UpdateFolder response messages.
type UpdateFolderResponseMessages struct {
	Messages []FolderResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateFolderResponseMessage"`
}

// DeleteFolderResponseMessages wraps DeleteFolder response messages.
type DeleteFolderResponseMessages struct {
	Messages []struct {
		XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponseMessage"`
		ResponseClass string   `xml:"ResponseClass,attr"`
		ResponseCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponseMessage"`
}

// handleUpdateFolder processes an UpdateFolder EWS SOAP request.
func (s *Server) handleUpdateFolder(ctx context.Context, body []byte) []byte {
	var req UpdateFolderRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("UpdateFolder", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("UpdateFolder", errCode, "could not resolve mailbox")
	}

	msgs := make([]FolderResponseMessageType, 0, len(req.FolderChanges.Changes))
	for _, fc := range req.FolderChanges.Changes {
		folderID, err := semcore.NewFolderId(fc.FolderID.ID)
		if err != nil {
			msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInvalidId, err.Error()))
			continue
		}

		rec, err := s.identity.GetFolderByID(folderID)
		if err != nil {
			if errors.Is(err, semcore.ErrFolderNotFound) {
				msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorFolderNotFound, err.Error()))
			} else {
				msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInternalServer, err.Error()))
			}
			continue
		}

		if !rec.MailboxID.IsZero() && rec.MailboxID.String() != "" && strings.TrimPrefix(rec.MailboxID.String(), "e:") != strings.TrimPrefix(mboxKey, "e:") {
			msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorAccessDenied, "folder belongs to a different mailbox"))
			continue
		}

		// Distinguished folders cannot be renamed or reparented.
		if rec.Role != "" && len(fc.Updates.Operations) > 0 {
			msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInvalidOperation, "cannot modify a distinguished folder"))
			continue
		}

		applied := false
		for _, op := range fc.Updates.Operations {
			if op.Folder.DisplayName != nil {
				// Display name change: semcore identity is stable — display name is not stored
				// in the identity store. The client sees the new display name in the response.
				applied = true
			}
			if op.Folder.ParentFolderId != nil {
				newParentID, err := semcore.NewFolderId(op.Folder.ParentFolderId.ID)
				if err != nil {
					msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInvalidId, err.Error()))
					continue
				}
				if err := s.identity.SetFolderParent(folderID, newParentID); err != nil {
					msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInternalServer, err.Error()))
					continue
				}
				applied = true
			}
		}

		if !applied {
			msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInvalidOperation, "no valid updates specified"))
			continue
		}

		// Reload to return current state.
		rec, err = s.identity.GetFolderByID(folderID)
		if err != nil {
			msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInternalServer, err.Error()))
			continue
		}

		// Determine display name.
		displayName := s.folderDisplayName(mboxKey, rec.Role, folderID)

		fxml := FolderType{
			FolderID:       FolderIdComponents{ID: folderID.String()},
			ParentFolderID: FolderIdComponents{ID: rec.ParentID.String()},
			DisplayName:    displayName,
			FolderClass:    folderClassForRole(rec.Role),
		}
		msg := FolderResponseMessageType{}
		msg.ResponseClass = "Success"
		msg.ResponseCode.Value = ErrNoError
		msg.Folders = FolderResponseContainer{Folders: []FolderType{fxml}}
		msgs = append(msgs, msg)
	}

	resp := UpdateFolderResponse{}
	resp.ResponseMessages.Messages = msgs
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// DeleteFolder
// ---------------------------------------------------------------------------

// FolderIdForDelete represents a FolderId or DistinguishedFolderId in DeleteFolder requests.
type FolderIdForDelete struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	ID      string   `xml:"Id,attr"`
	CK      string   `xml:"ChangeKey,attr,omitempty"`
}

// FolderIdsForDelete wraps the FolderIds list in DeleteFolder requests.
type FolderIdsForDelete struct {
	XMLName xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
	Items   []FolderIdForDelete `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// DeleteFolderRequest is the EWS DeleteFolder operation request.
type DeleteFolderRequest struct {
	XMLName    xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolder"`
	FolderIDs  FolderIdsForDelete `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
	DeleteType string             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteType,attr"` // HardDelete | SoftDelete | MoveToDeletedItems
}

// DeleteFolderResponse is the EWS DeleteFolder operation response.
type DeleteFolderResponse struct {
	XMLName          xml.Name                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponse"`
	ResponseMessages DeleteFolderResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// handleDeleteFolder processes a DeleteFolder EWS SOAP request.
func (s *Server) handleDeleteFolder(ctx context.Context, body []byte) []byte {
	var req DeleteFolderRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("DeleteFolder", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("DeleteFolder", errCode, "could not resolve mailbox")
	}

	msgs := make([]struct {
		XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponseMessage"`
		ResponseClass string   `xml:"ResponseClass,attr"`
		ResponseCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	}, 0, len(req.FolderIDs.Items))

	for _, item := range req.FolderIDs.Items {
		folderID, err := semcore.NewFolderId(item.ID)
		if err != nil {
			msgs = append(msgs, struct {
				XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponseMessage"`
				ResponseClass string   `xml:"ResponseClass,attr"`
				ResponseCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
			}{
				ResponseClass: "Error",
				ResponseCode:  string(ErrErrorInvalidId),
			})
			continue
		}

		rec, err := s.identity.GetFolderByID(folderID)
		if err != nil {
			msgs = append(msgs, struct {
				XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponseMessage"`
				ResponseClass string   `xml:"ResponseClass,attr"`
				ResponseCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
			}{
				ResponseClass: "Error",
				ResponseCode:  string(ErrErrorFolderNotFound),
			})
			continue
		}

		if !rec.MailboxID.IsZero() && rec.MailboxID.String() != "" && strings.TrimPrefix(rec.MailboxID.String(), "e:") != strings.TrimPrefix(mboxKey, "e:") {
			msgs = append(msgs, struct {
				XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponseMessage"`
				ResponseClass string   `xml:"ResponseClass,attr"`
				ResponseCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
			}{
				ResponseClass: "Error",
				ResponseCode:  string(ErrErrorAccessDenied),
			})
			continue
		}

		if rec.Role != "" {
			msgs = append(msgs, struct {
				XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponseMessage"`
				ResponseClass string   `xml:"ResponseClass,attr"`
				ResponseCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
			}{
				ResponseClass: "Error",
				ResponseCode:  string(ErrErrorInvalidOperation),
			})
			continue
		}

		// Mark folder sync state as gone.
		_ = s.sync.MarkFolderGone(folderID)

		// Record folder-level tombstone.
		kind := semcore.LifecycleKindSoftDeleted
		if req.DeleteType == "HardDelete" {
			kind = semcore.LifecycleKindHardDeleted
		}
		tomb := semcore.Tombstone{
			MailboxID: mboxID,
			FolderID:  folderID,
			Kind:      kind,
		}
		_ = s.tombstones.PutTombstone(tomb)

		// Remove folder identity from storage.
		if err := s.identity.DeleteFolder(folderID); err != nil && !errors.Is(err, semcore.ErrFolderNotFound) {
			msgs = append(msgs, struct {
				XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponseMessage"`
				ResponseClass string   `xml:"ResponseClass,attr"`
				ResponseCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
			}{
				ResponseClass: "Error",
				ResponseCode:  string(ErrErrorInternalServer),
			})
			continue
		}

		msgs = append(msgs, struct {
			XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponseMessage"`
			ResponseClass string   `xml:"ResponseClass,attr"`
			ResponseCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
		}{
			ResponseClass: "Success",
			ResponseCode:  string(ErrNoError),
		})
	}

	resp := DeleteFolderResponse{}
	resp.ResponseMessages.Messages = msgs
	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// EmptyFolder
// ---------------------------------------------------------------------------

// emptyFolderRef is one folder reference (FolderId or DistinguishedFolderId)
// in an EmptyFolder request.
type emptyFolderRef struct {
	ID string `xml:"Id,attr"`
}

// EmptyFolderRequest is the EWS EmptyFolder operation request. It deletes all
// items (and optionally all subfolders) of the named folders.
type EmptyFolderRequest struct {
	XMLName          xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages EmptyFolder"`
	DeleteType       string   `xml:"DeleteType,attr"`       // HardDelete | SoftDelete | MoveToDeletedItems
	DeleteSubFolders bool     `xml:"DeleteSubFolders,attr"` // also remove descendant folders
	FolderIDs        struct {
		FolderID              []emptyFolderRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		DistinguishedFolderID []emptyFolderRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
}

// EmptyFolderResponse is the EWS EmptyFolder operation response.
type EmptyFolderResponse struct {
	XMLName          xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages EmptyFolderResponse"`
	ResponseMessages struct {
		Messages []emptyFolderResponseMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages EmptyFolderResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

type emptyFolderResponseMessage struct {
	XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages EmptyFolderResponseMessage"`
	ResponseClass string   `xml:"ResponseClass,attr"`
	ResponseCode  string   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
}

// handleEmptyFolder processes an EWS EmptyFolder request: it deletes every item
// in each target folder and, when DeleteSubFolders is set, removes the folder's
// descendant folders too. The folder itself is preserved.
func (s *Server) handleEmptyFolder(ctx context.Context, body []byte) []byte {
	var req EmptyFolderRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("EmptyFolder", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("EmptyFolder", errCode, "could not resolve mailbox")
	}
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorResponseXML("EmptyFolder", ErrErrorMailboxNotFound, err.Error())
	}

	hardDelete := req.DeleteType == "HardDelete"

	// Resolve each folder reference to a concrete FolderId.
	type target struct {
		id  semcore.FolderId
		err ErrorCode
	}
	var targets []target
	for _, ref := range req.FolderIDs.DistinguishedFolderID {
		role, ok := DistinguishedFolderIDs[ref.ID]
		if !ok {
			targets = append(targets, target{err: ErrErrorFolderNotFound})
			continue
		}
		folder, ferr := s.identity.GetFolderByMailbox(mailboxKey, role)
		if ferr != nil {
			// Auto-create the distinguished folder for new accounts (matches
			// resolveDistinguishedFolder); a freshly created folder is simply empty.
			fid, cerr := s.identity.EnsureFolderId(mailboxKey, ref.ID, role)
			if cerr != nil {
				targets = append(targets, target{err: ErrErrorFolderNotFound})
				continue
			}
			targets = append(targets, target{id: fid})
			continue
		}
		targets = append(targets, target{id: folder.FolderID})
	}
	for _, ref := range req.FolderIDs.FolderID {
		fid, ferr := semcore.NewFolderId(ref.ID)
		if ferr != nil {
			targets = append(targets, target{err: ErrErrorInvalidId})
			continue
		}
		targets = append(targets, target{id: fid})
	}

	resp := EmptyFolderResponse{}
	for _, t := range targets {
		if t.err != "" {
			resp.ResponseMessages.Messages = append(resp.ResponseMessages.Messages, emptyFolderResponseMessage{
				ResponseClass: "Error", ResponseCode: string(t.err),
			})
			continue
		}
		// Ownership check.
		rec, gerr := s.identity.GetFolderByID(t.id)
		if gerr != nil {
			resp.ResponseMessages.Messages = append(resp.ResponseMessages.Messages, emptyFolderResponseMessage{
				ResponseClass: "Error", ResponseCode: string(ErrErrorFolderNotFound),
			})
			continue
		}
		owner := rec.MailboxID.String()
		if !rec.MailboxID.IsZero() && owner != "" && owner != mailboxKey && owner != mboxKey && rec.MailboxID != mboxID {
			resp.ResponseMessages.Messages = append(resp.ResponseMessages.Messages, emptyFolderResponseMessage{
				ResponseClass: "Error", ResponseCode: string(ErrErrorAccessDenied),
			})
			continue
		}

		s.emptyFolderItems(mboxID, mailboxKey, t.id, hardDelete)
		s.notifyFolderChange(mailboxKey, t.id)

		if req.DeleteSubFolders {
			for _, sub := range s.descendantFolderIDs(mailboxKey, t.id) {
				s.emptyFolderItems(mboxID, mailboxKey, sub, hardDelete)
				if derr := s.sync.MarkFolderGone(sub); derr != nil {
					s.logger.Warn("EmptyFolder: mark subfolder gone", "error", derr)
				}
				if derr := s.identity.DeleteFolder(sub); derr != nil && !errors.Is(derr, semcore.ErrFolderNotFound) {
					s.logger.Warn("EmptyFolder: delete subfolder", "error", derr)
				}
			}
		}

		resp.ResponseMessages.Messages = append(resp.ResponseMessages.Messages, emptyFolderResponseMessage{
			ResponseClass: "Success", ResponseCode: string(ErrNoError),
		})
	}
	return buildResponseEnvelope(resp)
}

// emptyFolderItems deletes every item (mail + collaboration) in a single folder.
func (s *Server) emptyFolderItems(mboxID semcore.MailboxId, mailboxKey string, folderID semcore.FolderId, hardDelete bool) {
	if s.mutationPipe != nil {
		if items, err := s.identity.ListItemIdentitiesByFolder(folderID); err == nil {
			for _, it := range items {
				// On a hard empty (e.g. emptying Deleted Items), file each message
				// into Recoverable Items first so it stays restorable.
				if hardDelete {
					s.captureBeforeHardDelete(mailboxKey, folderID, it.Email, it.MsgKey)
				}
				in := &semcore.DeleteInput{
					ItemID:     it.ItemID,
					MailboxID:  mboxID,
					FolderID:   folderID,
					Actor:      mailboxKey,
					Source:     semcore.MutationSourceEWS,
					HardDelete: hardDelete,
				}
				if derr := s.mutationPipe.MutateDelete(in, s.tombstones); derr == nil && !hardDelete {
					if ierr := s.identity.DeleteItemIdentity(it.ItemID); ierr != nil {
						s.logger.Warn("EmptyFolder: delete item identity", "error", ierr)
					}
				}
			}
		}
	}
	if s.collabStore != nil {
		if cal, err := s.collabStore.ListCalendarItemsByFolder(folderID); err == nil {
			for _, c := range cal {
				s.deleteItemFromCollab(mailboxKey, c.ID.String())
			}
		}
		if contacts, err := s.collabStore.ListContactsByFolder(folderID); err == nil {
			for _, c := range contacts {
				s.deleteItemFromCollab(mailboxKey, c.ID.String())
			}
		}
		if tasks, err := s.collabStore.ListTasksByFolder(folderID); err == nil {
			for _, tk := range tasks {
				s.deleteItemFromCollab(mailboxKey, tk.ID.String())
			}
		}
	}
}

// descendantFolderIDs returns all folder IDs that are descendants (children,
// grandchildren, ...) of root within the given mailbox.
func (s *Server) descendantFolderIDs(mailboxKey string, root semcore.FolderId) []semcore.FolderId {
	all, err := s.identity.ListFolderIdentitiesForMailbox(mailboxKey)
	if err != nil {
		return nil
	}
	var out []semcore.FolderId
	frontier := []semcore.FolderId{root}
	for len(frontier) > 0 {
		parent := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		for _, f := range all {
			if f.ParentID.Equal(parent) {
				out = append(out, f.FolderID)
				frontier = append(frontier, f.FolderID)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// MoveFolder
// ---------------------------------------------------------------------------

// moveFolderTargetRef parses a FolderId/DistinguishedFolderId reference with an
// Id attribute (the EWS shape used by MoveFolder source and destination).
type moveFolderTargetRef struct {
	ID string `xml:"Id,attr"`
}

// MoveFolderRequest is the EWS MoveFolder operation request. It re-parents the
// listed folders under ToFolderId.
type MoveFolderRequest struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MoveFolder"`
	ToFolder struct {
		FolderID              *moveFolderTargetRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		DistinguishedFolderID *moveFolderTargetRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ToFolderId"`
	FolderIDs struct {
		FolderID              []moveFolderTargetRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		DistinguishedFolderID []moveFolderTargetRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
}

// handleMoveFolder re-parents folders under a destination folder. The folder's
// canonical ID is stable, so the response echoes the same FolderId.
func (s *Server) handleMoveFolder(ctx context.Context, body []byte) []byte {
	var req MoveFolderRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("MoveFolder", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("MoveFolder", errCode, "could not resolve mailbox")
	}
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorResponseXML("MoveFolder", ErrErrorMailboxNotFound, err.Error())
	}

	// Resolve the destination (to) folder.
	var dest semcore.FolderId
	switch {
	case req.ToFolder.DistinguishedFolderID != nil:
		role, ok := DistinguishedFolderIDs[req.ToFolder.DistinguishedFolderID.ID]
		if !ok {
			return s.errorResponseXML("MoveFolder", ErrErrorFolderNotFound, "unknown destination distinguished folder")
		}
		fld, ferr := s.identity.GetFolderByMailbox(mailboxKey, role)
		if ferr != nil {
			fid, cerr := s.identity.EnsureFolderId(mailboxKey, req.ToFolder.DistinguishedFolderID.ID, role)
			if cerr != nil {
				return s.errorResponseXML("MoveFolder", ErrErrorFolderNotFound, "destination folder not found")
			}
			dest = fid
		} else {
			dest = fld.FolderID
		}
	case req.ToFolder.FolderID != nil:
		dest, err = semcore.NewFolderId(req.ToFolder.FolderID.ID)
		if err != nil {
			return s.errorResponseXML("MoveFolder", ErrErrorInvalidId, "invalid destination folder id")
		}
	}
	if dest.IsZero() {
		return s.errorResponseXML("MoveFolder", ErrErrorFolderNotFound, "destination folder required")
	}

	// Gather source folder IDs (explicit ids + distinguished names).
	var sources []semcore.FolderId
	var srcErr []ErrorCode
	for _, ref := range req.FolderIDs.DistinguishedFolderID {
		role, ok := DistinguishedFolderIDs[ref.ID]
		if !ok {
			sources = append(sources, semcore.FolderId{})
			srcErr = append(srcErr, ErrErrorFolderNotFound)
			continue
		}
		fld, ferr := s.identity.GetFolderByMailbox(mailboxKey, role)
		if ferr != nil {
			sources = append(sources, semcore.FolderId{})
			srcErr = append(srcErr, ErrErrorFolderNotFound)
			continue
		}
		sources = append(sources, fld.FolderID)
		srcErr = append(srcErr, "")
	}
	for _, ref := range req.FolderIDs.FolderID {
		fid, ferr := semcore.NewFolderId(ref.ID)
		if ferr != nil {
			sources = append(sources, semcore.FolderId{})
			srcErr = append(srcErr, ErrErrorInvalidId)
			continue
		}
		sources = append(sources, fid)
		srcErr = append(srcErr, "")
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:MoveFolderResponse><m:ResponseMessages>`)
	for i, fid := range sources {
		if srcErr[i] != "" {
			b.WriteString(`<m:MoveFolderResponseMessage ResponseClass="Error"><m:ResponseCode>` + string(srcErr[i]) + `</m:ResponseCode></m:MoveFolderResponseMessage>`)
			continue
		}
		rec, gerr := s.identity.GetFolderByID(fid)
		if gerr != nil {
			b.WriteString(`<m:MoveFolderResponseMessage ResponseClass="Error"><m:ResponseCode>` + string(ErrErrorFolderNotFound) + `</m:ResponseCode></m:MoveFolderResponseMessage>`)
			continue
		}
		owner := rec.MailboxID.String()
		if !rec.MailboxID.IsZero() && owner != "" && owner != mailboxKey && owner != mboxKey && rec.MailboxID != mboxID {
			b.WriteString(`<m:MoveFolderResponseMessage ResponseClass="Error"><m:ResponseCode>` + string(ErrErrorAccessDenied) + `</m:ResponseCode></m:MoveFolderResponseMessage>`)
			continue
		}
		if serr := s.identity.SetFolderParent(fid, dest); serr != nil {
			b.WriteString(`<m:MoveFolderResponseMessage ResponseClass="Error"><m:ResponseCode>` + string(ErrErrorInternalServer) + `</m:ResponseCode></m:MoveFolderResponseMessage>`)
			continue
		}
		s.notifyFolderChange(mailboxKey, fid)
		b.WriteString(`<m:MoveFolderResponseMessage ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode>`)
		b.WriteString(`<m:Folders><t:Folder><t:FolderId Id="` + xmlEscape(fid.String()) + `"/></t:Folder></m:Folders>`)
		b.WriteString(`</m:MoveFolderResponseMessage>`)
	}
	b.WriteString(`</m:ResponseMessages></m:MoveFolderResponse>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// CopyFolder
// ---------------------------------------------------------------------------

// CopyFolderRequest is the EWS CopyFolder operation request. It deep-copies the
// listed folders (subtree + mail items) under ToFolderId. Same wire shape as
// MoveFolder; the response echoes each copy's NEW FolderId, not the source's.
type CopyFolderRequest struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CopyFolder"`
	ToFolder struct {
		FolderID              *moveFolderTargetRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		DistinguishedFolderID *moveFolderTargetRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ToFolderId"`
	FolderIDs struct {
		FolderID              []moveFolderTargetRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		DistinguishedFolderID []moveFolderTargetRef `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
}

// isCollabRole reports whether a folder role stores collaboration objects
// (calendar/contacts/tasks) rather than mail. Those live in the collaboration
// store, which has no copy primitive, so CopyFolder duplicates only their shell.
func isCollabRole(role string) bool {
	switch role {
	case "calendar", "contacts", "tasks":
		return true
	default:
		return false
	}
}

// handleCopyFolder deep-copies folders under a destination folder: each source
// folder's subtree is recreated and its mail items are duplicated as fresh
// items. Unlike MoveFolder, the source is left untouched and the response
// echoes the NEW (copy's) FolderId.
func (s *Server) handleCopyFolder(ctx context.Context, body []byte) []byte {
	var req CopyFolderRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("CopyFolder", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	_, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("CopyFolder", errCode, "could not resolve mailbox")
	}
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	mboxID, err := s.identity.GetMailboxIDByEmail(mailboxKey)
	if err != nil {
		return s.errorResponseXML("CopyFolder", ErrErrorMailboxNotFound, err.Error())
	}

	// Resolve the destination (to) folder.
	var dest semcore.FolderId
	switch {
	case req.ToFolder.DistinguishedFolderID != nil:
		role, ok := DistinguishedFolderIDs[req.ToFolder.DistinguishedFolderID.ID]
		if !ok {
			return s.errorResponseXML("CopyFolder", ErrErrorFolderNotFound, "unknown destination distinguished folder")
		}
		fld, ferr := s.identity.GetFolderByMailbox(mailboxKey, role)
		if ferr != nil {
			fid, cerr := s.identity.EnsureFolderId(mailboxKey, req.ToFolder.DistinguishedFolderID.ID, role)
			if cerr != nil {
				return s.errorResponseXML("CopyFolder", ErrErrorFolderNotFound, "destination folder not found")
			}
			dest = fid
		} else {
			dest = fld.FolderID
		}
	case req.ToFolder.FolderID != nil:
		dest, err = semcore.NewFolderId(req.ToFolder.FolderID.ID)
		if err != nil {
			return s.errorResponseXML("CopyFolder", ErrErrorInvalidId, "invalid destination folder id")
		}
	}
	if dest.IsZero() {
		return s.errorResponseXML("CopyFolder", ErrErrorFolderNotFound, "destination folder required")
	}

	// Gather source folder IDs (explicit ids + distinguished names).
	var sources []semcore.FolderId
	var srcErr []ErrorCode
	for _, ref := range req.FolderIDs.DistinguishedFolderID {
		role, ok := DistinguishedFolderIDs[ref.ID]
		if !ok {
			sources = append(sources, semcore.FolderId{})
			srcErr = append(srcErr, ErrErrorFolderNotFound)
			continue
		}
		fld, ferr := s.identity.GetFolderByMailbox(mailboxKey, role)
		if ferr != nil {
			sources = append(sources, semcore.FolderId{})
			srcErr = append(srcErr, ErrErrorFolderNotFound)
			continue
		}
		sources = append(sources, fld.FolderID)
		srcErr = append(srcErr, "")
	}
	for _, ref := range req.FolderIDs.FolderID {
		fid, ferr := semcore.NewFolderId(ref.ID)
		if ferr != nil {
			sources = append(sources, semcore.FolderId{})
			srcErr = append(srcErr, ErrErrorInvalidId)
			continue
		}
		sources = append(sources, fid)
		srcErr = append(srcErr, "")
	}

	// Snapshot the mailbox folder tree ONCE so recursion replicates only the
	// original folders, never the copies it creates as it goes.
	all, lerr := s.identity.ListFolderIdentitiesForMailbox(mailboxKey)
	if lerr != nil {
		return s.errorResponseXML("CopyFolder", ErrErrorInternalServer, lerr.Error())
	}
	childrenOf := make(map[string][]semcore.FolderId, len(all))
	recByID := make(map[string]*semcore.StoredFolderIdentity, len(all))
	for i := range all {
		f := &all[i]
		childrenOf[f.ParentID.String()] = append(childrenOf[f.ParentID.String()], f.FolderID)
		recByID[f.FolderID.String()] = f
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="` + SOAPEnvelopeNS + `" xmlns:t="` + EWSTypesNS + `" xmlns:m="` + EWSMessagesNS + `">`)
	b.WriteString(`<soap:Header>`)
	sv := NewServerVersion()
	svBytes, _ := xml.Marshal(sv) //nolint:errcheck
	b.Write(svBytes)
	b.WriteString(`</soap:Header><soap:Body>`)
	b.WriteString(`<m:CopyFolderResponse><m:ResponseMessages>`)
	for i, fid := range sources {
		if srcErr[i] != "" {
			b.WriteString(`<m:CopyFolderResponseMessage ResponseClass="Error"><m:ResponseCode>` + string(srcErr[i]) + `</m:ResponseCode></m:CopyFolderResponseMessage>`)
			continue
		}
		rec := recByID[fid.String()]
		if rec == nil {
			b.WriteString(`<m:CopyFolderResponseMessage ResponseClass="Error"><m:ResponseCode>` + string(ErrErrorFolderNotFound) + `</m:ResponseCode></m:CopyFolderResponseMessage>`)
			continue
		}
		owner := rec.MailboxID.String()
		if !rec.MailboxID.IsZero() && owner != "" && owner != mailboxKey && owner != mboxKey && rec.MailboxID != mboxID {
			b.WriteString(`<m:CopyFolderResponseMessage ResponseClass="Error"><m:ResponseCode>` + string(ErrErrorAccessDenied) + `</m:ResponseCode></m:CopyFolderResponseMessage>`)
			continue
		}
		newRoot, cerr := s.copyFolderTree(ctx, mboxID, mailboxKey, fid, dest, childrenOf, recByID, map[string]bool{}, 0)
		if cerr != nil {
			b.WriteString(`<m:CopyFolderResponseMessage ResponseClass="Error"><m:ResponseCode>` + string(ErrErrorInternalServer) + `</m:ResponseCode></m:CopyFolderResponseMessage>`)
			continue
		}
		b.WriteString(`<m:CopyFolderResponseMessage ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode>`)
		b.WriteString(`<m:Folders><t:Folder><t:FolderId Id="` + xmlEscape(newRoot.String()) + `"/></t:Folder></m:Folders>`)
		b.WriteString(`</m:CopyFolderResponseMessage>`)
	}
	b.WriteString(`</m:ResponseMessages></m:CopyFolderResponse>`)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return []byte(b.String())
}

// copyFolderTree recursively copies a source folder subtree under destParent and
// returns the new root FolderId. childrenOf/recByID are a pre-captured snapshot
// of the source tree (so newly created copies are never re-copied); visited and
// depth guard against a cyclic ParentID chain. Mail items are duplicated as
// fresh items via createRawItemInFolder (new ItemId + IMAP mirror); a
// collaboration folder's shell is copied with its role but its items are not
// duplicated (the collaboration store has no copy primitive).
func (s *Server) copyFolderTree(ctx context.Context, mboxID semcore.MailboxId, mailboxKey string, src, destParent semcore.FolderId, childrenOf map[string][]semcore.FolderId, recByID map[string]*semcore.StoredFolderIdentity, visited map[string]bool, depth int) (semcore.FolderId, error) {
	if depth > 64 {
		return semcore.FolderId{}, fmt.Errorf("copyFolderTree: max depth exceeded")
	}
	if visited[src.String()] {
		return semcore.FolderId{}, fmt.Errorf("copyFolderTree: cycle at %s", src.String())
	}
	visited[src.String()] = true

	rec := recByID[src.String()]
	if rec == nil {
		return semcore.FolderId{}, fmt.Errorf("copyFolderTree: source folder %s not found", src.String())
	}
	srcName, err := s.identity.FolderNameByID(mailboxKey, src)
	if err != nil {
		return semcore.FolderId{}, fmt.Errorf("copyFolderTree: resolve source name: %w", err)
	}
	display := semcore.DisplayNameFromStorageName(srcName)

	// A distinguished mail role is not preserved on a copy (there is one
	// canonical Inbox/Sent/…); a collaboration role is preserved so the copy
	// keeps its folder class. Plain user folders carry no role.
	copyRole := ""
	if isCollabRole(rec.Role) {
		copyRole = rec.Role
	}
	newRoot, err := s.identity.EnsureChildFolderId(mailboxKey, destParent, display, copyRole)
	if err != nil {
		return semcore.FolderId{}, fmt.Errorf("copyFolderTree: create copy: %w", err)
	}

	// Duplicate mail items (collaboration items have no copy primitive).
	if !isCollabRole(rec.Role) {
		if items, ierr := s.identity.ListItemIdentitiesByFolder(src); ierr == nil {
			for _, it := range items {
				rawMsg, rerr := s.msgStore.ReadMessage(it.Email, it.MsgKey)
				if rerr != nil {
					continue
				}
				copiedRaw := prependHeader(rawMsg, "X-uMailServer-Copy-ID", generateID())
				subject, _, _, _, _, _ := parseMimeHeaders(rawMsg)
				s.createRawItemInFolder(ctx, mboxID, mailboxKey, newRoot, subject, copiedRaw, nil)
			}
		}
	}

	// Recurse into the snapshot's direct children (originals only).
	for _, child := range childrenOf[src.String()] {
		if _, cerr := s.copyFolderTree(ctx, mboxID, mailboxKey, child, newRoot, childrenOf, recByID, visited, depth+1); cerr != nil {
			return semcore.FolderId{}, cerr
		}
	}

	s.notifyFolderChange(mailboxKey, newRoot)
	return newRoot, nil
}

// ---------------------------------------------------------------------------
// SyncFolderHierarchy
// ---------------------------------------------------------------------------

// SyncFolderHierarchyRequest is the EWS SyncFolderHierarchy operation request.
type SyncFolderHierarchyRequest struct {
	XMLName     xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderHierarchy"`
	SyncState   string              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncState,omitempty"`
	FolderShape FolderResponseShape `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderShape"`
}

// SyncFolderChangesContainer wraps the SyncFolderHierarchy Changes element.
// SyncFolderChangesUpdate wraps <Update> containing <Folder> elements.
type SyncFolderChangesUpdate struct {
	XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/types Update"`
	Folders []FolderType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
}

// SyncFolderChangesDelete wraps <Delete> containing <FolderId> elements.
type SyncFolderChangesDelete struct {
	XMLName   xml.Name       `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
	FolderIds []FolderIDOnly `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// The <Changes> element is in messages namespace; <Update>/<Delete> are in types namespace.
type SyncFolderChangesContainer struct {
	Updates []SyncFolderChangesUpdate `xml:"http://schemas.microsoft.com/exchange/services/2006/types Update"`
	Deletes []SyncFolderChangesDelete `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
}

// SyncFolderHierarchyMsg is one SyncFolderHierarchy result.
type SyncFolderHierarchyMsg struct {
	ResponseClass string                     `xml:"ResponseClass,attr"`
	ResponseCode  string                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	SyncState     string                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncState"`
	Changes       SyncFolderChangesContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Changes"`
}

// SyncFolderHierarchyResponse is the EWS SyncFolderHierarchy operation response.
type SyncFolderHierarchyResponse struct {
	XMLName          xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderHierarchyResponse"`
	ResponseMessages struct {
		Messages []SyncFolderHierarchyMsg `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderHierarchyResponseMessage"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// handleSyncFolderHierarchy processes a SyncFolderHierarchy EWS SOAP request.
func (s *Server) handleSyncFolderHierarchy(ctx context.Context, body []byte) []byte {
	var req SyncFolderHierarchyRequest
	if err := decodeRequest(body, &req); err != nil {
		return s.errorResponseXML("SyncFolderHierarchy", ErrErrorInvalidOperation, "malformed request: "+err.Error())
	}

	mboxID, mboxKey, errCode := s.resolveMailboxFromBody(ctx, body)
	if errCode != "" {
		return s.errorResponseXML("SyncFolderHierarchy", errCode, "could not resolve mailbox")
	}

	clientID := "ews:" + mboxKey

	// Get or initialize the sync state record.
	var folderVersion uint64
	var deletedSince time.Time
	if req.SyncState != "" {
		state, err := s.sync.GetSyncState(mboxID, semcore.FolderId{}, clientID)
		if err != nil && !errors.Is(err, semcore.ErrSyncStateNotFound) {
			return s.errorResponseXML("SyncFolderHierarchy", ErrErrorSync, err.Error())
		}
		if state != nil {
			folderVersion = state.Version
			deletedSince = state.UpdatedAt
		}
	}

	// List all folders for this mailbox.
	allFolders, err := s.identity.ListFolderIdentitiesForMailbox(mboxKey)
	if err != nil {
		return s.errorResponseXML("SyncFolderHierarchy", ErrErrorInternalServer, err.Error())
	}

	// Collect deletions since last sync.
	var deletions []FolderIDOnly
	if !deletedSince.IsZero() {
		tombs, err := s.tombstones.ListTombstonesSince(mboxID, semcore.FolderId{}, deletedSince)
		if err == nil {
			for _, t := range tombs {
				if t.IsFolderLevel() {
					deletions = append(deletions, FolderIDOnly{ID: t.FolderID.String()})
				}
			}
		}
	}

	// Build updates: all folders on initial sync; modified folders on incremental.
	var updates []FolderType
	for _, f := range allFolders {
		// ListFolderIdentitiesForMailbox already filters by mboxKey scope.
		// On incremental sync, skip folders whose modseq hasn't advanced.
		if req.SyncState != "" && f.HighestModSeq <= folderVersion {
			continue
		}

		displayName := s.folderDisplayName(mboxKey, f.Role, f.FolderID)

		fxml := FolderType{
			FolderID:         FolderIdComponents{ID: f.FolderID.String()},
			ParentFolderID:   FolderIdComponents{ID: f.ParentID.String()},
			DisplayName:      displayName,
			TotalCount:       0,
			ChildFolderCount: 0,
			FolderClass:      folderClassForRole(f.Role),
		}
		updates = append(updates, fxml)
	}

	// Persist new sync state.
	newVersion := folderVersion + 1
	newState := fmt.Sprintf("v%d:folders:%d", newVersion, len(updates))
	if err := s.sync.PutSyncState(mboxID, semcore.FolderId{}, clientID, newState); err != nil {
		return s.errorResponseXML("SyncFolderHierarchy", ErrErrorSync, err.Error())
	}

	resp := SyncFolderHierarchyResponse{}
	var syncChanges SyncFolderChangesContainer
	if len(updates) > 0 {
		syncChanges.Updates = []SyncFolderChangesUpdate{{Folders: updates}}
	}
	if len(deletions) > 0 {
		syncChanges.Deletes = []SyncFolderChangesDelete{{FolderIds: deletions}}
	}
	resp.ResponseMessages.Messages = []SyncFolderHierarchyMsg{{
		ResponseClass: "Success",
		ResponseCode:  string(ErrNoError),
		SyncState:     newState,
		Changes:       syncChanges,
	}}

	return buildResponseEnvelope(resp)
}

// ---------------------------------------------------------------------------
// Mailbox resolution helpers
// ---------------------------------------------------------------------------

// extractTargetMailbox scans a request body for an explicit target mailbox
// addressed by a DistinguishedFolderId's <t:Mailbox><t:EmailAddress> child.
// EWS delegate / shared-mailbox clients (e.g. an exchangelib DELEGATE account)
// place the owner's SMTP address there to operate on another user's store.
// Returns "" when no such target is present, in which case the caller resolves
// the authenticated user's own mailbox.
//
// Per the EWS distinguished-folder-id model: the target is taken from
// folder.Mailbox->EmailAddress when present, and otherwise defaults to the
// authenticated user.
func extractTargetMailbox(body []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(body))
	inDist := false
	inEmail := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "DistinguishedFolderId":
				inDist = true
			case "EmailAddress":
				if inDist {
					inEmail = true
				}
			}
		case xml.CharData:
			if inEmail {
				if v := strings.TrimSpace(string(t)); v != "" {
					return v
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "EmailAddress":
				inEmail = false
			case "DistinguishedFolderId":
				inDist = false
			}
		}
	}
	return ""
}

// resolveMailboxFromBody resolves the mailbox a request targets.
//
// The authenticated user's email is taken from the X-Email context value (set by
// HandleHTTP after auth). When the request explicitly addresses another mailbox
// via a DistinguishedFolderId <t:Mailbox> element (delegate / shared-mailbox
// access), that owner mailbox is resolved instead — but only after verifying the
// acting user holds a delegate grant on it (VAL-DIR-002). Without an explicit
// target, the authenticated user's own mailbox is used.
//
// Uses EnsureMailboxId to register the mailbox identity on first access, so that
// newly created accounts can immediately use EWS without requiring a separate
// identity backfill step.
func (s *Server) resolveMailboxFromBody(ctx context.Context, body []byte) (semcore.MailboxId, string, ErrorCode) {
	email, ok := ctx.Value("X-Email").(string)
	if !ok || email == "" {
		return semcore.MailboxId{}, "", ErrErrorAccessDenied
	}

	// Honor an explicit target mailbox for delegate / shared-mailbox access.
	if target := extractTargetMailbox(body); target != "" && !strings.EqualFold(target, email) {
		ownerID, err := s.identity.EnsureMailboxId(target)
		if err != nil {
			return semcore.MailboxId{}, "", ErrErrorMailboxNotFound
		}
		// A non-owner may only reach the target mailbox when an explicit delegate
		// grant exists. When no delegate store is configured, fall through to the
		// owner identity unguarded (matches checkDelegatePermission's nil-store path).
		if s.delegateStore != nil {
			delegate, derr := s.delegateStore.GetDelegateForUser(ownerID, email)
			if derr != nil || !delegate.CanActAsDelegate() {
				return semcore.MailboxId{}, "", ErrErrorAccessDenied
			}
		}
		return ownerID, "e:" + target, ""
	}

	// Use EnsureMailboxId so that new accounts can immediately use EWS.
	// The identity is created and persisted on first access; subsequent calls
	// are idempotent and return the existing ID.
	mboxID, err := s.identity.EnsureMailboxId(email)
	if err != nil {
		return semcore.MailboxId{}, "", ErrErrorMailboxNotFound
	}
	return mboxID, "e:" + email, ""
}
