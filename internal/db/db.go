package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.etcd.io/bbolt"

	"github.com/umailserver/umailserver/internal/db/migrations"
)

// Bucket names
const (
	BucketAccounts       = "accounts"
	BucketDomains        = "domains"
	BucketQueue          = "queue"
	BucketSpam           = "spam"
	BucketMetrics        = "metrics"
	BucketMessageMeta    = "messagemeta"
	BucketIndex          = "index"
	BucketAliases        = "aliases"
	BucketContacts       = "contacts"
	BucketFilters        = "filters"
	BucketVacation       = "vacation"
	BucketPreferences    = "preferences"
	BucketRevokedTokens  = "revoked_tokens"
	BucketClientSessions = "client_sessions"
	BucketMailGroups     = "mailgroups"
	BucketTenants        = "tenants"
	BucketScheduled      = "scheduled"
	BucketRecoverable    = "recoverable_items"
	BucketEASDevices     = "activesync_devices"
	BucketTLSCache       = "tls_cache"
	BucketRoles          = "roles"
	BucketRolePermissions = "role_permissions"
	BucketUserRoles      = "user_roles"
)

// DB wraps bbolt database
type DB struct {
	bolt *bbolt.DB

	// In-process TLS issuance/renewal coordination locks. bbolt is a
	// single-process store, so these only serialize goroutines within this
	// process; the multi-node equivalent lives in the postgres backend. The
	// lease map mirrors that contract (owner + expiry) so the certmagic lock
	// adapter is identical across backends.
	tlsLockMu  sync.Mutex
	tlsLocks   map[string]tlsLockEntry
}

// tlsLockEntry is an in-process lease on a TLS lock name.
type tlsLockEntry struct {
	owner     string
	expiresAt time.Time
}

// AccountData holds account information
type AccountData struct {
	Email            string `json:"email"`
	LocalPart        string `json:"local_part"`
	Domain           string `json:"domain"`
	PasswordHash     string `json:"password_hash"`
	APOPHash         string `json:"apop_hash,omitempty"` // SHA-256(password) for APOP authentication
	NTHash           string `json:"nt_hash,omitempty"`   // hex MD4(UTF-16LE password) for NTLM; set only when MAPI NTLM is enabled
	TOTPSecret       string `json:"totp_secret,omitempty"`
	TOTPEnabled      bool   `json:"totp_enabled"`
	TOTPLastUsedStep int64  `json:"totp_last_used_step,omitempty"`
	QuotaUsed        int64  `json:"quota_used"`
	QuotaLimit       int64  `json:"quota_limit"`
	// Graduated-quota thresholds (absolute bytes; 0 = inherit the domain default,
	// then disabled). QuotaWarn fires a one-time warning notification; QuotaProhibitSend
	// blocks outbound mail; QuotaLimit (composed with the domain's MaxMailboxSize)
	// stays the hard send+receive cap. QuotaWarnSent latches the warning so it is
	// emitted once per crossing (reset when usage falls back below the threshold).
	QuotaWarn          int64     `json:"quota_warn,omitempty"`
	QuotaProhibitSend  int64     `json:"quota_prohibit_send,omitempty"`
	QuotaWarnSent      bool      `json:"quota_warn_sent,omitempty"`
	MaxMessageSize     int64     `json:"max_message_size"`
	ForwardTo          string    `json:"forward_to,omitempty"`
	ForwardKeepCopy    bool      `json:"forward_keep_copy"`
	SendPolicy         string    `json:"send_policy,omitempty"`    // "" / "anyone" (default) = unrestricted; "internal" = may only send to locally hosted domains
	ReceivePolicy      string    `json:"receive_policy,omitempty"` // "" / "anyone" (default) = unrestricted; "internal" = only accepts mail from locally hosted domains
	SieveScript        string    `json:"sieve_script,omitempty"`
	VacationSettings   string    `json:"vacation_settings,omitempty"`
	MustChangePassword bool      `json:"must_change_password"`
	IsAdmin            bool      `json:"is_admin"`        // global super-admin (all tenants)
	IsTenantAdmin      bool      `json:"is_tenant_admin"` // self-service admin scoped to the account's own tenant
	IsActive           bool      `json:"is_active"`
	CompatibilityTier  uint8     `json:"compatibility_tier"` // per-account Exchange compatibility tier; 0 = TierIMAPOnly, 1 = TierExchange; defaults to 0
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	LastLoginAt        time.Time `json:"last_login_at,omitempty"`
	Avatar             []byte    `json:"avatar,omitempty"`       // raw profile photo bytes (capped, small)
	AvatarType         string    `json:"avatar_type,omitempty"`  // avatar MIME type, e.g. "image/png"
	DisplayName        string    `json:"display_name,omitempty"` // GAL/Outlook display name
	Title              string    `json:"title,omitempty"`        // job title
	Department         string    `json:"department,omitempty"`   // department / team
	Phone              string    `json:"phone,omitempty"`        // business phone number
	Timezone           string    `json:"timezone,omitempty"`     // IANA tz (e.g. "Europe/Istanbul") for user-facing time rendering; empty = follow device/UTC
	Locale             string    `json:"locale,omitempty"`       // preferred UI language (e.g. "tr", "en")
	Theme              string    `json:"theme,omitempty"`        // preferred UI theme ("light", "dark", "system")
	Onboarded          bool      `json:"onboarded"`              // true once the user finished first-run onboarding
}

// ClientSession holds HTTP/API client session information for the account portal.
type ClientSession struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`       // owner of this session
	TokenHash  string    `json:"token_hash"`  // hash of the JWT token
	DeviceType string    `json:"device_type"` // "desktop", "mobile", "tablet", "unknown"
	ClientIP   string    `json:"client_ip"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
	Revoked    bool      `json:"revoked"` // true if session was manually revoked
}

// EASDevice is an Exchange ActiveSync device partnership: one row per
// (Email, DeviceID). It persists the provisioning PolicyKey the client must
// echo on every request, the negotiated protocol version, and an
// admin-requested remote-wipe flag. Unlike a ClientSession (a short-lived auth
// token), a partnership lives until the device is removed or wiped.
type EASDevice struct {
	Email           string `json:"email"`
	DeviceID        string `json:"device_id"`
	DeviceType      string `json:"device_type"`
	UserAgent       string `json:"user_agent"`
	PolicyKey       string `json:"policy_key"`       // current accepted provisioning policy key
	ProtocolVersion string `json:"protocol_version"` // negotiated EAS version, e.g. "16.1"
	WipeRequested   bool   `json:"wipe_requested"`   // admin-requested remote wipe
	// Device-identity attributes the client reports through the Settings
	// DeviceInformation command (MS-ASCMD). They are informational metadata
	// surfaced to admins; access control never depends on them.
	Model          string    `json:"model,omitempty"`
	IMEI           string    `json:"imei,omitempty"`
	FriendlyName   string    `json:"friendly_name,omitempty"`
	OS             string    `json:"os,omitempty"`
	OSLanguage     string    `json:"os_language,omitempty"`
	PhoneNumber    string    `json:"phone_number,omitempty"`
	MobileOperator string    `json:"mobile_operator,omitempty"`
	FirstSync      time.Time `json:"first_sync"`
	LastSync       time.Time `json:"last_sync"`
}

