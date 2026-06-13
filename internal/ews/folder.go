// Package ews implements the Exchange Web Services (EWS) SOAP interface for
// uMailServer. This file implements folder operations.
package ews

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// folderDisplayName resolves a folder's human-readable name for EWS responses.
// A distinguished folder reports its canonical name; a user-created folder
// reports the name it was created with (recovered from the identity store, which
// keys folders by name). The generic "User Folder" is only a last resort when
// neither a role nor a stored name is available, so custom folders no longer all
// surface as "User Folder".
func (s *Server) folderDisplayName(mailboxKey, role string, id semcore.FolderId) string {
	if name := exchangeFolderName(role); name != "" {
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
	wantPerms := folderShapeWantsPermissions(req.FolderShape)

	// Process distinguished folder IDs.
	for _, d := range req.FolderIDs.Distinguished {
		msg := s.resolveDistinguishedFolder(ctx, mboxID, mboxKey, d.ID, wantPerms)
		msgs = append(msgs, msg)
	}

	// Process explicit folder IDs.
	for _, f := range req.FolderIDs.Folder {
		msg := s.getFolderByID(ctx, mboxID, mboxKey, f.ID, f.ChangeKey, wantPerms)
		msgs = append(msgs, msg)
	}

	resp := GetFolderResponse{}
	resp.ResponseMessages.Messages = msgs
	return buildResponseEnvelope(resp)
}

// resolveDistinguishedFolder resolves a distinguished folder by its name.
// It looks up the folder by its role rather than by folder name.
// For new accounts, it auto-creates the distinguished folder identity.
func (s *Server) resolveDistinguishedFolder(ctx context.Context, mboxID semcore.MailboxId, mboxKey, name string, wantPerms bool) FolderResponseMessageType {
	role, ok := DistinguishedFolderIDs[name]
	if !ok {
		return errorMsg("GetFolder", ErrErrorFolderNotFound, "unknown distinguished folder: "+name)
	}

	// publicfoldersroot resolves to the per-domain public tree, not the caller's
	// own mailbox, so it is answered with a synthetic root.
	if role == publicFoldersRole {
		return s.resolvePublicFoldersRoot(mboxKey)
	}

	// Strip "e:" prefix to match the key format used by all other handlers.
	// resolveMailboxFromBody returns mboxKey = "e:" + email, but folder/identity
	// operations use the raw email as the mailbox key.
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")

	// For the container folders (store root, IPM subtree), bridge the caller's
	// canonical storage folders into the identity store so the response's
	// ChildFolderCount reflects the real children (Outlook keys provisioning off
	// the IPM subtree's child count).
	if isContainerRole(role) {
		s.reconcileMailboxFolderIdentities(mailboxKey)
	}

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

	return s.buildFolderResponse(ctx, mboxID, mboxKey, folder.FolderID, wantPerms)
}

// getFolderByID resolves an explicit folder by its ID.
func (s *Server) getFolderByID(ctx context.Context, mboxID semcore.MailboxId, mboxKey, folderIDStr, changeKey string, wantPerms bool) FolderResponseMessageType {
	folderID, err := semcore.NewFolderId(folderIDStr)
	if err != nil {
		return errorMsg("GetFolder", ErrErrorInvalidId, err.Error())
	}

	return s.buildFolderResponse(ctx, mboxID, mboxKey, folderID, wantPerms)
}

// buildFolderResponse builds a FolderResponseMessageType for a resolved folder ID.
// mboxKey is needed for ownership check because stored MailboxId may be the key string.
func (s *Server) buildFolderResponse(ctx context.Context, mboxID semcore.MailboxId, mboxKey string, folderID semcore.FolderId, wantPerms bool) FolderResponseMessageType {
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

	// Project the flat store onto the Exchange hierarchy for the ParentFolderId.
	// The store root reports itself as parent — a self-reference signals "no
	// parent" to EWS clients (exchangelib treats parent==self as None) without an
	// empty, unresolvable ParentFolderId that strict clients reject.
	rootID := s.rootFolderID(checkKey)
	ipmID := s.ipmSubtreeFolderID(checkKey)
	parentRef := s.effectiveParentID(rec.Role, rec.ParentID, rootID, ipmID)
	if parentRef.IsZero() {
		parentRef = folderID
	}
	child, total, unread := s.folderCounts(checkKey, folderID, rec.Role, rootID, ipmID)
	fxml := FolderType{
		FolderID:         newFolderID(folderID.String(), child, total, unread),
		ParentFolderID:   FolderIdComponents{ID: parentRef.String()},
		DisplayName:      displayName,
		TotalCount:       total,
		UnreadCount:      unread,
		ChildFolderCount: child,
		FolderClass:      folderClassForRole(rec.Role),
	}
	// Project the canonical RFC 4314 ACL onto the folder's permission set and the
	// caller's effective rights. folderName == displayName for distinguished and
	// top-level folders (the same key IMAP/storage use for the ACL).
	s.decorateFolderPermissions(ctx, &fxml, checkKey, displayName, wantPerms)
	msg.Folders = FolderResponseContainer{Folders: []FolderType{fxml}}
	return msg
}

// exchangeFolderName maps a distinguished folder role to the Exchange display
// name Outlook expects (e.g. "Sent Items", "Deleted Items", "Top of Information
// Store"). This is deliberately distinct from semcore.CanonicalFolderNameForRole,
// which returns the IMAP-canonical names ("INBOX", "Sent", "Trash") that drive
// IMAP change notifications — rewriting that would corrupt IMAP folder names.
// Returns "" for a role with no Exchange distinguished name (user folders).
func exchangeFolderName(role string) string {
	switch role {
	case "inbox":
		return "Inbox"
	case "sent":
		return "Sent Items"
	case "drafts":
		return "Drafts"
	case "trash":
		return "Deleted Items"
	case "junk":
		return "Junk Email"
	case "outbox":
		return "Outbox"
	case "archive":
		return "Archive"
	case "calendar":
		return "Calendar"
	case "contacts":
		return "Contacts"
	case "tasks":
		return "Tasks"
	case "notes":
		return "Notes"
	case "scheduled":
		return "Scheduled"
	case "recoverableitems":
		return "Recoverable Items"
	case "ipmsubtree":
		return "Top of Information Store"
	default:
		return ""
	}
}

// folderClassForRole returns the MAPI container class (PR_CONTAINER_CLASS) for a
// distinguished folder role. Calendar/contacts/tasks/notes carry their typed
// classes so Outlook renders the right item view; every other folder is IPF.Note.
func folderClassForRole(role string) string {
	switch role {
	case "calendar":
		return "IPF.Appointment"
	case "contacts":
		return "IPF.Contact"
	case "tasks":
		return "IPF.Task"
	case "notes":
		return "IPF.StickyNote"
	default:
		return "IPF.Note"
	}
}

// newFolderID builds the EWS FolderId for a folder, pairing its stable id with a
// ChangeKey. Real Exchange advances a folder's PR_CHANGE_KEY whenever the
// folder's state moves; we mirror that contract by hashing the id together with
// the folder's child/total/unread counts, so the key stays constant while the
// folder is unchanged and advances the moment its contents move. Outlook's typed
// deserializer base64-decodes the ChangeKey, so the value is base64; emitting it
// (instead of a bare Id) also lets Outlook dedupe unchanged folders across the
// repeated SyncFolderHierarchy updates rather than reprocessing every folder.
func newFolderID(id string, child, total, unread int) FolderIdComponents {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%d|%d", id, child, total, unread))
	return FolderIdComponents{
		ID: id,
		CK: base64.StdEncoding.EncodeToString(sum[:16]),
	}
}

// publicFoldersRole is the internal role the publicfoldersroot distinguished id
// maps to. Unlike other roles it names no per-mailbox folder; it selects the
// per-domain public-folder owner whose tree is browsed in place of the caller's
// own mailbox.
const publicFoldersRole = "publicfolders"

// publicOwnerForCaller derives the reserved per-domain public-folder owner from
// the caller's mailbox key ("e:"+email or a raw email). It returns "" when the
// domain cannot be determined, which keeps each domain's public tree isolated.
func publicOwnerForCaller(mboxKey string) string {
	email := strings.TrimPrefix(mboxKey, "e:")
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return ""
	}
	return storage.PublicFolderOwner(email[at+1:])
}

