package activesync

import "strings"

// mergeRFCSection merges a freshly built RFC 5545/6350 object (`rebuilt`, a
// Build* of the edited item carrying only the EAS-modeled properties with the
// client's new values) with the canonical store's `existing` raw object, so a
// mobile edit cannot erase properties the projection does not model. It keeps
// the top-level properties of `existing`'s <tag> section whose names are NOT in
// `owned` (RRULE, EXDATE, ATTENDEE, ORGANIZER, PHOTO, CATEGORIES, X-*, …) and
// splices them into `rebuilt`. Nested sub-components (VALARM and the like) are
// preserved verbatim regardless of their inner property names, so an alarm's
// own DESCRIPTION/SUMMARY is not stripped by the top-level owned filter.
func mergeRFCSection(existing, rebuilt, tag string, owned map[string]bool) string {
	preserved := unmodeledLines(existing, tag, owned)
	if len(preserved) == 0 {
		return rebuilt
	}
	idx := strings.Index(rebuilt, "END:"+tag)
	if idx < 0 {
		return rebuilt
	}
	var b strings.Builder
	b.WriteString(rebuilt[:idx])
	for _, ln := range preserved {
		b.WriteString(ln)
		b.WriteString("\r\n")
	}
	b.WriteString(rebuilt[idx:])
	return b.String()
}

// unmodeledLines returns the lines of `existing`'s <tag> section whose
// top-level property is not in `owned`. Every line inside a nested
// sub-component (between BEGIN:X and its matching END:X) is returned verbatim,
// since the owned filter only describes the section's own properties.
func unmodeledLines(existing, tag string, owned map[string]bool) []string {
	body := sectionBody(existing, tag)
	if body == "" {
		return nil
	}
	var preserved []string
	depth := 0
	for _, line := range unfoldICal(body) {
		name, _, _ := parseICalLine(line)
		if depth == 0 && name != "BEGIN" {
			// A top-level property: keep it only if the projection cannot model it.
			if !owned[name] {
				preserved = append(preserved, line)
			}
			continue
		}
		// Entering or inside a nested sub-component: keep every line verbatim.
		preserved = append(preserved, line)
		switch name {
		case "BEGIN":
			depth++
		case "END":
			depth--
		}
	}
	return preserved
}
