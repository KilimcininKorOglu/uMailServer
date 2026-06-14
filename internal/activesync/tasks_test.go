package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// stubTasks is a controllable TaskSource: ListItems returns whatever to-dos the
// test stages, so a test can simulate adds/changes/deletes between syncs by
// mutating the slice.
type stubTasks struct{ items []TaskItem }

func (t *stubTasks) ListItems(string, string) ([]TaskItem, error) { return t.items, nil }

// stubTaskMutator records the up-sync changes applied to it and reflects a create
// into src so the reconciliation path (a just-added to-do must not echo back) is
// exercised. failOn fails the command whose subject/server id matches.
type stubTaskMutator struct {
	src             *stubTasks
	created, edited map[string]TaskItem
	deleted         []string
	failOn          string
}

func (m *stubTaskMutator) CreateItem(_, _ string, it TaskItem) (string, error) {
	// A client Add carries no UID (the server assigns one), so failOn matches on
	// the subject; an empty failOn never triggers.
	if m.failOn != "" && it.Subject == m.failOn {
		return "", errMutator
	}
	id := it.UID
	if id == "" {
		id = "srv-" + it.Subject
	}
	it.ServerID, it.ETag = id, "e1"
	if m.created == nil {
		m.created = map[string]TaskItem{}
	}
	m.created[id] = it
	if m.src != nil {
		m.src.items = append(m.src.items, it)
	}
	return id, nil
}

func (m *stubTaskMutator) UpdateItem(_, _, serverID string, it TaskItem) error {
	if serverID == m.failOn {
		return errMutator
	}
	if m.edited == nil {
		m.edited = map[string]TaskItem{}
	}
	m.edited[serverID] = it
	return nil
}

func (m *stubTaskMutator) DeleteItem(_, _, serverID string) error {
	if serverID == m.failOn {
		return errMutator
	}
	m.deleted = append(m.deleted, serverID)
	return nil
}

const sampleVTODO = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\n" +
	"UID:t-7\r\nSUMMARY:File taxes\r\nDESCRIPTION:Before the deadline; no excuses.\r\n" +
	"DTSTART:20260401T090000Z\r\nDUE:20260415T170000Z\r\n" +
	"PRIORITY:1\r\nSTATUS:NEEDS-ACTION\r\nCLASS:PRIVATE\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"

// TestTaskItemFromVTODO verifies the VTODO projection resolves the dates to
// absolute UTC instants, maps PRIORITY to importance and CLASS to sensitivity,
// and decodes the escaped description.
func TestTaskItemFromVTODO(t *testing.T) {
	it := TaskItemFromVTODO("t-7", "etag1", sampleVTODO)
	if it.UID != "t-7" || it.ServerID != "t-7" || it.ETag != "etag1" {
		t.Fatalf("identity wrong: %+v", it)
	}
	if it.Subject != "File taxes" || it.Body != "Before the deadline; no excuses." {
		t.Fatalf("text fields wrong: %+v", it)
	}
	if it.Start.UTC().Format(compactDateTime) != "20260401T090000Z" {
		t.Fatalf("DTSTART parse wrong: %v", it.Start)
	}
	if it.Due.UTC().Format(compactDateTime) != "20260415T170000Z" {
		t.Fatalf("DUE parse wrong: %v", it.Due)
	}
	if it.Importance != "2" {
		t.Fatalf("PRIORITY:1 must map to high importance: %q", it.Importance)
	}
	if it.Sensitivity != "2" {
		t.Fatalf("CLASS:PRIVATE must map to sensitivity 2: %q", it.Sensitivity)
	}
	if it.Complete {
		t.Fatalf("NEEDS-ACTION must not be complete")
	}
}

// TestTaskAppData verifies the wire projection emits the page-9 fields with each
// date paired to its UTC twin, the completion flag, and the notes through
// AirSyncBase Body (the 16.x carrier).
func TestTaskAppData(t *testing.T) {
	it := TaskItemFromVTODO("t-7", "e", sampleVTODO)
	app := &wbxml.Element{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: taskAppData(it)}
	if app.Sub("Subject").Text != "File taxes" {
		t.Fatalf("Subject not projected")
	}
	if app.Sub("DueDate").Text != "20260415T170000Z" || app.Sub("UtcDueDate").Text != "20260415T170000Z" {
		t.Fatalf("DueDate/UtcDueDate not projected as compact UTC: %v / %v", app.Sub("DueDate"), app.Sub("UtcDueDate"))
	}
	if app.Sub("Complete").Text != "0" {
		t.Fatalf("Complete must be 0 for an open task")
	}
	if app.Sub("Importance").Text != "2" {
		t.Fatalf("Importance not projected")
	}
	body := app.Sub("Body")
	if body == nil || body.Page != wbxml.PageAirSyncBase || body.Sub("Data").Text != "Before the deadline; no excuses." {
		t.Fatalf("notes must ride AirSyncBase Body: %v", body)
	}
}

