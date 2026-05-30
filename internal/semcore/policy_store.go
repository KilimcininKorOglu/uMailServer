package semcore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.etcd.io/bbolt"
)

// Bucket names for policy storage
const (
	bucketRule         = "rules"
	bucketOOF          = "oof"
	bucketResource     = "resources"
	bucketNotification = "notifications"
)

// BoltPolicyStore is the canonical policy store. It owns bbolt buckets for
// rules, OOF, resources, and notification policies.
// All operations are safe for concurrent use; callers must hold the
// appropriate lock for the scope they are operating in.
type BoltPolicyStore struct {
	db *bbolt.DB
	mu sync.RWMutex
}

// NewBoltPolicyStore opens the policy store, creating all buckets if needed.
// It returns the store ready for use. The db should be opened with bbolt.Open.
func NewBoltPolicyStore(db *bbolt.DB) (*BoltPolicyStore, error) {
	if err := db.Update(func(tx *bbolt.Tx) error {
		buckets := []string{
			bucketRule,
			bucketOOF,
			bucketResource,
			bucketNotification,
		}
		for _, b := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(b)); err != nil {
				return fmt.Errorf("create bucket %s: %w", b, err)
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("NewBoltPolicyStore: %w", err)
	}
	return &BoltPolicyStore{db: db}, nil
}

// ---------------------------------------------------------------------------
// Rule storage
// ---------------------------------------------------------------------------

// ruleKey returns the bucket key for a rule.
func ruleKey(id RuleId) []byte {
	return []byte(id.raw)
}

// ListRules returns all rules for a mailbox, sorted by priority.
func (s *BoltPolicyStore) ListRules(mailboxID MailboxId) ([]*Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Rule
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketRule))
		c := b.Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rule Rule
			if err := json.Unmarshal(v, &rule); err != nil {
				continue
			}
			if !rule.MailboxID.Equal(mailboxID) {
				continue
			}
			result = append(result, &rule)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort by priority (lower = higher precedence)
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Priority < result[i].Priority {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result, nil
}

// GetRule returns a rule by RuleId.
func (s *BoltPolicyStore) GetRule(id RuleId) (*Rule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rule *Rule
	err := s.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket([]byte(bucketRule)).Get(ruleKey(id))
		if data == nil {
			return fmt.Errorf("rule not found: %s", id.String())
		}
		var r Rule
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		rule = &r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rule, nil
}

// PutRule stores a rule. If the rule has a zero ChangeKey, a new one is assigned.
func (s *BoltPolicyStore) PutRule(rule *Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if rule.ChangeKey.IsZero() {
		changeKey, err := NewRuleChangeKey(generateChangeKey())
		if err != nil {
			return err
		}
		rule.ChangeKey = changeKey
	}

	data, err := json.Marshal(rule)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketRule)).Put(ruleKey(rule.ID), data)
	})
}

// DeleteRule removes a rule.
func (s *BoltPolicyStore) DeleteRule(id RuleId) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketRule)).Delete(ruleKey(id))
	})
}

// ---------------------------------------------------------------------------
// OOF storage
// ---------------------------------------------------------------------------

// oofKey returns the bucket key for an OOF policy.
func oofKey(id OOFId) []byte {
	return []byte(id.raw)
}

// GetOOF returns the OOF policy for a mailbox (OOFId == MailboxId).
func (s *BoltPolicyStore) GetOOF(id OOFId) (*OOFPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var policy *OOFPolicy
	err := s.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket([]byte(bucketOOF)).Get(oofKey(id))
		if data == nil {
			return fmt.Errorf("OOF policy not found: %s", id.String())
		}
		var p OOFPolicy
		if err := json.Unmarshal(data, &p); err != nil {
			return err
		}
		policy = &p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return policy, nil
}

// PutOOF stores an OOF policy. If the policy has a zero ChangeKey, a new one is assigned.
func (s *BoltPolicyStore) PutOOF(policy *OOFPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if policy.ChangeKey.IsZero() {
		changeKey, err := NewOOFChangeKey(generateChangeKey())
		if err != nil {
			return err
		}
		policy.ChangeKey = changeKey
	}

	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketOOF)).Put(oofKey(policy.ID), data)
	})
}

