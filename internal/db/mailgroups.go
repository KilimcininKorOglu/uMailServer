package db

import (
	"encoding/json"
	"path"
	"strings"
	"time"
)

// MailGroup is a distribution group: a single address whose mail fans out to a
// set of recipients. Membership is either an explicit static list (Dynamic ==
// false) or a live query over accounts (Dynamic == true).
type MailGroup struct {
	Email       string `json:"email"`
	LocalPart   string `json:"local_part"`
	Domain      string `json:"domain"`
	Description string `json:"description,omitempty"`
	IsActive    bool   `json:"is_active"`

	// Static membership: explicit recipient addresses.
	Members []string `json:"members,omitempty"`

	// Dynamic membership: when Dynamic is true, members are resolved at delivery
	// time from accounts matching all of the set criteria below.
	Dynamic             bool   `json:"dynamic"`
	DynamicDomain       string `json:"dynamic_domain,omitempty"`        // domain to scan; defaults to Domain
	DynamicAdminOnly    *bool  `json:"dynamic_admin_only,omitempty"`    // nil = any; true = admins only; false = non-admins only
	DynamicLocalPattern string `json:"dynamic_local_pattern,omitempty"` // glob match on local-part (e.g. "sales-*")

	// SenderPolicy controls who may send to the group: "internal" (only senders
	// in a local domain) or "anyone".
	SenderPolicy string `json:"sender_policy"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func mailGroupKey(domain, localPart string) string {
	return strings.ToLower(domain) + ":" + strings.ToLower(localPart)
}

// GetMailGroup retrieves a mail group by domain and local part.
func (d *DB) GetMailGroup(domain, localPart string) (*MailGroup, error) {
	var group MailGroup
	if err := d.Get(BucketMailGroups, mailGroupKey(domain, localPart), &group); err != nil {
		return nil, err
	}
	return &group, nil
}

// ListMailGroups returns all mail groups.
func (d *DB) ListMailGroups() ([]*MailGroup, error) {
	var groups []*MailGroup
	err := d.ForEach(BucketMailGroups, func(_ string, value []byte) error {
		var group MailGroup
		if err := json.Unmarshal(value, &group); err != nil {
			return err
		}
		groups = append(groups, &group)
		return nil
	})
	return groups, err
}

// CreateMailGroup stores a new mail group.
func (d *DB) CreateMailGroup(group *MailGroup) error {
	now := time.Now()
	if group.CreatedAt.IsZero() {
		group.CreatedAt = now
	}
	group.UpdatedAt = now
	return d.Put(BucketMailGroups, mailGroupKey(group.Domain, group.LocalPart), group)
}

// UpdateMailGroup persists changes to an existing mail group.
func (d *DB) UpdateMailGroup(group *MailGroup) error {
	group.UpdatedAt = time.Now()
	return d.Put(BucketMailGroups, mailGroupKey(group.Domain, group.LocalPart), group)
}

// DeleteMailGroup removes a mail group.
func (d *DB) DeleteMailGroup(domain, localPart string) error {
	return d.Delete(BucketMailGroups, mailGroupKey(domain, localPart))
}

// ExpandMailGroup resolves a group to its current recipient addresses. Static
// groups return their explicit members; dynamic groups query active accounts in
// the scan domain and apply the admin-only and local-part criteria.
func (d *DB) ExpandMailGroup(group *MailGroup) ([]string, error) {
	if group == nil || !group.IsActive {
		return nil, nil
	}
	if !group.Dynamic {
		out := make([]string, 0, len(group.Members))
		for _, m := range group.Members {
			if m = strings.TrimSpace(m); m != "" {
				out = append(out, m)
			}
		}
		return out, nil
	}

	scanDomain := group.DynamicDomain
	if scanDomain == "" {
		scanDomain = group.Domain
	}
	accounts, err := d.ListAccountsByDomain(scanDomain)
	if err != nil {
		return nil, err
	}
	pattern := strings.ToLower(strings.TrimSpace(group.DynamicLocalPattern))
	out := make([]string, 0, len(accounts))
	for _, a := range accounts {
		if !a.IsActive {
			continue
		}
		if group.DynamicAdminOnly != nil && a.IsAdmin != *group.DynamicAdminOnly {
			continue
		}
		if pattern != "" {
			ok, mErr := path.Match(pattern, strings.ToLower(a.LocalPart))
			if mErr != nil || !ok {
				continue
			}
		}
		out = append(out, a.Email)
	}
	return out, nil
}
