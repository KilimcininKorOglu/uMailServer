package activesync

import (
	"bytes"
	"errors"
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
func (m *stubMail) Fetch(_, _, serverID string) (*SyncMessage, error) {
	for i := range m.list {
		if m.list[i].ServerID == serverID {
			sm := m.list[i]
			return &sm, nil
		}
	}
	return nil, nil
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

var errMutator = errors.New("mutator failure")

// stubMutator records the up-sync changes applied to it; failOn makes the named
// ServerId fail so the Responses path can be exercised.
type stubMutator struct {
	reads   map[string]bool
	deletes []string
	moves   map[string]string // serverID -> destination collection
	failOn  string
}

func (m *stubMutator) SetRead(_, _, serverID string, read bool) error {
	if serverID == m.failOn {
		return errMutator
	}
	if m.reads == nil {
		m.reads = map[string]bool{}
	}
	m.reads[serverID] = read
	return nil
}

func (m *stubMutator) Delete(_, _, serverID string) error {
	if serverID == m.failOn {
		return errMutator
	}
	m.deletes = append(m.deletes, serverID)
	return nil
}

func (m *stubMutator) Move(_, _, dst, serverID string) (bool, error) {
	if serverID == m.failOn {
		return false, errMutator
	}
	if m.moves == nil {
		m.moves = map[string]string{}
	}
	m.moves[serverID] = dst
	return true, nil
}

// TestMoveItems verifies the MoveItems command relocates an item via the Mutator
// and reports per-Move success (status 3, not 1) with the destination id.
func TestMoveItems(t *testing.T) {
	mut := &stubMutator{}
	s := NewServer(allowAuth)
	s.SetMutator(mut)
	body, err := wbxml.Marshal(&wbxml.Element{Page: wbxml.PageMove, Name: "MoveItems", Children: []*wbxml.Element{
		{Page: wbxml.PageMove, Name: "Move", Children: []*wbxml.Element{
			{Page: wbxml.PageMove, Name: "SrcMsgId", Text: "blob1"},
			{Page: wbxml.PageMove, Name: "SrcFldId", Text: "INBOX"},
			{Page: wbxml.PageMove, Name: "DstFldId", Text: "Archive"},
		}},
	}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=MoveItems&DeviceId=DEV1", bytes.NewReader(body))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("MoveItems status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	r := resp.Sub("Response")
	if r == nil || r.Sub("Status").Text != moveStatusSuccess {
		t.Fatalf("move Status = %v, want %s", r, moveStatusSuccess)
	}
	if r.Sub("DstMsgId").Text != "blob1" {
		t.Fatalf("DstMsgId = %q, want blob1", r.Sub("DstMsgId").Text)
	}
	if mut.moves["blob1"] != "Archive" {
		t.Fatalf("move not applied to canonical store: %v", mut.moves)
	}
}

// TestSendMail verifies SendMail extracts the byte-exact opaque Mime and the
// SaveInSentItems flag, invokes the submitter, and returns an empty 200 (the
// MS-ASCMD success contract).
func TestSendMail(t *testing.T) {
	var gotMime []byte
	var gotSave, called bool
	s := NewServer(allowAuth)
	s.SetSubmitter(func(_ string, mime []byte, saveToSent bool) error {
		called, gotMime, gotSave = true, mime, saveToSent
		return nil
	})
	raw := []byte("From: u@x.test\r\nTo: r@y.test\r\nSubject: hi\r\n\r\nbody\r\n")
	body, err := wbxml.Marshal(&wbxml.Element{Page: wbxml.PageComposeMail, Name: "SendMail", Children: []*wbxml.Element{
		{Page: wbxml.PageComposeMail, Name: "ClientId", Text: "c1"},
		{Page: wbxml.PageComposeMail, Name: "SaveInSentItems"},
		{Page: wbxml.PageComposeMail, Name: "Mime", Opaque: raw},
	}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=SendMail&DeviceId=DEV1", bytes.NewReader(body))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("SendMail status = %d, want 200", rec.Code)
	}
	if !called {
		t.Fatalf("submitter was not invoked")
	}
	if string(gotMime) != string(raw) {
		t.Fatalf("Mime not byte-exact: got %q", gotMime)
	}
	if !gotSave {
		t.Fatalf("SaveInSentItems flag not detected")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("SendMail success must be an empty body, got %d bytes", rec.Body.Len())
	}
}

// TestItemOperations verifies a Fetch returns the item's full body under
// Properties (AirSyncBase Body > Data) with per-Fetch success status.
func TestItemOperations(t *testing.T) {
	s := syncServer(&stubMail{list: []SyncMessage{
		{ServerID: "blob1", Subject: "full subject", From: "a@x.test", Body: "the full untruncated body", BodyType: "1"},
	}})
	body, err := wbxml.Marshal(&wbxml.Element{Page: wbxml.PageItemOperations, Name: "ItemOperations", Children: []*wbxml.Element{
		{Page: wbxml.PageItemOperations, Name: "Fetch", Children: []*wbxml.Element{
			{Page: wbxml.PageItemOperations, Name: "Store", Text: "Mailbox"},
			{Page: wbxml.PageAirSync, Name: "CollectionId", Text: "inbox"},
			{Page: wbxml.PageAirSync, Name: "ServerId", Text: "blob1"},
		}},
	}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=ItemOperations&DeviceId=DEV1", bytes.NewReader(body))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ItemOperations status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	fetch := resp.Sub("Response").Sub("Fetch")
	if fetch == nil || fetch.Sub("Status").Text != itemOpStatusSuccess {
		t.Fatalf("Fetch Status = %v, want %s", fetch, itemOpStatusSuccess)
	}
	if fetch.Sub("ServerId").Text != "blob1" {
		t.Fatalf("Fetch ServerId = %q, want blob1", fetch.Sub("ServerId").Text)
	}
	data := ""
	if p := fetch.Sub("Properties"); p != nil {
		if b := p.Sub("Body"); b != nil {
			if d := b.Sub("Data"); d != nil {
				data = d.Text
			}
		}
	}
	if data != "the full untruncated body" {
		t.Fatalf("Properties body = %q, want the full untruncated body", data)
	}
}

// doSyncRaw sends a Sync request carrying arbitrary extra collection children
// (e.g. a Commands block) and returns the response Collection.
func doSyncRaw(t *testing.T, s *Server, syncKey string, extra ...*wbxml.Element) *wbxml.Element {
	t.Helper()
	coll := []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: "inbox"},
	}
	coll = append(coll, extra...)
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

func changeReadCmd(serverID, read string) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageAirSync, Name: "Change", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "ServerId", Text: serverID},
		{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: []*wbxml.Element{
			{Page: wbxml.PageEmail, Name: "Read", Text: read},
		}},
	}}
}

func deleteCmd(serverID string) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageAirSync, Name: "Delete", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "ServerId", Text: serverID},
	}}
}