// DomainData holds domain information
type DomainData struct {
	Name string `json:"name"`
	// TenantID is the owning tenant. Every domain belongs to exactly one tenant
	// (a tenant may own many domains). Backfilled at startup for legacy domains:
	// each gets its own single-domain tenant whose id equals the domain name.
	TenantID       string `json:"tenant_id,omitempty"`
	MaxAccounts    int    `json:"max_accounts"`
	MaxMailboxSize int64  `json:"max_mailbox_size"`
	// Per-domain graduated-quota defaults (absolute bytes; 0 = disabled), applied
	// to accounts that do not set their own QuotaWarn/QuotaProhibitSend.
	QuotaWarn         int64             `json:"quota_warn,omitempty"`
	QuotaProhibitSend int64             `json:"quota_prohibit_send,omitempty"`
	DKIMSelector      string            `json:"dkim_selector"`
	DKIMPublicKey     string            `json:"dkim_public_key,omitempty"`
	DKIMPrivateKey    string            `json:"dkim_private_key,omitempty"`
	Settings          map[string]string `json:"settings,omitempty"`
	CatchAllTarget    string            `json:"catch_all_target,omitempty"`
	// CompanyName feeds the {company} placeholder; FromTemplateInternal/External
	// are the From display-name templates applied to outbound mail for local-only
	// vs. any-external recipients (placeholders: {name} {title} {department}
	// {company} {email}). All empty by default → falls back to the DisplayName.
	CompanyName          string    `json:"company_name,omitempty"`
	FromTemplateInternal string    `json:"from_template_internal,omitempty"`
	FromTemplateExternal string    `json:"from_template_external,omitempty"`
	IsActive             bool      `json:"is_active"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// QueuePriority represents message priority levels
type QueuePriority int

const (
	PriorityLow    QueuePriority = 0
	PriorityNormal QueuePriority = 1
	PriorityHigh   QueuePriority = 2
	PriorityUrgent QueuePriority = 3
)

func (p QueuePriority) String() string {
	switch p {
	case PriorityLow:
		return "low"
	case PriorityNormal:
		return "normal"
	case PriorityHigh:
		return "high"
	case PriorityUrgent:
		return "urgent"
	default:
		return "normal"
	}
}

// QueueEntry holds message queue information
type QueueEntry struct {
	ID          string        `json:"id"`
	From        string        `json:"from"`
	To          []string      `json:"to"`
	MessagePath string        `json:"message_path"`
	CreatedAt   time.Time     `json:"created_at"`
	NextRetry   time.Time     `json:"next_retry"`
	RetryCount  int           `json:"retry_count"`
	LastError   string        `json:"last_error"`
	Status      string        `json:"status"`   // pending, sending, failed, delivered, bounced
	Priority    QueuePriority `json:"priority"` // 0=low, 1=normal, 2=high, 3=urgent
	// DSN fields
	Notify DSNNotify `json:"notify"` // DSN notification preferences (NEVER, SUCCESS, FAILURE, DELAY)
	Ret    DSNRet    `json:"ret"`    // What to return in DSN (FULL or HDRS)
}

// ScheduledMessage holds a message queued for future ("send later") delivery.
// It is the canonical record the leader-gated release loop reads; the "Scheduled"
// system folder is a visibility projection (FolderUID/BlobKey link the two), so a
// release moves the projection to Sent and a folder expunge cancels this record.
type ScheduledMessage struct {
	ID          string    `json:"id"`
	Owner       string    `json:"owner"`        // mailbox that scheduled the send
	From        string    `json:"from"`         // envelope sender
	To          []string  `json:"to"`           // envelope recipients
	MessagePath string    `json:"message_path"` // raw MIME on disk
	SendAt      time.Time `json:"send_at"`      // absolute UTC release time
	CreatedAt   time.Time `json:"created_at"`
	ClaimedAt   time.Time `json:"claimed_at"` // lease stamp while status=sending
	Status      string    `json:"status"`     // pending, sending, sent, failed, canceled
	Source      string    `json:"source"`     // webmail, ews, smtp, jmap
	FileSent    bool      `json:"file_sent"`  // file a Sent copy on release
	FolderUID   uint32    `json:"folder_uid"` // uid in the owner's Scheduled folder
	BlobKey     string    `json:"blob_key"`   // message-store key of the projection
	RetryCount  int       `json:"retry_count"`
	LastError   string    `json:"last_error"`
}

// RecoverableItem is a message that was permanently deleted but is held in the
// owner's "Recoverable Items" dumpster for a retention window so it can be
// restored ("Recover Deleted Items From Server"). It is the canonical record the
// leader-gated retention cleaner reads; the "Recoverable Items" system folder is
// a visibility projection (FolderUID/BlobKey link the two), so a restore moves
// the projection back to OriginalFolder and the cleaner purges both once
// DeletedAt ages past the retention window.
type RecoverableItem struct {
	ID             string    `json:"id"`
	Owner          string    `json:"owner"`           // mailbox that owns the dumpster
	OriginalFolder string    `json:"original_folder"` // folder the message was deleted from
	BlobKey        string    `json:"blob_key"`        // message-store key of the projection
	FolderUID      uint32    `json:"folder_uid"`      // uid in the owner's Recoverable Items folder
	DeletedAt      time.Time `json:"deleted_at"`      // absolute UTC; retention measured from here
	Size           int64     `json:"size"`
	Subject        string    `json:"subject"`
}

// DSNNotify represents delivery status notification preferences
type DSNNotify int32

// DSNRet represents what to return in DSN
type DSNRet int32

// AliasData holds email alias information
type AliasData struct {
	Alias     string    `json:"alias"`  // alias@domain
	Target    string    `json:"target"` // user@domain
	Domain    string    `json:"domain"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

// Open opens or creates the database
func Open(path string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	bolt, err := bbolt.Open(path, 0o600, &bbolt.Options{
		Timeout: 1 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// On POSIX systems, bbolt applies the mode at the syscall level so umask
	// is respected. However, we also explicitly chmod the file after opening
	// to ensure strict permissions even if umask is unusually permissive.
	// On Windows this is a no-op (Chmod returns nil).
	if err := os.Chmod(path, 0o600); err != nil {
		_ = bolt.Close()
		return nil, fmt.Errorf("failed to set database file permissions: %w", err)
	}

	closeOnErr := true
	defer func() {
		if closeOnErr {
			_ = bolt.Close()
		}
	}()

	db := &DB{bolt: bolt}

	// Initialize buckets
	if err := db.initBuckets(); err != nil {
		return nil, fmt.Errorf("failed to initialize buckets: %w", err)
	}

	closeOnErr = false
	return db, nil
}

// Close closes the database
func (d *DB) Close() error {
	return d.bolt.Close()
}

// BoltDB returns the underlying bbolt.DB for advanced operations
func (d *DB) BoltDB() *bbolt.DB {
	return d.bolt
}

// RunMigrations runs all pending database migrations
func (d *DB) RunMigrations() error {
	registry := migrations.NewRegistry()
	migrations.InitMigrations(registry)
	migrator := migrations.NewMigrator(d.bolt, registry)
	return migrator.Migrate()
}

// initBuckets creates all required buckets
func (d *DB) initBuckets() error {
	buckets := []string{
		BucketAccounts,
		BucketDomains,
		BucketQueue,
		BucketSpam,
		BucketMetrics,
		BucketMessageMeta,
		BucketIndex,
		BucketContacts,
		BucketAliases,
		BucketFilters,
		BucketVacation,
		BucketPreferences,
		BucketMailGroups,
		BucketTenants,
		BucketScheduled,
		BucketRecoverable,
		BucketEASDevices,
		BucketTLSCache,
	}

	return d.bolt.Update(func(tx *bbolt.Tx) error {
		for _, name := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return fmt.Errorf("failed to create bucket %s: %w", name, err)
			}
		}
		return nil
	})
}

// Put stores a value in a bucket
func (d *DB) Put(bucket string, key string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal value: %w", err)
	}

	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", bucket)
		}
		return b.Put([]byte(key), data)
	})
}

