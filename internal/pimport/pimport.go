// Package pimport parses and serializes the PIM interchange formats the
// canonical-store CLI imports and exports: iCalendar (.ics, RFC 5545) calendar
// events and vCard (.vcf, RFC 6350) contacts. It is the PIM sibling of
// internal/mailimport: a small, self-contained codec with no dependency on the
// protocol packages, so the CLI can split a multi-object export file into the
// per-UID records the collaboration store keys on, and merge stored records back
// into a single interchange file.
//
// Scope: calendar VEVENTs and contact VCARDs. VTODO (tasks) is a separate
// collaboration kind with its own store and is intentionally NOT imported here;
// ReadICS reports how many it skipped so the caller can surface it.
package pimport

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// Component is one parsed PIM object: its UID (the collaboration-store key) and
// the self-contained interchange document — a single-VEVENT VCALENDAR for an
// event, or one BEGIN:VCARD…END:VCARD block for a contact.
type Component struct {
	UID string
	Raw string
}

// icsComponent is a top-level iCalendar component (VEVENT, VTIMEZONE, …) captured
// verbatim, BEGIN/END inclusive, with its nested children (STANDARD/DAYLIGHT/
// VALARM) preserved.
type icsComponent struct {
	Type  string
	Lines []string
}

// unfold splits raw bytes into logical lines, undoing RFC 5545/6350 line folding
// (a physical line starting with a space or tab continues the previous one) and
// normalizing CRLF/LF/CR endings.
func unfold(data []byte) []string {
	raw := strings.ReplaceAll(string(data), "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var out []string
	for _, ln := range strings.Split(raw, "\n") {
		if (strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t")) && len(out) > 0 {
			out[len(out)-1] += ln[1:]
			continue
		}
		out = append(out, ln)
	}
	// Drop a trailing empty line produced by a final newline.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}

// propValue returns the value of the first property whose name matches one of
// names (case-insensitive), e.g. propValue(lines, "UID") for "UID:abc" or
// "UID;X-PARAM=y:abc". Empty when absent.
func propValue(lines []string, names ...string) string {
	for _, ln := range lines {
		colon := strings.IndexByte(ln, ':')
		if colon < 0 {
			continue
		}
		name := ln[:colon]
		if semi := strings.IndexByte(name, ';'); semi >= 0 {
			name = name[:semi]
		}
		for _, want := range names {
			if strings.EqualFold(strings.TrimSpace(name), want) {
				return strings.TrimSpace(ln[colon+1:])
			}
		}
	}
	return ""
}

// synthUID derives a stable UID from a component's content when it carries none,
// so a UID-less object still imports and re-imports idempotently.
func synthUID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("import-%x", sum[:16])
}

// walkICS splits an unfolded iCalendar stream into the VCALENDAR-level property
// lines and its top-level components. Depth tracking keeps nested STANDARD/
// DAYLIGHT/VALARM blocks inside their parent component.
func walkICS(lines []string) (header []string, comps []icsComponent) {
	inCal := false
	depth := 0
	var cur *icsComponent
	for _, ln := range lines {
		up := strings.ToUpper(strings.TrimSpace(ln))
		if !inCal {
			if up == "BEGIN:VCALENDAR" {
				inCal = true
			}
			continue
		}
		if cur == nil {
			switch {
			case up == "END:VCALENDAR":
				return header, comps
			case strings.HasPrefix(up, "BEGIN:"):
				cur = &icsComponent{Type: strings.TrimPrefix(up, "BEGIN:"), Lines: []string{ln}}
				depth = 1
			default:
				header = append(header, ln)
			}
			continue
		}
		cur.Lines = append(cur.Lines, ln)
		if strings.HasPrefix(up, "BEGIN:") {
			depth++
		} else if strings.HasPrefix(up, "END:") {
			depth--
			if depth == 0 {
				comps = append(comps, *cur)
				cur = nil
			}
		}
	}
	return header, comps
}

// ensureCalHeader returns calendar-level property lines guaranteeing a VERSION
// and PRODID are present (some minimal inputs omit them).
func ensureCalHeader(header []string) []string {
	out := append([]string{}, header...)
	if propValue(out, "VERSION") == "" {
		out = append([]string{"VERSION:2.0"}, out...)
	}
	if propValue(out, "PRODID") == "" {
		out = append(out, "PRODID:-//uMailServer//PIM import//EN")
	}
	return out
}

// buildVCalendar wraps timezone + event/component blocks in one VCALENDAR,
// CRLF-terminated per RFC 5545.
func buildVCalendar(header []string, tzs, comps []icsComponent) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	for _, ln := range ensureCalHeader(header) {
		b.WriteString(ln)
		b.WriteString("\r\n")
	}
	for _, c := range append(append([]icsComponent{}, tzs...), comps...) {
		for _, ln := range c.Lines {
			b.WriteString(ln)
			b.WriteString("\r\n")
		}
	}
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// withUID ensures a VEVENT/VCARD component carries a UID line, injecting the
// synthesized one right after its BEGIN line when missing, and returns the UID.
func withUID(c *icsComponent) string {
	uid := propValue(c.Lines, "UID")
	if uid != "" {
		return uid
	}
	uid = synthUID(strings.Join(c.Lines, "\n"))
	if len(c.Lines) > 0 {
		c.Lines = append(c.Lines[:1], append([]string{"UID:" + uid}, c.Lines[1:]...)...)
	}
	return uid
}

// ReadICS splits an iCalendar stream into one self-contained single-VEVENT
// VCALENDAR per event (each carrying the source VTIMEZONEs so DTSTART;TZID stays
// resolvable). skippedTodos counts VTODO components, which are not imported.
func ReadICS(data []byte) (comps []Component, skippedTodos int, err error) {
	header, parsed := walkICS(unfold(data))
	var tzs []icsComponent
	var events []icsComponent
	for _, c := range parsed {
		switch c.Type {
		case "VTIMEZONE":
			tzs = append(tzs, c)
		case "VEVENT":
			events = append(events, c)
		case "VTODO":
			skippedTodos++
		}
	}
	for i := range events {
		uid := withUID(&events[i])
		comps = append(comps, Component{
			UID: uid,
			Raw: buildVCalendar(header, tzs, []icsComponent{events[i]}),
		})
	}
	return comps, skippedTodos, nil
}

// ReadVCF splits a vCard stream into individual VCARD components, injecting a
// synthesized UID when one is absent (the store keys on UID).
func ReadVCF(data []byte) ([]Component, error) {
	lines := unfold(data)
	var comps []Component
	var cur []string
	in := false
	for _, ln := range lines {
		up := strings.ToUpper(strings.TrimSpace(ln))
		switch {
		case up == "BEGIN:VCARD":
			in = true
			cur = []string{ln}
		case up == "END:VCARD" && in:
			cur = append(cur, ln)
			uid := propValue(cur, "UID")
			if uid == "" {
				uid = synthUID(strings.Join(cur, "\n"))
				cur = append(cur[:1], append([]string{"UID:" + uid}, cur[1:]...)...)
			}
			var b strings.Builder
			for _, l := range cur {
				b.WriteString(l)
				b.WriteString("\r\n")
			}
			comps = append(comps, Component{UID: uid, Raw: b.String()})
			in = false
			cur = nil
		case in:
			cur = append(cur, ln)
		}
	}
	return comps, nil
}

// MergeICS merges stored single-VEVENT VCALENDAR documents into one VCALENDAR
// (header from the first, VTIMEZONEs deduplicated by TZID, every VEVENT), the
// canonical interchange shape for export.
func MergeICS(docs []string) []byte {
	var header []string
	seenTZ := map[string]bool{}
	var tzs, events []icsComponent
	for _, doc := range docs {
		h, comps := walkICS(unfold([]byte(doc)))
		if header == nil {
			header = h
		}
		for _, c := range comps {
			switch c.Type {
			case "VTIMEZONE":
				tzid := propValue(c.Lines, "TZID")
				if tzid == "" || !seenTZ[tzid] {
					seenTZ[tzid] = true
					tzs = append(tzs, c)
				}
			case "VEVENT":
				events = append(events, c)
			}
		}
	}
	return []byte(buildVCalendar(header, tzs, events))
}

// MergeVCF concatenates stored VCARD documents into one vCard file (a .vcf may
// hold any number of VCARD objects).
func MergeVCF(docs []string) []byte {
	var b strings.Builder
	for _, doc := range docs {
		s := strings.ReplaceAll(doc, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
		for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
			b.WriteString(ln)
			b.WriteString("\r\n")
		}
	}
	return []byte(b.String())
}
