package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// stubMail is a controllable MailSource: ListMessages returns the snapshot,
// ChangesSince returns the staged deltas, CurrentSeq returns seq.
type stubMail struct {
	list           []SyncMessage
	seq            uint64
	adds, changes  []SyncMessage
	deletes        []string
}

func (m *stubMail) ListMessages(string, string) ([]SyncMessage, error) { return m.list, nil }
func (m *stubMail) CurrentSeq(string) (uint64, error)                  { return m.seq, nil }
func (m *stubMail) ChangesSince(string, string, uint64) ([]SyncMessage, []SyncMessage, []string, uint64, error) {
	return m.adds, m.changes, m.deletes, m.seq, nil
}

func msg(id, subject string) SyncMessage {
	return SyncMessage{ServerID: "inbox:" + id, Subject: subject, From: "a@x.test", DateReceived: "2026-06-14T12:00:00.000Z", Body: "body of " + id}
}

func syncServer(mail *stubMail) *Server {
	s := NewServer(allowAuth)
	s.SetMailSource(mail)
	s.SetSyncState(&memSyncState{m: map[string]string{}})
	return s
}

func doSync(t *testing.T, s *Server, syncKey string, window int) *wbxml.Element {
	t.Helper()
	coll := []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: "inbox"},
	}
	if window > 0 {
		coll = append(coll, &wbxml.Element{Page: wbxml.PageAirSync, Name: "WindowSize", Text: strconv.Itoa(window)})
	}
	body, err := wbxml.Marshal(&wbxml.Element{Page: wbxml.PageAirSync, Name: "Sync", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "Collections", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSync, Name: "Collection", Children: coll},
		}},
	}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=Sync&DeviceId=DEV1", bytes.NewReader(body))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Sync status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return resp.Sub("Collections").Sub("Collection")
}

func countOps(collection *wbxml.Element, op string) int {
	cmds := collection.Sub("Commands")
	if cmds == nil {
		return 0
	}
	n := 0
	for _, c := range cmds.Children {
		if c.Name == op {
			n++
		}
	}
	return n
}

// TestSyncPrimeAndWindow exercises the EAS Sync flow: SyncKey 0 primes (key 1,
// no commands); the first real sync streams the snapshot windowed by WindowSize
// with MoreAvailable; the final window clears MoreAvailable. Keys advance each
// exchange.
func TestSyncPrimeAndWindow(t *testing.T) {
	s := syncServer(&stubMail{list: []SyncMessage{msg("1", "one"), msg("2", "two"), msg("3", "three")}, seq: 7})

	prime := doSync(t, s, "0", 2)
	if prime.Sub("SyncKey").Text != "1" || prime.Sub("Status").Text != syncStatusSuccess {
		t.Fatalf("prime SyncKey/Status wrong")
	}
	if prime.Sub("Commands") != nil {
		t.Fatalf("prime must carry no commands")
	}

	w1 := doSync(t, s, "1", 2)
	if w1.Sub("SyncKey").Text != "2" {
		t.Fatalf("window 1 SyncKey = %q, want 2", w1.Sub("SyncKey").Text)
	}
	if got := countOps(w1, "Add"); got != 2 {
		t.Fatalf("window 1 Adds = %d, want 2", got)
	}
	if w1.Sub("MoreAvailable") == nil {
		t.Fatalf("window 1 must set MoreAvailable")
	}

	w2 := doSync(t, s, "2", 2)
	if w2.Sub("SyncKey").Text != "3" {
		t.Fatalf("window 2 SyncKey = %q, want 3", w2.Sub("SyncKey").Text)
	}
	if got := countOps(w2, "Add"); got != 1 {
		t.Fatalf("window 2 Adds = %d, want 1", got)
	}
	if w2.Sub("MoreAvailable") != nil {
		t.Fatalf("window 2 must not set MoreAvailable")
	}
}

// TestSyncIncremental checks that, once the snapshot is drained, a later sync
// reports only the change feed's adds.
func TestSyncIncremental(t *testing.T) {
	mail := &stubMail{list: []SyncMessage{msg("1", "one")}, seq: 5}
	s := syncServer(mail)
	doSync(t, s, "0", 50)        // prime -> key 1
	w := doSync(t, s, "1", 50)   // drains the 1-message snapshot -> key 2, cursor j:5
	if countOps(w, "Add") != 1 || w.Sub("SyncKey").Text != "2" {
		t.Fatalf("initial drain wrong")
	}
	// Stage a new message on the change feed.
	mail.adds = []SyncMessage{msg("2", "new")}
	mail.seq = 6
	inc := doSync(t, s, "2", 50)
	if inc.Sub("SyncKey").Text != "3" {
		t.Fatalf("incremental SyncKey = %q, want 3", inc.Sub("SyncKey").Text)
	}
	if got := countOps(inc, "Add"); got != 1 {
		t.Fatalf("incremental Adds = %d, want 1", got)
	}
}

// TestSyncInvalidKey rejects a SyncKey that is not the last one issued, resetting
// the client to a fresh sync (Status 3, SyncKey 0).
func TestSyncInvalidKey(t *testing.T) {
	s := syncServer(&stubMail{list: []SyncMessage{msg("1", "one")}})
	doSync(t, s, "0", 50) // -> key 1
	bad := doSync(t, s, "99", 50)
	if bad.Sub("Status").Text != syncStatusInvalidKey || bad.Sub("SyncKey").Text != "0" {
		t.Fatalf("invalid key: Status=%v SyncKey=%v, want 3/0", bad.Sub("Status"), bad.Sub("SyncKey"))
	}
}
