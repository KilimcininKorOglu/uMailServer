// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file defines the canonical delegate model: one mailbox owner can grant
// another user specific permissions on specific folders (calendar, tasks, inbox,
// contacts, notes, journal) plus meeting delivery and private-item visibility
// settings. Delegation is separate from RFC 4314 IMAP ACLs — it is an
// Exchange-semantic capability that controls what a delegate can do on behalf
// of an owner through EWS, MAPI, and Outlook surfaces.
//
// Delegation is authoritative only when the delegate grant was created through
// an Exchange-facing management surface (EWS AddDelegate or admin UI). A raw ACL
// grant in the storage layer is not sufficient to expose Exchange-semantic
// delegate behavior.
package semcore

import (
	"errors"
	"time"
)

// ---------------------------------------------------------------------------
// Delegate identity
// ---------------------------------------------------------------------------

// DelegateId is the authoritative identity for a delegation grant.
// It is assigned when the grant is created and remains stable for the
// lifetime of the grant (until revoked or the owner removes it).
type DelegateId struct {
	raw string
}

// NewDelegateId constructs a DelegateId from its raw string representation.
func NewDelegateId(raw string) (DelegateId, error) {
	if raw == "" {
		return DelegateId{}, errors.New("DelegateId: empty value")
	}
	return DelegateId{raw: raw}, nil
}

