package api

import (
	"github.com/umailserver/umailserver/internal/semcore"
)

// IdentityStore is the mailbox/folder/item identity surface the API server (and
// its notes handler and the CalDAV/CardDAV collab bridges it wires) needs from
// the semantic core. *semcore.BoltIdentityStore satisfies it today; a relational
// identity store satisfies it later, so the API server carries no engine
// dependency on the bbolt-backed identity store.
type IdentityStore interface {
	EnsureMailboxId(email string) (semcore.MailboxId, error)
	GetMailboxIDByEmail(email string) (semcore.MailboxId, error)
	MailboxEmailsByID() (map[string]string, error)
	EnsureFolderId(mboxKey, folderName, role string) (semcore.FolderId, error)
	GetFolderID(mboxKey, folderName string) (semcore.FolderId, error)
	GetFolderByID(id semcore.FolderId) (*semcore.StoredFolderIdentity, error)
	FolderNameByID(mboxKey string, id semcore.FolderId) (string, error)
	SetFolderSearchDefinition(id semcore.FolderId, def *semcore.SearchFolderDef) error
	ListSearchFolders(mboxKey string) ([]semcore.StoredFolderIdentity, error)
	DeleteFolder(id semcore.FolderId) error
	ListItemIdentitiesByFolder(folderID semcore.FolderId) ([]semcore.StoredItemIdentity, error)
	DeleteItemIdentity(id semcore.ItemId) error
}

// PolicyStore is the rule/resource/room-list/out-of-office surface the API
// server needs (webmail filters, admin rules/diagnostics, directory resources
// and room lists, vacation). *semcore.BoltPolicyStore satisfies it.
type PolicyStore interface {
	ListRules(mailboxID semcore.MailboxId) ([]*semcore.Rule, error)
	ListAllRules() ([]*semcore.Rule, error)
	GetRule(id semcore.RuleId) (*semcore.Rule, error)
	PutRule(rule *semcore.Rule) error
	DeleteRule(id semcore.RuleId) error
	GetOOF(id semcore.OOFId) (*semcore.OOFPolicy, error)
	PutOOF(policy *semcore.OOFPolicy) error
	ListResources() ([]*semcore.ResourcePolicy, error)
	GetResource(id semcore.ResourceId) (*semcore.ResourcePolicy, error)
	PutResource(policy *semcore.ResourcePolicy) error
	DeleteResource(id semcore.ResourceId) error
	ListRoomLists() ([]*semcore.RoomList, error)
	GetRoomList(id string) (*semcore.RoomList, error)
	PutRoomList(rl *semcore.RoomList) error
	DeleteRoomList(id string) error
}

// DelegationStore is the delegate-grant surface the API server needs (admin and
// per-user delegation management). *semcore.BoltDelegateStore satisfies it.
type DelegationStore interface {
	PutDelegate(delegate *semcore.DelegateUser) (semcore.DelegateId, error)
	GetDelegate(id semcore.DelegateId) (*semcore.DelegateUser, error)
	ListDelegates(ownerID semcore.MailboxId) ([]*semcore.DelegateUser, error)
	RemoveDelegate(id semcore.DelegateId) error
	ListAllDelegates() ([]*semcore.DelegateUser, error)
}

// SubscriptionStore is the push-subscription surface the API server needs (admin
// diagnostics enumerate a mailbox's subscriptions). *semcore.BoltSubscriptionStore
// satisfies it.
type SubscriptionStore interface {
	ListSubscriptionsByMailbox(mboxID semcore.MailboxId) ([]semcore.Subscription, error)
}

