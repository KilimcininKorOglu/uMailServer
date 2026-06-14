package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// stubContacts is a controllable ContactSource: ListItems returns whatever cards
// the test stages, so a test can simulate adds/changes/deletes between syncs by
// mutating the slice.
type stubContacts struct{ items []ContactItem }

func (c *stubContacts) ListItems(string, string) ([]ContactItem, error) { return c.items, nil }

// stubContactMutator records the up-sync changes applied to it and reflects a
// create into src so the reconciliation path (a just-added card must not echo
// back) is exercised. failOn fails the command whose UID/server id matches.
type stubContactMutator struct {
	src             *stubContacts
	created, edited map[string]ContactItem
	deleted         []string
	failOn          string
}

func (m *stubContactMutator) CreateItem(_, _ string, it ContactItem) (string, error) {
	// A client Add carries no UID (the server assigns one), so failOn matches on
	// the last name; an empty failOn never triggers.
	if m.failOn != "" && it.LastName == m.failOn {
		return "", errMutator
	}
	id := it.UID
	if id == "" {
		id = "srv-" + it.LastName
	}
	it.ServerID, it.ETag = id, "e1"
	if m.created == nil {
		m.created = map[string]ContactItem{}
	}
	m.created[id] = it
	if m.src != nil {
		m.src.items = append(m.src.items, it)
	}
	return id, nil
}

func (m *stubContactMutator) UpdateItem(_, _, serverID string, it ContactItem) error {
	if serverID == m.failOn {
		return errMutator
	}
	if m.edited == nil {
		m.edited = map[string]ContactItem{}
	}
	m.edited[serverID] = it
	return nil
}

func (m *stubContactMutator) DeleteItem(_, _, serverID string) error {
	if serverID == m.failOn {
		return errMutator
	}
	m.deleted = append(m.deleted, serverID)
	return nil
}

const sampleVCard = "BEGIN:VCARD\r\nVERSION:3.0\r\n" +
	"UID:c-42\r\nN:Smith;Jane;Q;Dr.;Jr.\r\nFN:Dr. Jane Smith\r\n" +
	"ORG:Acme Inc.;Research\r\nTITLE:Engineer\r\n" +
	"EMAIL;TYPE=INTERNET:jane@acme.test\r\nEMAIL;TYPE=HOME:jane@home.test\r\n" +
	"TEL;TYPE=CELL:+1-555-0100\r\nTEL;TYPE=WORK:+1-555-0199\r\n" +
	"ADR;TYPE=WORK:;;1 Market St;Springfield;CA;90001;USA\r\n" +
	"NOTE:Met at the conference; follow up.\r\nBDAY:1990-04-15\r\nEND:VCARD\r\n"

// TestContactItemFromVCard verifies the vCard projection decodes the structured
// N/ORG/ADR components, maps EMAIL positionally and TEL/ADR by type, and resolves
// the BDAY — the canonical card fields a phone must see.
func TestContactItemFromVCard(t *testing.T) {
	it := ContactItemFromVCard("c-42", "etag1", sampleVCard)
	if it.UID != "c-42" || it.ServerID != "c-42" || it.ETag != "etag1" {
		t.Fatalf("identity wrong: %+v", it)
	}
	if it.LastName != "Smith" || it.FirstName != "Jane" || it.MiddleName != "Q" || it.Title != "Dr." || it.Suffix != "Jr." {
		t.Fatalf("N components wrong: %+v", it)
	}
	if it.FileAs != "Dr. Jane Smith" || it.CompanyName != "Acme Inc." || it.Department != "Research" || it.JobTitle != "Engineer" {
		t.Fatalf("FN/ORG/TITLE wrong: %+v", it)
	}
	if len(it.Emails) != 2 || it.Emails[0] != "jane@acme.test" || it.Emails[1] != "jane@home.test" {
		t.Fatalf("EMAIL positional mapping wrong: %v", it.Emails)
	}
	if it.MobilePhone != "+1-555-0100" || it.BusinessPhone != "+1-555-0199" {
		t.Fatalf("TEL type mapping wrong: %+v", it)
	}
	if it.Business.Street != "1 Market St" || it.Business.City != "Springfield" || it.Business.State != "CA" ||
		it.Business.PostalCode != "90001" || it.Business.Country != "USA" {
		t.Fatalf("ADR WORK parse wrong: %+v", it.Business)
	}
	if it.Body != "Met at the conference; follow up." {
		t.Fatalf("NOTE escaping wrong: %q", it.Body)
	}
	if it.Birthday.Format("2006-01-02") != "1990-04-15" {
		t.Fatalf("BDAY parse wrong: %v", it.Birthday)
	}
}