// Get retrieves a value from a bucket
func (d *DB) Get(bucket string, key string, dest interface{}) error {
	return d.bolt.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", bucket)
		}

		data := b.Get([]byte(key))
		if data == nil {
			return fmt.Errorf("key not found: %s: %w", key, ErrNotFound)
		}

		return json.Unmarshal(data, dest)
	})
}

// Delete removes a key from a bucket
func (d *DB) Delete(bucket string, key string) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", bucket)
		}
		return b.Delete([]byte(key))
	})
}

// ForEach iterates over all entries in a bucket
func (d *DB) ForEach(bucket string, fn func(key string, value []byte) error) error {
	return d.bolt.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", bucket)
		}

		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if err := fn(string(k), v); err != nil {
				return err
			}
		}
		return nil
	})
}

// ForEachPrefix iterates over entries with a given prefix
func (d *DB) ForEachPrefix(bucket string, prefix string, fn func(key string, value []byte) error) error {
	return d.bolt.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", bucket)
		}

		c := b.Cursor()
		prefixBytes := []byte(prefix)
		for k, v := c.Seek(prefixBytes); k != nil && len(k) >= len(prefixBytes) && string(k[:len(prefixBytes)]) == prefix; k, v = c.Next() {
			if err := fn(string(k), v); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- Account Operations ---

// AccountKey returns the database key for an account
func AccountKey(domain, localPart string) string {
	return fmt.Sprintf("%s/%s", domain, localPart)
}

// CreateAccount creates a new account
func (d *DB) CreateAccount(account *AccountData) error {
	if account.CreatedAt.IsZero() {
		account.CreatedAt = time.Now()
	}
	account.UpdatedAt = time.Now()

	key := AccountKey(account.Domain, account.LocalPart)
	return d.Put(BucketAccounts, key, account)
}

// GetAccount retrieves an account
func (d *DB) GetAccount(domain, localPart string) (*AccountData, error) {
	var account AccountData
	key := AccountKey(domain, localPart)
	if err := d.Get(BucketAccounts, key, &account); err != nil {
		return nil, err
	}
	return &account, nil
}

// UpdateAccount updates an existing account
func (d *DB) UpdateAccount(account *AccountData) error {
	account.UpdatedAt = time.Now()
	key := AccountKey(account.Domain, account.LocalPart)
	return d.Put(BucketAccounts, key, account)
}

// IncrementQuota atomically adds delta to an account's QuotaUsed inside a bbolt transaction.
// It returns an error if the account does not exist, quota would be exceeded, or int64 overflow.
func (d *DB) IncrementQuota(domain, localPart string, delta int64) error {
	key := AccountKey(domain, localPart)
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketAccounts))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketAccounts)
		}
		data := b.Get([]byte(key))
		if data == nil {
			return fmt.Errorf("key not found: %s", key)
		}
		var account AccountData
		if err := json.Unmarshal(data, &account); err != nil {
			return err
		}
		// Effective quota is the tighter of the account's own limit and the
		// domain's MaxMailboxSize ceiling (0 means unlimited on either side): a
		// per-account quota can never exceed the per-domain cap. Only growth
		// (delta > 0) can breach a limit, so the domain lookup is gated on it.
		effectiveLimit := account.QuotaLimit
		if delta > 0 {
			if domB := tx.Bucket([]byte(BucketDomains)); domB != nil {
				if domData := domB.Get([]byte(domain)); domData != nil {
					var dom DomainData
					if err := json.Unmarshal(domData, &dom); err != nil {
						return err
					}
					if dom.MaxMailboxSize > 0 && (effectiveLimit == 0 || dom.MaxMailboxSize < effectiveLimit) {
						effectiveLimit = dom.MaxMailboxSize
					}
				}
			}
		}
		if effectiveLimit > 0 && account.QuotaUsed+delta > effectiveLimit {
			return fmt.Errorf("quota exceeded for user: %s", key)
		}
		if delta > 0 && account.QuotaUsed > math.MaxInt64-delta {
			return fmt.Errorf("quota overflow for user: %s", key)
		}
		account.QuotaUsed += delta
		account.UpdatedAt = time.Now()
		newData, err := json.Marshal(&account)
		if err != nil {
			return fmt.Errorf("failed to marshal value: %w", err)
		}
		return b.Put([]byte(key), newData)
	})
}

// SetQuotaUsed sets an account's QuotaUsed to an absolute value inside a bbolt
// transaction, re-reading the row so it never clobbers a concurrently-changed
// field. Unlike IncrementQuota it applies NO cap check: it reconciles the
// counter to the canonical mailbox size (which may legitimately already exceed
// the effective limit), so it must never reject. A no-op when unchanged.
func (d *DB) SetQuotaUsed(domain, localPart string, used int64) error {
	key := AccountKey(domain, localPart)
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketAccounts))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketAccounts)
		}
		data := b.Get([]byte(key))
		if data == nil {
			return fmt.Errorf("key not found: %s", key)
		}
		var account AccountData
		if err := json.Unmarshal(data, &account); err != nil {
			return err
		}
		if account.QuotaUsed == used {
			return nil
		}
		account.QuotaUsed = used
		account.UpdatedAt = time.Now()
		newData, err := json.Marshal(&account)
		if err != nil {
			return fmt.Errorf("failed to marshal value: %w", err)
		}
		return b.Put([]byte(key), newData)
	})
}