// CollaborationStore is the calendar/task/contact identity surface the API
// server passes into the CalDAV and CardDAV collab bridges (webmail calendar,
// tasks, contacts). It is the union of the surfaces CalDAV and CardDAV need, so
// one interface value satisfies both bridges. *semcore.BoltCollaborationStore
// satisfies it.
type CollaborationStore interface {
	// Calendar (CalDAV).
	FindCalendarItemByUID(folderID semcore.FolderId, icalUID string) (msgKey string, rec *semcore.StoredCalendarItemIdentity, found bool, err error)
	ListCalendarItemsByFolder(folderID semcore.FolderId) ([]semcore.StoredCalendarItemIdentity, error)
	PutCalendarItemIdentityUnsafe(msgKey string, rec *semcore.StoredCalendarItemIdentity) error
	DeleteCalendarItemByUID(folderID semcore.FolderId, icalUID string) error
	// Tasks (CalDAV VTODO).
	FindTaskByUID(folderID semcore.FolderId, icalUID string) (msgKey string, rec *semcore.StoredTaskIdentity, found bool, err error)
	ListTasksByFolder(folderID semcore.FolderId) ([]semcore.StoredTaskIdentity, error)
	PutTaskIdentityUnsafe(msgKey string, rec *semcore.StoredTaskIdentity) error
	DeleteTaskByUID(folderID semcore.FolderId, icalUID string) error
	// Contacts (CardDAV).
	FindContactByUID(folderID semcore.FolderId, icalUID string) (msgKey string, rec *semcore.StoredContactIdentity, found bool, err error)
	ListContactsByFolder(folderID semcore.FolderId) ([]semcore.StoredContactIdentity, error)
	PutContactIdentityUnsafe(msgKey string, rec *semcore.StoredContactIdentity) error
	DeleteContactByUID(folderID semcore.FolderId, icalUID string) error
}

// SemanticStore is the aggregate semantic-core surface the API server holds. Its
// accessors return the sub-store interfaces above, so the API server never names
// a concrete *semcore.Bolt*Store. A bbolt-backed *semcore.Store is bridged to
// this interface by BoltSemanticStore; a relational aggregate provides its own
// SemanticStore implementation later, slotting in at SetSemcoreStore with no API
// handler change.
//
// Go does not allow return-type covariance, so *semcore.Store (whose accessors
// return concrete *Bolt*Store) cannot satisfy SemanticStore directly; the
// boltSemanticStore adapter performs the widening.
type SemanticStore interface {
	Identity() IdentityStore
	Policy() PolicyStore
	Delegation() DelegationStore
	Subscriptions() SubscriptionStore
	Collaboration() CollaborationStore
	NewJobStore() (semcore.JobStore, error)
}

// Compile-time assertions that the bbolt-backed sub-stores satisfy the consumer
// interfaces.
var (
	_ IdentityStore      = (*semcore.BoltIdentityStore)(nil)
	_ PolicyStore        = (*semcore.BoltPolicyStore)(nil)
	_ DelegationStore    = (*semcore.BoltDelegateStore)(nil)
	_ SubscriptionStore  = (*semcore.BoltSubscriptionStore)(nil)
	_ CollaborationStore = (*semcore.BoltCollaborationStore)(nil)
)

// boltSemanticStore bridges the bbolt-backed *semcore.Store aggregate to the
// SemanticStore interface, widening each concrete *Bolt*Store accessor return to
// the matching sub-store interface.
type boltSemanticStore struct{ s *semcore.Store }

func (b boltSemanticStore) Identity() IdentityStore                { return b.s.Identity() }
func (b boltSemanticStore) Policy() PolicyStore                    { return b.s.Policy() }
func (b boltSemanticStore) Delegation() DelegationStore            { return b.s.Delegation() }
func (b boltSemanticStore) Subscriptions() SubscriptionStore       { return b.s.Subscriptions() }
func (b boltSemanticStore) Collaboration() CollaborationStore      { return b.s.Collaboration() }
func (b boltSemanticStore) NewJobStore() (semcore.JobStore, error) { return b.s.NewJobStore() }

var _ SemanticStore = boltSemanticStore{}

// BoltSemanticStore wraps the bbolt-backed canonical *semcore.Store as a
// SemanticStore for injection into the API server via SetSemcoreStore.
func BoltSemanticStore(s *semcore.Store) SemanticStore { return boltSemanticStore{s} }
