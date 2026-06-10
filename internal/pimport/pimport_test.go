package pimport

import (
	"strings"
	"testing"
)

const sampleICS = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Test//EN\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:Europe/Istanbul\r\n" +
	"BEGIN:STANDARD\r\n" +
	"DTSTART:19701025T040000\r\n" +
	"TZOFFSETFROM:+0300\r\n" +
	"TZOFFSETTO:+0300\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:event-one@test\r\n" +
	"SUMMARY:First\r\n" +
	"DTSTART;TZID=Europe/Istanbul:20240101T100000\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:event-two@test\r\n" +
	"SUMMARY:Second\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VTODO\r\n" +
	"UID:task-one@test\r\n" +
	"SUMMARY:A task\r\n" +
	"END:VTODO\r\n" +
	"END:VCALENDAR\r\n"

func TestReadICSSplitsEventsAndTodosKeepsTimezone(t *testing.T) {
	events, todos, err := ReadICS([]byte(sampleICS))
	if err != nil {
		t.Fatalf("ReadICS: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if len(todos) != 1 {
		t.Fatalf("got %d todos, want 1", len(todos))
	}
	uids := map[string]bool{events[0].UID: true, events[1].UID: true}
	if !uids["event-one@test"] || !uids["event-two@test"] {
		t.Errorf("event UIDs = %v, want event-one@test + event-two@test", uids)
	}
	if todos[0].UID != "task-one@test" {
		t.Errorf("todo UID = %q, want task-one@test", todos[0].UID)
	}
	// Each event doc must be a self-contained VCALENDAR carrying the source
	// VTIMEZONE and exactly one VEVENT, with no VTODO leakage.
	for _, c := range events {
		if !strings.HasPrefix(c.Raw, "BEGIN:VCALENDAR") || !strings.Contains(c.Raw, "END:VCALENDAR") {
			t.Errorf("doc not a VCALENDAR: %q", c.Raw)
		}
		if !strings.Contains(c.Raw, "TZID:Europe/Istanbul") {
			t.Errorf("doc dropped the VTIMEZONE: %q", c.Raw)
		}
		if strings.Count(c.Raw, "BEGIN:VEVENT") != 1 || strings.Contains(c.Raw, "BEGIN:VTODO") {
			t.Errorf("doc should hold exactly one VEVENT and no VTODO: %q", c.Raw)
		}
	}
	// The todo doc holds the VTODO (and the timezone), no VEVENT.
	td := todos[0].Raw
	if strings.Count(td, "BEGIN:VTODO") != 1 || strings.Contains(td, "BEGIN:VEVENT") {
		t.Errorf("todo doc should hold exactly one VTODO and no VEVENT: %q", td)
	}
	if !strings.Contains(td, "TZID:Europe/Istanbul") {
		t.Errorf("todo doc dropped the VTIMEZONE: %q", td)
	}
}

func TestReadICSSynthesizesMissingUID(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nSUMMARY:No UID\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	events, _, err := ReadICS([]byte(ics))
	if err != nil {
		t.Fatalf("ReadICS: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d, want 1", len(events))
	}
	if events[0].UID == "" {
		t.Fatal("expected a synthesized UID")
	}
	if !strings.Contains(events[0].Raw, "UID:"+events[0].UID) {
		t.Errorf("synthesized UID not injected into the doc: %q", events[0].Raw)
	}
}

func TestReadICSUnfoldsContinuationLines(t *testing.T) {
	// A folded SUMMARY (continuation line begins with a space) must rejoin, and
	// the UID on its own line must still parse.
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:folded@test\r\nSUMMARY:Hello \r\n World\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	events, _, err := ReadICS([]byte(ics))
	if err != nil {
		t.Fatalf("ReadICS: %v", err)
	}
	if len(events) != 1 || events[0].UID != "folded@test" {
		t.Fatalf("got %+v, want one event uid folded@test", events)
	}
	if !strings.Contains(events[0].Raw, "SUMMARY:Hello World") {
		t.Errorf("folded line not rejoined: %q", events[0].Raw)
	}
}

func TestMergeICSRoundTrip(t *testing.T) {
	events, todos, err := ReadICS([]byte(sampleICS))
	if err != nil {
		t.Fatalf("ReadICS: %v", err)
	}
	// Export merges events AND tasks (both iCal components) into one VCALENDAR.
	docs := []string{events[0].Raw, events[1].Raw, todos[0].Raw}
	merged := string(MergeICS(docs))
	if strings.Count(merged, "BEGIN:VCALENDAR") != 1 || strings.Count(merged, "END:VCALENDAR") != 1 {
		t.Errorf("merged output must be exactly one VCALENDAR: %q", merged)
	}
	if strings.Count(merged, "BEGIN:VEVENT") != 2 {
		t.Errorf("merged output must hold both VEVENTs: %q", merged)
	}
	if strings.Count(merged, "BEGIN:VTODO") != 1 {
		t.Errorf("merged output must hold the VTODO: %q", merged)
	}
	// The shared VTIMEZONE must appear once, not duplicated per component.
	if got := strings.Count(merged, "BEGIN:VTIMEZONE"); got != 1 {
		t.Errorf("VTIMEZONE appears %d times, want 1 (dedup by TZID)", got)
	}
}

const sampleVCF = "BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"UID:contact-one@test\r\n" +
	"FN:Alice Example\r\n" +
	"EMAIL:alice@example.com\r\n" +
	"END:VCARD\r\n" +
	"BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"FN:Bob Example\r\n" +
	"END:VCARD\r\n"

func TestReadVCFSplitsAndSynthesizesUID(t *testing.T) {
	comps, err := ReadVCF([]byte(sampleVCF))
	if err != nil {
		t.Fatalf("ReadVCF: %v", err)
	}
	if len(comps) != 2 {
		t.Fatalf("got %d cards, want 2", len(comps))
	}
	if comps[0].UID != "contact-one@test" {
		t.Errorf("first UID = %q, want contact-one@test", comps[0].UID)
	}
	if comps[1].UID == "" {
		t.Error("second card should get a synthesized UID")
	}
	if !strings.Contains(comps[1].Raw, "UID:"+comps[1].UID) {
		t.Errorf("synthesized UID not injected: %q", comps[1].Raw)
	}
	for _, c := range comps {
		if !strings.HasPrefix(c.Raw, "BEGIN:VCARD") || !strings.Contains(c.Raw, "END:VCARD") {
			t.Errorf("not a VCARD block: %q", c.Raw)
		}
	}
}

func TestMergeVCFConcatenates(t *testing.T) {
	comps, err := ReadVCF([]byte(sampleVCF))
	if err != nil {
		t.Fatalf("ReadVCF: %v", err)
	}
	merged := string(MergeVCF([]string{comps[0].Raw, comps[1].Raw}))
	if strings.Count(merged, "BEGIN:VCARD") != 2 || strings.Count(merged, "END:VCARD") != 2 {
		t.Errorf("merged vCard must hold both cards: %q", merged)
	}
}
