package oab

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeDir is a mutable GAL source for handler tests.
type fakeDir struct {
	entries []Entry
}

func (f *fakeDir) GAL() []Entry { return f.entries }

func doGET(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestHandlerManifestMatchesFiles verifies the manifest advertises the same
// SHA-1 digests as the files the handler serves. This is the consistency the
// content-keyed cache exists to guarantee: Outlook rejects a Full or template
// file whose hash disagrees with the manifest.
func TestHandlerManifestMatchesFiles(t *testing.T) {
	h := NewHandler(&fakeDir{
		entries: []Entry{
			{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
			{Email: "list@x.test", DisplayName: "Team", ObjectClass: "DistributionList"},
		},
	})

	rec := doGET(t, h, "/mapi/oab/oab.xml")
	if rec.Code != http.StatusOK {
		t.Fatalf("oab.xml status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/xml; charset=utf-8" {
		t.Errorf("oab.xml content-type = %q", ct)
	}
	var man xmlOAB
	if err := xml.Unmarshal(rec.Body.Bytes(), &man); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	full := doGET(t, h, "/mapi/oab/"+man.OAL.Full.File)
	if full.Code != http.StatusOK {
		t.Fatalf("full file %q status = %d", man.OAL.Full.File, full.Code)
	}
	if full.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("full content-type = %q", full.Header().Get("Content-Type"))
	}
	if got := sha1Hex(full.Body.Bytes()); got != man.OAL.Full.SHA {
		t.Errorf("full SHA = %s, manifest advertises %s", got, man.OAL.Full.SHA)
	}

	tmpl := doGET(t, h, "/mapi/oab/"+man.OAL.Template.File)
	if tmpl.Code != http.StatusOK {
		t.Fatalf("template %q status = %d", man.OAL.Template.File, tmpl.Code)
	}
	if got := sha1Hex(tmpl.Body.Bytes()); got != man.OAL.Template.SHA {
		t.Errorf("template SHA = %s, manifest advertises %s", got, man.OAL.Template.SHA)
	}
}

// TestHandlerRebuildsOnContentChange verifies the content-keyed cache: an
// unchanged GAL returns the cached bundle, while removing a recipient (the
// effect of hiding one from the address book) rebuilds it, advances the
// sequence, and shrinks the Full file. Without this, a hidden recipient
// (MS-OXNSPI HiddenFromGAL) would leak into the Offline Address Book because the
// stale bundle would keep being served. The same mechanism covers any removal,
// so it equally guards against a deleted recipient lingering in the OAB.
func TestHandlerRebuildsOnContentChange(t *testing.T) {
	dir := &fakeDir{
		entries: []Entry{
			{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
			{Email: "bob@x.test", DisplayName: "Bob", ObjectClass: "User"},
		},
	}
	h := NewHandler(dir)

	before, err := h.current()
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	// An unchanged GAL must return the identical, cached bundle.
	again, err := h.current()
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if again != before {
		t.Error("unchanged GAL rebuilt the OAB instead of serving the cache")
	}

	// Removing a recipient must rebuild and advance the sequence.
	dir.entries = dir.entries[:1]
	after, err := h.current()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if after.sequence <= before.sequence {
		t.Errorf("sequence did not advance after the GAL changed: before=%d after=%d",
			before.sequence, after.sequence)
	}
	if after.manifest == before.manifest {
		t.Error("manifest did not change after the GAL changed")
	}
	if len(after.full) >= len(before.full) {
		t.Errorf("Full file did not shrink after removing a recipient: before=%d after=%d bytes",
			len(before.full), len(after.full))
	}
}

// TestHandlerStaleSequence404 verifies a request for a file whose sequence no
// longer matches the current GAL returns 404, prompting Outlook to re-fetch the
// manifest.
func TestHandlerStaleSequence404(t *testing.T) {
	h := NewHandler(&fakeDir{
		entries: []Entry{{Email: "a@x.test", DisplayName: "A", ObjectClass: "User"}},
	})
	if rec := doGET(t, h, "/mapi/oab/999999.lzx"); rec.Code != http.StatusNotFound {
		t.Errorf("stale .lzx status = %d, want 404", rec.Code)
	}
}

// TestHandlerMethodNotAllowed verifies non-GET/HEAD requests are rejected.
func TestHandlerMethodNotAllowed(t *testing.T) {
	h := NewHandler(&fakeDir{})
	req := httptest.NewRequest(http.MethodPost, "/mapi/oab/oab.xml", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}
