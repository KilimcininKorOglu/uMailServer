package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
	"github.com/umailserver/umailserver/internal/db"
)

// stubGALSource is a deterministic GALSource: it records the query it was asked
// and returns a fixed entry set, so the handler's dispatch, range windowing and
// element mapping can be asserted without a live directory.
type stubGALSource struct {
	entries   []GALResult
	lastQuery string
}

func (s *stubGALSource) ResolveGAL(query string) []GALResult {
	s.lastQuery = query
	return s.entries
}

// searchServer builds an EAS server with a seeded, provisioned device (so a
// Search clears the provisioning gate) and the given GAL source wired.
func searchServer(t *testing.T, gal GALSource) *Server {
	t.Helper()
	s, database := provisionServer(t)
	if err := database.PutEASDevice(&db.EASDevice{
		Email: "bob@x.test", DeviceID: "DEV1", PolicyKey: "12345",
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	s.SetGALSource(gal)
	return s
}

// doSearch POSTs a Search request through the full transport (clearing the
// provisioning gate with the seeded key) and returns the decoded response.
func doSearch(t *testing.T, s *Server, body *wbxml.Element) *wbxml.Element {
	t.Helper()
	b, err := wbxml.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/Microsoft-Server-ActiveSync?Cmd=Search&DeviceId=DEV1", bytes.NewReader(b))
	req.Header.Set("X-MS-PolicyKey", "12345")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Search status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

// searchEl builds a Search-code-page (15) element with the given children.
func searchEl(name string, children ...*wbxml.Element) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageSearch, Name: name, Children: children}
}

// searchText builds a Search-code-page (15) leaf element carrying text.
func searchText(name, text string) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageSearch, Name: name, Text: text}
}

// galQuery wraps a GAL store query string (and optional Range) in the request
// envelope: Search>Store>{Name=GAL, Query, Options>Range}.
func galQuery(query, rng string) *wbxml.Element {
	store := searchEl("Store", searchText("Name", "GAL"), searchText("Query", query))
	if rng != "" {
		store.Children = append(store.Children, searchEl("Options", searchText("Range", rng)))
	}
	return searchEl("Search", store)
}

// resultsOf returns the Result elements of a successful Search response's Store.
func resultsOf(t *testing.T, resp *wbxml.Element) (*wbxml.Element, []*wbxml.Element) {
	t.Helper()
	response := resp.Sub("Response")
	if response == nil {
		t.Fatalf("response missing Response block; top Status = %q", subText(resp, "Status"))
	}
	store := response.Sub("Store")
	if store == nil {
		t.Fatal("Response missing Store")
	}
	var results []*wbxml.Element
	for _, c := range store.Children {
		if c.Name == "Result" {
			results = append(results, c)
		}
	}
	return store, results
}