// SetQuotaWarnSent flips an account's quota-warning latch. It re-reads the row
// inside the transaction and rewrites ONLY the flag (plus UpdatedAt), so it never
// clobbers a concurrently-incremented QuotaUsed. A no-op when the flag already
// holds the requested value.
func (d *DB) SetQuotaWarnSent(domain, localPart string, sent bool) error {
	key := AccountKey(domain, localPart)
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketAccounts))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketAccounts)
		}
		data := b.Get([]byte(key))
		if data == nil {
			return fmt.Errorf("key not found: %s: %w", key, ErrNotFound)
		}
		var account AccountData
		if err := json.Unmarshal(data, &account); err != nil {
			return err
		}
		if account.QuotaWarnSent == sent {
			return nil
		}
		account.QuotaWarnSent = sent
		account.UpdatedAt = time.Now()
		newData, err := json.Marshal(&account)
		if err != nil {
			return fmt.Errorf("failed to marshal value: %w", err)
		}
		return b.Put([]byte(key), newData)
	})
}

// EffectiveQuotaThresholds resolves an account's graduated-quota thresholds by
// composing the account's own values with its domain's defaults. Each tier is
// the account value when set (>0), else the domain default, else 0 (disabled).
// hardCap is the existing send+receive ceiling — the tighter of the account's
// QuotaLimit and the domain's MaxMailboxSize (0 = unlimited on either side).
// warn and prohibitSend are clamped to never exceed a positive hardCap (a tier
// above the hard cap is meaningless). dom may be nil. All values are bytes; 0
// means that tier is disabled/unlimited.
func EffectiveQuotaThresholds(acct *AccountData, dom *DomainData) (warn, prohibitSend, hardCap int64) {
	if acct == nil {
		return 0, 0, 0
	}
	hardCap = acct.QuotaLimit
	var domWarn, domProhibit, domMax int64
	if dom != nil {
		domWarn, domProhibit, domMax = dom.QuotaWarn, dom.QuotaProhibitSend, dom.MaxMailboxSize
	}
	if domMax > 0 && (hardCap == 0 || domMax < hardCap) {
		hardCap = domMax
	}
	if warn = acct.QuotaWarn; warn == 0 {
		warn = domWarn
	}
	if prohibitSend = acct.QuotaProhibitSend; prohibitSend == 0 {
		prohibitSend = domProhibit
	}
	if hardCap > 0 {
		if warn > hardCap {
			warn = hardCap
		}
		if prohibitSend > hardCap {
			prohibitSend = hardCap
		}
	}
	return warn, prohibitSend, hardCap
}

// StoreRevokedToken persists a revoked token hash with its expiry time.
func (d *DB) StoreRevokedToken(tokenHash string, expiry time.Time) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(BucketRevokedTokens))
		if err != nil {
			return err
		}
		data, _ := json.Marshal(expiry)
		return b.Put([]byte(tokenHash), data)
	})
}

// IsTokenRevoked checks whether a token hash is in the persistent revocation list.
// It also performs lazy cleanup of expired entries.
func (d *DB) IsTokenRevoked(tokenHash string) (bool, error) {
	var revoked bool
	var toDelete []string
	now := time.Now()
	err := d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketRevokedTokens))
		if b == nil {
			return nil
		}
		data := b.Get([]byte(tokenHash))
		if data == nil {
			return nil
		}
		var expiry time.Time
		if err := json.Unmarshal(data, &expiry); err != nil {
			// Invalid entry - delete it
			toDelete = append(toDelete, tokenHash)
			return nil
		}
		if now.After(expiry) {
			toDelete = append(toDelete, tokenHash)
			return nil
		}
		revoked = true
		return nil
	})
	if err != nil {
		return false, err
	}
	// Cleanup expired/invalid entries in a separate transaction
	if len(toDelete) > 0 {
		_ = d.bolt.Update(func(tx *bbolt.Tx) error {
			b := tx.Bucket([]byte(BucketRevokedTokens))
			if b == nil {
				return nil
			}
			for _, h := range toDelete {
				_ = b.Delete([]byte(h))
			}
			return nil
		})
	}
	return revoked, nil
}

// CleanupRevokedTokens removes all expired token revocation entries.
func (d *DB) CleanupRevokedTokens() error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketRevokedTokens))
		if b == nil {
			return nil
		}
		now := time.Now()
		return b.ForEach(func(k, v []byte) error {
			var expiry time.Time
			if err := json.Unmarshal(v, &expiry); err != nil || now.After(expiry) {
				_ = b.Delete(k)
			}
			return nil
		})
	})
}

// DeleteAccount removes an account
func (d *DB) DeleteAccount(domain, localPart string) error {
	key := AccountKey(domain, localPart)
	return d.Delete(BucketAccounts, key)
}

// ListAccountsByDomain returns all accounts in a domain
func (d *DB) ListAccountsByDomain(domain string) ([]*AccountData, error) {
	var accounts []*AccountData
	prefix := domain + "/"

	err := d.ForEachPrefix(BucketAccounts, prefix, func(key string, value []byte) error {
		var account AccountData
		if err := json.Unmarshal(value, &account); err != nil {
			return err
		}
		accounts = append(accounts, &account)
		return nil
	})

	return accounts, err
}

// --- Domain Operations ---

// CreateDomain creates a new domain
func (d *DB) CreateDomain(domain *DomainData) error {
	if domain.CreatedAt.IsZero() {
		domain.CreatedAt = time.Now()
	}
	domain.UpdatedAt = time.Now()

	// Every domain must belong to a tenant. When none is given, the domain gets
	// its own single-domain tenant (id == name), keeping the invariant for
	// domains created at runtime via the admin API.
	if domain.TenantID == "" {
		if err := d.ensureSelfTenant(domain.Name); err != nil {
			return err
		}
		domain.TenantID = domain.Name
	}

	return d.Put(BucketDomains, domain.Name, domain)
}

// GetDomain retrieves a domain
func (d *DB) GetDomain(name string) (*DomainData, error) {
	var domain DomainData
	if err := d.Get(BucketDomains, name, &domain); err != nil {
		return nil, err
	}
	return &domain, nil
}

// UpdateDomain updates an existing domain
func (d *DB) UpdateDomain(domain *DomainData) error {
	domain.UpdatedAt = time.Now()
	return d.Put(BucketDomains, domain.Name, domain)
}

// DeleteDomain removes a domain
func (d *DB) DeleteDomain(name string) error {
	return d.Delete(BucketDomains, name)
}

// ListDomains returns all domains
func (d *DB) ListDomains() ([]*DomainData, error) {
	var domains []*DomainData

	err := d.ForEach(BucketDomains, func(key string, value []byte) error {
		var domain DomainData
		if err := json.Unmarshal(value, &domain); err != nil {
			return err
		}
		domains = append(domains, &domain)
		return nil
	})

	return domains, err
}

// --- Queue Operations ---

// Enqueue adds a message to the queue
func (d *DB) Enqueue(entry *QueueEntry) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	return d.Put(BucketQueue, entry.ID, entry)
}

