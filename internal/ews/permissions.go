// Package ews — folder permission model.
//
// This file bridges the canonical RFC 4314 ACL store (internal/storage) to the
// MAPI permission-bit model Outlook reads and writes over EWS as
// folder:PermissionSet. There is one source of truth for folder access — the
// ACL — and these helpers only project it onto the EWS shape and back.
package ews

import (
	"context"
	"strings"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// permissionSetFieldURI is the EWS property a client requests to retrieve a
// folder's permission set.
const permissionSetFieldURI = "folder:PermissionSet"

// folderShapeWantsPermissions reports whether a GetFolder/FindFolder shape asks
// for the folder permission set — either AllProperties or an explicit
// folder:PermissionSet additional property.
func folderShapeWantsPermissions(shape FolderResponseShape) bool {
	if shape.BaseShape == FolderAllProperties {
		return true
	}
	if shape.AdditionalProperties != nil {
		for _, f := range shape.AdditionalProperties.FieldURIs {
			if f.URI == permissionSetFieldURI {
				return true
			}
		}
	}
	return false
}

// decorateFolderPermissions fills a folder's read-only EffectiveRights (always)
// and, when wantPerms is set, its PermissionSet from the canonical RFC 4314 ACL
// store. ownerKey is the folder owner's mailbox key ("e:"+email or raw email);
// folderName is the storage mailbox name used as the ACL key. The owner of a
// folder implicitly holds all rights even with no explicit ACL entry.
func (s *Server) decorateFolderPermissions(ctx context.Context, fxml *FolderType, ownerKey, folderName string, wantPerms bool) {
	if s.storageDB == nil || folderName == "" {
		return
	}
	owner := strings.TrimPrefix(ownerKey, "e:")
	caller, _ := ctx.Value("X-Email").(string) //nolint:errcheck // optional auth identity; falls back to owner below
	caller = strings.ToLower(strings.TrimSpace(caller))
	if caller == "" {
		caller = owner
	}

	var eff storage.ACLRights
	if caller == owner {
		eff = storage.ACLAll
	} else if r, err := storage.ResolveEffectiveRights(s.storageDB.GetACL, caller, owner, folderName); err == nil {
		eff = r
	}
	fxml.EffectiveRights = aclToEffectiveRights(eff)

	if !wantPerms {
		return
	}
	entries, err := s.storageDB.ListACL(owner, folderName)
	if err != nil {
		return
	}
	ps := &PermissionSetType{Permissions: make([]PermissionType, 0, len(entries))}
	for _, e := range entries {
		ps.Permissions = append(ps.Permissions, aclToPermission(e.Grantee, e.Rights))
	}
	fxml.PermissionSet = ps
}

// aclFolderKey returns the storage mailbox name a folder's RFC 4314 ACL is keyed
// by — the SAME key the webmail ACL API (/api/v1/mailboxes/.../acl) and the
// storage layer use: the canonical IMAP name for distinguished folders
// (mailboxNameForFolder → CanonicalFolderNameForRole, e.g. "INBOX") and the full
// IMAP hierarchy path for user folders. It falls back to the EWS display name only
// for non-mail collaboration folders (calendar/contacts/tasks), which carry no
// mail ACL. Keying ACLs on the EWS display name ("Inbox") instead of the canonical
// storage name ("INBOX") made folder permissions set on one surface invisible to
// the other — the cross-surface divergence this closes.
func (s *Server) aclFolderKey(mailboxKey, role string, folderID semcore.FolderId) string {
	if name := s.mailboxNameForFolder(mailboxKey, folderID); name != "" {
		return name
	}
	return s.folderDisplayName(mailboxKey, role, folderID)
}

// EWS permission action / read-access values.
const (
	permNone        = "None"
	permOwned       = "Owned"
	permAll         = "All"
	permFullDetails = "FullDetails"
)

// EWS permission-level presets (the subset uMailServer maps to fixed ACL masks).
const (
	levelNone     = "None"
	levelReviewer = "Reviewer"
	levelAuthor   = "Author"
	levelEditor   = "Editor"
	levelOwner    = "Owner"
	levelCustom   = "Custom"
)

// distinguishedDefault is the EWS UserId that stands for the RFC 4314 "anyone"
// grant (everyone). Anonymous folds into it on the way in.
const distinguishedDefault = "Default"

// Preset ACL masks for each named permission level. They are deliberately fixed
// so a level chosen in Outlook round-trips to a stable RFC 4314 ACL. Editor adds
// edit/delete-all over Author; only Owner carries CreateSubfolders (x).
var (
	aclReviewer = storage.ACLLookup | storage.ACLRead
	aclAuthor   = storage.ACLLookup | storage.ACLRead | storage.ACLSeen | storage.ACLWrite | storage.ACLDelete
	aclEditor   = storage.ACLLookup | storage.ACLRead | storage.ACLSeen | storage.ACLWrite | storage.ACLWriteSeen | storage.ACLDelete | storage.ACLExpunge
	aclOwner    = storage.ACLAll
)

// aclToPermissionLevel returns the named level whose preset mask equals r, or
// levelCustom when no preset matches.
func aclToPermissionLevel(r storage.ACLRights) string {
	switch r {
	case 0:
		return levelNone
	case aclReviewer:
		return levelReviewer
	case aclAuthor:
		return levelAuthor
	case aclEditor:
		return levelEditor
	case aclOwner:
		return levelOwner
	default:
		return levelCustom
	}
}

// permissionLevelToACL maps a named level to its preset mask. It returns
// (mask, true) for a known level and (0, false) for Custom/unknown, telling the
// caller to derive the mask from the individual permission bits instead.
func permissionLevelToACL(level string) (storage.ACLRights, bool) {
	switch level {
	case levelNone:
		return 0, true
	case levelReviewer:
		return aclReviewer, true
	case levelAuthor:
		return aclAuthor, true
	case levelEditor:
		return aclEditor, true
	case levelOwner:
		return aclOwner, true
	default:
		return 0, false
	}
}

// granteeToUserID renders an ACL grantee as an EWS UserId: the reserved "anyone"
// grant becomes the distinguished "Default" user, everything else a plain SMTP
// address.
func granteeToUserID(grantee string) UserIdType {
	if grantee == storage.ACLAnyone {
		return UserIdType{DistinguishedUser: distinguishedDefault}
	}
	return UserIdType{PrimarySmtpAddress: grantee}
}

// userIDToGrantee resolves an EWS UserId back to an ACL grantee. Any
// distinguished user ("Default"/"Anonymous") maps to the reserved "anyone" grant.
func userIDToGrantee(u UserIdType) string {
	if strings.TrimSpace(u.DistinguishedUser) != "" {
		return storage.ACLAnyone
	}
	return strings.ToLower(strings.TrimSpace(u.PrimarySmtpAddress))
}

// aclToPermission projects an RFC 4314 ACL grant onto an EWS folder permission.
// EditItems is driven by the seen/write-seen bits and DeleteItems by the
// delete/expunge bits (independently of CanCreateItems), so the inverse mapping
// in permissionToACL is stable for any sensible rights set.
func aclToPermission(grantee string, r storage.ACLRights) PermissionType {
	p := PermissionType{
		UserID:              granteeToUserID(grantee),
		CanCreateItems:      r&storage.ACLWrite != 0,
		CanCreateSubFolders: r&storage.ACLCreate != 0,
		IsFolderOwner:       r == storage.ACLAll,
		IsFolderVisible:     r&storage.ACLLookup != 0,
		EditItems:           permNone,
		DeleteItems:         permNone,
		ReadItems:           permNone,
		PermissionLevel:     aclToPermissionLevel(r),
	}
	if r&storage.ACLRead != 0 {
		p.ReadItems = permFullDetails
	}
	switch {
	case r&storage.ACLWriteSeen != 0:
		p.EditItems = permAll
	case r&storage.ACLSeen != 0:
		p.EditItems = permOwned
	}
	switch {
	case r&storage.ACLExpunge != 0:
		p.DeleteItems = permAll
	case r&storage.ACLDelete != 0:
		p.DeleteItems = permOwned
	}
	return p
}

// permissionToACL maps an EWS folder permission back to an RFC 4314 ACL grant. A
// recognized PermissionLevel wins (so a level set in Outlook round-trips to its
// canonical mask); otherwise the mask is derived from the individual bits.
func permissionToACL(p PermissionType) storage.ACLRights {
	if r, ok := permissionLevelToACL(p.PermissionLevel); ok {
		return r
	}
	if p.IsFolderOwner {
		return storage.ACLAll
	}
	var r storage.ACLRights
	if p.IsFolderVisible {
		r |= storage.ACLLookup
	}
	if p.ReadItems == permFullDetails {
		r |= storage.ACLRead
	}
	if p.CanCreateItems {
		r |= storage.ACLWrite
	}
	if p.CanCreateSubFolders {
		r |= storage.ACLCreate
	}
	switch p.EditItems {
	case permAll:
		r |= storage.ACLSeen | storage.ACLWriteSeen
	case permOwned:
		r |= storage.ACLSeen
	}
	switch p.DeleteItems {
	case permAll:
		r |= storage.ACLDelete | storage.ACLExpunge
	case permOwned:
		r |= storage.ACLDelete
	}
	return r
}

// aclToEffectiveRights renders a caller's cumulative rights on a folder as the
// read-only EWS EffectiveRights element.
func aclToEffectiveRights(r storage.ACLRights) *EffectiveRightsType {
	return &EffectiveRightsType{
		CreateContents:  r&storage.ACLWrite != 0,
		CreateHierarchy: r&storage.ACLCreate != 0,
		Delete:          r&storage.ACLDelete != 0,
		Modify:          r&storage.ACLWriteSeen != 0,
		Read:            r&storage.ACLRead != 0,
	}
}