// publicFoldersReady reports whether the public-folder feature is wired and
// enabled, returning the resolved public owner for the caller's domain.
func (s *Server) publicFoldersReady(mboxKey string) (owner string, ok bool) {
	if s.publicFoldersEnabled == nil || !s.publicFoldersEnabled() || s.publicFolderACL == nil {
		return "", false
	}
	owner = publicOwnerForCaller(mboxKey)
	return owner, owner != ""
}

// reconcilePublicFolderIdentities ensures every admin-created public folder under
// owner has a semcore folder identity. Public folders are created in the
// canonical storage layer (the same source IMAP and webmail read), but EWS folder
// enumeration is identity-store driven; this bridges the two so the public tree is
// visible over EWS too. Standard auto-provisioned mailboxes (INBOX, Sent, ...) are
// skipped — they are not public folders. Best-effort: a folder that fails to
// reconcile is simply not yet visible over EWS.
func (s *Server) reconcilePublicFolderIdentities(owner string) {
	if s.storageDB == nil {
		return
	}
	names, err := s.storageDB.ListMailboxes(owner)
	if err != nil {
		return
	}
	for _, name := range names {
		if slices.Contains(storage.DefaultMailboxes, name) {
			continue
		}
		if _, err := s.identity.EnsureFolderId(owner, name, ""); err != nil {
			continue
		}
	}
}

// reconcileMailboxFolderIdentities ensures every folder in the caller's own
// canonical storage (the same source IMAP/JMAP/webmail read) has a semcore
// folder identity, so EWS folder enumeration — which is identity-store driven —
// reflects the real mailbox instead of an empty tree. Unlike
// reconcilePublicFolderIdentities it INCLUDES the standard folders (INBOX,
// Sent, ...) and assigns each its distinguished role, so they resolve as the
// expected distinguished folders. Best-effort and idempotent: EnsureFolderId
// fast-paths folders that already have an identity.
func (s *Server) reconcileMailboxFolderIdentities(mailboxKey string) {
	// Establish the container skeleton (store root + IPM subtree, the latter
	// parented under the former) so the hierarchy resolves even before any user
	// folder is touched. Idempotent.
	s.ipmSubtreeFolderID(mailboxKey)
	if s.storageDB == nil {
		return
	}
	names, err := s.storageDB.ListMailboxes(mailboxKey)
	if err != nil {
		return
	}
	for _, name := range names {
		role := semcore.RoleForCanonicalFolderName(name)
		if _, err := s.identity.EnsureFolderId(mailboxKey, name, role); err != nil {
			continue
		}
	}
}

