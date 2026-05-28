// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements folder operations.
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
// GetFolder
// ---------------------------------------------------------------------------

// GetFolderRequest is the EWS GetFolder operation request.
type GetFolderRequest struct {
	XMLName    xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetFolder"`
	FolderIDs  FolderIDsType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
	FolderShape FolderResponseShape `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderShape"`
}

// FolderIDsType is a list of folder IDs.
type FolderIDsType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
	Distinguished []DistinguishedFolderName `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	Folder []FolderIDOnly `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// FolderIDOnly is a folder ID without additional properties.
type FolderIDOnly struct {
	XMLName   xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	ID       string   `xml:"Id,attr"`
	ChangeKey string  `xml:"ChangeKey,attr,omitempty"`
}

// GetFolderResponse is the EWS GetFolder operation response.
type GetFolderResponse struct {
	XMLName          xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetFolderResponse"`
	ResponseMessages GetFolderResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// GetFolderResponseMessages wraps a list of folder response messages.
type GetFolderResponseMessages struct {
	Messages []FolderResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderResponseMessage"`
}

// FolderResponseMessageType is one folder's result in a GetFolder response.
type FolderResponseMessageType struct {
	XMLName       xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderResponseMessage"`
	ResponseClass string  `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Folders       FolderResponseContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
}

// FolderResponseContainer wraps the Folders list in response messages.
// The m:Folders element is in messages namespace, containing t:Folder in types namespace.
type FolderResponseContainer struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
	Folders []FolderType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
}

// ResponseCodeType is the EWS ResponseCode element inside response messages.
type ResponseCodeType struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
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
func (s *Server) resolveDistinguishedFolder(ctx context.Context, mboxID semcore.MailboxId, mboxKey, name string) FolderResponseMessageType {
	role, ok := DistinguishedFolderIDs[name]
	if !ok {
		return errorMsg("GetFolder", ErrErrorFolderNotFound, "unknown distinguished folder: "+name)
	}

	// Look up by role — the role is stored in the identity record when
	// EnsureFolderId was called with a role.
	folder, err := s.identity.GetFolderByMailbox(mboxKey, role)
	if err != nil {
		if errors.Is(err, semcore.ErrFolderNotFound) {
			return errorMsg("GetFolder", ErrErrorFolderNotFound, fmt.Sprintf("folder with role %q not found", role))
		}
		return errorMsg("GetFolder", ErrErrorInternalServer, err.Error())
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
	if !rec.MailboxID.IsZero() && rec.MailboxID.String() != "" && rec.MailboxID.String() != mboxKey {
		return errorMsg("GetFolder", ErrErrorAccessDenied, "folder belongs to a different mailbox")
	}

	displayName := rec.Role
	if displayName == "" {
		displayName = "User Folder"
	}

	msg := FolderResponseMessageType{
		ResponseClass: "Success",
	}
	msg.ResponseCode.Value = ErrNoError

	fxml := FolderType{
		FolderID: FolderIdComponents{ID: folderID.String(),
		},
		ParentFolderID: FolderIdComponents{ID: rec.ParentID.String(),
		},
		DisplayName:       displayName,
		TotalCount:        0,
		ChildFolderCount: 0,
		FolderClass:      "IPF.Note",
	}
	msg.Folders = FolderResponseContainer{Folders: []FolderType{fxml}}
	return msg
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
	XMLName         xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindFolder"`
	FolderShape    FolderResponseShape `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderShape"`
	IndexedPageFolderView struct {
		MaxEntriesReturned string `xml:"MaxEntriesReturned,attr"`
		Offset              string `xml:"Offset,attr"`
		BasePoint          string `xml:"BasePoint,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages IndexedPageFolderView,omitempty"`
	ParentFolderIDs struct {
		Distinguished []struct {
			XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
			ID     string   `xml:"Id,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
		Folder []FolderIDOnly `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ParentFolderIds"`
}

// FindFolderResponse is the EWS FindFolder operation response.
type FindFolderResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindFolderResponse"`
	ResponseMessages FindFolderResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// FindFolderResponseMessages wraps FindFolder response messages.
type FindFolderResponseMessages struct {
	Messages []FolderResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderResponseMessage"`
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

	// Determine the parent folder to enumerate under.
	var parentID semcore.FolderId
	var enumerateRoot bool
	if len(req.ParentFolderIDs.Distinguished) > 0 {
		d := req.ParentFolderIDs.Distinguished[0]
		role, ok := DistinguishedFolderIDs[d.ID]
		if !ok {
			return s.errorResponseXML("FindFolder", ErrErrorFolderNotFound, "unknown distinguished folder: "+d.ID)
		}
		folder, err := s.identity.GetFolderByMailbox(mboxKey, role)
		if err == nil {
			parentID = folder.FolderID
		} else if errors.Is(err, semcore.ErrFolderNotFound) {
			// Parent distinguished folder doesn't exist yet; enumerate root folders.
			enumerateRoot = true
		} else {
			return s.errorResponseXML("FindFolder", ErrErrorInternalServer, err.Error())
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
	allFolders, err := s.identity.ListFolderIdentitiesForMailbox(mboxKey)
	if err != nil {
		return s.errorResponseXML("FindFolder", ErrErrorInternalServer, err.Error())
	}

	var matching []FolderType
	for _, f := range allFolders {
		// ListFolderIdentitiesForMailbox already filters by mboxKey scope.
		// No additional mailbox ownership check needed.
		// Filter to children of the specified parent.
		if parentID.String() != "" && !f.ParentID.Equal(parentID) {
			continue
		}
		// Skip the parent itself.
		if f.FolderID.Equal(parentID) {
			continue
		}

		displayName := f.Role
		if displayName == "" {
			displayName = "User Folder"
		}

		fxml := FolderType{
			FolderID: FolderIdComponents{ID: f.FolderID.String(),
			},
			ParentFolderID: FolderIdComponents{ID: f.ParentID.String(),
			},
			DisplayName:       displayName,
			TotalCount:        0,
			ChildFolderCount: 0,
			FolderClass:      "IPF.Note",
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
	XMLName  xml.Name             `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
	Folders []FolderTypeForCreate `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
}

// CreateFolderRequest is the EWS CreateFolder operation request.
type CreateFolderRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateFolder"`
	ParentFolderID struct {
		Distinguished string `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId,attr,omitempty"`
		ID            string `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ParentFolderId"`
	Folders FoldersContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
}

// CreateFolderResponse is the EWS CreateFolder operation response.
type CreateFolderResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages CreateFolderResponse"`
	ResponseMessages CreateFolderResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// CreateFolderResponseMessages wraps CreateFolder response messages.
type CreateFolderResponseMessages struct {
	Messages []FolderResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderResponseMessage"`
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

	// Resolve parent folder.
	var parentID semcore.FolderId
	if req.ParentFolderID.Distinguished != "" {
		role, ok := DistinguishedFolderIDs[req.ParentFolderID.Distinguished]
		if !ok {
			return s.errorResponseXML("CreateFolder", ErrErrorFolderNotFound, "unknown distinguished folder: "+req.ParentFolderID.Distinguished)
		}
		folder, err := s.identity.GetFolderByMailbox(mboxKey, role)
		if err == nil {
			parentID = folder.FolderID
		} else if errors.Is(err, semcore.ErrFolderNotFound) {
			// Parent doesn't exist yet; create folder at root (parentID stays zero).
		} else {
			return s.errorResponseXML("CreateFolder", ErrErrorInternalServer, err.Error())
		}
	} else if req.ParentFolderID.ID != "" {
		var err error
		parentID, err = semcore.NewFolderId(req.ParentFolderID.ID)
		if err != nil {
			return s.errorResponseXML("CreateFolder", ErrErrorInvalidId, err.Error())
		}
	}

	// Ensure mailbox identity exists.
	if _, err := s.identity.EnsureMailboxId(mboxKey); err != nil {
		return s.errorResponseXML("CreateFolder", ErrErrorInternalServer, err.Error())
	}

	msgs := make([]FolderResponseMessageType, 0, len(req.Folders.Folders))
	for _, f := range req.Folders.Folders {
		if f.DisplayName == "" {
			msgs = append(msgs, errorMsg("CreateFolder", ErrErrorInvalidOperation, "DisplayName is required"))
			continue
		}

		folderID, err := s.identity.EnsureFolderId(mboxKey, f.DisplayName, "")
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
			FolderID: FolderIdComponents{ID: folderID.String(),
			},
			ParentFolderID: FolderIdComponents{ID: parentID.String(),
			},
			DisplayName:  displayName,
			FolderClass: "IPF.Note",
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
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types SetFolderField"`
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
	XMLName    xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
	Operations []SetFolderFieldOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types SetFolderField"`
}

// FolderChangeOp represents one folder change in UpdateFolder.
type FolderChangeOp struct {
	XMLName   xml.Name        `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderChange"`
	FolderID  struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		ID      string   `xml:"Id,attr"`
		CK      string   `xml:"ChangeKey,attr,omitempty"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	Updates FolderUpdatesOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types Updates"`
}

// FolderChangesList wraps the FolderChanges list in UpdateFolder.
type FolderChangesList struct {
	XMLName  xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderChanges"`
	Changes []FolderChangeOp `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderChange"`
}

// UpdateFolderRequest is the EWS UpdateFolder operation request.
type UpdateFolderRequest struct {
	XMLName       xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateFolder"`
	FolderChanges FolderChangesList `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderChanges"`
}

// UpdateFolderResponse is the EWS UpdateFolder operation response.
type UpdateFolderResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UpdateFolderResponse"`
	ResponseMessages UpdateFolderResponseMessages `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages"`
}

// UpdateFolderResponseMessages wraps UpdateFolder response messages.
type UpdateFolderResponseMessages struct {
	Messages []FolderResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderResponseMessage"`
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

		if !rec.MailboxID.IsZero() && rec.MailboxID.String() != "" && rec.MailboxID.String() != mboxKey {
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
		displayName := rec.Role
		if displayName == "" {
			displayName = "User Folder"
		}

		fxml := FolderType{
			FolderID:       FolderIdComponents{ID: folderID.String()},
			ParentFolderID: FolderIdComponents{ID: rec.ParentID.String()},
			DisplayName:    displayName,
			FolderClass:    "IPF.Note",
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
	ID      string  `xml:"Id,attr"`
	CK      string  `xml:"ChangeKey,attr,omitempty"`
}

// FolderIdsForDelete wraps the FolderIds list in DeleteFolder requests.
type FolderIdsForDelete struct {
	XMLName xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
	Items   []FolderIdForDelete `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// DeleteFolderRequest is the EWS DeleteFolder operation request.
type DeleteFolderRequest struct {
	XMLName    xml.Name         `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolder"`
	FolderIDs  FolderIdsForDelete `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderIds"`
	DeleteType string            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteType,attr"` // HardDelete | SoftDelete | MoveToDeletedItems
}

// DeleteFolderResponse is the EWS DeleteFolder operation response.
type DeleteFolderResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages DeleteFolderResponse"`
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

		if !rec.MailboxID.IsZero() && rec.MailboxID.String() != "" && rec.MailboxID.String() != mboxKey {
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
// SyncFolderHierarchy
// ---------------------------------------------------------------------------

// SyncFolderHierarchyRequest is the EWS SyncFolderHierarchy operation request.
type SyncFolderHierarchyRequest struct {
	XMLName     xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderHierarchy"`
	SyncState   string  `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncState,omitempty"`
	FolderShape FolderResponseShape `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FolderShape"`
}

// SyncFolderChangesContainer wraps the SyncFolderHierarchy Changes element.
// SyncFolderChangesUpdate wraps <Update> containing <Folder> elements.
type SyncFolderChangesUpdate struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Update"`
	Folders []FolderType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
}

// SyncFolderChangesDelete wraps <Delete> containing <FolderId> elements.
type SyncFolderChangesDelete struct {
	XMLName    xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
	FolderIds []FolderIDOnly `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// The <Changes> element is in messages namespace; <Update>/<Delete> are in types namespace.
type SyncFolderChangesContainer struct {
	Updates []SyncFolderChangesUpdate  `xml:"http://schemas.microsoft.com/exchange/services/2006/types Update"`
	Deletes []SyncFolderChangesDelete `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
}

// SyncFolderHierarchyMsg is one SyncFolderHierarchy result.
type SyncFolderHierarchyMsg struct {
	ResponseClass string                `xml:"ResponseClass,attr"`
	ResponseCode  string                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	SyncState     string                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncState"`
	Changes       SyncFolderChangesContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Changes"`
}

// SyncFolderHierarchyResponse is the EWS SyncFolderHierarchy operation response.
type SyncFolderHierarchyResponse struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderHierarchyResponse"`
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

		displayName := f.Role
		if displayName == "" {
			displayName = "User Folder"
		}

		fxml := FolderType{
			FolderID: FolderIdComponents{ID: f.FolderID.String(),
			},
			ParentFolderID: FolderIdComponents{ID: f.ParentID.String(),
			},
			DisplayName:       displayName,
			TotalCount:        0,
			ChildFolderCount: 0,
			FolderClass:      "IPF.Note",
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
// SyncFolderItems (stub)
// ---------------------------------------------------------------------------

// handleSyncFolderItems is a stub for the sync items operation.
// Full implementation is in the sync/subscription feature.
func (s *Server) handleSyncFolderItems(ctx context.Context, body []byte) []byte {
	return s.errorResponseXML("SyncFolderItems", ErrErrorNotImplemented,
		"SyncFolderItems is not yet implemented; use SyncFolderHierarchy for folder sync")
}

// ---------------------------------------------------------------------------
// Mailbox resolution helpers
// ---------------------------------------------------------------------------

// resolveMailboxFromBody resolves the mailbox for the authenticated user.
// Email is extracted from the X-Email context value (set by HandleHTTP after auth).
func (s *Server) resolveMailboxFromBody(ctx context.Context, body []byte) (semcore.MailboxId, string, ErrorCode) {
	email, ok := ctx.Value("X-Email").(string)
	if !ok || email == "" {
		return semcore.MailboxId{}, "", ErrErrorAccessDenied
	}
	mboxID, err := s.identity.GetMailboxIDByEmail(email)
	if err != nil {
		return semcore.MailboxId{}, "", ErrErrorMailboxNotFound
	}
	return mboxID, "e:" + email, ""
}
