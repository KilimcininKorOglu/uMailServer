package oab

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeDir is a fixed GAL source for handler tests.
type fakeDir struct {
	entries []Entry
	seq     uint32
}

func (f fakeDir) GAL() []Entry     { return f.entries }
func (f fakeDir) Sequence() uint32 { return f.seq }

func doGET(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestHandlerManifestMatchesFiles verifies the manifest advertises the same
// SHA-1 digests as the files the handler serves. This is the consistency the
// per-sequence cache exists to guarantee: Outlook rejects a Full or template
// file whose hash disagrees with the manifest.
func TestHandlerManifestMatchesFiles(t *testing.T) {
	h := NewHandler(fakeDir{
		entries: []Entry{
			{Email: "alice@x.test", DisplayName: "Alice", ObjectClass: "User"},
			{Email: "list@x.test", DisplayName: "Team", ObjectClass: "DistributionList"},
		},
		seq: 4242,
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

// TestHandlerStaleSequence404 verifies a request for a file whose sequence no
// longer matches the current GAL returns 404, prompting Outlook to re-fetch the
// manifest.
func TestHandlerStaleSequence404(t *testing.T) {
	h := NewHandler(fakeDir{
		entries: []Entry{{Email: "a@x.test", DisplayName: "A", ObjectClass: "User"}},
		seq:     100,
	})
	if rec := doGET(t, h, "/mapi/oab/999999.lzx"); rec.Code != http.StatusNotFound {
		t.Errorf("stale .lzx status = %d, want 404", rec.Code)
	}
}

// TestHandlerMethodNotAllowed verifies non-GET/HEAD requests are rejected.
func TestHandlerMethodNotAllowed(t *testing.T) {
	h := NewHandler(fakeDir{})
	req := httptest.NewRequest(http.MethodPost, "/mapi/oab/oab.xml", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}