// isContainerRole reports whether a role is one of the synthetic container
// folders that anchor the mailbox hierarchy and hold no messages of their own:
// the store root ("root") and the IPM subtree ("ipmsubtree", a.k.a.
// msgfolderroot, the parent of Inbox/Sent/etc.). Real Exchange exposes these as
// two distinct folders — the IPM subtree one level below the store root — so
// EWS clients can resolve the store root independently of the user folders.
func isContainerRole(role string) bool {
	return role == "root" || role == "ipmsubtree"
}

// rootFolderID returns the mailbox store root's identity-store folder id ("root"
// role), materializing it if absent. Best-effort: returns the zero id if neither
// lookup nor create succeeds.
func (s *Server) rootFolderID(mailboxKey string) semcore.FolderId {
	if rec, err := s.identity.GetFolderByMailbox(mailboxKey, "root"); err == nil {
		return rec.FolderID
	}
	if fid, err := s.identity.EnsureFolderId(mailboxKey, "root", "root"); err == nil {
		return fid
	}
	return semcore.FolderId{}
}

// ipmSubtreeFolderID returns the IPM subtree's identity-store folder id
// ("ipmsubtree" role, the EWS msgfolderroot), materializing it under the store
// root if absent. The IPM subtree is the parent of all user-visible folders.
// Best-effort: returns the zero id if it cannot be resolved or created.
func (s *Server) ipmSubtreeFolderID(mailboxKey string) semcore.FolderId {
	if rec, err := s.identity.GetFolderByMailbox(mailboxKey, "ipmsubtree"); err == nil && !rec.ParentID.IsZero() {
		return rec.FolderID
	}
	fid, err := s.identity.EnsureFolderId(mailboxKey, "msgfolderroot", "ipmsubtree")
	if err != nil {
		return semcore.FolderId{}
	}
	// Parent the IPM subtree under the store root. Best-effort: a failure leaves
	// the subtree id valid and is retried on the next (idempotent) reconcile.
	if rootID := s.rootFolderID(mailboxKey); !rootID.IsZero() {
		if err := s.identity.SetFolderParent(fid, rootID); err != nil {
			return fid
		}
	}
	return fid
}

// effectiveParentID projects the flat identity store onto the Exchange folder
// hierarchy and returns the parent a folder should report. The store root has no
// parent (zero); the IPM subtree hangs off the store root; every other top-level
// folder (stored with a zero parent) hangs off the IPM subtree; folders with a
// concrete stored parent (nested folders) keep it. rootID/ipmID are resolved once
// per request and passed in to avoid a store lookup per folder.
func (s *Server) effectiveParentID(role string, parentID, rootID, ipmID semcore.FolderId) semcore.FolderId {
	if role == "root" {
		return semcore.FolderId{}
	}
	if !parentID.IsZero() {
		return parentID
	}
	if role == "ipmsubtree" {
		return rootID
	}
	return ipmID
}

// isFolderUnder reports whether folder f is a (transitive) descendant of the
// ancestor folder under the projected hierarchy, used for Deep FindFolder
// traversal. It walks f's effective-parent chain until it reaches the ancestor
// or the store root. byID resolves each parent id to its record; a depth guard
// bounds a malformed cyclic chain.
func (s *Server) isFolderUnder(f semcore.StoredFolderIdentity, ancestor, rootID, ipmID semcore.FolderId, byID map[string]semcore.StoredFolderIdentity) bool {
	cur := f
	for i := 0; i < 64; i++ {
		ep := s.effectiveParentID(cur.Role, cur.ParentID, rootID, ipmID)
		if ep.IsZero() {
			return false
		}
		if ep.Equal(ancestor) {
			return true
		}
		next, ok := byID[ep.String()]
		if !ok {
			return false
		}
		cur = next
	}
	return false
}

