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

// TestSearchUnsupportedStore proves a non-GAL store (here Mailbox) reports the
// overall error Status with no Response block, the signal for the client to fall
// back rather than parse an empty success or hang. This guards the deliberate
// GAL-only scope: advertising Search must not imply mailbox search works.
func TestSearchUnsupportedStore(t *testing.T) {
	s := searchServer(t, &stubGALSource{})

	body := searchEl("Search", searchEl("Store",
		searchText("Name", "Mailbox"),
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
