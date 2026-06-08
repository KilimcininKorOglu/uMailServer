package ews

import (
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
	"github.com/umailserver/umailserver/internal/storage"
)

// This file collects the consumer-side interfaces the EWS server depends on for
// its semantic-core stores, decoupling the handlers from the concrete
// bbolt-backed implementations one store at a time so a relational backend can
// slot in later. The bbolt types satisfy these via compile-time assertions.

// MailStore is the message-metadata / UID-index surface the EWS server needs to
// keep the mailstore index in sync as items are created, fetched, and deleted.
// Both the bbolt *storage.Database and the relational *postgres.DB satisfy it;
// Maildir message bodies are read through msgStore, not this interface.
type MailStore interface {
	GetNextUID(user, mailbox string) (uint32, error)
	GetMessageUIDs(user, mailbox string) ([]uint32, error)
	GetMessageMetadata(user, mailbox string, uid uint32) (*storage.MessageMetadata, error)
	StoreMessageMetadata(user, mailbox string, uid uint32, meta *storage.MessageMetadata) error
	DeleteMessage(user, mailbox string, uid uint32) error
}

var _ MailStore = (*storage.Database)(nil)

// DelegateStore is the delegation surface the EWS server needs.
type DelegateStore interface {
	PutDelegate(delegate *semcore.DelegateUser) (semcore.DelegateId, error)
	ListDelegates(ownerID semcore.MailboxId) ([]*semcore.DelegateUser, error)
	GetDelegateForUser(ownerID semcore.MailboxId, delegateEmail string) (*semcore.DelegateUser, error)
	RemoveDelegate(id semcore.DelegateId) error
}

var _ DelegateStore = (*semcore.BoltDelegateStore)(nil)

// SubscriptionStore is the push-subscription surface the EWS server needs.
type SubscriptionStore interface {
	CreateSubscription(sub semcore.Subscription) (semcore.SubscriptionId, error)
	GetSubscription(id semcore.SubscriptionId) (*semcore.Subscription, error)
	RenewSubscription(id semcore.SubscriptionId) error
	RemoveSubscription(id semcore.SubscriptionId) error
}

var _ SubscriptionStore = (*semcore.BoltSubscriptionStore)(nil)

// LifecycleStore is the item-lifecycle event surface the EWS server needs.
type LifecycleStore interface {
	AppendLifecycle(event semcore.Lifecycle) error
	PollEvents(mboxID semcore.MailboxId, sinceSeq uint64, limit int) ([]semcore.Lifecycle, uint64, error)
	HighestSequence(mboxID semcore.MailboxId) (uint64, error)
}

var _ LifecycleStore = (*semcore.BoltLifecycleStore)(nil)

// PolicyStore is the rules/OOF/resource-policy surface the EWS server needs.
type PolicyStore interface {
	PutRule(rule *semcore.Rule) error
	GetRule(id semcore.RuleId) (*semcore.Rule, error)
	ListRules(mailboxID semcore.MailboxId) ([]*semcore.Rule, error)
	DeleteRule(id semcore.RuleId) error
	PutOOF(policy *semcore.OOFPolicy) error
	GetOOF(id semcore.OOFId) (*semcore.OOFPolicy, error)
	PutResource(policy *semcore.ResourcePolicy) error
	GetResource(id semcore.ResourceId) (*semcore.ResourcePolicy, error)
	ListResources() ([]*semcore.ResourcePolicy, error)
}

var _ PolicyStore = (*semcore.BoltPolicyStore)(nil)