// folderCounts computes the ChildFolderCount/TotalCount/UnreadCount a folder
// should report. ChildFolderCount is the top-level folder count for the mailbox
// root (whose children are stored with a zero parent) and the direct-child count
// otherwise. TotalCount/UnreadCount count only identities whose message body is
// present in msgStore — the exact readable set FindItem/SyncFolderItems surface —
// so GetFolder/FindFolder report the same count an item query returns. This
// same-surface consistency is required: a client that reads TotalCount=N then
// fetches items must see them agree. Counting
// the storage mailbox by display name instead was doubly wrong — it missed the
// bucket entirely (the display name "Inbox" never matches the canonical "INBOX"
// storage name, so the count was always zero) and it would have reported storage's
// pre-dedup message count, diverging from item enumeration. The synthetic
// container folders (root, IPM subtree) hold no items and report zero.
func (s *Server) folderCounts(mailboxKey string, folderID semcore.FolderId, role string, rootID, ipmID semcore.FolderId) (child, total, unread int) {
	if folders, err := s.identity.ListFolderIdentitiesForMailbox(mailboxKey); err == nil {
		for _, f := range folders {
			if f.FolderID.Equal(folderID) {
				continue
			}
			// Hidden dumpster folders are not enumerated, so they must not be
			// counted as children either, or ChildFolderCount overstates what the
			// client can actually see.
			if semcore.IsClientHiddenFolderRole(f.Role) {
				continue
			}
			if s.effectiveParentID(f.Role, f.ParentID, rootID, ipmID).Equal(folderID) {
				child++
			}
		}
	}
	if !isContainerRole(role) {
		if items, err := s.identity.ListItemIdentitiesByFolder(folderID); err == nil {
			for _, it := range items {
				// A cheap stat mirrors the ReadMessage filter collectMailItems
				// applies, so an orphaned identity whose blob is gone (identity
				// store drifted ahead of msgStore) is counted by neither surface.
				if !s.msgStore.MessageExists(it.Email, it.MsgKey) {
					continue
				}
				total++
				if !it.IsRead {
					unread++
				}
			}
		}
	}
	return child, total, unread
}

// callerCanReadPublicFolder reports whether the caller holds at least read rights
// on a public folder, via the union of its per-user and "anyone" grants.
func (s *Server) callerCanReadPublicFolder(callerEmail, owner, folder string) bool {
	rights, err := storage.ResolveEffectiveRights(s.publicFolderACL, callerEmail, owner, folder)
	if err != nil {
		return false
	}
	return rights&storage.ACLRead == storage.ACLRead
}

// resolvePublicFoldersRoot answers GetFolder(publicfoldersroot) with a synthetic
// root for the caller's per-domain public tree. ChildFolderCount counts only the
// folders the caller may read, so an empty or forbidden tree still resolves but
// shows nothing to browse into.
func (s *Server) resolvePublicFoldersRoot(mboxKey string) FolderResponseMessageType {
	owner, ok := s.publicFoldersReady(mboxKey)
	if !ok {
		return errorMsg("GetFolder", ErrErrorFolderNotFound, "public folders are not enabled")
	}
	if _, err := s.identity.EnsureMailboxId(owner); err != nil {
		return errorMsg("GetFolder", ErrErrorInternalServer, err.Error())
	}
	s.reconcilePublicFolderIdentities(owner)
	callerEmail := strings.TrimPrefix(mboxKey, "e:")
	folders, err := s.identity.ListFolderIdentitiesForMailbox(owner)
	if err != nil {
		return errorMsg("GetFolder", ErrErrorInternalServer, err.Error())
	}
	visible := 0
	for _, f := range folders {
		if f.Role == "root" {
			continue
		}
		if s.callerCanReadPublicFolder(callerEmail, owner, s.folderDisplayName(owner, f.Role, f.FolderID)) {
			visible++
		}
	}
	msg := FolderResponseMessageType{ResponseClass: "Success"}
	msg.ResponseCode.Value = ErrNoError
	msg.Folders = FolderResponseContainer{Folders: []FolderType{{
		FolderID:         newFolderID("publicfoldersroot", 0, 0, 0),
		DisplayName:      "Public Folders",
		FolderClass:      "IPF.Note",
		ChildFolderCount: visible,
	}}}
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
	XMLName               xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindFolder"`
	Traversal             string              `xml:"Traversal,attr"`
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
	Messages []FindFolderResponseMessageType `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindFolderResponseMessage"`
}

// FindFolderResponseMessageType is a single FindFolder response message. Unlike
// GetFolder (which lists folders directly under the message), FindFolder wraps
// the matched folders in a <RootFolder> element carrying result-set paging
// metadata. Outlook and other EWS clients require this wrapper to read the
// folder list — without it they see an empty mailbox.
type FindFolderResponseMessageType struct {
	ResponseClass string               `xml:"ResponseClass,attr"`
	ResponseCode  ResponseCodeType     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	RootFolder    FindFolderRootFolder `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RootFolder"`
}

// FindFolderRootFolder is the <RootFolder> wrapper in a FindFolder response: the
// result-set paging attributes plus the matched folders. (Distinct from FindItem's
// RootFolderType, which carries items rather than folders.)
type FindFolderRootFolder struct {
	XMLName                 xml.Name            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RootFolder"`
	TotalItemsInView        int                 `xml:"TotalItemsInView,attr"`
	IncludesLastItemInRange bool                `xml:"IncludesLastItemInRange,attr"`
	Folders                 FindFolderContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folders"`
}

