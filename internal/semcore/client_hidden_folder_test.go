package semcore

import "testing"

// TestIsClientHiddenFolderRole pins the single rule that decides which folders
// disappear from Exchange-style mail-client enumeration: only the Recoverable
// Items dumpster is hidden; every browsable distinguished/user folder stays
// visible. EWS FindFolder/SyncFolderHierarchy and IMAP LIST/LSUB all share this
// predicate, so a regression here would silently change every surface at once.
// "scheduled" is deliberately NOT hidden — the scope is Recoverable Items only.
func TestIsClientHiddenFolderRole(t *testing.T) {
	hidden := map[string]bool{
		"recoverableitems": true,
		"inbox":            false,
		"sent":             false,
		"drafts":           false,
		"trash":            false,
		"junk":             false,
		"archive":          false,
		"notes":            false,
		"scheduled":        false,
		"":                 false, // ordinary user folder (no distinguished role)
		"root":             false,
		"ipmsubtree":       false,
	}
	for role, want := range hidden {
		if got := IsClientHiddenFolderRole(role); got != want {
			t.Errorf("IsClientHiddenFolderRole(%q) = %v, want %v", role, got, want)
		}
	}
}

// TestIsClientHiddenFolderName checks the canonical-name form used by IMAP
// LIST/LSUB: the dumpster's display name is hidden, every other name is not.
func TestIsClientHiddenFolderName(t *testing.T) {
	if !IsClientHiddenFolderName("Recoverable Items") {
		t.Error(`IsClientHiddenFolderName("Recoverable Items") = false, want true`)
	}
	for _, name := range []string{"INBOX", "Sent", "Archive", "Scheduled", "My Project", ""} {
		if IsClientHiddenFolderName(name) {
			t.Errorf("IsClientHiddenFolderName(%q) = true, want false", name)
		}
	}
}
