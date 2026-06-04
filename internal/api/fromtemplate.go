package api

import (
	"regexp"
	"strings"
)

// From display-name template cleanup patterns. After placeholder substitution,
// an empty field can leave dangling punctuation (an empty "()" or a stray " - ");
// these collapse those so `{name} ({company} - {title})` reads cleanly when
// company or title is blank.
var (
	reTmplOpenDash    = regexp.MustCompile(`\(\s*-\s*`) // "( - " -> "("
	reTmplDashClose   = regexp.MustCompile(`\s*-\s*\)`) // " - )" -> ")"
	reTmplEmptyParens = regexp.MustCompile(`\(\s*\)`)   // "()"   -> ""
	reTmplMultiSpace  = regexp.MustCompile(`\s{2,}`)    // "  "   -> " "
)

// expandFromTemplate substitutes the {name} {title} {department} {company}
// {email} placeholders in tmpl and removes punctuation left dangling by empty
// fields, so a template like `{name} ({company} - {title})` degrades gracefully
// when some fields are blank. Returns the cleaned, trimmed display name.
func expandFromTemplate(tmpl string, fields map[string]string) string {
	out := tmpl
	for _, key := range []string{"name", "title", "department", "company", "email"} {
		out = strings.ReplaceAll(out, "{"+key+"}", fields[key])
	}
	return cleanupFromTemplate(out)
}

// cleanupFromTemplate strips parentheses/separators left empty by blank fields
// and collapses whitespace. It iterates until stable so nested empties
// (e.g. "( - )") fully collapse.
func cleanupFromTemplate(s string) string {
	for {
		prev := s
		s = reTmplOpenDash.ReplaceAllString(s, "(")
		s = reTmplDashClose.ReplaceAllString(s, ")")
		s = reTmplEmptyParens.ReplaceAllString(s, "")
		if s == prev {
			break
		}
	}
	s = reTmplMultiSpace.ReplaceAllString(s, " ")
	// Trim leftover separators/space at the ends (e.g. a trailing " -").
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "-")
	return strings.TrimSpace(s)
}
