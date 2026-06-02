package ews

import (
	"strings"
	"testing"
	"time"
)

// fixedTime is a deterministic timestamp for building iCalendar test payloads.
func fixedTime() time.Time { return time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC) }

// TestCalendarCategoriesRoundTrip verifies categories supplied on an EWS
// CreateCalendarItem are written into the canonical iCalendar (CATEGORIES) and
// parse back out. Without this, categories set over EWS are silently dropped and
// FindItem category filters never match the item.
func TestCalendarCategoriesRoundTrip(t *testing.T) {
	item := &CalendarItemTypeNew{
		Subject:    "Standup",
		Categories: &MessageCategoriesType{Strings: []string{"qa-token", "work"}},
	}
	ics := buildICalFromCalendarItem("uid-1", item, fixedTime(), fixedTime().Add(time.Hour))
	if !strings.Contains(ics, "CATEGORIES:qa-token,work") {
		t.Fatalf("CATEGORIES not written to iCal: %q", ics)
	}
	cats := parseICalCategories(ics)
	if len(cats) != 2 || cats[0] != "qa-token" || cats[1] != "work" {
		t.Errorf("parseICalCategories round-trip mismatch: %v", cats)
	}
}

// TestContactCategoriesRoundTrip mirrors the calendar round-trip for vCard.
func TestContactCategoriesRoundTrip(t *testing.T) {
	c := &ContactTypeNew{
		FullName:   "Grace Hopper",
		Categories: &MessageCategoriesType{Strings: []string{"qa-token"}},
	}
	vcf := buildVCardFromContact("uid-2", c)
	if !strings.Contains(vcf, "CATEGORIES:qa-token") {
		t.Fatalf("CATEGORIES not written to vCard: %q", vcf)
	}
	if cats := parseICalCategories(vcf); len(cats) != 1 || cats[0] != "qa-token" {
		t.Errorf("parseICalCategories round-trip mismatch: %v", cats)
	}
}

// TestParseICalCategoriesEmpty verifies absent CATEGORIES yields no categories.
func TestParseICalCategoriesEmpty(t *testing.T) {
	if cats := parseICalCategories("BEGIN:VEVENT\r\nUID:x\r\nEND:VEVENT\r\n"); cats != nil {
		t.Errorf("expected nil categories, got %v", cats)
	}
}

// TestCollabRestrictionMatchCategories verifies a FindItem restriction over
// item:Categories matches only items that actually carry the category. This is
// the behavior contact-crud/calendar-crud rely on: filter(categories=token)
// must return exactly the items tagged with that token, not every item in the
// folder (the previous lenient behavior over-selected and broke the count).
func TestCollabRestrictionMatchCategories(t *testing.T) {
	tagged := &MessageTypeResponse{Subject: "Standup", Categories: &MessageCategoriesType{Strings: []string{"qa-token"}}}
	untagged := &MessageTypeResponse{Subject: "Other", Categories: &MessageCategoriesType{Strings: []string{"misc"}}}
	noCats := &MessageTypeResponse{Subject: "Plain"}

	containsToken := SearchFilter{Contains: &ContainsFilter{
		FieldURI: &FieldURI{URI: "item:Categories"},
		Constant: ContainsConstType{Value: "qa-token"},
	}}
	if !collabRestrictionMatch(containsToken, tagged) {
		t.Error("tagged item should match its category")
	}
	if collabRestrictionMatch(containsToken, untagged) {
		t.Error("untagged item must not match a different category")
	}
	if collabRestrictionMatch(containsToken, noCats) {
		t.Error("item without categories must not match a category filter")
	}

	equalToken := SearchFilter{IsEqualTo: &ComparisonFilter{
		FieldURI: &FieldURI{URI: "item:Categories"},
		FieldURIOrConstant: &FieldURIOrConstant{Constant: &struct {
			Value string `xml:"Value,attr"`
		}{Value: "qa-token"}},
	}}
	if !collabRestrictionMatch(equalToken, tagged) {
		t.Error("IsEqualTo categories should match tagged item")
	}
	if collabRestrictionMatch(equalToken, untagged) {
		t.Error("IsEqualTo categories should not match untagged item")
	}

	// Subject restrictions remain functional alongside categories support.
	subjFilter := SearchFilter{Contains: &ContainsFilter{
		FieldURI: &FieldURI{URI: "item:Subject"},
		Constant: ContainsConstType{Value: "stand"},
	}}
	if !collabRestrictionMatch(subjFilter, tagged) {
		t.Error("subject contains should still match")
	}
}