// DeleteOOF removes an OOF policy.
func (s *BoltPolicyStore) DeleteOOF(id OOFId) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketOOF)).Delete(oofKey(id))
	})
}

// ListActiveOOF returns all OOF policies that are currently active.
func (s *BoltPolicyStore) ListActiveOOF() ([]*OOFPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*OOFPolicy
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketOOF))
		return b.ForEach(func(k, v []byte) error {
			var policy OOFPolicy
			if err := json.Unmarshal(v, &policy); err != nil {
				return nil
			}
			if policy.IsActiveNow() {
				result = append(result, &policy)
			}
			return nil
		})
	})
	return result, err
}

// ---------------------------------------------------------------------------
// Resource policy storage
// ---------------------------------------------------------------------------

// resourceKey returns the bucket key for a resource policy.
func resourceKey(id ResourceId) []byte {
	return []byte(id.raw)
}

// ListResources returns all resource policies.
func (s *BoltPolicyStore) ListResources() ([]*ResourcePolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*ResourcePolicy
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketResource))
		return b.ForEach(func(k, v []byte) error {
			var policy ResourcePolicy
			if err := json.Unmarshal(v, &policy); err != nil {
				return nil
			}
			result = append(result, &policy)
			return nil
		})
	})
	return result, err
}

// GetResource returns a resource policy by ResourceId.
func (s *BoltPolicyStore) GetResource(id ResourceId) (*ResourcePolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var policy *ResourcePolicy
	err := s.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket([]byte(bucketResource)).Get(resourceKey(id))
		if data == nil {
			return fmt.Errorf("resource policy not found: %s", id.String())
		}
		var p ResourcePolicy
		if err := json.Unmarshal(data, &p); err != nil {
			return err
		}
		policy = &p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return policy, nil
}

// PutResource stores a resource policy. If the policy has a zero ChangeKey, a new one is assigned.
func (s *BoltPolicyStore) PutResource(policy *ResourcePolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if policy.ChangeKey.IsZero() {
		changeKey, err := NewResourceChangeKey(generateChangeKey())
		if err != nil {
			return err
		}
		policy.ChangeKey = changeKey
	}

	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketResource)).Put(resourceKey(policy.ID), data)
	})
}

// DeleteResource removes a resource policy.
func (s *BoltPolicyStore) DeleteResource(id ResourceId) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketResource)).Delete(resourceKey(id))
	})
}

// ---------------------------------------------------------------------------
// Notification policy storage
// ---------------------------------------------------------------------------

// notificationKey returns the bucket key for a notification policy.
func notificationKey(id NotificationId) []byte {
	return []byte(id.raw)
}

// ListNotifications returns all notification policies for a mailbox.
func (s *BoltPolicyStore) ListNotifications(mailboxID MailboxId) ([]*NotificationPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*NotificationPolicy
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketNotification))
		c := b.Cursor()
		prefix := []byte(mailboxID.String() + "/")

		for k, v := c.Seek(prefix); k != nil && len(k) >= len(prefix) && string(k[:len(prefix)]) == mailboxID.String()+"/"; k, v = c.Next() {
			var policy NotificationPolicy
			if err := json.Unmarshal(v, &policy); err != nil {
				continue
			}
			result = append(result, &policy)
		}
		return nil
	})
	return result, err
}

// GetNotification returns a notification policy by NotificationId.
func (s *BoltPolicyStore) GetNotification(id NotificationId) (*NotificationPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var policy *NotificationPolicy
	err := s.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket([]byte(bucketNotification)).Get(notificationKey(id))
		if data == nil {
			return fmt.Errorf("notification policy not found: %s", id.String())
		}
		var p NotificationPolicy
		if err := json.Unmarshal(data, &p); err != nil {
			return err
		}
		policy = &p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return policy, nil
}

// PutNotification stores a notification policy. If the policy has a zero ChangeKey, a new one is assigned.
func (s *BoltPolicyStore) PutNotification(policy *NotificationPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if policy.ChangeKey.IsZero() {
		changeKey, err := NewNotificationChangeKey(generateChangeKey())
		if err != nil {
			return err
		}
		policy.ChangeKey = changeKey
	}

	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketNotification)).Put(notificationKey(policy.ID), data)
	})
}

// DeleteNotification removes a notification policy.
func (s *BoltPolicyStore) DeleteNotification(id NotificationId) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketNotification)).Delete(notificationKey(id))
	})
}