// TestSearchGALReturnsMatches proves a GAL Search resolves the query through the
// canonical source and encodes each match as Result>Properties carrying
// DisplayName, Alias and EmailAddress — the fields a mobile recipient picker
// renders. It also asserts the client's query reaches the source (a handler that
// dropped it would return everyone) and that Range/Total trail the results.
func TestSearchGALReturnsMatches(t *testing.T) {
	gal := &stubGALSource{entries: []GALResult{
		{DisplayName: "Alice Anderson", Email: "alice@x.test", Alias: "alice"},
		{DisplayName: "Allan Apple", Email: "allan@x.test", Alias: "allan"},
	}}
	s := searchServer(t, gal)

	resp := doSearch(t, s, galQuery("al", ""))

	if got := subText(resp, "Status"); got != searchStatusSuccess {
		t.Fatalf("top Status = %q, want %q", got, searchStatusSuccess)
	}
	if gal.lastQuery != "al" {
		t.Errorf("query passed to source = %q, want %q", gal.lastQuery, "al")
	}
	store, results := resultsOf(t, resp)
	if got := subText(store, "Status"); got != searchStatusStoreSuccess {
		t.Errorf("store Status = %q, want %q", got, searchStatusStoreSuccess)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	props := results[0].Sub("Properties")
	if props == nil {
		t.Fatal("Result missing Properties")
	}
	if got := subText(props, "DisplayName"); got != "Alice Anderson" {
		t.Errorf("DisplayName = %q, want %q", got, "Alice Anderson")
	}
	if got := subText(props, "EmailAddress"); got != "alice@x.test" {
		t.Errorf("EmailAddress = %q, want %q", got, "alice@x.test")
	}
	if got := subText(props, "Alias"); got != "alice" {
		t.Errorf("Alias = %q, want %q", got, "alice")
	}
	if got := subText(store, "Total"); got != "2" {
		t.Errorf("Total = %q, want 2", got)
	}
	if got := subText(store, "Range"); got != "0-1" {
		t.Errorf("Range = %q, want 0-1", got)
	}
}

// TestSearchGALAppliesRange proves the Options>Range window is honored: a
// "1-2" range over five matches returns exactly entries 1 and 2 with Range "1-2"
// and the full Total. A handler that ignored Range would over-send the whole set
// and a paging client would never advance.
func TestSearchGALAppliesRange(t *testing.T) {
	gal := &stubGALSource{entries: []GALResult{
		{DisplayName: "E0", Email: "e0@x.test"},
		{DisplayName: "E1", Email: "e1@x.test"},
		{DisplayName: "E2", Email: "e2@x.test"},
		{DisplayName: "E3", Email: "e3@x.test"},
		{DisplayName: "E4", Email: "e4@x.test"},
	}}
	s := searchServer(t, gal)

	resp := doSearch(t, s, galQuery("e", "1-2"))

	store, results := resultsOf(t, resp)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (range 1-2)", len(results))
	}
	if got := subText(results[0].Sub("Properties"), "DisplayName"); got != "E1" {
		t.Errorf("first windowed result = %q, want E1", got)
	}
	if got := subText(results[1].Sub("Properties"), "DisplayName"); got != "E2" {
		t.Errorf("second windowed result = %q, want E2", got)
	}
	if got := subText(store, "Range"); got != "1-2" {
		t.Errorf("Range = %q, want 1-2", got)
	}
	if got := subText(store, "Total"); got != "5" {
		t.Errorf("Total = %q, want 5 (full match count, not window size)", got)
	}
}

// TestSearchGALOmitsEmptyAlias proves a match with no alias emits only
// DisplayName and EmailAddress — no empty Alias element. Emitting empty optional
// GAL properties is what bloats the response and trips stricter clients.
func TestSearchGALOmitsEmptyAlias(t *testing.T) {
	gal := &stubGALSource{entries: []GALResult{
		{DisplayName: "Room 1", Email: "room1@x.test"}, // Alias empty
	}}
	s := searchServer(t, gal)

	resp := doSearch(t, s, galQuery("room", ""))

	_, results := resultsOf(t, resp)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	props := results[0].Sub("Properties")
	if props.Sub("Alias") != nil {
		t.Error("empty Alias should be omitted, but an Alias element was emitted")
	}
	if got := subText(props, "EmailAddress"); got != "room1@x.test" {
		t.Errorf("EmailAddress = %q, want room1@x.test", got)
	}
}

// TestSearchGALNoMatches proves a query with no hits is a success with an empty
// store (Status 1, no Result, no Range/Total) — not an error. A regression that
// reported an error status on zero matches would make a normal empty search look
// like a server failure on the device.
func TestSearchGALNoMatches(t *testing.T) {
	s := searchServer(t, &stubGALSource{entries: nil})

	resp := doSearch(t, s, galQuery("nobody", ""))

	if got := subText(resp, "Status"); got != searchStatusSuccess {
		t.Fatalf("top Status = %q, want %q", got, searchStatusSuccess)
	}
	store, results := resultsOf(t, resp)
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
	if got := subText(store, "Status"); got != searchStatusStoreSuccess {
		t.Errorf("store Status = %q, want %q", got, searchStatusStoreSuccess)
	}
	if store.Sub("Total") != nil {
		t.Error("Total should be omitted for an empty result set")
	}
}