// EnqueueWithLimit adds a message to the queue only if the total number of
// entries in the queue bucket is below maxSize. The count and insert are
// performed inside a single bbolt transaction so the check is atomic.
func (d *DB) EnqueueWithLimit(entry *QueueEntry, maxSize int) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal value: %w", err)
	}

	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketQueue))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketQueue)
		}

		// Count existing entries
		count := 0
		_ = b.ForEach(func(_, _ []byte) error {
			count++
			return nil
		})
		if count >= maxSize {
			return fmt.Errorf("queue is full (max %d entries)", maxSize)
		}

		return b.Put([]byte(entry.ID), data)
	})
}

// GetQueueEntry retrieves a queue entry
func (d *DB) GetQueueEntry(id string) (*QueueEntry, error) {
	var entry QueueEntry
	if err := d.Get(BucketQueue, id, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

// UpdateQueueEntry updates a queue entry
func (d *DB) UpdateQueueEntry(entry *QueueEntry) error {
	return d.Put(BucketQueue, entry.ID, entry)
}

// Dequeue removes a message from the queue
func (d *DB) Dequeue(id string) error {
	return d.Delete(BucketQueue, id)
}

// pendingStatusMarker is the byte sequence json.Marshal produces for
// `Status: "pending"` on a QueueEntry. The encoder is whitespace-free, so
// exact substring matching is reliable for entries written via db.Put.
var pendingStatusMarker = []byte(`"status":"pending"`)

// errInvalidQueueEntry signals corruption in the queue bucket. It's a
// package-level sentinel so the GetPendingQueue hot loop doesn't allocate
// per non-pending entry — any per-call error string with %q would force the
// key argument to escape on every iteration, costing ~1 alloc/entry.
var errInvalidQueueEntry = errors.New("queue entry has invalid JSON")

// GetPendingQueue returns entries ready for delivery. It is called on every
// queue sweep tick, so the bucket scan is hot — we substring-match the
// status marker before paying for a full json.Unmarshal of the entry, which
// skips the ~12 allocs/entry decode cost for non-pending rows.
func (d *DB) GetPendingQueue(now time.Time) ([]*QueueEntry, error) {
	var entries []*QueueEntry

	err := d.ForEach(BucketQueue, func(key string, value []byte) error {
		if !bytes.Contains(value, pendingStatusMarker) {
			// Cheap sniff: real entries always begin with `{` from json.Marshal.
			// Anything else is corruption and is surfaced rather than silently skipped.
			if len(value) == 0 || value[0] != '{' {
				return errInvalidQueueEntry
			}
			return nil
		}
		var entry QueueEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			return err
		}
		if entry.Status == "pending" && entry.NextRetry.Before(now) {
			entries = append(entries, &entry)
		}
		return nil
	})

	return entries, err
}

// ForEachQueueEntry calls fn with every decoded queue entry. It decodes the
// stored JSON internally so callers iterate typed entries without touching the
// underlying bucket or the on-disk encoding — the seam a relational queue store
// (which would iterate rows instead) slots into. Malformed entries are skipped.
func (d *DB) ForEachQueueEntry(fn func(*QueueEntry) error) error {
	return d.ForEach(BucketQueue, func(_ string, value []byte) error {
		var entry QueueEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			return nil // skip malformed entries, matching prior callers
		}
		return fn(&entry)
	})
}

// ---------------------------------------------------------------------------
// Scheduled ("send later") messages
// ---------------------------------------------------------------------------

// CreateScheduledMessage persists a scheduled message, stamping CreatedAt.
func (d *DB) CreateScheduledMessage(m *ScheduledMessage) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	return d.Put(BucketScheduled, m.ID, m)
}

// CreateScheduledMessageWithLimit refuses to insert when the owner already has
// maxPerOwner pending scheduled messages. The count and insert run in one bbolt
// transaction so the limit holds under concurrent submitters.
func (d *DB) CreateScheduledMessageWithLimit(m *ScheduledMessage, maxPerOwner int) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("failed to marshal value: %w", err)
	}
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketScheduled))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketScheduled)
		}
		count := 0
		if err := b.ForEach(func(_, v []byte) error {
			var e ScheduledMessage
			if json.Unmarshal(v, &e) == nil && e.Owner == m.Owner && e.Status == "pending" {
				count++
			}
			return nil
		}); err != nil {
			return err
		}
		if count >= maxPerOwner {
			return fmt.Errorf("too many scheduled messages (max %d per user)", maxPerOwner)
		}
		return b.Put([]byte(m.ID), data)
	})
}