// TestBuildVTODORoundTrip verifies the write projection produces canonical VTODO
// the read projection parses back identically — a to-do authored on a phone must
// survive the store round-trip so it renders the same on every surface, including
// the completed state.
func TestBuildVTODORoundTrip(t *testing.T) {
	src := TaskItem{
		UID: "rt-1", Subject: "Ship; release", Body: "Notes, with; separators",
		Start: parseCompactTime("20260501T080000Z"), Due: parseCompactTime("20260510T120000Z"),
		Complete: true, DateCompleted: parseCompactTime("20260509T100000Z"),
		Importance: "0", Sensitivity: "3",
	}
	got := TaskItemFromVTODO("rt-1", "e", BuildVTODO(src))
	if got.Subject != src.Subject || got.Body != src.Body {
		t.Fatalf("text fields lost in round-trip: %+v", got)
	}
	if !got.Start.Equal(src.Start) || !got.Due.Equal(src.Due) {
		t.Fatalf("dates lost: start %v due %v", got.Start, got.Due)
	}
	if !got.Complete || !got.DateCompleted.Equal(src.DateCompleted) {
		t.Fatalf("completion state lost: complete=%v completed=%v", got.Complete, got.DateCompleted)
	}
	if got.Importance != "0" {
		t.Fatalf("low importance not round-tripped: %q", got.Importance)
	}
	if got.Sensitivity != "3" {
		t.Fatalf("CLASS:CONFIDENTIAL not round-tripped: %q", got.Sensitivity)
	}
}

// TestTaskItemFromAppData verifies the client ApplicationData parse reads the
// fields from the Tasks page (preferring the UTC date twin) and the notes from
// AirSyncBase — the shape a 16.x client sends in an up-sync Add/Change.
func TestTaskItemFromAppData(t *testing.T) {
	app := &wbxml.Element{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: []*wbxml.Element{
		{Page: wbxml.PageTasks, Name: "Subject", Text: "Renew license"},
		{Page: wbxml.PageTasks, Name: "UtcDueDate", Text: "20260620T100000Z"},
		{Page: wbxml.PageTasks, Name: "Complete", Text: "1"},
		{Page: wbxml.PageTasks, Name: "Importance", Text: "2"},
		{Page: wbxml.PageAirSyncBase, Name: "Body", Children: []*wbxml.Element{
			{Page: wbxml.PageAirSyncBase, Name: "Data", Text: "DMV"},
		}},
	}}
	it := taskItemFromAppData(app)
	if it.Subject != "Renew license" || it.Body != "DMV" || it.Importance != "2" || !it.Complete {
		t.Fatalf("parsed fields wrong: %+v", it)
	}
	if it.Due.UTC().Format(compactDateTime) != "20260620T100000Z" {
		t.Fatalf("UtcDueDate parse wrong: %v", it.Due)
	}
}

// taskServer builds an EAS server wired for tasks Sync. A mail source is set too
// because handleSync's guard requires one (production always wires both).
func taskServer(tasks TaskSource) *Server {
	s := NewServer(allowAuth)
	s.SetMailSource(&stubMail{})
	s.SetTaskSource(tasks)
	s.SetSyncState(&memSyncState{m: map[string]string{}})
	return s
}

func doTaskRequest(t *testing.T, s *Server, coll []*wbxml.Element) *wbxml.Element {
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
		t.Fatalf("tasks Sync status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return resp.Sub("Collections").Sub("Collection")
}

// doTaskSync drives a Sync against the tasks collection and returns the response
// Collection.
func doTaskSync(t *testing.T, s *Server, syncKey string, window int) *wbxml.Element {
	t.Helper()
	coll := []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: TaskCollectionID("TSK1")},
	}
	if window > 0 {
		coll = append(coll, &wbxml.Element{Page: wbxml.PageAirSync, Name: "WindowSize", Text: strconv.Itoa(window)})
	}
	return doTaskRequest(t, s, coll)
}

// doTaskSyncCmds drives a tasks Sync carrying client up-sync Commands.
func doTaskSyncCmds(t *testing.T, s *Server, syncKey string, cmds ...*wbxml.Element) *wbxml.Element {
	t.Helper()
	coll := []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "SyncKey", Text: syncKey},
		{Page: wbxml.PageAirSync, Name: "CollectionId", Text: TaskCollectionID("TSK1")},
		{Page: wbxml.PageAirSync, Name: "Commands", Children: cmds},
	}
	return doTaskRequest(t, s, coll)
}

