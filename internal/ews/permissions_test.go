package ews

import (
	"testing"

	"github.com/umailserver/umailserver/internal/storage"
)

// TestPermissionLevelPresetsRoundTrip verifies that each named permission level
// maps to a fixed ACL mask and back, so a level chosen in Outlook is stable. The
// preset masks must round-trip exactly because real clients pick named levels.
func TestPermissionLevelPresetsRoundTrip(t *testing.T) {
	cases := []struct {
		level string
		mask  storage.ACLRights
	}{
		{levelNone, 0},
		{levelReviewer, aclReviewer},
		{levelAuthor, aclAuthor},
		{levelEditor, aclEditor},
		{levelOwner, aclOwner},
	}
	for _, c := range cases {
		if got := aclToPermissionLevel(c.mask); got != c.level {
			t.Errorf("aclToPermissionLevel(%08b) = %q, want %q", c.mask, got, c.level)
		}
		mask, ok := permissionLevelToACL(c.level)
		if !ok || mask != c.mask {
			t.Errorf("permissionLevelToACL(%q) = %08b,%v, want %08b,true", c.level, mask, ok, c.mask)
		}
		// Full projection round-trip through the Permission element.
		p := aclToPermission("alice@example.com", c.mask)
		if p.PermissionLevel != c.level {
			t.Errorf("aclToPermission(%08b).PermissionLevel = %q, want %q", c.mask, p.PermissionLevel, c.level)
		}
		if back := permissionToACL(p); back != c.mask {
			t.Errorf("round-trip %q: permissionToACL = %08b, want %08b", c.level, back, c.mask)
		}
	}
}

// TestACLProjectionIsIdempotent verifies that projecting any rights mask to a
// Permission and back yields a stable mask (applying the round-trip twice equals
// applying it once). This matters because the RFC 4314 model has bits (e.g. a
// bare seen or expunge) the coarser MAPI model cannot represent one-to-one; the
// mapping must still converge so repeated GetFolder/UpdateFolder cycles do not
// drift the stored ACL.
func TestACLProjectionIsIdempotent(t *testing.T) {
	rt := func(r storage.ACLRights) storage.ACLRights {
		return permissionToACL(aclToPermission("u@example.com", r))
	}
	for i := 0; i < 256; i++ {
		r := storage.ACLRights(i)
		once := rt(r)
		twice := rt(once)
		if once != twice {
			t.Errorf("round-trip not idempotent for %08b: once=%08b twice=%08b", r, once, twice)
		}
	}
}

// TestGranteeUserIDMapping verifies the grantee<->UserId bridge: the reserved
// "anyone" grant is the distinguished Default user (everyone), and a specific
// address travels in PrimarySmtpAddress. This is what lets an org-wide public
// folder grant render as "Default" in Outlook's permission dialog.
func TestGranteeUserIDMapping(t *testing.T) {
	anyone := granteeToUserID(storage.ACLAnyone)
	if anyone.DistinguishedUser != distinguishedDefault || anyone.PrimarySmtpAddress != "" {
		t.Errorf("anyone -> %+v, want DistinguishedUser=Default", anyone)
	}
	if g := userIDToGrantee(anyone); g != storage.ACLAnyone {
		t.Errorf("userIDToGrantee(Default) = %q, want %q", g, storage.ACLAnyone)
	}
	// Anonymous also folds into "anyone".
	if g := userIDToGrantee(UserIdType{DistinguishedUser: "Anonymous"}); g != storage.ACLAnyone {
		t.Errorf("userIDToGrantee(Anonymous) = %q, want %q", g, storage.ACLAnyone)
	}
	user := granteeToUserID("Bob@Example.com")
	if user.PrimarySmtpAddress != "Bob@Example.com" || user.DistinguishedUser != "" {
		t.Errorf("user -> %+v, want PrimarySmtpAddress set", user)
	}
	if g := userIDToGrantee(UserIdType{PrimarySmtpAddress: "Bob@Example.com"}); g != "bob@example.com" {
		t.Errorf("userIDToGrantee normalises case: got %q, want bob@example.com", g)
	}
}

// TestPermissionLevelWinsOverBits verifies that a recognized PermissionLevel is
// authoritative even when the individual bits disagree, so a client that sends
// only a level (the common case) gets the canonical mask.
func TestPermissionLevelWinsOverBits(t *testing.T) {
	p := PermissionType{
		PermissionLevel: levelReviewer,
		// Contradictory bits a sloppy client might leave set:
		CanCreateItems: true,
		DeleteItems:    permAll,
	}
	if got := permissionToACL(p); got != aclReviewer {
		t.Errorf("level wins: got %08b, want Reviewer %08b", got, aclReviewer)
	}
}

// TestEffectiveRightsMapping verifies the caller's cumulative rights project onto
// the read-only EffectiveRights element: an Owner sees create/modify/delete/read,
// a Reviewer only read.
func TestEffectiveRightsMapping(t *testing.T) {
	owner := aclToEffectiveRights(aclOwner)
	if !owner.CreateContents || !owner.CreateHierarchy || !owner.Delete || !owner.Modify || !owner.Read {
		t.Errorf("owner effective rights incomplete: %+v", owner)
	}
	reviewer := aclToEffectiveRights(aclReviewer)
	if !reviewer.Read || reviewer.CreateContents || reviewer.Delete || reviewer.Modify {
		t.Errorf("reviewer effective rights should be read-only: %+v", reviewer)
	}
}

// TestCustomDerivesFromBits verifies that when no preset matches, the mask is
// reconstructed from the individual permission bits.
func TestCustomDerivesFromBits(t *testing.T) {
	p := PermissionType{
		PermissionLevel: levelCustom,
		IsFolderVisible: true,
		ReadItems:       permFullDetails,
		CanCreateItems:  true,
	}
	want := storage.ACLLookup | storage.ACLRead | storage.ACLWrite
	if got := permissionToACL(p); got != want {
		t.Errorf("custom derive: got %08b, want %08b", got, want)
	}
}
