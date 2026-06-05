package db

import (
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})
	return database
}

// TestTenantCRUD covers the basic tenant persistence roundtrip.
func TestTenantCRUD(t *testing.T) {
	database := openTestDB(t)

	if err := database.CreateTenant(&TenantData{ID: "t1", Name: "Acme", IsActive: true}); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := database.CreateTenant(&TenantData{ID: "", Name: "no-id"}); err == nil {
		t.Error("expected error creating a tenant with empty id")
	}

	got, err := database.GetTenant("t1")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Name != "Acme" || !got.IsActive {
		t.Errorf("unexpected tenant: %+v", got)
	}

	got.Name = "Acme Corp"
	if err := database.UpdateTenant(got); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	reread, err := database.GetTenant("t1")
	if err != nil {
		t.Fatalf("GetTenant after update: %v", err)
	}
	if reread.Name != "Acme Corp" {
		t.Errorf("update not persisted: %+v", reread)
	}

	list, err := database.ListTenants()
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 tenant, got %d", len(list))
	}

	if err := database.DeleteTenant("t1"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if _, err := database.GetTenant("t1"); err == nil {
		t.Error("expected error getting deleted tenant")
	}
}

// TestCreateDomainAssignsSelfTenant verifies the invariant that a domain
// created without a TenantID gets its own single-domain tenant (id == name).
func TestCreateDomainAssignsSelfTenant(t *testing.T) {
	database := openTestDB(t)

	if err := database.CreateDomain(&DomainData{Name: "solo.test", MaxAccounts: 5}); err != nil {
		t.Fatalf("CreateDomain: %v", err)
	}
	dom, err := database.GetDomain("solo.test")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if dom.TenantID != "solo.test" {
		t.Errorf("expected self-tenant id solo.test, got %q", dom.TenantID)
	}
	if _, err := database.GetTenant("solo.test"); err != nil {
		t.Errorf("self-tenant record should exist: %v", err)
	}
}

// TestEnsureTenantsForDomainsBackfill verifies legacy domains (no TenantID) are
// backfilled to their own tenant, and that the operation is idempotent.
func TestEnsureTenantsForDomainsBackfill(t *testing.T) {
	database := openTestDB(t)

	// Simulate a legacy domain written before tenants existed (bypass
	// CreateDomain's invariant by writing the bucket directly).
	if err := database.Put(BucketDomains, "legacy.test", &DomainData{Name: "legacy.test"}); err != nil {
		t.Fatalf("seed legacy domain: %v", err)
	}

	n, err := database.EnsureTenantsForDomains()
	if err != nil {
		t.Fatalf("EnsureTenantsForDomains: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 domain backfilled, got %d", n)
	}
	dom, err := database.GetDomain("legacy.test")
	if err != nil {
		t.Fatalf("GetDomain legacy: %v", err)
	}
	if dom.TenantID != "legacy.test" {
		t.Errorf("legacy domain not assigned its tenant: %q", dom.TenantID)
	}
	if _, err := database.GetTenant("legacy.test"); err != nil {
		t.Errorf("backfilled tenant record missing: %v", err)
	}

	// Idempotent: a second run backfills nothing.
	if n2, err := database.EnsureTenantsForDomains(); err != nil || n2 != 0 {
		t.Errorf("expected idempotent backfill (0), got n=%d err=%v", n2, err)
	}
}

// TestListDomainsByTenant verifies a tenant can own multiple domains.
func TestListDomainsByTenant(t *testing.T) {
	database := openTestDB(t)

	if err := database.CreateTenant(&TenantData{ID: "shared", Name: "Shared", IsActive: true}); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := database.CreateDomain(&DomainData{Name: "a.test", TenantID: "shared"}); err != nil {
		t.Fatalf("CreateDomain a: %v", err)
	}
	if err := database.CreateDomain(&DomainData{Name: "b.test", TenantID: "shared"}); err != nil {
		t.Fatalf("CreateDomain b: %v", err)
	}
	// A third domain on its own tenant must not appear under "shared".
	if err := database.CreateDomain(&DomainData{Name: "c.test"}); err != nil {
		t.Fatalf("CreateDomain c: %v", err)
	}

	domains, err := database.ListDomainsByTenant("shared")
	if err != nil {
		t.Fatalf("ListDomainsByTenant: %v", err)
	}
	if len(domains) != 2 {
		t.Errorf("expected 2 domains for tenant shared, got %d", len(domains))
	}
}