// FindFolderContainer is the <t:Folders> collection inside a FindFolder
// RootFolder. The EWS schema places this collection in the TYPES namespace —
// confirmed by the exchangelib FindFolder service, whose element_container_name
// is {types}Folders — unlike GetFolder's {messages}Folders (FolderResponseContainer).
// Emitting it in the messages namespace makes strict clients (exchangelib,
// Outlook) see no folders and treat the mailbox as empty.
type FindFolderContainer struct {
	XMLName xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folders"`
	Folders []FolderType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
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
	wantPerms := folderShapeWantsPermissions(req.FolderShape)

	// Resolve the container ids once; they drive the effective-parent projection
	// of the flat store onto the Exchange hierarchy.
	rootID := s.rootFolderID(mailboxKey)
	ipmID := s.ipmSubtreeFolderID(mailboxKey)
	deep := strings.EqualFold(req.Traversal, "Deep")

	// Determine the parent folder to enumerate under.
	var parentID semcore.FolderId
	// publicTree enumerates the per-domain public-folder owner's tree instead of
	// the caller's own mailbox; callerEmail keeps the original identity for the
	// per-folder ACL filter after mailboxKey is rebound to the public owner.
	var publicTree bool
	var callerEmail string
	if len(req.ParentFolderIDs.Distinguished) > 0 {
		d := req.ParentFolderIDs.Distinguished[0]
		role, ok := DistinguishedFolderIDs[d.ID]
		if !ok {
			return s.errorResponseXML("FindFolder", ErrErrorFolderNotFound, "unknown distinguished folder: "+d.ID)
		}
		switch role {
		case publicFoldersRole:
			owner, ready := s.publicFoldersReady(mailboxKey)
			if !ready {
				return s.errorResponseXML("FindFolder", ErrErrorFolderNotFound, "public folders are not enabled")
			}
			if _, err := s.identity.EnsureMailboxId(owner); err != nil {
				return s.errorResponseXML("FindFolder", ErrErrorInternalServer, err.Error())
			}
			s.reconcilePublicFolderIdentities(owner)
			callerEmail = mailboxKey // mailboxKey is the caller's email until rebound below
			mailboxKey = owner
			publicTree = true
			// parentID stays zero: the public tree is enumerated flat below.
		case "root":
			parentID = rootID // store root: its child is the IPM subtree
		case "ipmsubtree":
			parentID = ipmID // IPM subtree (msgfolderroot): children are the user folders
		default:
			folder, err := s.identity.GetFolderByMailbox(mailboxKey, role)
			if err == nil {
				parentID = folder.FolderID
			} else if errors.Is(err, semcore.ErrFolderNotFound) {
				// Parent distinguished folder doesn't exist yet; enumerate the IPM subtree.
				parentID = ipmID
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
	} else {
		parentID = ipmID // no parent specified: default to the IPM subtree
	}

	// Bridge the caller's canonical storage folders into the identity store so
	// enumeration reflects the real mailbox. Skipped for the public tree, which
	// has already reconciled its own (non-default) folders above.
	if !publicTree {
		s.reconcileMailboxFolderIdentities(mailboxKey)
	}

	// List all folders for this mailbox.
	allFolders, err := s.identity.ListFolderIdentitiesForMailbox(mailboxKey)
	if err != nil {
		return s.errorResponseXML("FindFolder", ErrErrorInternalServer, err.Error())
	}

	// Index folders by id so Deep traversal can walk the effective-parent chain.
	byID := make(map[string]semcore.StoredFolderIdentity, len(allFolders))
	for _, f := range allFolders {
		byID[f.FolderID.String()] = f
	}

	var matching []FolderType
	for _, f := range allFolders {
		// ListFolderIdentitiesForMailbox already filters by mboxKey scope. The
		// store root and the IPM subtree (msgfolderroot) are sync anchors, not
		// user-visible folders — emitting the IPM subtree as a child of root makes
		// Outlook render "msgfolderroot" as a sibling folder, so skip both.
		if isContainerRole(f.Role) {
			continue
		}
		// The Recoverable Items dumpster is a soft-delete retention area, not a
		// browsable IPM folder — Exchange keeps it out of the mail-client folder
		// hierarchy and Outlook never lists it. It stays addressable by
		// distinguished id and powers the recover flow, so only enumeration hides it.
		if semcore.IsClientHiddenFolderRole(f.Role) {
			continue
		}
		// Skip the parent itself.
		if f.FolderID.Equal(parentID) {
			continue
		}

		// Filter to the requested subtree. The public tree is enumerated flat;
		// the caller's own mailbox uses the projected Exchange hierarchy.
		if publicTree {
			if parentID.String() != "" && !f.ParentID.Equal(parentID) {
				continue
			}
		} else if deep {
			if !s.isFolderUnder(f, parentID, rootID, ipmID, byID) {
				continue
			}
		} else if !s.effectiveParentID(f.Role, f.ParentID, rootID, ipmID).Equal(parentID) {
			continue
		}

		displayName := s.folderDisplayName(mailboxKey, f.Role, f.FolderID)

		// In the public tree, only surface folders the caller may read (per-user
		// or "anyone" grant); mailboxKey is the public owner here.
		if publicTree && !s.callerCanReadPublicFolder(callerEmail, mailboxKey, displayName) {
			continue
		}

		parentRef := f.ParentID
		if !publicTree {
			parentRef = s.effectiveParentID(f.Role, f.ParentID, rootID, ipmID)
		}
		child, total, unread := s.folderCounts(mailboxKey, f.FolderID, f.Role, rootID, ipmID)
		fxml := FolderType{
			FolderID:         newFolderID(f.FolderID.String(), child, total, unread),
			ParentFolderID:   FolderIdComponents{ID: parentRef.String()},
			DisplayName:      displayName,
			TotalCount:       total,
			UnreadCount:      unread,
			ChildFolderCount: child,
			FolderClass:      folderClassForRole(f.Role),
		}
		// mailboxKey is the folder owner here (the caller for own folders, the
		// public owner in the public tree), which is exactly the ACL owner key.
		s.decorateFolderPermissions(ctx, &fxml, mailboxKey, displayName, wantPerms)
		matching = append(matching, fxml)
	}

	msg := FindFolderResponseMessageType{}
	msg.ResponseClass = "Success"
	msg.ResponseCode.Value = ErrNoError
	msg.RootFolder = FindFolderRootFolder{
		TotalItemsInView:        len(matching),
		IncludesLastItemInRange: true,
		Folders:                 FindFolderContainer{Folders: matching},
	}

	resp := FindFolderResponse{}
	resp.ResponseMessages.Messages = []FindFolderResponseMessageType{msg}
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

// SearchFolderForCreate is a <t:SearchFolder> element in a CreateFolder request:
// a persistent saved-query folder carrying the restriction and the folder set it
// is evaluated over.
type SearchFolderForCreate struct {
	XMLName          xml.Name             `xml:"http://schemas.microsoft.com/exchange/services/2006/types SearchFolder"`
	DisplayName      string               `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName"`
	SearchParameters SearchParametersType `xml:"http://schemas.microsoft.com/exchange/services/2006/types SearchParameters"`
}

// SearchParametersType is the <t:SearchParameters> of a search folder: the
// restriction plus its base folder set and traversal depth.
type SearchParametersType struct {
	Traversal string `xml:"Traversal,attr"`
	// Restriction reuses the same SearchFilter tree the FindItem path parses;
	// only the wrapping element's namespace differs (types here vs messages in
	// FindItem), so the embedded filter elements decode identically.
	Restriction   *SearchFolderRestriction `xml:"http://schemas.microsoft.com/exchange/services/2006/types Restriction"`
	BaseFolderIDs BaseFolderIDsType        `xml:"http://schemas.microsoft.com/exchange/services/2006/types BaseFolderIds"`
}

// SearchFolderRestriction wraps the restriction filter under a types-namespaced
// <t:Restriction> element (as carried inside SearchParameters).
type SearchFolderRestriction struct {
	SearchFilter
}

// BaseFolderIDsType lists the folders a search folder is evaluated over, as
// distinguished ids and/or explicit folder ids.
type BaseFolderIDsType struct {
	Distinguished []struct {
		ID string `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId"`
	Folder []struct {
		ID string `xml:"Id,attr"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// FoldersContainer wraps the Folders list in CreateFolder requests.
// The m:Folders element is in messages namespace, containing t:Folder (plain
// folders) and/or t:SearchFolder (search folders) in types namespace.
type FoldersContainer struct {
	XMLName       xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
	Folders       []FolderTypeForCreate   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
	SearchFolders []SearchFolderForCreate `xml:"http://schemas.microsoft.com/exchange/services/2006/types SearchFolder"`
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
			FolderID:       newFolderID(folderID.String(), 0, 0, 0),
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

	// Search folders carry a saved restriction and base folder set rather than
	// items of their own; they are stored as folder identities marked with a
	// SearchDefinition (role stays empty so they never collapse onto a
	// distinguished-role folder).
	for _, sf := range req.Folders.SearchFolders {
		if sf.DisplayName == "" {
			msgs = append(msgs, errorMsg("CreateFolder", ErrErrorInvalidOperation, "DisplayName is required"))
			continue
		}

		def := &semcore.SearchFolderDef{
			Traversal:   sf.SearchParameters.Traversal,
			BaseFolders: s.resolveBaseFolderNames(mailboxKey, sf.SearchParameters.BaseFolderIDs),
		}
		if sf.SearchParameters.Restriction != nil {
			searchDefFromFilter(&sf.SearchParameters.Restriction.SearchFilter, def)
		}

		folderID, err := s.identity.EnsureFolderId(mailboxKey, sf.DisplayName, "")
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
		if err := s.identity.SetFolderSearchDefinition(folderID, def); err != nil {
			msgs = append(msgs, errorMsg("CreateFolder", ErrErrorInternalServer, err.Error()))
			continue
		}

		fxml := FolderType{
			FolderID:       newFolderID(folderID.String(), 0, 0, 0),
			ParentFolderID: FolderIdComponents{ID: parentID.String()},
			DisplayName:    sf.DisplayName,
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
		PermissionSet *PermissionSetType `xml:"http://schemas.microsoft.com/exchange/services/2006/types PermissionSet"`
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

		// Distinguished folders cannot be renamed or reparented, but their
		// permissions may still be changed (Outlook sets permissions on INBOX,
		// Calendar, etc.), so the rename/reparent guard is applied per operation.
		applied := false
		failed := false
	opLoop:
		for _, op := range fc.Updates.Operations {
			switch {
			case op.Folder.PermissionSet != nil:
				if ec := s.applyFolderPermissions(ctx, rec.Role, mboxKey, folderID, op.Folder.PermissionSet); ec != "" {
					msgs = append(msgs, errorMsg("UpdateFolder", ec, "cannot change folder permissions"))
					failed = true
					break opLoop
				}
				applied = true
			case op.Folder.DisplayName != nil:
				if rec.Role != "" {
					msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInvalidOperation, "cannot rename a distinguished folder"))
					failed = true
					break opLoop
				}
				// Display name change: semcore identity is stable — display name is not stored
				// in the identity store. The client sees the new display name in the response.
				applied = true
			case op.Folder.ParentFolderId != nil:
				if rec.Role != "" {
					msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInvalidOperation, "cannot reparent a distinguished folder"))
					failed = true
					break opLoop
				}
				newParentID, err := semcore.NewFolderId(op.Folder.ParentFolderId.ID)
				if err != nil {
					msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInvalidId, err.Error()))
					failed = true
					break opLoop
				}
				if err := s.identity.SetFolderParent(folderID, newParentID); err != nil {
					msgs = append(msgs, errorMsg("UpdateFolder", ErrErrorInternalServer, err.Error()))
					failed = true
					break opLoop
				}
				applied = true
			}
		}
		if failed {
			continue
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

		// Recompute counts so the echoed FolderId carries the same state-derived
		// ChangeKey the next SyncFolderHierarchy will report for this folder.
		ucKey := strings.TrimPrefix(mboxKey, "e:")
		ucRoot := s.rootFolderID(ucKey)
		ucIPM := s.ipmSubtreeFolderID(ucKey)
		child, total, unread := s.folderCounts(ucKey, folderID, rec.Role, ucRoot, ucIPM)
		fxml := FolderType{
			FolderID:         newFolderID(folderID.String(), child, total, unread),
			ParentFolderID:   FolderIdComponents{ID: rec.ParentID.String()},
			DisplayName:      displayName,
			TotalCount:       total,
			UnreadCount:      unread,
			ChildFolderCount: child,
			FolderClass:      folderClassForRole(rec.Role),
		}
		// Echo the resulting permission set and effective rights so the client
		// sees the applied state.
		s.decorateFolderPermissions(ctx, &fxml, mboxKey, displayName, true)
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

// applyFolderPermissions writes a folder's incoming PermissionSet back to the
// canonical RFC 4314 ACL store. It is owner-only self-service: a caller may edit
// permissions only on their own folders (owner == authenticated caller), so the
// public-folder tree and delegate-accessed mailboxes stay read-only here. The
// incoming set is reconciled against the current ACL — each grant is written
// (rights derived from the permission level or bits) and any grantee no longer
// present is removed. It returns "" on success or an EWS error code.
func (s *Server) applyFolderPermissions(ctx context.Context, role, mboxKey string, folderID semcore.FolderId, ps *PermissionSetType) ErrorCode {
	if s.storageDB == nil {
		return ErrErrorInternalServer
	}
	owner := strings.ToLower(strings.TrimPrefix(mboxKey, "e:"))
	caller, _ := ctx.Value("X-Email").(string) //nolint:errcheck // missing identity falls through to the owner check
	caller = strings.ToLower(strings.TrimSpace(caller))
	if owner == "" || owner != caller {
		return ErrErrorAccessDenied
	}
	folderName := s.folderDisplayName(mboxKey, role, folderID)
	if folderName == "" {
		return ErrErrorInvalidOperation
	}
	current, err := s.storageDB.ListACL(owner, folderName)
	if err != nil {
		return ErrErrorInternalServer
	}
	seen := make(map[string]bool, len(ps.Permissions))
	for _, p := range ps.Permissions {
		grantee := userIDToGrantee(p.UserID)
		if grantee == "" {
			continue
		}
		seen[grantee] = true
		if err := s.storageDB.SetACL(owner, folderName, grantee, permissionToACL(p), caller); err != nil {
			return ErrErrorInternalServer
		}
	}
	for _, e := range current {
		if !seen[e.Grantee] {
			if err := s.storageDB.DeleteACL(owner, folderName, e.Grantee); err != nil {
				return ErrErrorInternalServer
			}
		}
	}
	return ""
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

// SyncFolderHierarchyChangesType is a choice of Create | Update | Delete repeated
// per change, each holding exactly one folder (one folder id for Delete). Outlook
// walks the <Changes> children expecting a single folder per element, so every
// changed folder is emitted as its own <Create>/<Update> — never one element
// wrapping the whole set.

// SyncFolderChangesCreate wraps a single <Create><Folder/></Create>. The initial
// sync (empty SyncState) reports every folder as a Create so the client's empty
// local hierarchy is populated rather than asked to update folders it has never
// seen.
type SyncFolderChangesCreate struct {
	XMLName xml.Name   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Create"`
	Folder  FolderType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
}

// SyncFolderChangesUpdate wraps a single <Update><Folder/></Update> for a folder
// whose state advanced since the client's last sync.
type SyncFolderChangesUpdate struct {
	XMLName xml.Name   `xml:"http://schemas.microsoft.com/exchange/services/2006/types Update"`
	Folder  FolderType `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
}

// SyncFolderChangesDelete wraps a single <Delete><FolderId/></Delete>.
type SyncFolderChangesDelete struct {
	XMLName  xml.Name     `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
	FolderID FolderIDOnly `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// The <Changes> element is in messages namespace; <Create>/<Update>/<Delete> are
// in types namespace.
type SyncFolderChangesContainer struct {
	Creates []SyncFolderChangesCreate `xml:"http://schemas.microsoft.com/exchange/services/2006/types Create"`
	Updates []SyncFolderChangesUpdate `xml:"http://schemas.microsoft.com/exchange/services/2006/types Update"`
	Deletes []SyncFolderChangesDelete `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
}

// SyncFolderHierarchyMsg is one SyncFolderHierarchy result. The hierarchy syncs
// in a single shot (no paging), so IncludesLastFolderInRange is always true; it
// sits between SyncState and Changes per the EWS response-message schema.
type SyncFolderHierarchyMsg struct {
	ResponseClass             string                     `xml:"ResponseClass,attr"`
	ResponseCode              string                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	SyncState                 string                     `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncState"`
	IncludesLastFolderInRange bool                       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages IncludesLastFolderInRange"`
	Changes                   SyncFolderChangesContainer `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Changes"`
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

	// Folder identities are keyed by the raw email; the "e:" prefix is a
	// mailbox-resolution artifact. Strip it so enumeration matches how
	// FindFolder/GetFolder store and read folders, then bridge the caller's
	// canonical storage folders into the identity store.
	mailboxKey := strings.TrimPrefix(mboxKey, "e:")
	s.reconcileMailboxFolderIdentities(mailboxKey)

	// List all folders for this mailbox.
	allFolders, err := s.identity.ListFolderIdentitiesForMailbox(mailboxKey)
	if err != nil {
		return s.errorResponseXML("SyncFolderHierarchy", ErrErrorInternalServer, err.Error())
	}

	// Container ids drive the effective-parent projection onto the Exchange hierarchy.
	rootID := s.rootFolderID(mailboxKey)
	ipmID := s.ipmSubtreeFolderID(mailboxKey)

	// Collect deletions since last sync, one <Delete> per removed folder.
	var deletes []SyncFolderChangesDelete
	if !deletedSince.IsZero() {
		tombs, err := s.tombstones.ListTombstonesSince(mboxID, semcore.FolderId{}, deletedSince)
		if err == nil {
			for _, t := range tombs {
				if t.IsFolderLevel() {
					deletes = append(deletes, SyncFolderChangesDelete{FolderID: FolderIDOnly{ID: t.FolderID.String()}})
				}
			}
		}
	}

	// Initial sync (empty SyncState) reports every folder as a Create so the
	// client populates an empty hierarchy; incremental sync reports advanced
	// folders as Update. Each folder is its own change element.
	initial := req.SyncState == ""
	var creates []SyncFolderChangesCreate
	var updates []SyncFolderChangesUpdate
	for _, f := range allFolders {
		// ListFolderIdentitiesForMailbox already filters by mboxKey scope. The
		// store root and the IPM subtree (msgfolderroot) are sync anchors, not
		// user-visible child folders — emitting either makes Outlook render the
		// anchor itself as a sibling folder, so neither is ever a Create.
		if isContainerRole(f.Role) {
			continue
		}
		// The Recoverable Items dumpster is a soft-delete retention area, not a
		// browsable IPM folder — Exchange keeps it out of the mail-client folder
		// hierarchy, so it is never synced into the client tree. It stays
		// addressable by distinguished id and powers the recover flow.
		if semcore.IsClientHiddenFolderRole(f.Role) {
			continue
		}
		// On incremental sync, skip folders whose modseq hasn't advanced.
		if !initial && f.HighestModSeq <= folderVersion {
			continue
		}

		displayName := s.folderDisplayName(mailboxKey, f.Role, f.FolderID)

		parentRef := s.effectiveParentID(f.Role, f.ParentID, rootID, ipmID)
		child, total, unread := s.folderCounts(mailboxKey, f.FolderID, f.Role, rootID, ipmID)
		fxml := FolderType{
			FolderID:         newFolderID(f.FolderID.String(), child, total, unread),
			ParentFolderID:   FolderIdComponents{ID: parentRef.String()},
			DisplayName:      displayName,
			TotalCount:       total,
			UnreadCount:      unread,
			ChildFolderCount: child,
			FolderClass:      folderClassForRole(f.Role),
		}
		if initial {
			creates = append(creates, SyncFolderChangesCreate{Folder: fxml})
		} else {
			updates = append(updates, SyncFolderChangesUpdate{Folder: fxml})
		}
	}

	// Persist new sync state.
	newVersion := folderVersion + 1
	newState := fmt.Sprintf("v%d:folders:%d", newVersion, len(creates)+len(updates))
	if err := s.sync.PutSyncState(mboxID, semcore.FolderId{}, clientID, newState); err != nil {
		return s.errorResponseXML("SyncFolderHierarchy", ErrErrorSync, err.Error())
	}

	resp := SyncFolderHierarchyResponse{}
	syncChanges := SyncFolderChangesContainer{
		Creates: creates,
		Updates: updates,
		Deletes: deletes,
	}
	resp.ResponseMessages.Messages = []SyncFolderHierarchyMsg{{
		ResponseClass:             "Success",
		ResponseCode:              string(ErrNoError),
		SyncState:                 newState,
		IncludesLastFolderInRange: true,
		Changes:                   syncChanges,
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
