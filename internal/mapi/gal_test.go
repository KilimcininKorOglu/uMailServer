package mapi

import (
	"fmt"
	"testing"

	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/semcore"
)

// fakeStore is a minimal in-memory Store for GAL tests.
type fakeStore struct {
	domains  []*db.DomainData
	accounts map[string][]*db.AccountData
}

func (f *fakeStore) GetAccount(domain, localPart string) (*db.AccountData, error) {
	for _, a := range f.accounts[domain] {
		if a.LocalPart == localPart {
			return a, nil
		}
	}
	return nil, fmt.Errorf("account not found")
}

func (f *fakeStore) ListAccountsByDomain(domain string) ([]*db.AccountData, error) {
	return f.accounts[domain], nil
}

func (f *fakeStore) ListDomains() ([]*db.DomainData, error) { return f.domains, nil }

// fakePolicy reports nothing hidden and no resource overrides.
type fakePolicy struct{}

func (fakePolicy) GetResource(semcore.ResourceId) (*semcore.ResourcePolicy, error) {
	return nil, nil
}

// galServer returns a Server backed by n active user accounts in one domain.
func galServer(n int) *Server {
	accts := make([]*db.AccountData, n)
	for i := range accts {
		accts[i] = &db.AccountData{
			LocalPart:   fmt.Sprintf("user%04d", i),
			Domain:      "x.test",
			Email:       fmt.Sprintf("user%04d@x.test", i),
			DisplayName: fmt.Sprintf("User %04d", i),
			IsActive:    true,
		}
	}
	return NewServer(&fakeStore{
		domains:  []*db.DomainData{{Name: "x.test", IsActive: true}},
		accounts: map[string][]*db.AccountData{"x.test": accts},
	}, fakePolicy{})
}

// TestResolveGALFullNotCapped checks the complete address book is served
// uncapped. The OAB and the binary NSPI GAL are the whole directory, not a
// search result, so the 100-entry search cap must not apply to the empty query;
// capping it shipped a truncated address book to any organization with more
// than 100 recipients.
func TestResolveGALFullNotCapped(t *testing.T) {
	const n = 150
	s := galServer(n)

	if full := s.ResolveGAL(""); len(full) != n {
		t.Errorf("full GAL = %d entries, want %d", len(full), n)
	}
	if search := s.ResolveGAL("user"); len(search) != 100 {
		t.Errorf("search results = %d entries, want 100 (capped)", len(search))
	}
}