// MustDelegateId constructs a DelegateId and panics on invalid input.
// Use only in tests or trusted initialization code.
func MustDelegateId(raw string) DelegateId {
	id, err := NewDelegateId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value. Clients must treat this as opaque.
func (id DelegateId) String() string { return id.raw }

// IsZero returns true for a nil/empty DelegateId.
func (id DelegateId) IsZero() bool { return id.raw == "" }

// Equal reports whether two DelegateIds have the same raw value.
func (id DelegateId) Equal(other DelegateId) bool { return id.raw == other.raw }

// MarshalJSON serializes DelegateId to its raw string value.
func (id DelegateId) MarshalJSON() ([]byte, error) {
	return []byte(`"` + id.raw + `"`), nil
}

// UnmarshalJSON reconstructs DelegateId from its raw string value.
func (id *DelegateId) UnmarshalJSON(data []byte) error {
	if len(data) < 2 {
		return errors.New("DelegateId: too short")
	}
	// Strip surrounding quotes.
	*id = DelegateId{raw: string(data[1 : len(data)-1])}
	return nil
}

// ---------------------------------------------------------------------------
// DelegateFolderPermissionLevel
// ---------------------------------------------------------------------------

// DelegateFolderPermissionLevel represents the permission level a delegate
// has on a specific folder type in the owner's mailbox.
type DelegateFolderPermissionLevel string

const (
	// DelegateFolderPermissionNone — delegate has no access to this folder.
	DelegateFolderPermissionNone DelegateFolderPermissionLevel = "None"
	// DelegateFolderPermissionReviewer — delegate can read items in this folder.
	DelegateFolderPermissionReviewer DelegateFolderPermissionLevel = "Reviewer"
	// DelegateFolderPermissionAuthor — delegate can read and create items.
	DelegateFolderPermissionAuthor DelegateFolderPermissionLevel = "Author"
	// DelegateFolderPermissionCustom — delegate has custom permissions (not used by uMailServer).
	DelegateFolderPermissionCustom DelegateFolderPermissionLevel = "Custom"
	// DelegateFolderPermissionDelegate — delegate has full delegate access (read, create, modify).
	DelegateFolderPermissionDelegate DelegateFolderPermissionLevel = "Delegate"
)

// DelegateFolderPermissions holds permission levels per folder type.
type DelegateFolderPermissions struct {
	Calendar  DelegateFolderPermissionLevel `json:"calendar,omitempty"`
	Tasks     DelegateFolderPermissionLevel `json:"tasks,omitempty"`
	Inbox     DelegateFolderPermissionLevel `json:"inbox,omitempty"`
	Contacts  DelegateFolderPermissionLevel `json:"contacts,omitempty"`
	Notes     DelegateFolderPermissionLevel `json:"notes,omitempty"`
	Journal   DelegateFolderPermissionLevel `json:"journal,omitempty"`
}

// HasAccess reports whether the delegate has at least Reviewer access on any folder.
func (p DelegateFolderPermissions) HasAccess() bool {
	return p.Calendar != "" || p.Tasks != "" || p.Inbox != "" ||
		p.Contacts != "" || p.Notes != "" || p.Journal != ""
}

// CanReadCalendar reports whether the delegate can read calendar items.
func (p DelegateFolderPermissions) CanReadCalendar() bool {
	return p.Calendar != DelegateFolderPermissionNone
}

// CanWriteCalendar reports whether the delegate can create/update calendar items.
func (p DelegateFolderPermissions) CanWriteCalendar() bool {
	return p.Calendar == DelegateFolderPermissionAuthor ||
		p.Calendar == DelegateFolderPermissionDelegate
}

// CanReadInbox reports whether the delegate can read inbox items.
func (p DelegateFolderPermissions) CanReadInbox() bool {
	return p.Inbox != DelegateFolderPermissionNone
}

// CanWriteInbox reports whether the delegate can create items in inbox.
func (p DelegateFolderPermissions) CanWriteInbox() bool {
	return p.Inbox == DelegateFolderPermissionAuthor ||
		p.Inbox == DelegateFolderPermissionDelegate
}

// CanReadTasks reports whether the delegate can read task items.
func (p DelegateFolderPermissions) CanReadTasks() bool {
	return p.Tasks != DelegateFolderPermissionNone
}

// CanWriteTasks reports whether the delegate can create/update task items.
func (p DelegateFolderPermissions) CanWriteTasks() bool {
	return p.Tasks == DelegateFolderPermissionAuthor ||
		p.Tasks == DelegateFolderPermissionDelegate
}

// ---------------------------------------------------------------------------
// DeliverMeetingRequests
// ---------------------------------------------------------------------------

// DeliverMeetingRequests defines how meeting requests are delivered to a delegate.
type DeliverMeetingRequests string

const (
	// DeliverDelegatesAndMe — meeting requests forwarded to delegate and kept in owner's inbox.
	DeliverDelegatesAndMe DeliverMeetingRequests = "DelegatesAndMe"
	// DeliverDelegatesOnly — meeting requests forwarded to delegate only.
	DeliverDelegatesOnly DeliverMeetingRequests = "DelegatesOnly"
	// DeliverDelegatesAndSendInfoToMe — forwarded to delegate; owner receives info copies.
	DeliverDelegatesAndSendInfoToMe DeliverMeetingRequests = "DelegatesAndSendInformationToMe"
)

// ---------------------------------------------------------------------------
// DelegateUser
// ---------------------------------------------------------------------------

// DelegateUser represents a single delegate grant on a mailbox.
// The grant is scoped to one mailbox (the owner) and one delegate user.
type DelegateUser struct {
	ID             DelegateId               `json:"id"`
	OwnerID        MailboxId               `json:"owner_id"`         // MailboxId of the mailbox owner
	DelegateEmail  string                  `json:"delegate_email"`   // email of the delegate user
	DelegateUserID string                  `json:"delegate_user_id"` // opaque user ID of delegate
	Permissions    DelegateFolderPermissions `json:"permissions"`     // per-folder permission levels
	ViewPrivateItems bool                  `json:"view_private_items"` // can see private calendar items
	ReceiveCopies  bool                    `json:"receive_copies"`     // receives copies of meeting messages
	DeliverRequests DeliverMeetingRequests  `json:"deliver_meeting_requests"` // meeting delivery mode
	CreatedAt      time.Time               `json:"created_at"`
	UpdatedAt      time.Time               `json:"updated_at"`
	GrantedBy      string                  `json:"granted_by"` // who made the grant (owner email)
}

// CanDelegateCalendar reports whether the delegate can act on calendar items.
func (d *DelegateUser) CanDelegateCalendar() bool {
	return d.Permissions.CanReadCalendar()
}

// CanActAsDelegate reports whether this grant gives any meaningful delegate access.
func (d *DelegateUser) CanActAsDelegate() bool {
	return d.Permissions.HasAccess()
}

// ---------------------------------------------------------------------------
// Delegate meeting audit context
// ---------------------------------------------------------------------------

// DelegateAuditContext records the acting identity when a delegate performs
// an operation on behalf of an owner. This is written into lifecycle events
// so that audit logs and sync consumers can distinguish delegate actions
// from direct owner actions.
type DelegateAuditContext struct {
	OwnerMailboxID MailboxId  // the mailbox being acted on
	OwnerEmail    string     // email of the mailbox owner
	DelegateEmail string     // email of the acting delegate
	DelegateID    DelegateId // the delegation grant ID
	Action        string     // e.g. "create", "update", "delete", "send"
	ItemID        ItemId     // item affected if applicable
	FolderID      FolderId   // folder affected if applicable
	At            time.Time  // when the action occurred
}

// NewDelegateAuditContext builds an audit context for a delegate action.
func NewDelegateAuditContext(ownerID MailboxId, ownerEmail, delegateEmail string, delegateID DelegateId) DelegateAuditContext {
	return DelegateAuditContext{
		OwnerMailboxID: ownerID,
		OwnerEmail:    ownerEmail,
		DelegateEmail: delegateEmail,
		DelegateID:    delegateID,
		At:            time.Now(),
	}
}