// GetScheduledMessage retrieves a scheduled message by id.
func (d *DB) GetScheduledMessage(id string) (*ScheduledMessage, error) {
	var m ScheduledMessage
	if err := d.Get(BucketScheduled, id, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateScheduledMessage overwrites a scheduled message.
func (d *DB) UpdateScheduledMessage(m *ScheduledMessage) error {
	return d.Put(BucketScheduled, m.ID, m)
}

// DeleteScheduledMessage removes a scheduled message.
func (d *DB) DeleteScheduledMessage(id string) error {
	return d.Delete(BucketScheduled, id)
}

// ListScheduledByOwner returns all scheduled messages owned by the given mailbox.
func (d *DB) ListScheduledByOwner(owner string) ([]*ScheduledMessage, error) {
	var out []*ScheduledMessage
	err := d.ForEach(BucketScheduled, func(_ string, value []byte) error {
		var m ScheduledMessage
		if err := json.Unmarshal(value, &m); err != nil {
			return nil // skip malformed
		}
		if m.Owner == owner {
			out = append(out, &m)
		}
		return nil
	})
	return out, err
}

// ListDueScheduledMessages returns pending messages whose send time has arrived.
func (d *DB) ListDueScheduledMessages(now time.Time) ([]*ScheduledMessage, error) {
	var out []*ScheduledMessage
	err := d.ForEach(BucketScheduled, func(_ string, value []byte) error {
		var m ScheduledMessage
		if err := json.Unmarshal(value, &m); err != nil {
			return nil // skip malformed
		}
		if m.Status == "pending" && !m.SendAt.After(now) {
			out = append(out, &m)
		}
		return nil
	})
	return out, err
}

// CancelScheduledByFolderRef deletes the scheduled message whose Scheduled-folder
// projection (owner + folder uid) was expunged, so canceling from any surface's
// folder view cancels the send. Returns true if a matching record was removed.
func (d *DB) CancelScheduledByFolderRef(owner string, uid uint32) (bool, error) {
	var target string
	err := d.ForEach(BucketScheduled, func(_ string, value []byte) error {
		var m ScheduledMessage
		if err := json.Unmarshal(value, &m); err != nil {
			return nil
		}
		if m.Owner == owner && m.FolderUID == uid {
			target = m.ID
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if target == "" {
		return false, nil
	}
	if err := d.Delete(BucketScheduled, target); err != nil {
		return false, err
	}
	return true, nil
}

// ResetStaleScheduledMessages flips messages stuck in 'sending' (claimed before
// the given cutoff, e.g. by a node that crashed mid-release) back to 'pending' so
// the release loop retries them. Returns how many were reset.
func (d *DB) ResetStaleScheduledMessages(before time.Time) (int, error) {
	var stale []*ScheduledMessage
	err := d.ForEach(BucketScheduled, func(_ string, value []byte) error {
		var m ScheduledMessage
		if err := json.Unmarshal(value, &m); err != nil {
			return nil
		}
		if m.Status == "sending" && m.ClaimedAt.Before(before) {
			stale = append(stale, &m)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, m := range stale {
		m.Status = "pending"
		if err := d.Put(BucketScheduled, m.ID, m); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
}

// CreateRecoverableItem persists a recoverable (soft-deleted) item, stamping
// DeletedAt when unset.
func (d *DB) CreateRecoverableItem(m *RecoverableItem) error {
	if m.DeletedAt.IsZero() {
		m.DeletedAt = time.Now().UTC()
	}
	return d.Put(BucketRecoverable, m.ID, m)
}

// GetRecoverableItem retrieves a recoverable item by id.
func (d *DB) GetRecoverableItem(id string) (*RecoverableItem, error) {
	var m RecoverableItem
	if err := d.Get(BucketRecoverable, id, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteRecoverableItem removes a recoverable item record.
func (d *DB) DeleteRecoverableItem(id string) error {
	return d.Delete(BucketRecoverable, id)
}

// ListRecoverableByOwner returns all recoverable items owned by the given mailbox.
func (d *DB) ListRecoverableByOwner(owner string) ([]*RecoverableItem, error) {
	var out []*RecoverableItem
	err := d.ForEach(BucketRecoverable, func(_ string, value []byte) error {
		var m RecoverableItem
		if err := json.Unmarshal(value, &m); err != nil {
			return nil // skip malformed
		}
		if m.Owner == owner {
			out = append(out, &m)
		}
		return nil
	})
	return out, err
}

// ListExpiredRecoverableItems returns items deleted at or before the cutoff, so
// the retention cleaner can purge them.
func (d *DB) ListExpiredRecoverableItems(cutoff time.Time) ([]*RecoverableItem, error) {
	var out []*RecoverableItem
	err := d.ForEach(BucketRecoverable, func(_ string, value []byte) error {
		var m RecoverableItem
		if err := json.Unmarshal(value, &m); err != nil {
			return nil // skip malformed
		}
		if !m.DeletedAt.After(cutoff) {
			out = append(out, &m)
		}
		return nil
	})
	return out, err
}

// FindRecoverableByFolderRef returns the recoverable item whose Recoverable-Items
// projection (owner + folder uid) matches, or nil when none does. It backs both
// webmail restore (it needs OriginalFolder) and the move/expunge-out cleanup.
func (d *DB) FindRecoverableByFolderRef(owner string, uid uint32) (*RecoverableItem, error) {
	var found *RecoverableItem
	err := d.ForEach(BucketRecoverable, func(_ string, value []byte) error {
		var m RecoverableItem
		if err := json.Unmarshal(value, &m); err != nil {
			return nil
		}
		if m.Owner == owner && m.FolderUID == uid {
			cp := m
			found = &cp
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// GetAlias retrieves an alias by domain and local part
func (d *DB) GetAlias(domain, localPart string) (*AliasData, error) {
	key := domain + ":" + strings.ToLower(localPart)
	var alias AliasData
	if err := d.Get(BucketAliases, key, &alias); err != nil {
		return nil, err
	}
	return &alias, nil
}

// ResolveAlias resolves an alias to its target address
func (d *DB) ResolveAlias(domain, localPart string) (string, error) {
	alias, err := d.GetAlias(domain, localPart)
	if err != nil {
		return "", err
	}
	if alias == nil || !alias.IsActive {
		return "", nil
	}
	return alias.Target, nil
}

// ListAliases returns all aliases
func (d *DB) ListAliases() ([]*AliasData, error) {
	var aliases []*AliasData
	err := d.ForEach(BucketAliases, func(key string, value []byte) error {
		var alias AliasData
		if err := json.Unmarshal(value, &alias); err != nil {
			return err
		}
		aliases = append(aliases, &alias)
		return nil
	})
	return aliases, err
}

// CreateAlias creates a new alias
func (d *DB) CreateAlias(alias *AliasData) error {
	if alias.CreatedAt.IsZero() {
		alias.CreatedAt = time.Now()
	}
	key := alias.Domain + ":" + strings.ToLower(alias.Alias)
	return d.Put(BucketAliases, key, alias)
}

// UpdateAlias updates an existing alias
func (d *DB) UpdateAlias(alias *AliasData) error {
	key := alias.Domain + ":" + strings.ToLower(alias.Alias)
	return d.Put(BucketAliases, key, alias)
}

// DeleteAlias removes an alias
func (d *DB) DeleteAlias(domain, localPart string) error {
	key := domain + ":" + strings.ToLower(localPart)
	return d.Delete(BucketAliases, key)
}

// --- Client Session Operations ---

// CreateClientSession stores a new client session.
func (d *DB) CreateClientSession(session *ClientSession) error {
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now()
	}
	session.LastActive = session.CreatedAt
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(BucketClientSessions))
		if err != nil {
			return fmt.Errorf("failed to create bucket: %w", err)
		}
		return b.Put([]byte(session.ID), data)
	})
}

// GetClientSession retrieves a client session by ID.
func (d *DB) GetClientSession(id string) (*ClientSession, error) {
	var session ClientSession
	if err := d.Get(BucketClientSessions, id, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// UpdateClientSession updates an existing client session.
func (d *DB) UpdateClientSession(session *ClientSession) error {
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketClientSessions))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketClientSessions)
		}
		return b.Put([]byte(session.ID), data)
	})
}

// DeleteClientSession removes a client session.
func (d *DB) DeleteClientSession(id string) error {
	return d.Delete(BucketClientSessions, id)
}

// ListClientSessionsByEmail returns all non-revoked sessions for an email.
func (d *DB) ListClientSessionsByEmail(email string) ([]*ClientSession, error) {
	var sessions []*ClientSession
	err := d.ForEach(BucketClientSessions, func(key string, value []byte) error {
		var session ClientSession
		if err := json.Unmarshal(value, &session); err != nil {
			return err
		}
		if session.Email == email && !session.Revoked {
			sessions = append(sessions, &session)
		}
		return nil
	})
	// If bucket doesn't exist yet, return empty list instead of error
	if err != nil && strings.Contains(err.Error(), "bucket not found: "+BucketClientSessions) {
		return sessions, nil
	}
	return sessions, err
}

// RevokeClientSession marks a session as revoked.
func (d *DB) RevokeClientSession(id string) error {
	session, err := d.GetClientSession(id)
	if err != nil {
		return err
	}
	session.Revoked = true
	return d.UpdateClientSession(session)
}

// CleanupExpiredSessions removes sessions older than maxAge and revoked sessions.
// Sessions are considered expired if they haven't been active within maxAge.
func (d *DB) CleanupExpiredSessions(maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge)
	var toDelete []string
	err := d.ForEach(BucketClientSessions, func(key string, value []byte) error {
		var session ClientSession
		if err := json.Unmarshal(value, &session); err != nil {
			return err
		}
		// Delete revoked sessions or sessions past their activity cutoff
		if session.Revoked || session.LastActive.Before(cutoff) {
			toDelete = append(toDelete, key)
		}
		return nil
	})
	// If bucket doesn't exist, nothing to cleanup
	if err != nil && strings.Contains(err.Error(), "bucket not found: "+BucketClientSessions) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, id := range toDelete {
		if err := d.Delete(BucketClientSessions, id); err != nil {
			// Best-effort cleanup; session will be retried on next cleanup
			_ = err
		}
	}
	return nil
}

// easDeviceKey is the bbolt key for an EAS device partnership: the owning email
// and the device id joined by a NUL, so ListEASDevicesByEmail can prefix-scan.
func easDeviceKey(email, deviceID string) string {
	return email + "\x00" + deviceID
}

// PutEASDevice creates or updates an EAS device partnership.
func (d *DB) PutEASDevice(dev *EASDevice) error {
	return d.Put(BucketEASDevices, easDeviceKey(dev.Email, dev.DeviceID), dev)
}

// GetEASDevice returns the partnership for (email, deviceID), or a wrapped
// ErrNotFound when none exists.
func (d *DB) GetEASDevice(email, deviceID string) (*EASDevice, error) {
	var dev EASDevice
	if err := d.Get(BucketEASDevices, easDeviceKey(email, deviceID), &dev); err != nil {
		return nil, err
	}
	return &dev, nil
}

// ListEASDevicesByEmail returns every partnership owned by email.
func (d *DB) ListEASDevicesByEmail(email string) ([]*EASDevice, error) {
	var devices []*EASDevice
	err := d.ForEach(BucketEASDevices, func(_ string, value []byte) error {
		var dev EASDevice
		if err := json.Unmarshal(value, &dev); err != nil {
			return err
		}
		if dev.Email == email {
			devices = append(devices, &dev)
		}
		return nil
	})
	return devices, err
}

// ListAllEASDevices returns every device partnership across all accounts.
// It is the unfiltered counterpart of ListEASDevicesByEmail and is used by
// admin views that aggregate last-sync activity across the deployment.
func (d *DB) ListAllEASDevices() ([]*EASDevice, error) {
	var devices []*EASDevice
	err := d.ForEach(BucketEASDevices, func(_ string, value []byte) error {
		var dev EASDevice
		if err := json.Unmarshal(value, &dev); err != nil {
			return err
		}
		devices = append(devices, &dev)
		return nil
	})
	return devices, err
}

// DeleteEASDevice removes an EAS device partnership.
func (d *DB) DeleteEASDevice(email, deviceID string) error {
	return d.Delete(BucketEASDevices, easDeviceKey(email, deviceID))
}

// GetTLSCacheEntry returns the raw bytes stored under key, or a wrapped
// ErrNotFound when the key is absent. The value is stored verbatim (no JSON
// envelope) so certificate bundles and account keys round-trip byte-for-byte.
func (d *DB) GetTLSCacheEntry(key string) ([]byte, error) {
	var out []byte
	err := d.bolt.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketTLSCache))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketTLSCache)
		}
		data := b.Get([]byte(key))
		if data == nil {
			return fmt.Errorf("tls cache key %q not found: %w", key, ErrNotFound)
		}
		out = make([]byte, len(data)) // copy: bbolt's slice is only valid within the txn
		copy(out, data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PutTLSCacheEntry stores raw bytes under key, overwriting any existing value.
func (d *DB) PutTLSCacheEntry(key string, data []byte) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketTLSCache))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketTLSCache)
		}
		return b.Put([]byte(key), data)
	})
}

