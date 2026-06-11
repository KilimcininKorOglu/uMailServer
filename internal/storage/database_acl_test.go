package storage

import "testing"

// TestPublicFolderOwner pins the canonical per-domain public-folder owner key so
// every surface (IMAP/EWS/webmail/admin) derives the same reserved principal and
// one domain's public tree can never resolve to another's.
func TestPublicFolderOwner(t *testing.T) {
	if got := PublicFolderOwner("example.com"); got != "public@example.com" {
		t.Errorf("PublicFolderOwner = %q, want public@example.com", got)
	}
	if PublicFolderOwner("a.test") == PublicFolderOwner("b.test") {
		t.Error("different domains must not share a public owner")
	}
}

// TestEffectiveRightsAnyoneGrant verifies that the reserved "anyone" grant backs
// organization-wide access: a user with no personal grant inherits the anyone
// grant, and a personal grant is unioned with it. This is the mechanism that
// makes a public folder readable/postable by all users without per-user ACLs,
// while a folder with no grant at all stays invisible (admin-only).
func TestEffectiveRightsAnyoneGrant(t *testing.T) {
	db := setupTestDB(t)
	owner := PublicFolderOwner("example.com")
	const folder = "Announcements"
	user := "alice@example.com"

	// No grant at all: no rights, access denied.
	if r, err := db.EffectiveRights(user, owner, folder); err != nil || r != 0 {
		t.Fatalf("EffectiveRights with no grant = %v (err %v), want 0", r, err)
	}
	if ok, err := db.CanAccess(user, owner, folder, ACLRead); err != nil || ok {
		t.Fatalf("CanAccess with no grant = %v (err %v), want false", ok, err)
	}

	// "anyone: read" makes every user a reader without a personal grant.
	if err := db.SetACL(owner, folder, ACLAnyone, ACLLookup|ACLRead, "admin@example.com"); err != nil {
		t.Fatalf("SetACL anyone: %v", err)
	}
	if ok, err := db.CanAccess(user, owner, folder, ACLRead); err != nil || !ok {
		t.Fatalf("CanAccess after anyone:read = %v (err %v), want true", ok, err)
	}
	// Anyone grant alone does not confer write.
	if ok, err := db.CanAccess(user, owner, folder, ACLWrite); err != nil || ok {
		t.Errorf("anyone:read must not grant write (ok=%v err=%v)", ok, err)
	}

	// A personal grant is unioned with the anyone grant: alice now also posts.
	if err := db.SetACL(owner, folder, user, ACLWrite, "admin@example.com"); err != nil {
		t.Fatalf("SetACL user: %v", err)
	}
	r, err := db.EffectiveRights(user, owner, folder)
	if err != nil {
		t.Fatalf("EffectiveRights: %v", err)
	}
	if want := ACLLookup | ACLRead | ACLWrite; r != want {
		t.Errorf("EffectiveRights = %q, want %q (anyone ∪ user)", r, want)
	}

	// A different user still only has the anyone grant (read), not alice's write.
	if ok, err := db.CanAccess("bob@example.com", owner, folder, ACLWrite); err != nil || ok {
		t.Errorf("bob must not inherit alice's personal write grant (ok=%v err=%v)", ok, err)
	}

	// The owner principal implicitly holds all rights.
	if ok, err := db.CanAccess(owner, owner, folder, ACLAll); err != nil || !ok {
		t.Fatalf("owner CanAccess(ACLAll) = %v (err %v), want true", ok, err)
	}
}