// TestSearchUnsupportedStore proves an unknown store (here DocumentLibrary)
// reports the overall error Status with no Response block, the signal for the
// client to fall back rather than parse an empty success or hang. Only GAL and
// Mailbox are implemented; anything else must report the error.
func TestSearchUnsupportedStore(t *testing.T) {
	s := searchServer(t, &stubGALSource{})

	body := searchEl("Search", searchEl("Store",
		searchText("Name", "DocumentLibrary"),
		searchText("Query", "anything"),
	))
	resp := doSearch(t, s, body)

	if got := subText(resp, "Status"); got != searchStatusServerError {
		t.Fatalf("top Status = %q, want %q for unsupported store", got, searchStatusServerError)
	}
	if resp.Sub("Response") != nil {
		t.Error("error response should carry only Status, no Response block")
	}
}

// stubMailSearch is a deterministic MailSearch: it records the query it was asked
// and returns a fixed, unwindowed hit set (the handler windows).
type stubMailSearch struct {
	hits      []MailHit
	lastQuery string
}

func (s *stubMailSearch) SearchMail(_, query string) ([]MailHit, error) {
	s.lastQuery = query
	return s.hits, nil
}

// stubMailSource serves messages by server id for the Fetch path; the other
// MailSource methods are unused by the Search/ItemOperations tests.
type stubMailSource struct{ msgs map[string]SyncMessage }

func (s stubMailSource) ListMessages(string, string) ([]SyncMessage, error) { return nil, nil }
func (s stubMailSource) ChangesSince(string, string, uint64) ([]SyncMessage, []SyncMessage, []string, uint64, error) {
	return nil, nil, nil, 0, nil
}
func (s stubMailSource) CurrentSeq(string) (uint64, error) { return 0, nil }
func (s stubMailSource) Fetch(_, _, serverID string) (*SyncMessage, error) {
	if m, ok := s.msgs[serverID]; ok {
		return &m, nil
	}
	return nil, nil
}

