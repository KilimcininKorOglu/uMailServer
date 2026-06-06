package ews

import "github.com/umailserver/umailserver/internal/semcore"

// This file collects the consumer-side interfaces the EWS server depends on for
// its semantic-core stores, decoupling the handlers from the concrete
// bbolt-backed implementations one store at a time so a relational backend can
// slot in later. The bbolt types satisfy these via compile-time assertions.

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
