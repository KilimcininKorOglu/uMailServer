package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

type stubFolders struct{ list []Folder }

func (s stubFolders) Folders(string) ([]Folder, error) { return s.list, nil }

type memSyncState struct{ m map[string]string }

func (s *memSyncState) k(email, c, d string) string { return email + "|" + c + "|" + d }
func (s *memSyncState) GetSyncState(email, c, d string) (string, error) {
	return s.m[s.k(email, c, d)], nil
}
func (s *memSyncState) PutSyncState(email, c, d, w string) error {
	s.m[s.k(email, c, d)] = w
	return nil
}

func folderSyncServer() *Server {
	s := NewServer(allowAuth)
	s.SetFolderSource(stubFolders{list: []Folder{
		{ServerID: "inbox", ParentID: "0", DisplayName: "Inbox", Type: FolderTypeInbox},
		{ServerID: "sent", ParentID: "0", DisplayName: "Sent Items", Type: FolderTypeSent},
		{ServerID: "cal", ParentID: "0", DisplayName: "Calendar", Type: FolderTypeCalendar},
	}})
	s.SetSyncState(&memSyncState{m: map[string]string{}})
	return s
}

func doFolderSync(t *testing.T, s *Server, syncKey string) *wbxml.Element {
	t.Helper()
	body, err := wbxml.Marshal(&wbxml.Element{Page: wbxml.PageFolderHierarchy, Name: "FolderSync", Children: []*wbxml.Element{
		{Page: wbxml.PageFolderHierarchy, Name: "SyncKey", Text: syncKey},
	}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=FolderSync&DeviceId=DEV1", bytes.NewReader(body))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("FolderSync status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return resp
}

// TestFolderSyncInitial checks the initial SyncKey 0 returns the full hierarchy
// (every folder as an Add) and advances the key to "1".
func TestFolderSyncInitial(t *testing.T) {
	resp := doFolderSync(t, folderSyncServer(), "0")
	if st := resp.Sub("Status"); st == nil || st.Text != folderStatusSuccess {
		t.Fatalf("Status = %v, want 1", st)
	}
	if sk := resp.Sub("SyncKey"); sk == nil || sk.Text != "1" {
		t.Fatalf("SyncKey = %v, want 1", sk)
	}
	changes := resp.Sub("Changes")
	if changes == nil || changes.Sub("Count") == nil || changes.Sub("Count").Text != "3" {
		t.Fatalf("Changes/Count != 3")
	}
	adds := 0
	for _, c := range changes.Children {
		if c.Name == "Add" {
			adds++
		}
	}
	if adds != 3 {
		t.Fatalf("Add count = %d, want 3", adds)
	}
}

// TestFolderSyncResyncNoChange checks that re-syncing with the current key
// reports zero changes and keeps the same key (the default hierarchy is static).
func TestFolderSyncResyncNoChange(t *testing.T) {
	s := folderSyncServer()
	doFolderSync(t, s, "0") // establishes key "1"
	resp := doFolderSync(t, s, "1")
	if resp.Sub("Status").Text != folderStatusSuccess || resp.Sub("SyncKey").Text != "1" {
		t.Fatalf("resync status/synckey wrong")
	}
	if resp.Sub("Changes").Sub("Count").Text != "0" {
		t.Fatalf("resync Count != 0")
	}
}

// TestFolderSyncInvalidKey checks an unrecognized SyncKey is rejected with
// Status 9, which tells the client to restart the hierarchy sync from 0.
func TestFolderSyncInvalidKey(t *testing.T) {
	s := folderSyncServer()
	doFolderSync(t, s, "0")
	resp := doFolderSync(t, s, "999")
	if resp.Sub("Status") == nil || resp.Sub("Status").Text != folderStatusInvalidSyncKey {
		t.Fatalf("invalid-key Status = %v, want 9", resp.Sub("Status"))
	}
}