// mailboxSearchServer builds an EAS server with a seeded, provisioned device and
// both the mailbox search source and the mail source wired.
func mailboxSearchServer(t *testing.T, ms MailSearch, mail MailSource) *Server {
	t.Helper()
	s, database := provisionServer(t)
	if err := database.PutEASDevice(&db.EASDevice{
		Email: "bob@x.test", DeviceID: "DEV1", PolicyKey: "12345",
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	s.SetMailSearch(ms)
	s.SetMailSource(mail)
	return s
}

// mailboxReq builds a Mailbox Search request: Search>Store>{Name=Mailbox,
// Query>And>FreeText, Options>Range}.
func mailboxReq(freetext, rng string) *wbxml.Element {
	store := searchEl("Store",
		searchText("Name", "Mailbox"),
		searchEl("Query", searchEl("And", searchText("FreeText", freetext))),
	)
	if rng != "" {
		store.Children = append(store.Children, searchEl("Options", searchText("Range", rng)))
	}
	return searchEl("Search", store)
}

// doItemOps POSTs an ItemOperations request through the full transport and returns
// the decoded response.
func doItemOps(t *testing.T, s *Server, body *wbxml.Element) *wbxml.Element {
	t.Helper()
	b, err := wbxml.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/Microsoft-Server-ActiveSync?Cmd=ItemOperations&DeviceId=DEV1", bytes.NewReader(b))
	req.Header.Set("X-MS-PolicyKey", "12345")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ItemOperations status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

// TestSearchMailboxReturnsHits proves a Mailbox Search passes the free-text term
// to the canonical search, applies the requested Range window over the full match
// set, and encodes each hit as a Result with its AirSync Class/CollectionId, an
// opaque LongId that decodes back to the (folder, server-id) identity, and the
// message under Properties. Range describes the emitted rows and Total the exact
// match count.
func TestSearchMailboxReturnsHits(t *testing.T) {
	ms := &stubMailSearch{hits: []MailHit{
		{CollectionID: "INBOX", ServerID: "blob-1", Class: "Email"},
		{CollectionID: "INBOX", ServerID: "blob-2", Class: "Email"},
	}}
	mail := stubMailSource{msgs: map[string]SyncMessage{
		"blob-1": {Subject: "Quarterly report", From: "alice@x.test", Body: "numbers inside"},
		"blob-2": {Subject: "Lunch?", From: "carol@x.test", Body: "tomorrow"},
	}}
	s := mailboxSearchServer(t, ms, mail)

	resp := doSearch(t, s, mailboxReq("report", "0-9"))

	if got := subText(resp, "Status"); got != searchStatusSuccess {
		t.Fatalf("top Status = %q, want %q", got, searchStatusSuccess)
	}
	if ms.lastQuery != "report" {
		t.Errorf("free-text passed to search = %q, want %q", ms.lastQuery, "report")
	}
	store, results := resultsOf(t, resp)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	r0 := results[0]
	if got := subText(r0, "Class"); got != "Email" {
		t.Errorf("Result Class = %q, want Email", got)
	}
	if got := subText(r0, "CollectionId"); got != "INBOX" {
		t.Errorf("Result CollectionId = %q, want INBOX", got)
	}
	cid, sid, ok := decodeLongID(subText(r0, "LongId"))
	if !ok || cid != "INBOX" || sid != "blob-1" {
		t.Errorf("LongId decoded to (%q,%q,%v), want (INBOX,blob-1,true)", cid, sid, ok)
	}
	if got := subText(r0.Sub("Properties"), "Subject"); got != "Quarterly report" {
		t.Errorf("Result Properties Subject = %q, want %q", got, "Quarterly report")
	}
	if got := subText(store, "Range"); got != "0-1" {
		t.Errorf("Range = %q, want 0-1", got)
	}
	if got := subText(store, "Total"); got != "2" {
		t.Errorf("Total = %q, want 2", got)
	}
}

// TestSearchMailboxFetchByLongID is the load-bearing round-trip: a Mailbox Search
// returns a LongId, and feeding that LongId back through ItemOperations Fetch
// (Store=Mailbox) returns the full, untruncated message body. A result the client
// cannot open is worthless; this proves the open path, not just that search
// returned rows.
func TestSearchMailboxFetchByLongID(t *testing.T) {
	ms := &stubMailSearch{hits: []MailHit{{CollectionID: "INBOX", ServerID: "blob-7", Class: "Email"}}}
	mail := stubMailSource{msgs: map[string]SyncMessage{
		"blob-7": {Subject: "Invoice 42", From: "billing@x.test", Body: "amount due: 100", BodyType: "1"},
	}}
	s := mailboxSearchServer(t, ms, mail)

	resp := doSearch(t, s, mailboxReq("invoice", "0-9"))
	_, results := resultsOf(t, resp)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	longID := subText(results[0], "LongId")
	if longID == "" {
		t.Fatal("Result missing LongId")
	}

	fetchReq := &wbxml.Element{Page: wbxml.PageItemOperations, Name: "ItemOperations", Children: []*wbxml.Element{
		{Page: wbxml.PageItemOperations, Name: "Fetch", Children: []*wbxml.Element{
			{Page: wbxml.PageItemOperations, Name: "Store", Text: "Mailbox"},
			{Page: wbxml.PageSearch, Name: "LongId", Text: longID},
		}},
	}}
	fr := doItemOps(t, s, fetchReq)

	if got := subText(fr, "Status"); got != itemOpStatusSuccess {
		t.Fatalf("ItemOperations Status = %q, want %q", got, itemOpStatusSuccess)
	}
	fetch := fr.Sub("Response").Sub("Fetch")
	if fetch == nil {
		t.Fatal("ItemOperations response missing Fetch")
	}
	if got := subText(fetch, "Status"); got != itemOpStatusSuccess {
		t.Errorf("Fetch Status = %q, want %q", got, itemOpStatusSuccess)
	}
	if got := subText(fetch, "LongId"); got != longID {
		t.Errorf("Fetch echoed LongId = %q, want %q", got, longID)
	}
	props := fetch.Sub("Properties")
	if got := subText(props, "Subject"); got != "Invoice 42" {
		t.Errorf("fetched Subject = %q, want %q", got, "Invoice 42")
	}
	body := props.Sub("Body")
	if body == nil || body.Sub("Data") == nil || body.Sub("Data").Text != "amount due: 100" {
		t.Errorf("fetched body = %v, want the full message body %q", body, "amount due: 100")
	}
}
