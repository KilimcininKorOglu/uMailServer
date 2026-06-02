package db

import (
	"sort"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func mkAccount(t *testing.T, d *DB, email string, admin, active bool) {
	t.Helper()
	local, domain := splitEmailForTest(email)
	if err := d.CreateAccount(&AccountData{
		Email:     email,
		LocalPart: local,
		Domain:    domain,
		IsAdmin:   admin,
		IsActive:  active,
	}); err != nil {
		t.Fatalf("CreateAccount(%s): %v", email, err)
	}
}

func splitEmailForTest(email string) (string, string) {
	for i := len(email) - 1; i >= 0; i-- {
		if email[i] == '@' {
			return email[:i], email[i+1:]
		}
	}
	return email, ""
}

func sortedEmails(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestExpandMailGroup(t *testing.T) {
	d, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if cerr := d.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()

	// Seed accounts in ex.test.
	mkAccount(t, d, "alice@ex.test", false, true)
	mkAccount(t, d, "bob@ex.test", false, true)
	mkAccount(t, d, "boss@ex.test", true, true)    // admin
	mkAccount(t, d, "ghost@ex.test", false, false) // inactive
	mkAccount(t, d, "sales-eu@ex.test", false, true)
	mkAccount(t, d, "sales-us@ex.test", false, true)

	t.Run("static returns explicit members", func(t *testing.T) {
		g := &MailGroup{IsActive: true, Members: []string{"x@ex.test", " y@ex.test ", ""}}
		got, err := d.ExpandMailGroup(g)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"x@ex.test", "y@ex.test"}
		if !equalStrings(got, want) {
			t.Errorf("static members = %v, want %v", got, want)
		}
	})

	t.Run("inactive group expands to nothing", func(t *testing.T) {
		g := &MailGroup{IsActive: false, Members: []string{"x@ex.test"}}
		got, err := d.ExpandMailGroup(g)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("inactive group = %v, want empty", got)
		}
	})

	t.Run("dynamic domain returns active accounts only", func(t *testing.T) {
		g := &MailGroup{IsActive: true, Dynamic: true, Domain: "ex.test"}
		got, err := d.ExpandMailGroup(g)
		if err != nil {
			t.Fatal(err)
		}
		want := sortedEmails([]string{"alice@ex.test", "bob@ex.test", "boss@ex.test", "sales-eu@ex.test", "sales-us@ex.test"})
		if !equalStrings(sortedEmails(got), want) {
			t.Errorf("dynamic = %v, want %v (inactive ghost must be excluded)", sortedEmails(got), want)
		}
	})

	t.Run("dynamic admin-only filter", func(t *testing.T) {
		g := &MailGroup{IsActive: true, Dynamic: true, Domain: "ex.test", DynamicAdminOnly: boolPtr(true)}
		got, err := d.ExpandMailGroup(g)
		if err != nil {
			t.Fatal(err)
		}
		if !equalStrings(got, []string{"boss@ex.test"}) {
			t.Errorf("admin-only = %v, want [boss@ex.test]", got)
		}
	})

	t.Run("dynamic local-part pattern", func(t *testing.T) {
		g := &MailGroup{IsActive: true, Dynamic: true, Domain: "ex.test", DynamicLocalPattern: "sales-*"}
		got, err := d.ExpandMailGroup(g)
		if err != nil {
			t.Fatal(err)
		}
		want := sortedEmails([]string{"sales-eu@ex.test", "sales-us@ex.test"})
		if !equalStrings(sortedEmails(got), want) {
			t.Errorf("pattern = %v, want %v", sortedEmails(got), want)
		}
	})
}