// TestContactAppData verifies the wire projection emits the populated page-1
// fields and carries the notes through AirSyncBase Body (the 16.x carrier), and
// omits empty fields.
func TestContactAppData(t *testing.T) {
	it := ContactItem{
		FirstName: "Jane", LastName: "Smith", FileAs: "Jane Smith",
		CompanyName: "Acme", JobTitle: "Engineer",
		Emails: []string{"jane@acme.test"}, MobilePhone: "+1-555-0100",
		Body: "note text",
	}
	app := &wbxml.Element{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: contactAppData(it)}
	if app.Sub("FirstName").Text != "Jane" || app.Sub("LastName").Text != "Smith" {
		t.Fatalf("name fields not projected: %v", app)
	}
	if app.Sub("Email1Address").Text != "jane@acme.test" {
		t.Fatalf("Email1Address not projected")
	}
	if app.Sub("MobilePhoneNumber").Text != "+1-555-0100" {
		t.Fatalf("MobilePhoneNumber not projected")
	}
	if app.Sub("HomePhoneNumber") != nil {
		t.Fatalf("empty HomePhoneNumber must be omitted")
	}
	body := app.Sub("Body")
	if body == nil || body.Page != wbxml.PageAirSyncBase || body.Sub("Data").Text != "note text" {
		t.Fatalf("notes must ride AirSyncBase Body: %v", body)
	}
}

// TestBuildVCardRoundTrip verifies the write projection produces canonical vCard
// the read projection parses back identically — a card authored on a phone must
// survive the store round-trip so it renders the same on every surface.
func TestBuildVCardRoundTrip(t *testing.T) {
	src := ContactItem{
		UID: "rt-1", FirstName: "Bob", LastName: "O'Neil", FileAs: "Bob O'Neil",
		CompanyName: "Globex", Department: "Sales", JobTitle: "Rep",
		Emails: []string{"bob@globex.test"}, MobilePhone: "555-0001", HomePhone: "555-0002",
		Business: contactAddress{Street: "7 Elm; Ave", City: "Metropolis", State: "NY", PostalCode: "10001", Country: "USA"},
		Body:     "Note, with; separators",
	}
	got := ContactItemFromVCard("rt-1", "e", BuildVCard(src))
	if got.FirstName != src.FirstName || got.LastName != src.LastName || got.FileAs != src.FileAs {
		t.Fatalf("name fields lost in round-trip: %+v", got)
	}
	if got.CompanyName != src.CompanyName || got.Department != src.Department || got.JobTitle != src.JobTitle {
		t.Fatalf("org fields lost: %+v", got)
	}
	if len(got.Emails) != 1 || got.Emails[0] != "bob@globex.test" || got.MobilePhone != "555-0001" || got.HomePhone != "555-0002" {
		t.Fatalf("contact methods lost: %+v", got)
	}
	if got.Business != src.Business {
		t.Fatalf("ADR escaping lost in round-trip: %+v != %+v", got.Business, src.Business)
	}
	if got.Body != src.Body {
		t.Fatalf("NOTE escaping lost in round-trip: %q != %q", got.Body, src.Body)
	}
}

// TestContactItemFromAppData verifies the client ApplicationData parse reads the
// fields from the Contacts page and the notes from AirSyncBase — the shape a 16.x
// client sends in an up-sync Add/Change.
func TestContactItemFromAppData(t *testing.T) {
	app := &wbxml.Element{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: []*wbxml.Element{
		{Page: wbxml.PageContacts, Name: "FirstName", Text: "Ada"},
		{Page: wbxml.PageContacts, Name: "LastName", Text: "Lovelace"},
		{Page: wbxml.PageContacts, Name: "Email1Address", Text: "ada@x.test"},
		{Page: wbxml.PageContacts, Name: "MobilePhoneNumber", Text: "555-7"},
		{Page: wbxml.PageAirSyncBase, Name: "Body", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSyncBase, Name: "Data", Text: "pioneer"},
		}},
	}}
	it := contactItemFromAppData(app)
	if it.FirstName != "Ada" || it.LastName != "Lovelace" || it.MobilePhone != "555-7" || it.Body != "pioneer" {
		t.Fatalf("parsed fields wrong: %+v", it)
	}
	if len(it.Emails) != 1 || it.Emails[0] != "ada@x.test" {
		t.Fatalf("Email1Address parse wrong: %v", it.Emails)
	}
}

// conServer builds an EAS server wired for contacts Sync. A mail source is set
// too because handleSync's guard requires one (production always wires both).
func conServer(con ContactSource) *Server {
	s := NewServer(allowAuth)
	s.SetMailSource(&stubMail{})
	s.SetContactSource(con)
	s.SetSyncState(&memSyncState{m: map[string]string{}})
	return s
}

// doConSync drives a Sync against the contacts collection and returns the
// response Collection.
func doConSync(t *testing.T, s *Server, syncKey string, window int) *wbxml.Element {
	t.Helper()
	coll := []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: ContactCollectionID("CON1")},
	}
	if window > 0 {
		coll = append(coll, &wbxml.Element{Page: wbxml.PageAirSync, Name: "WindowSize", Text: strconv.Itoa(window)})
	}
	return doConRequest(t, s, coll)
}

// doConSyncCmds drives a contacts Sync carrying client up-sync Commands.
func doConSyncCmds(t *testing.T, s *Server, syncKey string, cmds ...*wbxml.Element) *wbxml.Element {
	t.Helper()
	coll := []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: ContactCollectionID("CON1")},
		{Page: wbxml.PageAirSync, Name: "Commands", Children: cmds},
	}
	return doConRequest(t, s, coll)
}