// DeleteTLSCacheEntry removes key from the TLS cache; absence is not an error.
func (d *DB) DeleteTLSCacheEntry(key string) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketTLSCache))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketTLSCache)
		}
		return b.Delete([]byte(key))
	})
}

// ListTLSCacheKeys returns every TLS-cache key with the given prefix (empty
// prefix = all keys) in ascending key order. The bbolt cursor iterates in
// byte-sorted order, so the result is sorted without an extra step.
func (d *DB) ListTLSCacheKeys(prefix string) ([]string, error) {
	var keys []string
	if err := d.ForEachPrefix(BucketTLSCache, prefix, func(key string, _ []byte) error {
		keys = append(keys, key)
		return nil
	}); err != nil {
		return nil, err
	}
	return keys, nil
}

// StatTLSCacheEntry returns the byte size of the value under key (and a zero
// modified time, since bbolt keeps no per-key timestamp), or a wrapped
// ErrNotFound when the key is absent.
func (d *DB) StatTLSCacheEntry(key string) (int64, time.Time, error) {
	var size int64
	err := d.bolt.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketTLSCache))
		if b == nil {
			return fmt.Errorf("bucket not found: %s", BucketTLSCache)
		}
		data := b.Get([]byte(key))
		if data == nil {
			return fmt.Errorf("tls cache key %q not found: %w", key, ErrNotFound)
		}
		size = int64(len(data))
		return nil
	})
	if err != nil {
		return 0, time.Time{}, err
	}
	return size, time.Time{}, nil
}

// LockTLSCache holds an in-process lease for name. A single-process bbolt store
// only coordinates goroutines within this process, so a map-backed lease with
// owner + expiry mirrors the postgres multi-node contract: the owner may
// re-acquire its own live lease, and any caller may steal a lease past its
// expiry so a crashed holder cannot wedge issuance for the process.
func (d *DB) LockTLSCache(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := time.Now()
	d.tlsLockMu.Lock()
	defer d.tlsLockMu.Unlock()
	if d.tlsLocks == nil {
		d.tlsLocks = make(map[string]tlsLockEntry)
	}
	if cur, held := d.tlsLocks[name]; held && cur.expiresAt.After(now) && cur.owner != owner {
		return false, nil // live lease held by another owner
	}
	d.tlsLocks[name] = tlsLockEntry{owner: owner, expiresAt: now.Add(ttl)}
	return true, nil
}

// UnlockTLSCache releases name when held by owner; a lock held by another owner
// (or nobody) is left untouched and is not an error.
func (d *DB) UnlockTLSCache(ctx context.Context, name, owner string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.tlsLockMu.Lock()
	defer d.tlsLockMu.Unlock()
	if cur, held := d.tlsLocks[name]; held && cur.owner == owner {
		delete(d.tlsLocks, name)
	}
	return nil
}

// RBAC: Role management ---------------------------------------------------------

func (d *DB) CreateRole(role *Role) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(BucketRoles))
		if err != nil {
			return err
		}
		role.CreatedAt = time.Now()
		role.UpdatedAt = role.CreatedAt
		data, err := json.Marshal(role)
		if err != nil {
			return err
		}
		return b.Put([]byte(role.ID), data)
	})
}

func (d *DB) GetRole(id string) (*Role, error) {
	var role *Role
	err := d.bolt.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketRoles))
		if b == nil {
			return ErrNotFound
		}
		data := b.Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &role)
	})
	if err != nil {
		return nil, err
	}
	return role, nil
}

