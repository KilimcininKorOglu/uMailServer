package mailcheck

import "testing"

// --- in-memory fakes (single user) -----------------------------------------

type fakeIndex struct {
	mailboxes []string
	uids      map[string][]uint32
	ids       map[string]map[uint32]string
}

func (f *fakeIndex) ListMailboxes(string) ([]string, error) { return f.mailboxes, nil }
func (f *fakeIndex) GetMessageUIDs(_, mailbox string) ([]uint32, error) {
	return f.uids[mailbox], nil
}
func (f *fakeIndex) MessageID(_, mailbox string, uid uint32) (string, error) {
	return f.ids[mailbox][uid], nil
}

type fakeBlob struct{ present map[string]bool }

func (f *fakeBlob) MessageExists(_, id string) bool { return f.present[id] }

type fakeIdent struct {
	key     string
	hasMbox bool
	folders []string
	items   map[string][]string
}

func (f *fakeIdent) MailboxKey(string) (string, bool, error) { return f.key, f.hasMbox, nil }
func (f *fakeIdent) FolderIDs(string) ([]string, error)      { return f.folders, nil }
func (f *fakeIdent) ItemKeys(folderID string) ([]string, error) {
	return f.items[folderID], nil
}

func mustCheck(t *testing.T, idx IndexStore, blob BlobStore, ident IdentityStore) *Report {
	t.Helper()
	rep, err := Check("u@x", idx, blob, ident)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return rep
}

func kinds(r *Report) map[string]int {
	m := map[string]int{}
	for _, i := range r.Issues {
		m[i.Kind]++
	}
	return m
}

// healthy returns fakes describing a consistent mailbox with two messages in
// INBOX (blobs present, both in the IMAP index and semcore).
func healthy() (*fakeIndex, *fakeBlob, *fakeIdent) {
	idx := &fakeIndex{
		mailboxes: []string{"INBOX"},
		uids:      map[string][]uint32{"INBOX": {1, 2}},
		ids:       map[string]map[uint32]string{"INBOX": {1: "aaa", 2: "bbb"}},
	}
	blob := &fakeBlob{present: map[string]bool{"aaa": true, "bbb": true}}
	ident := &fakeIdent{
		key:     "u@x",
		hasMbox: true,
		folders: []string{"f1"},
		items:   map[string][]string{"f1": {"aaa", "bbb"}},
	}
	return idx, blob, ident
}

func TestCheckCleanMailbox(t *testing.T) {
	idx, blob, ident := healthy()
	rep, err := Check("u@x", idx, blob, ident)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !rep.Clean() {
		t.Errorf("expected clean, got issues: %v", rep.Issues)
	}
	if rep.IndexCount != 2 || rep.SemcoreCount != 2 {
		t.Errorf("counts = index %d semcore %d, want 2/2", rep.IndexCount, rep.SemcoreCount)
	}
}

func TestCheckOrphanIndex(t *testing.T) {
	idx, blob, ident := healthy()
	delete(blob.present, "bbb") // index entry "bbb" now has no blob
	rep := mustCheck(t, idx, blob, ident)
	if kinds(rep)[KindOrphanIndex] != 1 {
		t.Errorf("want 1 orphan-index, got %v", rep.Issues)
	}
}

func TestCheckGhostInEWS(t *testing.T) {
	idx, blob, ident := healthy()
	// "bbb" is in the IMAP index but drop its semcore identity.
	ident.items["f1"] = []string{"aaa"}
	rep := mustCheck(t, idx, blob, ident)
	k := kinds(rep)
	if k[KindGhostInEWS] != 1 {
		t.Errorf("want 1 ghost-in-ews, got %v", rep.Issues)
	}
	// "bbb" blob still present, so no orphan-index.
	if k[KindOrphanIndex] != 0 {
		t.Errorf("unexpected orphan-index: %v", rep.Issues)
	}
}

func TestCheckOrphanSemcore(t *testing.T) {
	idx, blob, ident := healthy()
	// Extra semcore identity "ccc" with no IMAP index entry (blob present).
	ident.items["f1"] = []string{"aaa", "bbb", "ccc"}
	blob.present["ccc"] = true
	rep := mustCheck(t, idx, blob, ident)
	if kinds(rep)[KindOrphanSemcore] != 1 {
		t.Errorf("want 1 orphan-semcore, got %v", rep.Issues)
	}
}

func TestCheckOrphanIdentity(t *testing.T) {
	idx, blob, ident := healthy()
	// semcore identity "ddd" whose blob is missing, also no IMAP entry.
	ident.items["f1"] = []string{"aaa", "bbb", "ddd"}
	rep := mustCheck(t, idx, blob, ident)
	k := kinds(rep)
	if k[KindOrphanIdentity] != 1 {
		t.Errorf("want 1 orphan-identity, got %v", rep.Issues)
	}
	if k[KindOrphanSemcore] != 1 {
		t.Errorf("want 1 orphan-semcore for the same item, got %v", rep.Issues)
	}
}

func TestCheckNoSemcoreMailbox(t *testing.T) {
	idx, blob, ident := healthy()
	ident.hasMbox = false // IMAP messages exist but no semcore mailbox identity
	rep := mustCheck(t, idx, blob, ident)
	if kinds(rep)[KindNoSemcoreMailbox] != 1 {
		t.Errorf("want 1 no-semcore-mailbox, got %v", rep.Issues)
	}
	// Short-circuits before per-item cross-checks.
	if rep.SemcoreCount != 0 {
		t.Errorf("semcore count = %d, want 0", rep.SemcoreCount)
	}
}

func TestCheckEmptyMailboxIsClean(t *testing.T) {
	idx := &fakeIndex{mailboxes: []string{"INBOX"}, uids: map[string][]uint32{"INBOX": {}}}
	blob := &fakeBlob{present: map[string]bool{}}
	ident := &fakeIdent{key: "u@x", hasMbox: true, folders: []string{"f1"}, items: map[string][]string{"f1": {}}}
	rep, err := Check("u@x", idx, blob, ident)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !rep.Clean() {
		t.Errorf("empty mailbox should be clean, got %v", rep.Issues)
	}
}