// ---------------------------------------------------------------------------
// File-based policy storage (for dataDir-based migration compatibility)
// ---------------------------------------------------------------------------

// FilePolicyStore provides file-based fallback for legacy compatibility.
// This allows the policy core to work with existing file-based vacation
// and sieve data while migrating to bbolt storage.
type FilePolicyStore struct {
	dataDir string
	logger  PolicyLogger
}

// PolicyLogger logs policy store operations.
type PolicyLogger interface {
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
	Error(msg string, args ...interface{})
}

// NewFilePolicyStore creates a file-based policy store.
func NewFilePolicyStore(dataDir string, logger PolicyLogger) (*FilePolicyStore, error) {
	if logger == nil {
		logger = &noopPolicyLogger{}
	}

	// Ensure directory structure
	dirs := []string{
		filepath.Join(dataDir, "rules"),
		filepath.Join(dataDir, "oof"),
		filepath.Join(dataDir, "resources"),
		filepath.Join(dataDir, "notifications"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", d, err)
		}
	}

	return &FilePolicyStore{
		dataDir: dataDir,
		logger:  logger,
	}, nil
}

// LoadRule loads a rule from file.
func (s *FilePolicyStore) LoadRule(id RuleId) (*Rule, error) {
	path := filepath.Join(s.dataDir, "rules", id.raw+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rule Rule
	if err := json.Unmarshal(data, &rule); err != nil {
		return nil, err
	}
	return &rule, nil
}

// SaveRule saves a rule to file.
func (s *FilePolicyStore) SaveRule(rule *Rule) error {
	path := filepath.Join(s.dataDir, "rules", rule.ID.raw+".json")
	data, err := json.MarshalIndent(rule, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// DeleteRule deletes a rule file.
func (s *FilePolicyStore) DeleteRule(id RuleId) error {
	path := filepath.Join(s.dataDir, "rules", id.raw+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadOOF loads an OOF policy from file.
func (s *FilePolicyStore) LoadOOF(id OOFId) (*OOFPolicy, error) {
	path := filepath.Join(s.dataDir, "oof", id.raw+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var policy OOFPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// SaveOOF saves an OOF policy to file.
func (s *FilePolicyStore) SaveOOF(policy *OOFPolicy) error {
	path := filepath.Join(s.dataDir, "oof", policy.ID.raw+".json")
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// DeleteOOF deletes an OOF policy file.
func (s *FilePolicyStore) DeleteOOF(id OOFId) error {
	path := filepath.Join(s.dataDir, "oof", id.raw+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadResource loads a resource policy from file.
func (s *FilePolicyStore) LoadResource(id ResourceId) (*ResourcePolicy, error) {
	path := filepath.Join(s.dataDir, "resources", id.raw+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var policy ResourcePolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// SaveResource saves a resource policy to file.
func (s *FilePolicyStore) SaveResource(policy *ResourcePolicy) error {
	path := filepath.Join(s.dataDir, "resources", policy.ID.raw+".json")
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// DeleteResource deletes a resource policy file.
func (s *FilePolicyStore) DeleteResource(id ResourceId) error {
	path := filepath.Join(s.dataDir, "resources", id.raw+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadNotification loads a notification policy from file.
func (s *FilePolicyStore) LoadNotification(id NotificationId) (*NotificationPolicy, error) {
	path := filepath.Join(s.dataDir, "notifications", id.raw+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var policy NotificationPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// SaveNotification saves a notification policy to file.
func (s *FilePolicyStore) SaveNotification(policy *NotificationPolicy) error {
	path := filepath.Join(s.dataDir, "notifications", policy.ID.raw+".json")
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// DeleteNotification deletes a notification policy file.
func (s *FilePolicyStore) DeleteNotification(id NotificationId) error {
	path := filepath.Join(s.dataDir, "notifications", id.raw+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// noopPolicyLogger is a no-op logger implementation.
type noopPolicyLogger struct{}

func (n *noopPolicyLogger) Info(msg string, args ...interface{})  { _ = msg }
func (n *noopPolicyLogger) Warn(msg string, args ...interface{})  { _ = msg }
func (n *noopPolicyLogger) Error(msg string, args ...interface{}) { _ = msg }