func (d *DB) ListRoles() ([]*Role, error) {
	var roles []*Role
	err := d.bolt.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketRoles))
		if b == nil {
			return nil // empty bucket = no roles
		}
		return b.ForEach(func(k, v []byte) error {
			var role Role
			if err := json.Unmarshal(v, &role); err != nil {
				return err
			}
			roles = append(roles, &role)
			return nil
		})
	})
	return roles, err
}

func (d *DB) UpdateRole(role *Role) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketRoles))
		if b == nil {
			return ErrNotFound
		}
		existing := b.Get([]byte(role.ID))
		if existing == nil {
			return ErrNotFound
		}
		role.UpdatedAt = time.Now()
		data, err := json.Marshal(role)
		if err != nil {
			return err
		}
		return b.Put([]byte(role.ID), data)
	})
}

func (d *DB) DeleteRole(id string) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketRoles))
		if b == nil {
			return ErrNotFound
		}
		if b.Get([]byte(id)) == nil {
			return ErrNotFound
		}
		// Delete role
		if err := b.Delete([]byte(id)); err != nil {
			return err
		}
		// Delete all permissions for this role
		permBucket := tx.Bucket([]byte(BucketRolePermissions))
		if permBucket != nil {
			prefix := []byte(id + ":")
			toDelete := [][]byte{}
			permBucket.ForEach(func(k, v []byte) error { //nolint:errcheck
				if bytes.HasPrefix(k, prefix) {
					toDelete = append(toDelete, k)
				}
				return nil
			})
			for _, k := range toDelete {
				if err := permBucket.Delete(k); err != nil {
					return err
				}
			}
		}
		// Delete all user-role assignments for this role
		urBucket := tx.Bucket([]byte(BucketUserRoles))
		if urBucket != nil {
			prefix := []byte(":" + id)
			toDelete := [][]byte{}
			_ = urBucket.ForEach(func(k, v []byte) error { //nolint:errcheck
				if bytes.HasSuffix(k, prefix) {
					toDelete = append(toDelete, k)
				}
				return nil
			})
			for _, k := range toDelete {
				if err := urBucket.Delete(k); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// RBAC: Permission management --------------------------------------------------

func (d *DB) GetRolePermissions(roleID string) ([]*RolePermission, error) {
	var perms []*RolePermission
	err := d.bolt.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(BucketRolePermissions))
		if b == nil {
			return nil
		}
		prefix := []byte(roleID + ":")
		return b.ForEach(func(k, v []byte) error {
			if !bytes.HasPrefix(k, prefix) {
				return nil
			}
			var perm RolePermission
			if err := json.Unmarshal(v, &perm); err != nil {
				return err
			}
			perms = append(perms, &perm)
			return nil
		})
	})
	return perms, err
}

func (d *DB) SetRolePermissions(roleID string, perms []*RolePermission) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		// Verify role exists
		roleBucket := tx.Bucket([]byte(BucketRoles))
		if roleBucket == nil || roleBucket.Get([]byte(roleID)) == nil {
			return ErrNotFound
		}
		// Delete existing permissions for this role
		var permBucket *bbolt.Bucket
		var err error
		permBucket = tx.Bucket([]byte(BucketRolePermissions))
		if permBucket != nil {
			prefix := []byte(roleID + ":")
			toDelete := [][]byte{}
			permBucket.ForEach(func(k, v []byte) error { //nolint:errcheck
				if bytes.HasPrefix(k, prefix) {
					toDelete = append(toDelete, copyBytes(k))
				}
				return nil
			})
			for _, k := range toDelete {
				if permBucket.Delete(k) != nil {
					return err
				}
			}
		} else {
			permBucket, err = tx.CreateBucketIfNotExists([]byte(BucketRolePermissions))
			if err != nil {
				return err
			}
		}
		// Insert new permissions
		for _, p := range perms {
			p.RoleID = roleID
			data, err := json.Marshal(p)
			if err != nil {
				return err
			}
			key := []byte(roleID + ":" + p.Permission)
			if err := permBucket.Put(key, data); err != nil {
				return err
			}
		}
		return nil
	})
}

// copyBytes returns an independent copy of b.
func copyBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// RBAC: User-role assignment ---------------------------------------------------

func (d *DB) AssignRoleToUser(userID, roleID string) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		// Verify role exists
		roleBucket := tx.Bucket([]byte(BucketRoles))
		if roleBucket == nil || roleBucket.Get([]byte(roleID)) == nil {
			return ErrNotFound
		}
		urBucket, err := tx.CreateBucketIfNotExists([]byte(BucketUserRoles))
		if err != nil {
			return err
		}
		rel := AdminRoleRelation{UserID: userID, RoleID: roleID}
		data, err := json.Marshal(rel)
		if err != nil {
			return err
		}
		key := []byte(userID + ":" + roleID)
		return urBucket.Put(key, data)
	})
}

func (d *DB) RemoveRoleFromUser(userID, roleID string) error {
	return d.bolt.Update(func(tx *bbolt.Tx) error {
		urBucket := tx.Bucket([]byte(BucketUserRoles))
		if urBucket == nil {
			return ErrNotFound
		}
		key := []byte(userID + ":" + roleID)
		if urBucket.Get(key) == nil {
			return ErrNotFound
		}
		return urBucket.Delete(key)
	})
}

func (d *DB) GetUserRoles(userID string) ([]*Role, error) {
	var roles []*Role
	err := d.bolt.View(func(tx *bbolt.Tx) error {
		urBucket := tx.Bucket([]byte(BucketUserRoles))
		if urBucket == nil {
			return nil
		}
		roleBucket := tx.Bucket([]byte(BucketRoles))
		if roleBucket == nil {
			return nil
		}
		prefix := []byte(userID + ":")
		return urBucket.ForEach(func(k, v []byte) error {
			if !bytes.HasPrefix(k, prefix) {
				return nil
			}
			var rel AdminRoleRelation
			if err := json.Unmarshal(v, &rel); err != nil {
				return err
			}
			roleData := roleBucket.Get([]byte(rel.RoleID))
			if roleData == nil {
				return nil // role was deleted after assignment
			}
			var role Role
			if err := json.Unmarshal(roleData, &role); err != nil {
				return err
			}
			roles = append(roles, &role)
			return nil
		})
	})
	return roles, err
}

func (d *DB) GetUsersByRole(roleID string) ([]string, error) {
	var users []string
	err := d.bolt.View(func(tx *bbolt.Tx) error {
		urBucket := tx.Bucket([]byte(BucketUserRoles))
		if urBucket == nil {
			return nil
		}
		suffix := []byte(":" + roleID)
		return urBucket.ForEach(func(k, v []byte) error {
			if !bytes.HasSuffix(k, suffix) {
				return nil
			}
			var rel AdminRoleRelation
			if err := json.Unmarshal(v, &rel); err != nil {
				return err
			}
			users = append(users, rel.UserID)
			return nil
		})
	})
	return users, err
}
