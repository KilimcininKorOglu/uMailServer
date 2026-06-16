package db

import (
	"context"
	"errors"
	"time"

	"github.com/umailserver/umailserver/internal/vacation"
)

// ErrNotFound is returned (wrapped) by a Store's lookup methods when the
// requested record does not exist. Callers distinguish "absent" from a real
// failure with errors.Is(err, db.ErrNotFound) rather than matching an
// engine-specific error string, so the bbolt and PostgreSQL backends are
// interchangeable.
var ErrNotFound = errors.New("not found")

// Store is the account/domain/alias/group/tenant/queue/auth/preferences surface
// the server and its protocol handlers depend on at runtime. The bbolt-backed
// *DB satisfies it today; the relational *postgres.DB satisfies it too, so the
// composition root can pick a backend with database.backend and hand every
// consumer the same interface — no handler names a concrete engine.
//
// Engine-specific setup (Open, RunMigrations/Migrate, BoltDB) is intentionally
// NOT part of this interface: the composition root performs it on the concrete
// type before exposing the store as a Store.
type Store interface {
	// Accounts.
	CreateAccount(account *AccountData) error
	GetAccount(domain, localPart string) (*AccountData, error)
	UpdateAccount(account *AccountData) error
	DeleteAccount(domain, localPart string) error
	ListAccountsByDomain(domain string) ([]*AccountData, error)
	IncrementQuota(domain, localPart string, delta int64) error
	SetQuotaUsed(domain, localPart string, used int64) error

	// Domains.
	CreateDomain(domain *DomainData) error
	GetDomain(name string) (*DomainData, error)
	UpdateDomain(domain *DomainData) error
	DeleteDomain(name string) error
	ListDomains() ([]*DomainData, error)

	// Aliases.
	CreateAlias(alias *AliasData) error
	GetAlias(domain, localPart string) (*AliasData, error)
	UpdateAlias(alias *AliasData) error
	DeleteAlias(domain, localPart string) error
	ListAliases() ([]*AliasData, error)
	ResolveAlias(domain, localPart string) (string, error)

	// Mail groups.
	CreateMailGroup(group *MailGroup) error
	GetMailGroup(domain, localPart string) (*MailGroup, error)
	UpdateMailGroup(group *MailGroup) error
	DeleteMailGroup(domain, localPart string) error
	ListMailGroups() ([]*MailGroup, error)
	ExpandMailGroup(group *MailGroup) ([]string, error)

	// Tenants.
	CreateTenant(t *TenantData) error
	GetTenant(id string) (*TenantData, error)
	UpdateTenant(t *TenantData) error
	DeleteTenant(id string) error
	ListTenants() ([]*TenantData, error)
	ListDomainsByTenant(tenantID string) ([]*DomainData, error)
	EnsureTenantsForDomains() (int, error)

	// Outbound queue.
	Enqueue(entry *QueueEntry) error
	EnqueueWithLimit(entry *QueueEntry, maxSize int) error
	Dequeue(id string) error
	GetQueueEntry(id string) (*QueueEntry, error)
	UpdateQueueEntry(entry *QueueEntry) error
	GetPendingQueue(now time.Time) ([]*QueueEntry, error)
	ForEachQueueEntry(fn func(*QueueEntry) error) error

	// Scheduled ("send later") messages.
	CreateScheduledMessage(m *ScheduledMessage) error
	CreateScheduledMessageWithLimit(m *ScheduledMessage, maxPerOwner int) error
	GetScheduledMessage(id string) (*ScheduledMessage, error)
	UpdateScheduledMessage(m *ScheduledMessage) error
	DeleteScheduledMessage(id string) error
	ListScheduledByOwner(owner string) ([]*ScheduledMessage, error)
	ListDueScheduledMessages(now time.Time) ([]*ScheduledMessage, error)
	CancelScheduledByFolderRef(owner string, uid uint32) (bool, error)
	ResetStaleScheduledMessages(before time.Time) (int, error)

	// SetQuotaWarnSent flips the graduated-quota warning latch without touching
	// the concurrently-incremented QuotaUsed.
	SetQuotaWarnSent(domain, localPart string, sent bool) error

	// Recoverable Items (soft-delete dumpster) and TTL retention.
	CreateRecoverableItem(m *RecoverableItem) error
	GetRecoverableItem(id string) (*RecoverableItem, error)
	DeleteRecoverableItem(id string) error
	ListRecoverableByOwner(owner string) ([]*RecoverableItem, error)
	ListExpiredRecoverableItems(cutoff time.Time) ([]*RecoverableItem, error)
	FindRecoverableByFolderRef(owner string, uid uint32) (*RecoverableItem, error)

	// Auth: token blacklist and portal sessions.
	StoreRevokedToken(tokenHash string, expiry time.Time) error
	IsTokenRevoked(tokenHash string) (bool, error)
	CleanupRevokedTokens() error
	CreateClientSession(session *ClientSession) error
	GetClientSession(id string) (*ClientSession, error)
	UpdateClientSession(session *ClientSession) error
	DeleteClientSession(id string) error
	ListClientSessionsByEmail(email string) ([]*ClientSession, error)
	RevokeClientSession(id string) error
	CleanupExpiredSessions(maxAge time.Duration) error

	// Exchange ActiveSync device partnerships (policy key, negotiated version,
	// remote-wipe state); keyed by (email, deviceID). GetEASDevice returns a
	// wrapped ErrNotFound when no partnership exists.
	PutEASDevice(dev *EASDevice) error
	GetEASDevice(email, deviceID string) (*EASDevice, error)
	ListEASDevicesByEmail(email string) ([]*EASDevice, error)
	ListAllEASDevices() ([]*EASDevice, error)
	DeleteEASDevice(email, deviceID string) error

	// TLS certificate-cache blob storage: a generic keyed byte store shared by
	// active-active nodes so ACME-issued certificates and account keys survive a
	// restart and are reachable cluster-wide. Kept intentionally generic (string
	// key -> raw bytes) so any certificate manager's cache can adapt onto it.
	// GetTLSCacheEntry returns a wrapped ErrNotFound when the key is absent.
	GetTLSCacheEntry(key string) ([]byte, error)
	PutTLSCacheEntry(key string, data []byte) error
	DeleteTLSCacheEntry(key string) error
	// ListTLSCacheKeys returns every key with the given prefix (empty prefix =
	// all keys), in ascending key order. Backs certmagic.Storage.List.
	ListTLSCacheKeys(prefix string) ([]string, error)
	// StatTLSCacheEntry returns the byte size and last-modified time of the
	// value under key, or a wrapped ErrNotFound when absent. Backs
	// certmagic.Storage.Stat; bbolt keeps no per-key timestamp and reports a
	// zero modified time (certmagic treats Modified as optional).
	StatTLSCacheEntry(key string) (size int64, modified time.Time, err error)
	// LockTLSCache attempts a single, non-blocking acquisition of a named
	// distributed lock held for ttl by owner. Returns true when owner now holds
	// the lock (a fresh acquire, or a re-acquire of its own live lease), false
	// when another live lease blocks it. The certmagic.Storage adapter drives
	// the blocking retry/ctx loop on top of this try primitive. bbolt is a
	// single-process store, so its lock is an in-process lease with the same
	// contract.
	LockTLSCache(ctx context.Context, name, owner string, ttl time.Duration) (bool, error)
	// UnlockTLSCache releases name when held by owner; a lock held by another
	// owner (or nobody) is left untouched and is not an error.
	UnlockTLSCache(ctx context.Context, name, owner string) error

	// Typed preferences (replacing the generic-KV buckets).
	GetUIPrefs(user string) (map[string]bool, error)
	PutUIPrefs(user string, prefs map[string]bool) error
	GetSignature(user string) (string, error)
	PutSignature(user, signature string) error
	GetCategories(user string) ([]Category, error)
	PutCategories(user string, categories []Category) error
	GetVacation(user string) (*vacation.Config, error)
	PutVacation(user string, c *vacation.Config) error
	DeleteVacation(user string) error
	GetUserConfig(owner, name string) (*UserConfigBlob, error)
	PutUserConfig(owner, name string, b *UserConfigBlob) error
	DeleteUserConfig(owner, name string) error

	// Admin RBAC: roles, permissions, and user-role assignments.
	// Role management.
	CreateRole(role *Role) error
	GetRole(id string) (*Role, error)
	ListRoles() ([]*Role, error)
	UpdateRole(role *Role) error
	DeleteRole(id string) error

	// Permission management.
	GetRolePermissions(roleID string) ([]*RolePermission, error)
	SetRolePermissions(roleID string, perms []*RolePermission) error

	// User-role assignment.
	AssignRoleToUser(userID, roleID string) error
	RemoveRoleFromUser(userID, roleID string) error
	GetUserRoles(userID string) ([]*Role, error)
	GetUsersByRole(roleID string) ([]string, error)

	// Spam history: records spam check events for admin visibility.
	LogSpamEvent(entry *SpamHistoryEntry) error
	ListSpamHistory(opts SpamHistoryListOptions) ([]*SpamHistoryEntry, int, error)

	// Lifecycle.
	Close() error
}

// Compile-time assertion that the bbolt-backed *DB satisfies Store.
var _ Store = (*DB)(nil)