// taskAddCmd builds a client up-sync Add command (ClientId + page-9 ApplicationData).
func taskAddCmd(clientID, subject, due string) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageAirSync, Name: "Add", Children: []*wbxml.Element{
		{Page: wbxml.PageAirSync, Name: "ClientId", Text: clientID},
		{Page: wbxml.PageAirSync, Name: "ApplicationData", Children: []*wbxml.Element{
			{Page: wbxml.PageTasks, Name: "Subject", Text: subject},
			{Page: wbxml.PageTasks, Name: "UtcDueDate", Text: due},
		}},
	}}
}

// TestTaskSyncFlow exercises the tasks Sync end to end through the dispatcher:
// SyncKey 0 primes; the first real sync streams the to-do as an Add keyed by its
// VTODO UID; an unchanged re-sync is empty; advancing the ETag produces a Change;
// removing the to-do produces a Delete. This proves the "tsk:" CollectionId
// routes to the tasks path, not the mail path.
func TestTaskSyncFlow(t *testing.T) {
	tasks := &stubTasks{items: []TaskItem{
		TaskItemFromVTODO("t-1", "e1", sampleVTODO),
	}}
	s := taskServer(tasks)

	prime := doTaskSync(t, s, "0", 0)
	if prime.Sub("SyncKey").Text != "1" || prime.Sub("Status").Text != syncStatusSuccess {
		t.Fatalf("prime wrong: %v", prime)
	}
	if prime.Sub("Commands") != nil {
		t.Fatalf("prime must carry no commands")
	}

	first := doTaskSync(t, s, "1", 0)
	if first.Sub("SyncKey").Text != "2" {
		t.Fatalf("first sync key = %q, want 2", first.Sub("SyncKey").Text)
	}
	if countOps(first, "Add") != 1 {
		t.Fatalf("first sync must Add the to-do: %d", countOps(first, "Add"))
	}
	add := first.Sub("Commands").Sub("Add")
	if add.Sub("ServerId").Text != "t-1" {
		t.Fatalf("Add ServerId = %q, want t-1 (VTODO UID)", add.Sub("ServerId").Text)
	}
	if sb := add.Sub("ApplicationData").Sub("Subject"); sb == nil || sb.Text != "File taxes" {
		t.Fatalf("Add must project Subject: %v", sb)
	}

	// Unchanged set -> no commands.
	second := doTaskSync(t, s, "2", 0)
	if second.Sub("Commands") != nil {
		t.Fatalf("unchanged tasks must produce no commands")
	}

	// Advance the ETag -> a Change.
	tasks.items[0] = TaskItemFromVTODO("t-1", "e2", sampleVTODO)
	third := doTaskSync(t, s, "3", 0)
	if countOps(third, "Change") != 1 {
		t.Fatalf("ETag change must produce a Change: %d", countOps(third, "Change"))
	}

	// Remove the to-do -> a Delete.
	tasks.items = nil
	fourth := doTaskSync(t, s, "4", 0)
	if countOps(fourth, "Delete") != 1 {
		t.Fatalf("removed to-do must produce a Delete: %d", countOps(fourth, "Delete"))
	}
}

// TestTaskUpSync verifies the client up-sync path: an Add creates the to-do
// through the mutator and is echoed as an Add response mapping the ClientId to
// the assigned ServerId, without being re-emitted as a server-side change in the
// same response; a Delete removes it through the mutator.
func TestTaskUpSync(t *testing.T) {
	tasks := &stubTasks{}
	mut := &stubTaskMutator{src: tasks}
	s := taskServer(tasks)
	s.SetTaskMutator(mut)
	doTaskSync(t, s, "0", 0) // prime -> key 1

	coll := doTaskSyncCmds(t, s, "1", taskAddCmd("c1", "Buy milk", "20260701T120000Z"))

	add := coll.Sub("Responses").Sub("Add")
	if add == nil || add.Sub("ClientId").Text != "c1" {
		t.Fatalf("Add response must echo ClientId: %v", add)
	}
	if add.Sub("Status").Text != syncStatusSuccess {
		t.Fatalf("Add response status = %v, want 1", add.Sub("Status"))
	}
	serverID := add.Sub("ServerId").Text
	if serverID == "" || mut.created[serverID].Subject != "Buy milk" {
		t.Fatalf("CreateItem not applied with parsed fields: %+v", mut.created)
	}
	// Reconciliation: the client's own Add must not echo back as a server Command.
	if countOps(coll, "Add") != 0 {
		t.Fatalf("client's own Add must not be re-emitted as a server Add: %d", countOps(coll, "Add"))
	}

	// Delete the to-do on the client; the mutator removes it from the canonical store.
	cur := coll.Sub("SyncKey").Text
	doTaskSyncCmds(t, s, cur, deleteCmd(serverID))
	if len(mut.deleted) != 1 || mut.deleted[0] != serverID {
		t.Fatalf("DeleteItem not applied: %v", mut.deleted)
	}
}
