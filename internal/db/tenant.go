package db

import (
	"encoding/json"
	"fmt"
	"time"
)

// TenantData is the top-level isolation boundary. A tenant owns one or more
// domains; every domain belongs to exactly one tenant (see DomainData.TenantID).
// Legacy single-domain deployments are backfilled so each domain gets its own
// tenant whose id equals the domain name.
type TenantData struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
	// Settings carries per-tenant branding/feature flags consumed by later
	// phases (logo, theme, login domain, feature toggles).
	Settings  map[string]string `json:"settings,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// CreateTenant persists a new tenant. ID is required and is the stable key.
func (d *DB) CreateTenant(t *TenantData) error {
	if t.ID == "" {
		return fmt.Errorf("tenant id is required")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	t.UpdatedAt = time.Now()
	return d.Put(BucketTenants, t.ID, t)
}

// GetTenant retrieves a tenant by id.
func (d *DB) GetTenant(id string) (*TenantData, error) {
	var t TenantData
	if err := d.Get(BucketTenants, id, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateTenant updates an existing tenant.
func (d *DB) UpdateTenant(t *TenantData) error {
	t.UpdatedAt = time.Now()
	return d.Put(BucketTenants, t.ID, t)
}

// DeleteTenant removes a tenant record. Callers are responsible for handling
// the tenant's domains first (cascade is a later-phase lifecycle concern).
func (d *DB) DeleteTenant(id string) error {
	return d.Delete(BucketTenants, id)
}

// ListTenants returns all tenants.
func (d *DB) ListTenants() ([]*TenantData, error) {
	var out []*TenantData
	err := d.ForEach(BucketTenants, func(_ string, value []byte) error {
		var t TenantData
		if err := json.Unmarshal(value, &t); err != nil {
			return err
		}
		out = append(out, &t)
		return nil
	})
	return out, err
}

// ListDomainsByTenant returns the domains owned by the given tenant.
func (d *DB) ListDomainsByTenant(tenantID string) ([]*DomainData, error) {
	all, err := d.ListDomains()
	if err != nil {
		return nil, err
	}
	var out []*DomainData
	for _, dom := range all {
		if dom.TenantID == tenantID {
			out = append(out, dom)
		}
	}
	return out, nil
}

// ensureSelfTenant guarantees that a single-domain tenant (id == domain name)
// exists, creating it if absent. Idempotent.
func (d *DB) ensureSelfTenant(domainName string) error {
	if _, err := d.GetTenant(domainName); err == nil {
		return nil // already exists
	}
	return d.CreateTenant(&TenantData{ID: domainName, Name: domainName, IsActive: true})
}

// EnsureTenantsForDomains backfills tenant ownership for any domain missing a
// TenantID (or referencing a tenant that no longer exists): it creates a
// single-domain tenant (id == domain name) and assigns it. Idempotent and safe
// to run on every startup. Returns the number of domains backfilled.
func (d *DB) EnsureTenantsForDomains() (int, error) {
	domains, err := d.ListDomains()
	if err != nil {
		return 0, err
	}
	backfilled := 0
	for _, dom := range domains {
		if dom.TenantID != "" {
			if _, gerr := d.GetTenant(dom.TenantID); gerr == nil {
				continue // already owned by an existing tenant
			}
		}
		if err := d.ensureSelfTenant(dom.Name); err != nil {
			return backfilled, err
		}
		dom.TenantID = dom.Name
		if err := d.UpdateDomain(dom); err != nil {
			return backfilled, err
		}
		backfilled++
	}
	return backfilled, nil
}
