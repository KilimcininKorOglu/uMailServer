package server

import (
	"sort"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/db"
)

// helperCreateMailGroup creates a mail group in the server's database.
func helperCreateMailGroup(t *testing.T, srv *Server, localPart, domain string, members []string, senderPolicy string) {
	t.Helper()
	group := &db.MailGroup{
		LocalPart:    localPart,
		Domain:       domain,
		Email:        localPart + "@" + domain,
		Description:  "test group",
		IsActive:     true,
		Members:      members,
		SenderPolicy: senderPolicy,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := srv.database.CreateMailGroup(group); err != nil {
		t.Fatalf("CreateMailGroup(%s@%s): %v", localPart, domain, err)
	}
}

// helperCreateDynamicMailGroup creates a dynamic mail group.
func helperCreateDynamicMailGroup(t *testing.T, srv *Server, localPart, domain string, dynamicDomain string, adminOnly *bool, pattern string, senderPolicy string) {
	t.Helper()
	group := &db.MailGroup{
		LocalPart:           localPart,
		Domain:              domain,
		Email:               localPart + "@" + domain,
		Description:         "test dynamic group",
		IsActive:            true,
		Dynamic:             true,
		DynamicDomain:       dynamicDomain,
		DynamicAdminOnly:    adminOnly,
		DynamicLocalPattern: pattern,
		SenderPolicy:        senderPolicy,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	if err := srv.database.CreateMailGroup(group); err != nil {
		t.Fatalf("CreateMailGroup(dynamic %s@%s): %v", localPart, domain, err)
	}
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

func sortedStrings(a []string) []string {
	out := append([]string(nil), a...)
	sort.Strings(out)
	return out
}

// TestExpandMailGroups_StaticGroups covers the basic static group expansion path
// with no sender policy restriction.
func TestExpandMailGroups_StaticGroups(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", false, false, 0, 0)
	helperCreateAccount(t, srv, "bob", "example.com", false, false, 0, 0)
	helperCreateAccount(t, srv, "carol", "example.com", false, false, 0, 0)

	// Static group: members are explicit addresses.
	helperCreateMailGroup(t, srv, "group", "example.com",
		[]string{"alice@example.com", "bob@example.com"}, "anyone")

	result := srv.expandMailGroups("carol@example.com",
		[]string{"group@example.com"})

	want := []string{"alice@example.com", "bob@example.com"}
	if got := sortedStrings(result); !equalStrings(got, want) {
		t.Errorf("static group expansion = %v, want %v", got, want)
	}
}

// TestExpandMailGroups_SenderPolicyInternal rejects an external sender
// when the group's sender policy is "internal".
func TestExpandMailGroups_SenderPolicyInternal(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", false, false, 0, 0)

	// Internal-only group.
	helperCreateMailGroup(t, srv, "internal-group", "example.com",
		[]string{"alice@example.com"}, "internal")

	// External sender → group should be silently dropped (policy rejects).
	result := srv.expandMailGroups("external@other.com",
		[]string{"internal-group@example.com"})

	if len(result) != 0 {
		t.Errorf("external sender to internal group = %v, want empty", result)
	}
}

// TestExpandMailGroups_SenderPolicyInternal_LocalSender covers the inverse:
// a local sender should be allowed through even when the policy is internal.
func TestExpandMailGroups_SenderPolicyInternal_LocalSender(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", false, false, 0, 0)
	helperCreateAccount(t, srv, "bob", "example.com", false, false, 0, 0)

	helperCreateMailGroup(t, srv, "internal-group", "example.com",
		[]string{"alice@example.com"}, "internal")

	// Local sender → allowed through.
	result := srv.expandMailGroups("bob@example.com",
		[]string{"internal-group@example.com"})

	want := []string{"alice@example.com"}
	if got := sortedStrings(result); !equalStrings(got, want) {
		t.Errorf("local sender to internal group = %v, want %v", got, want)
	}
}

// TestExpandMailGroups_NestedGroups covers depth-limited recursive expansion.
func TestExpandMailGroups_NestedGroups(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", false, false, 0, 0)
	helperCreateAccount(t, srv, "bob", "example.com", false, false, 0, 0)
	helperCreateAccount(t, srv, "carol", "example.com", false, false, 0, 0)

	// Level 1 group points to level 2 group.
	helperCreateMailGroup(t, srv, "level1", "example.com",
		[]string{"level2@example.com"}, "anyone")
	helperCreateMailGroup(t, srv, "level2", "example.com",
		[]string{"alice@example.com", "bob@example.com"}, "anyone")

	result := srv.expandMailGroups("carol@example.com",
		[]string{"level1@example.com"})

	want := []string{"alice@example.com", "bob@example.com"}
	if got := sortedStrings(result); !equalStrings(got, want) {
		t.Errorf("nested group = %v, want %v", got, want)
	}
}

// TestExpandMailGroups_CycleGuard covers the visited map preventing infinite
// recursion when group A contains group B which contains group A.
func TestExpandMailGroups_CycleGuard(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", false, false, 0, 0)

	// Cycle: group-a → group-b → group-a.
	helperCreateMailGroup(t, srv, "group-a", "example.com",
		[]string{"group-b@example.com", "alice@example.com"}, "anyone")
	helperCreateMailGroup(t, srv, "group-b", "example.com",
		[]string{"group-a@example.com"}, "anyone")

	// Should not infinite-loop; cycle is cut at the second visit.
	result := srv.expandMailGroups("alice@example.com",
		[]string{"group-a@example.com"})

	// alice is in group-a, but group-b is dropped because it was already
	// visited via group-a. So only alice should appear.
	want := []string{"alice@example.com"}
	if got := sortedStrings(result); !equalStrings(got, want) {
		t.Errorf("cycle guard = %v, want %v", got, want)
	}
}

// TestExpandMailGroups_DepthLimit covers the depth > 10 guard.
func TestExpandMailGroups_DepthLimit(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", false, false, 0, 0)

	// Build a chain of 12 groups (depth 0..11).
	// The last group (depth 11) contains alice, but the chain depth 12
	// exceeds the limit before alice is reached, so alice is dropped.
	const n = 12
	groups := make([]string, n)
	for i := 0; i < n; i++ {
		local := "g" + string(rune('a'+i))
		members := []string{}
		if i < n-1 {
			members = []string{"g" + string(rune('a'+i+1)) + "@example.com"}
		} else {
			members = []string{"alice@example.com"}
		}
		helperCreateMailGroup(t, srv, local, "example.com", members, "anyone")
		groups[i] = local + "@example.com"
	}

	result := srv.expandMailGroups("alice@example.com", []string{groups[0]})

	// g0..g10 each contain only the next group; none contain alice.
	// At depth 11, gk contains alice, but the call is at depth 12 → dropped.
	if len(result) != 0 {
		t.Errorf("depth-limit chain = %v, want empty (alice at depth 12 exceeds limit 10)", result)
	}
}

// TestExpandMailGroups_MixedRecipients covers the case where to contains both
// regular accounts and groups; regular accounts pass through unchanged.
func TestExpandMailGroups_MixedRecipients(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", true, false, 0, 0)
	helperCreateAccount(t, srv, "bob", "example.com", true, false, 0, 0)
	helperCreateAccount(t, srv, "carol", "example.com", true, false, 0, 0)

	helperCreateMailGroup(t, srv, "sales", "example.com",
		[]string{"alice@example.com", "bob@example.com"}, "anyone")

	result := srv.expandMailGroups("carol@example.com", []string{
		"carol@example.com",          // regular account
		"sales@example.com",           // group
		"unknown@example.com",         // non-existent
	})

	// carol (pass-through) + alice + bob (group expansion) + unknown (pass-through of non-existent)
	want := []string{"alice@example.com", "bob@example.com", "carol@example.com", "unknown@example.com"}
	if got := sortedStrings(result); !equalStrings(got, want) {
		t.Errorf("mixed recipients = %v, want %v", got, want)
	}
}

// TestExpandMailGroups_ExternalRecipient covers when a group member is an
// external address — it should pass through unchanged (expandMailGroups does
// not verify whether a resolved member is local).
func TestExpandMailGroups_ExternalRecipient(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", false, false, 0, 0)

	helperCreateMailGroup(t, srv, "mixed", "example.com",
		[]string{"alice@example.com", "external@other.com"}, "anyone")

	result := srv.expandMailGroups("alice@example.com",
		[]string{"mixed@example.com"})

	want := []string{"alice@example.com", "external@other.com"}
	if got := sortedStrings(result); !equalStrings(got, want) {
		t.Errorf("external member = %v, want %v", got, want)
	}
}

// TestExpandMailGroups_DynamicGroup covers dynamic group expansion through
// the server-level expandMailGroups call.
func TestExpandMailGroups_DynamicGroup(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", true, false, 0, 0)
	helperCreateAccount(t, srv, "bob", "example.com", true, false, 0, 0)
	helperCreateAccount(t, srv, "admin", "example.com", true, false, 0, 0) // admin

	helperCreateDynamicMailGroup(t, srv, "all-users", "example.com", "", nil, "", "anyone")

	result := srv.expandMailGroups("admin@example.com",
		[]string{"all-users@example.com"})

	// Dynamic group: all active accounts in example.com.
	want := sortedStrings([]string{"alice@example.com", "bob@example.com", "admin@example.com"})
	if got := sortedStrings(result); !equalStrings(got, want) {
		t.Errorf("dynamic group all-users = %v, want %v", got, want)
	}
}

// TestExpandMailGroups_DynamicGroup_AdminOnly covers the admin-only dynamic group.
func TestExpandMailGroups_DynamicGroup_AdminOnly(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", true, false, 0, 0)
	helperCreateAccount(t, srv, "admin", "example.com", true, true, 0, 0)

	adminOnly := true
	helperCreateDynamicMailGroup(t, srv, "admins", "example.com", "", &adminOnly, "", "anyone")

	result := srv.expandMailGroups("alice@example.com",
		[]string{"admins@example.com"})

	want := []string{"admin@example.com"}
	if got := sortedStrings(result); !equalStrings(got, want) {
		t.Errorf("dynamic admin-only = %v, want %v", got, want)
	}
}

// TestExpandMailGroups_DynamicGroup_Pattern covers the dynamic group with a
// local-part pattern.
func TestExpandMailGroups_DynamicGroup_Pattern(t *testing.T) {
	srv := helperServer(t)
	helperCreateDomain(t, srv, "example.com", true)
	helperCreateAccount(t, srv, "alice", "example.com", true, false, 0, 0)
	helperCreateAccount(t, srv, "bob", "example.com", true, false, 0, 0)
	helperCreateAccount(t, srv, "admin", "example.com", true, true, 0, 0)

	// Pattern matches only accounts starting with "alice".
	helperCreateDynamicMailGroup(t, srv, "sales", "example.com", "", nil, "alice*", "anyone")

	result := srv.expandMailGroups("admin@example.com",
		[]string{"sales@example.com"})

	want := []string{"alice@example.com"}
	if got := sortedStrings(result); !equalStrings(got, want) {
		t.Errorf("dynamic pattern = %v, want %v", got, want)
	}
}
