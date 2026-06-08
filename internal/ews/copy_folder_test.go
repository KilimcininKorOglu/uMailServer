package ews

import (
	"net/http"
	"strings"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestCopyFolder_DeepCopy proves CopyFolder recreates a source subtree under the
// destination with brand-new FolderIds, leaves the source untouched, and — the
// reason the whole parent-scoped identity change exists — produces a copied
// child folder that is DISTINCT from a same-named top-level folder instead of
// collapsing into it. Items are not exercised here (the unit harness has no
// message store); the exchangelib probe covers item duplication end-to-end.
func TestCopyFolder_DeepCopy(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	const email = "alice@example.com"
	if _, err := srv.identity.EnsureMailboxId(email); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}
	// handleCopyFolder strips the "e:" prefix, so the source tree must be keyed
	// by the raw email to match what the handler reads.
	const mbox = email
	id := srv.identity

	projects, err := id.EnsureFolderId(mbox, "Projects", "")
	if err != nil {
		t.Fatalf("EnsureFolderId(Projects): %v", err)
	}
	reports, err := id.EnsureChildFolderId(mbox, projects, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId(Projects/Reports): %v", err)
	}
	q1, err := id.EnsureChildFolderId(mbox, reports, "Q1", "")
	if err != nil {
		t.Fatalf("EnsureChildFolderId(Projects/Reports/Q1): %v", err)
	}
	archive, err := id.EnsureFolderId(mbox, "Archive", "")
	if err != nil {
		t.Fatalf("EnsureFolderId(Archive): %v", err)
	}
	// A top-level "Reports" to stress the collision case: the copied
	// Projects/Reports must NOT collapse into this one.
	topReports, err := id.EnsureFolderId(mbox, "Reports", "")
	if err != nil {
		t.Fatalf("EnsureFolderId(top Reports): %v", err)
	}

	body := `<CopyFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <ToFolderId><t:FolderId Id="` + archive.String() + `"/></ToFolderId>
  <FolderIds><t:FolderId Id="` + projects.String() + `"/></FolderIds>
</CopyFolder>`

	rec := ewsRequest(t, srv, email, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	respBody := rec.Body.String()
	if !strings.Contains(respBody, `ResponseClass="Success"`) || strings.Contains(respBody, `ResponseClass="Error"`) {
		t.Fatalf("CopyFolder response not successful:\n%s", respBody)
	}

	// findChild locates a folder by client-visible display name under a parent.
	findChild := func(parent semcore.FolderId, display string) (semcore.StoredFolderIdentity, bool) {
		all, lerr := id.ListFolderIdentitiesForMailbox(mbox)
		if lerr != nil {
			t.Fatalf("ListFolderIdentitiesForMailbox: %v", lerr)
		}
		for _, f := range all {
			if !f.ParentID.Equal(parent) {
				continue
			}
			name, nerr := id.FolderNameByID(mbox, f.FolderID)
			if nerr != nil {
				continue
			}
			if semcore.DisplayNameFromStorageName(name) == display {
				return f, true
			}
		}
		return semcore.StoredFolderIdentity{}, false
	}

	// The copy of "Projects" lives under Archive with a fresh id.
	projCopy, ok := findChild(archive, "Projects")
	if !ok {
		t.Fatal("copied Projects not found under Archive")
	}
	if projCopy.FolderID.Equal(projects) {
		t.Fatal("copied Projects reused the source id (move, not copy)")
	}

	// The subtree is recreated: Reports under the Projects copy, Q1 under that.
	reportsCopy, ok := findChild(projCopy.FolderID, "Reports")
	if !ok {
		t.Fatal("copied Reports not found under the Projects copy")
	}
	if reportsCopy.FolderID.Equal(reports) {
		t.Error("copied Reports reused the source id")
	}
	// The collision guarantee: the copied Reports is distinct from the
	// pre-existing top-level Reports.
	if reportsCopy.FolderID.Equal(topReports) {
		t.Fatal("copied Reports collapsed into the top-level Reports")
	}
	if _, ok := findChild(reportsCopy.FolderID, "Q1"); !ok {
		t.Error("copied Q1 not found under the copied Reports")
	}

	// The source subtree is untouched: original ids still resolve with their
	// original parents.
	for _, tc := range []struct {
		id     semcore.FolderId
		parent semcore.FolderId
		tag    string
	}{
		{reports, projects, "source Reports"},
		{q1, reports, "source Q1"},
	} {
		rec, gerr := id.GetFolderByID(tc.id)
		if gerr != nil {
			t.Errorf("%s missing after copy: %v", tc.tag, gerr)
			continue
		}
		if !rec.ParentID.Equal(tc.parent) {
			t.Errorf("%s parent changed to %v (copy mutated the source)", tc.tag, rec.ParentID)
		}
	}
}
