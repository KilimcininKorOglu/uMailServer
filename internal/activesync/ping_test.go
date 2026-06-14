package activesync

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// pimCalendarSource is a concurrency-safe CalendarSource for the Ping PIM-ticker
// tests: a held Ping re-enumerates it from the select loop while another
// goroutine mutates it mid-hold, so access must be locked. With rotate set it
// returns the same set in a different order each call, exercising the
// order-independent (map-equality) change comparison.
type pimCalendarSource struct {
	mu     sync.Mutex
	items  []CalendarItem
	rotate bool
	calls  int
}

func (c *pimCalendarSource) ListItems(string, string) ([]CalendarItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	out := make([]CalendarItem, len(c.items))
	copy(out, c.items)
	if c.rotate && len(out) > 1 {
		k := c.calls % len(out)
		rot := make([]CalendarItem, 0, len(out))
		rot = append(rot, out[k:]...)
		rot = append(rot, out[:k]...)
		out = rot
	}
	return out, nil
}

func (c *pimCalendarSource) set(items []CalendarItem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = items
}

// stubNotifier is a MailboxNotifier whose channel the test fills with the folder
// names a held Ping should observe as changes.
type stubNotifier struct{ ch chan string }

func (n stubNotifier) Subscribe(string) (<-chan string, func()) { return n.ch, func() {} }

// pingReq builds a Ping request: an optional HeartbeatInterval plus a folder
// list of {Id, Class} pairs.
func pingReq(heartbeat int, folders ...[2]string) *wbxml.Element {
	root := &wbxml.Element{Page: wbxml.PagePing, Name: "Ping"}
	if heartbeat > 0 {
		root.Children = append(root.Children, &wbxml.Element{Page: wbxml.PagePing, Name: "HeartbeatInterval", Text: strconv.Itoa(heartbeat)})
	}
	if len(folders) > 0 {
		fs := &wbxml.Element{Page: wbxml.PagePing, Name: "Folders"}
		for _, f := range folders {
			fs.Children = append(fs.Children, &wbxml.Element{Page: wbxml.PagePing, Name: "Folder", Children: []*wbxml.Element{
				{Page: wbxml.PagePing, Name: "Id", Text: f[0]},
				{Page: wbxml.PagePing, Name: "Class", Text: f[1]},
			}})
		}
		root.Children = append(root.Children, fs)
	}
	return root
}

