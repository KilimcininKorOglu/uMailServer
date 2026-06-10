package mailcheck

import (
	"fmt"
	"testing"
)

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
func (f *fakeBlob) ReadMessage(_, id string) ([]byte, error) {
	if f.present[id] {
		return []byte("raw-" + id), nil
	}
	return nil, fmt.Errorf("blob %s not found", id)
}

type fakeRepairer struct {
	recreated    []string // mailbox per recreate
	deletedIndex []string // "mailbox/uid"
	deletedIdent []string // item id
}

func (f *fakeRepairer) RecreateIdentity(_, mailbox string, _ []byte) error {
	f.recreated = append(f.recreated, mailbox)
	return nil
}
func (f *fakeRepairer) DeleteIndexEntry(_, mailbox string, uid uint32) error {
	f.deletedIndex = append(f.deletedIndex, fmt.Sprintf("%s/%d", mailbox, uid))
	return nil
}
func (f *fakeRepairer) DeleteIdentity(itemID string) error {
	f.deletedIdent = append(f.deletedIdent, itemID)
	return nil
}

type fakeRepairIdent struct {
	key         string
	hasMbox     bool
	folderItems map[string][]ItemRef
}

func (f *fakeRepairIdent) MailboxKey(string) (string, bool, error) { return f.key, f.hasMbox, nil }
func (f *fakeRepairIdent) FolderItems(string) (map[string][]ItemRef, error) {
	return f.folderItems, nil
}

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

// --- Repair tests -----------------------------------------------------------

// healthyRepair mirrors healthy() but for the Repair interfaces: semcore items
// are grouped under the IMAP folder name ("INBOX"), each carrying an ItemID and
// its blob key, so the per-folder cross-check matches the index.
func healthyRepair() (*fakeIndex, *fakeBlob, *fakeRepairIdent) {
	idx := &fakeIndex{
		mailboxes: []string{"INBOX"},
		uids:      map[string][]uint32{"INBOX": {1, 2}},
		ids:       map[string]map[uint32]string{"INBOX": {1: "aaa", 2: "bbb"}},
	}
	blob := &fakeBlob{present: map[string]bool{"aaa": true, "bbb": true}}
	ident := &fakeRepairIdent{
		key:     "u@x",
		hasMbox: true,
		folderItems: map[string][]ItemRef{
			"INBOX": {{ItemID: "i-aaa", MsgKey: "aaa"}, {ItemID: "i-bbb", MsgKey: "bbb"}},
		},
	}
	return idx, blob, ident
}

func mustRepair(t *testing.T, idx IndexStore, blob RepairBlob, ident RepairIdentity, w Repairer) *RepairReport {
	t.Helper()
	rep, err := Repair("u@x", idx, blob, ident, w)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	return rep
}

func TestRepairHealthyNoop(t *testing.T) {
	idx, blob, ident := healthyRepair()
	w := &fakeRepairer{}
	rep := mustRepair(t, idx, blob, ident, w)
	if !rep.Clean() {
		t.Errorf("healthy mailbox should need no repair, got %+v", rep)
	}
	if len(w.recreated)+len(w.deletedIndex)+len(w.deletedIdent) != 0 {
		t.Errorf("no writes expected, got recreated=%v delIdx=%v delIdent=%v",
			w.recreated, w.deletedIndex, w.deletedIdent)
	}
}

func TestRepairRecreatesGhost(t *testing.T) {
	idx, blob, ident := healthyRepair()
	// "bbb" is in the IMAP index with its blob present, but has no semcore
	// identity in INBOX -> EWS-ghost the repairer must refile.
	ident.folderItems["INBOX"] = []ItemRef{{ItemID: "i-aaa", MsgKey: "aaa"}}
	w := &fakeRepairer{}
	rep := mustRepair(t, idx, blob, ident, w)
	if rep.Recreated != 1 {
		t.Errorf("Recreated = %d, want 1 (actions: %v)", rep.Recreated, rep.Actions)
	}
	if len(w.recreated) != 1 || w.recreated[0] != "INBOX" {
		t.Errorf("recreated mailbox = %v, want [INBOX]", w.recreated)
	}
	if rep.DeletedIndex != 0 || rep.DeletedIdentity != 0 {
		t.Errorf("unexpected deletes: %+v", rep)
	}
}

func TestRepairDeletesOrphanIndex(t *testing.T) {
	idx, blob, ident := healthyRepair()
	// "bbb" blob is gone -> its IMAP index entry (uid 2) is a dangling orphan.
	delete(blob.present, "bbb")
	// Its semcore identity is gone too (so only the index entry remains).
	ident.folderItems["INBOX"] = []ItemRef{{ItemID: "i-aaa", MsgKey: "aaa"}}
	w := &fakeRepairer{}
	rep := mustRepair(t, idx, blob, ident, w)
	if rep.DeletedIndex != 1 {
		t.Errorf("DeletedIndex = %d, want 1 (actions: %v)", rep.DeletedIndex, rep.Actions)
	}
	if len(w.deletedIndex) != 1 || w.deletedIndex[0] != "INBOX/2" {
		t.Errorf("deletedIndex = %v, want [INBOX/2]", w.deletedIndex)
	}
	if rep.Recreated != 0 {
		t.Errorf("should not recreate an entry with a missing blob: %+v", rep)
	}
}

func TestRepairDeletesOrphanIdentity(t *testing.T) {
	idx, blob, ident := healthyRepair()
	// "bbb" blob is gone, but a semcore identity for it lingers -> orphan
	// identity the repairer must delete. The index entry for "bbb" is also
	// orphaned, but this test asserts the identity-side deletion.
	delete(blob.present, "bbb")
	w := &fakeRepairer{}
	rep := mustRepair(t, idx, blob, ident, w)
	if rep.DeletedIdentity != 1 {
		t.Errorf("DeletedIdentity = %d, want 1 (actions: %v)", rep.DeletedIdentity, rep.Actions)
	}
	if len(w.deletedIdent) != 1 || w.deletedIdent[0] != "i-bbb" {
		t.Errorf("deletedIdent = %v, want [i-bbb]", w.deletedIdent)
	}
	// The dangling index entry for the same message is also cleaned up.
	if rep.DeletedIndex != 1 {
		t.Errorf("DeletedIndex = %d, want 1 (orphan index for the same blob)", rep.DeletedIndex)
	}
}

func TestRepairNoSemcoreMailboxRecreatesAll(t *testing.T) {
	idx, blob, ident := healthyRepair()
	// No semcore mailbox at all -> every indexed message is an EWS-ghost.
	ident.hasMbox = false
	ident.folderItems = nil
	w := &fakeRepairer{}
	rep := mustRepair(t, idx, blob, ident, w)
	if rep.Recreated != 2 {
		t.Errorf("Recreated = %d, want 2 (actions: %v)", rep.Recreated, rep.Actions)
	}
}