func doConRequest(t *testing.T, s *Server, coll []*wbxml.Element) *wbxml.Element {
	t.Helper()
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
		t.Fatalf("contacts Sync status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return resp.Sub("Collections").Sub("Collection")
}

// conAddCmd builds a client up-sync Add command (ClientId + page-1 ApplicationData).
func conAddCmd(clientID, first, last, email string) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageAirSync, Name: "Add", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "ClientId", Text: clientID},
		{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: []*wbxml.Element{
			{Page: wbxml.PageContacts, Name: "FirstName", Text: first},
			{Page: wbxml.PageContacts, Name: "LastName", Text: last},
			{Page: wbxml.PageContacts, Name: "Email1Address", Text: email},
		}},
	}}
}

// TestContactSyncFlow exercises the contacts Sync end to end through the
// dispatcher: SyncKey 0 primes; the first real sync streams the card as an Add
// keyed by its vCard UID; an unchanged re-sync is empty; advancing the ETag
// produces a Change; removing the card produces a Delete. This proves the "con:"
// CollectionId routes to the contacts path, not the mail path.
func TestContactSyncFlow(t *testing.T) {
	con := &stubContacts{items: []ContactItem{
		ContactItemFromVCard("c-1", "e1", sampleVCard),
	}}
	s := conServer(con)

	prime := doConSync(t, s, "0", 0)
	if prime.Sub("SyncKey").Text != "1" || prime.Sub("Status").Text != syncStatusSuccess {
		t.Fatalf("prime wrong: %v", prime)
	}
	if prime.Sub("Commands") != nil {
		t.Fatalf("prime must carry no commands")
	}

	first := doConSync(t, s, "1", 0)
	if first.Sub("SyncKey").Text != "2" {
		t.Fatalf("first sync key = %q, want 2", first.Sub("SyncKey").Text)
	}
	if countOps(first, "Add") != 1 {
		t.Fatalf("first sync must Add the card: %d", countOps(first, "Add"))
	}
	add := first.Sub("Commands").Sub("Add")
	if add.Sub("ServerId").Text != "c-1" {
		t.Fatalf("Add ServerId = %q, want c-1 (vCard UID)", add.Sub("ServerId").Text)
	}
	if fn := add.Sub("ApplicationData").Sub("LastName"); fn == nil || fn.Text != "Smith" {
		t.Fatalf("Add must project LastName: %v", fn)
	}

	// Unchanged set -> no commands.
	second := doConSync(t, s, "2", 0)
	if second.Sub("Commands") != nil {
		t.Fatalf("unchanged contacts must produce no commands")
	}

	// Advance the ETag -> a Change.
	con.items[0] = ContactItemFromVCard("c-1", "e2", sampleVCard)
	third := doConSync(t, s, "3", 0)
	if countOps(third, "Change") != 1 {
		t.Fatalf("ETag change must produce a Change: %d", countOps(third, "Change"))
	}

	// Remove the card -> a Delete.
	con.items = nil
	fourth := doConSync(t, s, "4", 0)
	if countOps(fourth, "Delete") != 1 {
		t.Fatalf("removed card must produce a Delete: %d", countOps(fourth, "Delete"))
	}
}

// TestContactUpSync verifies the client up-sync path: an Add creates the card
// through the mutator and is echoed as an Add response mapping the ClientId to
// the assigned ServerId, without being re-emitted as a server-side change in the
// same response; a Delete removes it through the mutator.
func TestContactUpSync(t *testing.T) {
	con := &stubContacts{}
	mut := &stubContactMutator{src: con}
	s := conServer(con)
	s.SetContactMutator(mut)
	doConSync(t, s, "0", 0) // prime -> key 1

	coll := doConSyncCmds(t, s, "1", conAddCmd("c1", "Grace", "Hopper", "grace@x.test"))

	add := coll.Sub("Responses").Sub("Add")
	if add == nil || add.Sub("ClientId").Text != "c1" {
		t.Fatalf("Add response must echo ClientId: %v", add)
	}
	if add.Sub("Status").Text != syncStatusSuccess {
		t.Fatalf("Add response status = %v, want 1", add.Sub("Status"))
	}
	serverID := add.Sub("ServerId").Text
	if serverID == "" || mut.created[serverID].LastName != "Hopper" {
		t.Fatalf("CreateItem not applied with parsed fields: %+v", mut.created)
	}
	// Reconciliation: the client's own Add must not echo back as a server Command.
	if countOps(coll, "Add") != 0 {
		t.Fatalf("client's own Add must not be re-emitted as a server Add: %d", countOps(coll, "Add"))
	}

	// Delete the card on the client; the mutator removes it from the canonical store.
	cur := coll.Sub("SyncKey").Text
	doConSyncCmds(t, s, cur, deleteCmd(serverID))
	if len(mut.deleted) != 1 || mut.deleted[0] != serverID {
		t.Fatalf("DeleteItem not applied: %v", mut.deleted)
	}
}