// CollabStore is the calendar/contact/task identity surface the EWS server
// needs (CalendarItemId/ContactId/TaskId with their ChangeKey variants).
type CollabStore interface {
	GetCalendarItemByID(id semcore.CalendarItemId) (*semcore.StoredCalendarItemIdentity, error)
	ListCalendarItemsByFolder(folderID semcore.FolderId) ([]semcore.StoredCalendarItemIdentity, error)
	PutCalendarItemIdentity(msgKey string, rec *semcore.StoredCalendarItemIdentity, currentChangeKey semcore.CalendarChangeKey) error
	PutCalendarItemIdentityUnsafe(msgKey string, rec *semcore.StoredCalendarItemIdentity) error
	DeleteCalendarItemIdentity(msgKey string, currentChangeKey semcore.CalendarChangeKey) error

	GetContactByID(id semcore.ContactId) (*semcore.StoredContactIdentity, error)
	ListContactsByFolder(folderID semcore.FolderId) ([]semcore.StoredContactIdentity, error)
	PutContactIdentity(msgKey string, rec *semcore.StoredContactIdentity, currentChangeKey semcore.ContactChangeKey) error
	PutContactIdentityUnsafe(msgKey string, rec *semcore.StoredContactIdentity) error
	DeleteContactIdentity(msgKey string, currentChangeKey semcore.ContactChangeKey) error

	GetTaskByID(id semcore.TaskId) (*semcore.StoredTaskIdentity, error)
	ListTasksByFolder(folderID semcore.FolderId) ([]semcore.StoredTaskIdentity, error)
	PutTaskIdentity(msgKey string, rec *semcore.StoredTaskIdentity, currentChangeKey semcore.TaskChangeKey) error
	PutTaskIdentityUnsafe(msgKey string, rec *semcore.StoredTaskIdentity) error
	DeleteTaskIdentity(msgKey string, currentChangeKey semcore.TaskChangeKey) error
}

var _ CollabStore = (*semcore.BoltCollaborationStore)(nil)

// IdentityStore is the canonical mailbox/folder/item identity surface the EWS
// server needs (MailboxId/FolderId/ItemId resolution and item state).
type IdentityStore interface {
	EnsureMailboxId(email string) (semcore.MailboxId, error)
	GetMailboxIDByEmail(email string) (semcore.MailboxId, error)
	EnsureFolderId(mboxKey, folderName, role string) (semcore.FolderId, error)
	EnsureChildFolderId(mboxKey string, parentID semcore.FolderId, displayName, role string) (semcore.FolderId, error)
	GetFolderID(mboxKey, folderName string) (semcore.FolderId, error)
	GetFolderByID(id semcore.FolderId) (*semcore.StoredFolderIdentity, error)
	GetFolderByMailbox(mboxKey, role string) (*semcore.StoredFolderIdentity, error)
	ListFolderIdentitiesForMailbox(mboxKey string) ([]semcore.StoredFolderIdentity, error)
	FolderNameByID(mboxKey string, id semcore.FolderId) (string, error)
	SetFolderParent(id semcore.FolderId, parentID semcore.FolderId) error
	DeleteFolder(id semcore.FolderId) error
	GetItemIdentity(id semcore.ItemId) (*semcore.StoredItemIdentity, error)
	ListItemIdentitiesByFolder(folderID semcore.FolderId) ([]semcore.StoredItemIdentity, error)
	SetItemFolder(id semcore.ItemId, folderID semcore.FolderId) error
	SetItemMsgKey(id semcore.ItemId, msgKey string) error
	UpdateItemState(id semcore.ItemId, isRead *bool, categories []string) error
	DeleteItemIdentity(id semcore.ItemId) error
}

var _ IdentityStore = (*semcore.BoltIdentityStore)(nil)

// SyncStore is the per-folder sync-state surface the EWS server needs.
type SyncStore interface {
	PutSyncState(mboxID semcore.MailboxId, folderID semcore.FolderId, clientID string, watermark string) error
	GetSyncState(mboxID semcore.MailboxId, folderID semcore.FolderId, clientID string) (*semcore.StoredSyncState, error)
	MarkFolderGone(folderID semcore.FolderId) error
}

var _ SyncStore = (*semcore.BoltSyncStateStore)(nil)

// TombstoneStore is the deletion-tombstone surface the EWS server needs. It is
// a superset of semcore.TombstoneWriter, so a value of this type can be handed
// to mutationPipe.MutateDelete.
type TombstoneStore interface {
	PutTombstone(t semcore.Tombstone) error
	ListTombstonesSince(mboxID semcore.MailboxId, folderID semcore.FolderId, since time.Time) ([]semcore.Tombstone, error)
}

var _ TombstoneStore = (*semcore.BoltTombstoneStore)(nil)