// TestSyncUpSync verifies the client up-sync path: a Change applies the read flag
// and a Delete removes the item via the Mutator. Successful commands advance the
// SyncKey and are not echoed in Responses.
func TestSyncUpSync(t *testing.T) {
	mut := &stubMutator{}
	s := syncServer(&stubMail{list: []SyncMessage{msg("1", "one")}, seq: 3})
	s.SetMutator(mut)
	doSync(t, s, "0", 50) // prime -> key 1

	coll := doSyncRaw(t, s, "1", &wbxml.Element{Page: wbxml.PageAirSync, Name: "Commands", Children: []*wbxml.Element{
		changeReadCmd("inbox:1", "1"),
		deleteCmd("inbox:2"),
	}})

	if !mut.reads["inbox:1"] {
		t.Fatalf("Change did not set the read flag for inbox:1: %v", mut.reads)
	}
	if len(mut.deletes) != 1 || mut.deletes[0] != "inbox:2" {
		t.Fatalf("Delete not applied: %v", mut.deletes)
	}
	if coll.Sub("SyncKey").Text != "2" {
		t.Fatalf("up-sync SyncKey = %q, want 2", coll.Sub("SyncKey").Text)
	}
	if coll.Sub("Responses") != nil {
		t.Fatalf("successful commands must not be echoed in Responses")
	}
}

// TestSyncUpSyncFailureReported checks that a command the mutator rejects is
// surfaced in the Responses block with the protocol-error status, while the
// SyncKey still advances.
func TestSyncUpSyncFailureReported(t *testing.T) {
	mut := &stubMutator{failOn: "inbox:1"}
	s := syncServer(&stubMail{list: []SyncMessage{msg("1", "one")}, seq: 2})
	s.SetMutator(mut)
	doSync(t, s, "0", 50) // prime -> key 1

	coll := doSyncRaw(t, s, "1", &wbxml.Element{Page: wbxml.PageAirSync, Name: "Commands", Children: []*wbxml.Element{
		deleteCmd("inbox:1"),
	}})

	resp := coll.Sub("Responses")
	if resp == nil {
		t.Fatalf("a failed command must be reported in Responses")
	}
	del := resp.Sub("Delete")
	if del == nil || del.Sub("Status").Text != syncStatusProtocolError {
		t.Fatalf("failed Delete status = %v, want %s", del, syncStatusProtocolError)
	}
	if coll.Sub("SyncKey").Text != "2" {
		t.Fatalf("SyncKey must still advance on a reported failure")
	}
}