// doPing POSTs a Ping request through the full transport and returns the decoded
// response. It expects a 200 with a body (every status but a client disconnect).
func doPing(t *testing.T, s *Server, body *wbxml.Element) *wbxml.Element {
	t.Helper()
	b, err := wbxml.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=Ping&DeviceId=DEV1", bytes.NewReader(b))
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Ping status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

func folderTexts(folders *wbxml.Element) []string {
	var out []string
	if folders == nil {
		return out
	}
	for _, c := range folders.Children {
		if c.Name == "Folder" {
			out = append(out, c.Text)
		}
	}
	return out
}

// TestPingParseAndMarshal proves the page-13 codec round-trips: a request's
// heartbeat and folder ids decode, and a Status-2 response carries the changed
// ids back as Folder text (the response shape differs from the request, where a
// Folder nests Id/Class).
func TestPingParseAndMarshal(t *testing.T) {
	b, err := wbxml.Marshal(pingReq(300, [2]string{"INBOX", "Email"}, [2]string{"cal:abc", "Calendar"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	hb, folders := parsePing(b)
	if hb != 300 {
		t.Fatalf("heartbeat = %d, want 300", hb)
	}
	if len(folders) != 2 || folders[0].id != "INBOX" || folders[0].class != "Email" || folders[1].id != "cal:abc" {
		t.Fatalf("folders = %+v", folders)
	}

	resp, err := marshalPing(pingStatusChanges, 0, []string{"INBOX", "cal:abc"})
	if err != nil {
		t.Fatalf("marshalPing: %v", err)
	}
	root, err := wbxml.Unmarshal(resp)
	if err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if textOf(root.Sub("Status")) != pingStatusChanges {
		t.Fatalf("Status = %q, want 2", textOf(root.Sub("Status")))
	}
	if got := folderTexts(root.Sub("Folders")); len(got) != 2 || got[0] != "INBOX" || got[1] != "cal:abc" {
		t.Fatalf("changed folders = %v", got)
	}
}

// TestPingChangeWakesMonitoredFolder proves a held Ping returns Status 2 with the
// changed folder as soon as the hub reports a change to a monitored mail folder —
// the push that lets a phone fetch new mail without polling.
func TestPingChangeWakesMonitoredFolder(t *testing.T) {
	s := NewServer(allowAuth)
	ch := make(chan string, 4)
	ch <- "INBOX"
	s.SetMailNotifier(stubNotifier{ch: ch})

	resp := doPing(t, s, pingReq(120, [2]string{"INBOX", "Email"}))
	if st := textOf(resp.Sub("Status")); st != pingStatusChanges {
		t.Fatalf("Status = %q, want 2", st)
	}
	if got := folderTexts(resp.Sub("Folders")); len(got) != 1 || got[0] != "INBOX" {
		t.Fatalf("changed folders = %v, want [INBOX]", got)
	}
}

// TestPingIgnoresUnmonitoredFolder proves a change to a folder the Ping does not
// monitor does not wake it: the loop must keep holding until a monitored folder
// changes, or a busy mailbox would spuriously return for every unrelated folder.
func TestPingIgnoresUnmonitoredFolder(t *testing.T) {
	s := NewServer(allowAuth)
	ch := make(chan string, 4)
	ch <- "Sent"  // not monitored — must be ignored
	ch <- "INBOX" // monitored — must wake the Ping
	s.SetMailNotifier(stubNotifier{ch: ch})

	resp := doPing(t, s, pingReq(120, [2]string{"INBOX", "Email"}))
	if got := folderTexts(resp.Sub("Folders")); len(got) != 1 || got[0] != "INBOX" {
		t.Fatalf("changed folders = %v, want [INBOX]", got)
	}
}

// TestPingHeartbeatExpiry proves that with nothing changing, the Ping holds for
// the heartbeat then returns Status 1 (no changes), prompting the client to
// re-issue. The heartbeat unit is shrunk so the test does not wait a real minute.
func TestPingHeartbeatExpiry(t *testing.T) {
	old := heartbeatUnit
	heartbeatUnit = time.Millisecond
	defer func() { heartbeatUnit = old }()

	s := NewServer(allowAuth)
	s.SetMailNotifier(stubNotifier{ch: make(chan string)}) // never emits

	resp := doPing(t, s, pingReq(60, [2]string{"INBOX", "Email"}))
	if st := textOf(resp.Sub("Status")); st != pingStatusNoChanges {
		t.Fatalf("Status = %q, want 1", st)
	}
	if resp.Sub("Folders") != nil {
		t.Fatalf("Status 1 must carry no Folders")
	}
}

// TestPingMissingParams proves a Ping with no folder list and no cached state is
// rejected with Status 3, the signal that the client must send the full request.
func TestPingMissingParams(t *testing.T) {
	s := NewServer(allowAuth)
	resp := doPing(t, s, pingReq(120))
	if st := textOf(resp.Sub("Status")); st != pingStatusMissingParams {
		t.Fatalf("Status = %q, want 3", st)
	}
}

// TestPingHeartbeatOutOfRange proves an out-of-range heartbeat returns Status 5
// with the nearest acceptable interval, so the client can retry inside the range
// rather than the server silently holding for an unintended duration.
func TestPingHeartbeatOutOfRange(t *testing.T) {
	s := NewServer(allowAuth)
	resp := doPing(t, s, pingReq(10, [2]string{"INBOX", "Email"}))
	if st := textOf(resp.Sub("Status")); st != pingStatusHeartbeatRange {
		t.Fatalf("Status = %q, want 5", st)
	}
	if hb := textOf(resp.Sub("HeartbeatInterval")); hb != strconv.Itoa(minHeartbeat) {
		t.Fatalf("HeartbeatInterval = %q, want %d", hb, minHeartbeat)
	}
}

// TestPingBareRequestUsesCache proves the server saves a device's heartbeat and
// folder list (MS-ASCMD), so a later bare Ping with no body still monitors the
// cached folders instead of being rejected with Status 3.
func TestPingBareRequestUsesCache(t *testing.T) {
	s := NewServer(allowAuth)
	ch := make(chan string, 4)
	s.SetMailNotifier(stubNotifier{ch: ch})

	ch <- "INBOX"
	if st := textOf(doPing(t, s, pingReq(120, [2]string{"INBOX", "Email"})).Sub("Status")); st != pingStatusChanges {
		t.Fatalf("priming Ping Status = %q, want 2", st)
	}

	ch <- "INBOX"
	resp := doPing(t, s, pingReq(0)) // bare: no heartbeat, no folders
	if st := textOf(resp.Sub("Status")); st != pingStatusChanges {
		t.Fatalf("bare Ping Status = %q, want 2 (cache miss?)", st)
	}
}

// TestPingClientDisconnect proves a Ping whose request context is already
// canceled returns without writing a body, so a dropped long-poll connection is
// not treated as a server error.
func TestPingClientDisconnect(t *testing.T) {
	s := NewServer(allowAuth)
	s.SetMailNotifier(stubNotifier{ch: make(chan string)})
	b, err := wbxml.Marshal(pingReq(120, [2]string{"INBOX", "Email"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Microsoft-Server-ActiveSync?Cmd=Ping&DeviceId=DEV1", bytes.NewReader(b)).WithContext(ctx)
	s.ServeHTTP(rec, req)
	if rec.Body.Len() != 0 {
		t.Fatalf("disconnect should write no body, got %d bytes", rec.Body.Len())
	}
}

// TestPingPIMChangeWakesViaTicker proves a Ping on a pure-PIM collection (no
// mail folder, so the hub channel is never used) wakes with Status 2 when the
// collection changes mid-hold. PIM classes have no hub, so the ticker that
// re-enumerates and diffs is the only thing that can detect the change — this is
// the push that lets a phone see a new calendar event without a manual sync.
func TestPingPIMChangeWakesViaTicker(t *testing.T) {
	oldUnit, oldPoll := heartbeatUnit, pimPollInterval
	heartbeatUnit, pimPollInterval = 20*time.Millisecond, 5*time.Millisecond
	defer func() { heartbeatUnit, pimPollInterval = oldUnit, oldPoll }()

	src := &pimCalendarSource{items: []CalendarItem{{ServerID: "e1", ETag: "v1"}}}
	s := NewServer(allowAuth)
	s.SetCalendarSource(src)

	// Advance the item's ETag after the baseline is captured; the ticker must
	// observe the differing serverId->etag set and wake.
	go func() {
		time.Sleep(30 * time.Millisecond)
		src.set([]CalendarItem{{ServerID: "e1", ETag: "v2"}})
	}()

	calID := calendarCollectionPrefix + "fid"
	resp := doPing(t, s, pingReq(600, [2]string{calID, "Calendar"}))
	if st := textOf(resp.Sub("Status")); st != pingStatusChanges {
		t.Fatalf("Status = %q, want 2 (PIM change via ticker)", st)
	}
	if got := folderTexts(resp.Sub("Folders")); len(got) != 1 || got[0] != calID {
		t.Fatalf("changed folders = %v, want [%s]", got, calID)
	}
}

// TestPingPIMStableSetDoesNotWake proves a PIM collection whose contents are
// unchanged does not wake the Ping even when the source returns the same items
// in a different order each poll. The comparison is map equality, not an
// order-sensitive digest, so a re-enumeration in a new order is not mistaken for
// a change; the Ping holds to the heartbeat and returns Status 1.
func TestPingPIMStableSetDoesNotWake(t *testing.T) {
	oldUnit, oldPoll := heartbeatUnit, pimPollInterval
	heartbeatUnit, pimPollInterval = time.Millisecond, 2*time.Millisecond
	defer func() { heartbeatUnit, pimPollInterval = oldUnit, oldPoll }()

	src := &pimCalendarSource{rotate: true, items: []CalendarItem{
		{ServerID: "a", ETag: "1"}, {ServerID: "b", ETag: "1"}, {ServerID: "c", ETag: "1"},
	}}
	s := NewServer(allowAuth)
	s.SetCalendarSource(src)

	resp := doPing(t, s, pingReq(60, [2]string{calendarCollectionPrefix + "fid", "Calendar"}))
	if st := textOf(resp.Sub("Status")); st != pingStatusNoChanges {
		t.Fatalf("Status = %q, want 1 (a reordered but unchanged set must not wake)", st)
	}
}
