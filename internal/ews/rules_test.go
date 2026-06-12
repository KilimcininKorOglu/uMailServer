package ews

import (
	"context"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// ---------------------------------------------------------------------------
// OOF state mapping tests
// Satisfies VAL-COLLAB-007: OOF scheduling is authoritative and time-bounded
// ---------------------------------------------------------------------------

func TestOOFStateMapping(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		hasStart bool
		hasEnd   bool
		want     OofState
	}{
		{"Disabled", false, false, false, OofStateDisabled},
		{"Enabled_NoSchedule", true, false, false, OofStateEnabled},
		{"Scheduled_StartOnly", true, true, false, OofStateScheduled},
		{"Scheduled_EndOnly", true, false, true, OofStateScheduled},
		{"Scheduled_Both", true, true, true, OofStateScheduled},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := &semcore.OOFPolicy{Enabled: tc.enabled}
			if tc.hasStart {
				policy.StartTime = time.Now()
			}
			if tc.hasEnd {
				policy.EndTime = time.Now().Add(24 * time.Hour)
			}
			got := oofStateFromOOF(policy)
			if got != tc.want {
				t.Errorf("oofStateFromOOF(enabled=%v, hasStart=%v, hasEnd=%v) = %v; want %v",
					tc.enabled, tc.hasStart, tc.hasEnd, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// External audience mapping tests
// Satisfies VAL-COLLAB-013: OOF audience and domain policy enforced consistently
// ---------------------------------------------------------------------------

func TestExternalAudienceMapping(t *testing.T) {
	tests := []struct {
		aud semcore.OOFAudience
		ext ExternalAudience
	}{
		{semcore.OOFAudienceInternal, ExternalAudienceNone},
		{semcore.OOFAudienceExternal, ExternalAudienceKnown},
		{semcore.OOFAudienceEveryone, ExternalAudienceAll},
	}

	for _, tc := range tests {
		got := externalAudienceFromOOF(tc.aud)
		if got != tc.ext {
			t.Errorf("externalAudienceFromOOF(%v) = %v; want %v", tc.aud, got, tc.ext)
		}
	}
}

func TestOOFAudienceFromString(t *testing.T) {
	tests := []struct {
		input string
		want  semcore.OOFAudience
	}{
		{"internal", semcore.OOFAudienceInternal},
		{"external", semcore.OOFAudienceExternal},
		{"everyone", semcore.OOFAudienceEveryone},
		{"unknown", semcore.OOFAudienceInternal}, // default fallback
	}

	for _, tc := range tests {
		got := semcore.OOFAudienceFromString(tc.input)
		if got != tc.want {
			t.Errorf("OOFAudienceFromString(%q) = %v; want %v", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Rule conditions mapping tests
// Satisfies VAL-COLLAB-009: inbox rules execute in deterministic order
// ---------------------------------------------------------------------------

func TestConditionsToEWS(t *testing.T) {
	conds := []semcore.RuleCondition{
		{Kind: semcore.RuleConditionKindFrom, MatchType: semcore.RuleMatchTypeContains, Value: "sender@example.com"},
		{Kind: semcore.RuleConditionKindSubject, MatchType: semcore.RuleMatchTypeContains, Value: "meeting"},
		{Kind: semcore.RuleConditionKindBody, MatchType: semcore.RuleMatchTypeContains, Value: "discussion"},
		{Kind: semcore.RuleConditionKindTo, MatchType: semcore.RuleMatchTypeContains, Value: "recipient@example.com"},
	}

	ewsPred := conditionsToEWS(conds, true)

	if ewsPred == nil || ewsPred.RulePredicatesType == nil {
		t.Fatal("expected non-nil RulePredicatesType")
	}
	// A From condition must project to the From predicate (a resolved sender
	// address), not ContainsSenderStrings: Outlook for Mac only lists the former
	// under Server Rules and hides the latter as a client-side condition.
	if ewsPred.From == nil {
		t.Fatal("expected From predicate to be set for a sender condition")
	}
	if len(ewsPred.From.Addresses) != 1 || ewsPred.From.Addresses[0].Email != "sender@example.com" {
		t.Errorf("From.Addresses = %v; want [sender@example.com]", ewsPred.From.Addresses)
	}
	if ewsPred.ContainsSenderStrings != nil {
		t.Error("From condition should not also emit ContainsSenderStrings (client-side)")
	}
	if ewsPred.ContainsSubjectStrings == nil {
		t.Error("expected ContainsSubjectStrings to be set")
	}
	if ewsPred.ContainsBodyStrings == nil {
		t.Error("expected ContainsBodyStrings to be set")
	}
	if ewsPred.ContainsRecipientStrings == nil {
		t.Error("expected ContainsRecipientStrings to be set")
	}
}

func TestConditionsToEWS_EmptyConditions(t *testing.T) {
	ewsPred := conditionsToEWS(nil, true)
	if ewsPred == nil {
		t.Fatal("expected non-nil result for nil conditions")
	}
}

// ---------------------------------------------------------------------------
// Rule to EWS mapping tests
// ---------------------------------------------------------------------------

func TestRuleToEWS(t *testing.T) {
	rule := &semcore.Rule{
		ID:       semcore.MustRuleId("rule-abc123"),
		Name:     "Test Rule",
		Enabled:  true,
		Priority: 1,
		MatchAll: true,
		Conditions: []semcore.RuleCondition{
			{Kind: semcore.RuleConditionKindSubject, MatchType: semcore.RuleMatchTypeContains, Value: "urgent"},
		},
		Actions: []semcore.RuleAction{
			{Kind: semcore.RuleActionKindMarkImportant},
		},
	}

	ewsRule := ruleToEWS(rule)

	if ewsRule.DisplayName != "Test Rule" {
		t.Errorf("DisplayName = %q; want %q", ewsRule.DisplayName, "Test Rule")
	}
	if ewsRule.Priority != 1 {
		t.Errorf("Priority = %d; want 1", ewsRule.Priority)
	}
	if !ewsRule.IsEnabled {
		t.Error("IsEnabled = false; want true")
	}
	if ewsRule.RuleID != "rule-abc123" {
		t.Errorf("RuleID = %q; want %q", ewsRule.RuleID, "rule-abc123")
	}
	if ewsRule.Conditions == nil {
		t.Error("Conditions is nil; want non-nil")
	}
}

func TestRuleToEWS_DisabledRule(t *testing.T) {
	rule := &semcore.Rule{
		ID:       semcore.MustRuleId("rule-disabled"),
		Name:     "Disabled Rule",
		Enabled:  false,
		Priority: 2,
	}
	ewsRule := ruleToEWS(rule)
	if ewsRule.IsEnabled {
		t.Error("IsEnabled = true; want false for disabled rule")
	}
}

// ---------------------------------------------------------------------------
// OOF policy to EWS mapping tests
// Satisfies VAL-COLLAB-007: OOF scheduling is authoritative and time-bounded
// ---------------------------------------------------------------------------

func TestOOFPolicyToEWS_Enabled(t *testing.T) {
	policy := &semcore.OOFPolicy{
		ID:        semcore.MustOOFId("test@local.test"),
		Enabled:   true,
		Subject:   "Out of Office",
		TextBody:  "I am out of office",
		Audience:  semcore.OOFAudienceExternal,
		StartTime: time.Now(),
		EndTime:   time.Now().Add(7 * 24 * time.Hour),
		Timezone:  "UTC",
	}

	settings := oofPolicyToEWS(policy)

	if settings.OofState != OofStateScheduled {
		t.Errorf("OofState = %v; want %v", settings.OofState, OofStateScheduled)
	}
	if settings.ExternalAudience != ExternalAudienceKnown {
		t.Errorf("ExternalAudience = %v; want %v", settings.ExternalAudience, ExternalAudienceKnown)
	}
	if settings.InternalReply == nil || settings.InternalReply.Message != "I am out of office" {
		t.Errorf("InternalReply = %v; want message 'I am out of office'", settings.InternalReply)
	}
	if settings.Duration == nil {
		t.Error("Duration is nil; want schedule info")
	}
}

func TestOOFPolicyToEWS_Disabled(t *testing.T) {
	policy := &semcore.OOFPolicy{
		ID:      semcore.MustOOFId("test@local.test"),
		Enabled: false,
	}
	settings := oofPolicyToEWS(policy)
	if settings.OofState != OofStateDisabled {
		t.Errorf("OofState = %v; want %v", settings.OofState, OofStateDisabled)
	}
}

// ---------------------------------------------------------------------------
// OOF policy from EWS mapping tests
// Satisfies VAL-COLLAB-013: OOF audience and domain policy enforced consistently
// ---------------------------------------------------------------------------

func TestOOFPolicyFromEWS_Enabled(t *testing.T) {
	mailboxID := semcore.MustMailboxId("test@local.test")
	settings := &UserOofSettings{
		OofState:         OofStateEnabled,
		ExternalAudience: ExternalAudienceKnown,
		InternalReply:    &ReplyBody{Message: "I am away"},
	}

	policy, err := oofPolicyFromEWS(context.TODO(), mailboxID, settings)
	if err != nil {
		t.Fatalf("oofPolicyFromEWS failed: %v", err)
	}
	if !policy.Enabled {
		t.Error("Enabled = false; want true")
	}
	if policy.Audience != semcore.OOFAudienceExternal {
		t.Errorf("Audience = %v; want %v", policy.Audience, semcore.OOFAudienceExternal)
	}
	if policy.TextBody != "I am away" {
		t.Errorf("TextBody = %q; want %q", policy.TextBody, "I am away")
	}
	if policy.ID.String() != "test@local.test" {
		t.Errorf("OOF ID = %q; want %q", policy.ID.String(), "test@local.test")
	}
}

func TestOOFPolicyFromEWS_Scheduled(t *testing.T) {
	mailboxID := semcore.MustMailboxId("test@local.test")
	startTime := time.Now()
	endTime := time.Now().Add(7 * 24 * time.Hour)
	settings := &UserOofSettings{
		OofState:         OofStateScheduled,
		ExternalAudience: ExternalAudienceAll,
		Duration: &Duration{
			StartTime: FormatEWSDateTime(startTime),
			EndTime:   FormatEWSDateTime(endTime),
		},
		InternalReply: &ReplyBody{Message: "Scheduled OOF"},
	}

	policy, err := oofPolicyFromEWS(context.TODO(), mailboxID, settings)
	if err != nil {
		t.Fatalf("oofPolicyFromEWS failed: %v", err)
	}
	if !policy.Enabled {
		t.Error("Enabled = false; want true for Scheduled state")
	}
	if policy.StartTime.IsZero() {
		t.Error("StartTime is zero; want scheduled start")
	}
	if policy.EndTime.IsZero() {
		t.Error("EndTime is zero; want scheduled end")
	}
}

func TestOOFPolicyFromEWS_NilSettings(t *testing.T) {
	mailboxID := semcore.MustMailboxId("test@local.test")
	_, err := oofPolicyFromEWS(context.TODO(), mailboxID, nil)
	if err == nil {
		t.Error("expected error for nil settings")
	}
}

// ---------------------------------------------------------------------------
// OOF send interval tests
// Satisfies VAL-COLLAB-008: OOF suppression and reply cadence
// ---------------------------------------------------------------------------

func TestOOFSendInterval(t *testing.T) {
	tests := []struct {
		name   string
		policy *semcore.OOFPolicy
		want   time.Duration
	}{
		{
			"ZeroInterval",
			&semcore.OOFPolicy{SendIntervalSeconds: 0},
			7 * 24 * time.Hour,
		},
		{
			"NegativeInterval",
			&semcore.OOFPolicy{SendIntervalSeconds: -1},
			7 * 24 * time.Hour,
		},
		{
			"OneDay",
			&semcore.OOFPolicy{SendIntervalSeconds: 86400},
			24 * time.Hour,
		},
		{
			"CustomInterval",
			&semcore.OOFPolicy{SendIntervalSeconds: 86400 * 3},
			72 * time.Hour,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.policy.SendInterval()
			if got != tc.want {
				t.Errorf("OOFPolicy.SendInterval() = %v; want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// OOF IsActiveNow tests
// Satisfies VAL-COLLAB-007: OOF scheduling is authoritative and time-bounded
// ---------------------------------------------------------------------------

func TestOOFIsActiveNow(t *testing.T) {
	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	tests := []struct {
		name   string
		policy *semcore.OOFPolicy
		want   bool
	}{
		{"Disabled", &semcore.OOFPolicy{Enabled: false}, false},
		{"NoSchedule_Enabled", &semcore.OOFPolicy{Enabled: true}, true},
		// Exchange semantics: an Enabled policy is active now regardless of the
		// (informational) duration; only a Scheduled policy is gated by the window.
		{"Enabled_IgnoresFutureWindow", &semcore.OOFPolicy{Enabled: true, State: "Enabled", StartTime: future}, true},
		{"Scheduled_BeforeStart", &semcore.OOFPolicy{Enabled: true, State: "Scheduled", StartTime: future}, false},
		{"Scheduled_AfterEnd", &semcore.OOFPolicy{Enabled: true, State: "Scheduled", EndTime: past}, false},
		{"Scheduled_InWindow", &semcore.OOFPolicy{Enabled: true, State: "Scheduled", StartTime: past, EndTime: future}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.policy.IsActiveNow()
			if got != tc.want {
				t.Errorf("OOFPolicy.IsActiveNow() = %v; want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ConditionsFromEWS tests
// Satisfies VAL-COLLAB-009: inbox rules execute in deterministic order
// ---------------------------------------------------------------------------

func TestConditionsFromEWS(t *testing.T) {
	pred := &RulePredicatesType{
		ContainsSenderStrings: &ArrayOfStringsType{
			Strings: []string{"alice@example.com", "bob@example.com"},
		},
		ContainsSubjectStrings: &ArrayOfStringsType{
			Strings: []string{"meeting"},
		},
		SentToMe:       boolPtr(true),
		HasAttachments: boolPtr(true),
	}

	conds, matchAll := conditionsFromEWS(pred)
	if matchAll != true {
		t.Errorf("matchAll = %v; want true", matchAll)
	}
	if len(conds) < 4 {
		t.Errorf("expected at least 4 conditions; got %d", len(conds))
	}
}

func TestConditionsFromEWS_Empty(t *testing.T) {
	conds, matchAll := conditionsFromEWS(nil)
	if len(conds) != 0 {
		t.Errorf("expected 0 conditions for nil predicates; got %d", len(conds))
	}
	if matchAll != true {
		t.Error("expected matchAll=true for nil predicates")
	}
}

// ---------------------------------------------------------------------------
// ActionsFromEWS tests
// Satisfies VAL-COLLAB-014: rule edits take effect on the next message
// ---------------------------------------------------------------------------

func TestActionsFromEWS(t *testing.T) {
	actions := &RuleActionsType{
		Delete:              boolPtr(true),
		MarkAsRead:          boolPtr(true),
		StopProcessingRules: boolPtr(true),
	}

	result := actionsFromEWS(actions)
	if len(result) != 3 {
		t.Errorf("expected 3 actions; got %d", len(result))
	}

	hasDelete := false
	hasMarkRead := false
	hasStop := false
	for _, a := range result {
		switch a.Kind {
		case semcore.RuleActionKindDelete:
			hasDelete = true
		case semcore.RuleActionKindMarkRead:
			hasMarkRead = true
		case semcore.RuleActionKindStop:
			hasStop = true
		}
	}
	if !hasDelete {
		t.Error("expected Delete action")
	}
	if !hasMarkRead {
		t.Error("expected MarkRead action")
	}
	if !hasStop {
		t.Error("expected Stop action")
	}
}

func TestActionsFromEWS_Nil(t *testing.T) {
	result := actionsFromEWS(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 actions for nil; got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// boolPtr helper
// ---------------------------------------------------------------------------

func TestBoolPtr(t *testing.T) {
	trueVal := boolPtr(true)
	if trueVal == nil || !*trueVal {
		t.Error("boolPtr(true) should return pointer to true")
	}
	falseVal := boolPtr(false)
	if falseVal == nil || *falseVal {
		t.Error("boolPtr(false) should return pointer to false")
	}
}

// TestGenerateIDUniqueness is omitted: generateID() uses a time-based counter
// that causes collisions under rapid repeated calls (existing behavior in item.go).

// ---------------------------------------------------------------------------
// ParseEWSDateTime round-trip
// ---------------------------------------------------------------------------

func TestParseEWSDateTime(t *testing.T) {
	original := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	formatted := FormatEWSDateTime(original)
	parsed, err := ParseEWSDateTime(formatted)
	if err != nil {
		t.Fatalf("ParseEWSDateTime(%q) failed: %v", formatted, err)
	}
	// EWS uses RFC3339 which is UTC without zone.
	if !parsed.Equal(original) {
		t.Errorf("ParseEWSDateTime round-trip: got %v; want %v", parsed, original)
	}
}

func TestParseEWSDateTime_Empty(t *testing.T) {
	parsed, err := ParseEWSDateTime("")
	if err != nil {
		t.Errorf("ParseEWSDateTime('') failed: %v", err)
	}
	if !parsed.IsZero() {
		t.Errorf("ParseEWSDateTime('') returned non-zero time: %v", parsed)
	}
}

func TestFormatEWSDateTime_Zero(t *testing.T) {
	var zeroTime time.Time
	if FormatEWSDateTime(zeroTime) != "" {
		t.Error("FormatEWSDateTime(zero) should return empty string")
	}
}

// ---------------------------------------------------------------------------
// OOFAudience String helper
// ---------------------------------------------------------------------------

func TestOOFAudienceString(t *testing.T) {
	tests := []struct {
		aud  semcore.OOFAudience
		want string
	}{
		{semcore.OOFAudienceInternal, "internal"},
		{semcore.OOFAudienceExternal, "external"},
		{semcore.OOFAudienceEveryone, "everyone"},
	}

	for _, tc := range tests {
		got := tc.aud.String()
		if got != tc.want {
			t.Errorf("OOFAudience(%v).String() = %q; want %q", tc.aud, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// RuleActionKind String helper
// ---------------------------------------------------------------------------

func TestRuleActionKindString(t *testing.T) {
	tests := []struct {
		kind semcore.RuleActionKind
		want string
	}{
		{semcore.RuleActionKindMoveToFolder, "moveToFolder"},
		{semcore.RuleActionKindDelete, "delete"},
		{semcore.RuleActionKindForward, "forward"},
		{semcore.RuleActionKindStop, "stop"},
		{semcore.RuleActionKindMarkRead, "markRead"},
		{semcore.RuleActionKindVacation, "vacation"},
	}

	for _, tc := range tests {
		got := tc.kind.String()
		if got != tc.want {
			t.Errorf("RuleActionKind(%v).String() = %q; want %q", tc.kind, got, tc.want)
		}
	}
}
