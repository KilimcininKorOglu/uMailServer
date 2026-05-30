package semcore

import (
	"strings"
	"testing"
	"time"
)

func TestCompileRulesToSieve_Empty(t *testing.T) {
	script := CompileRulesToSieve(nil)
	if script != "" {
		t.Errorf("CompileRulesToSieve(nil) = %q, want empty string", script)
	}

	script = CompileRulesToSieve([]*Rule{})
	if script != "" {
		t.Errorf("CompileRulesToSieve([]) = %q, want empty string", script)
	}
}

func TestCompileRulesToSieve_DisabledRule(t *testing.T) {
	id := MustRuleId("rule-1")
	rule := &Rule{
		ID:      id,
		Enabled: false,
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindFrom, MatchType: RuleMatchTypeContains, Value: "test@example.com"},
		},
		Actions: []RuleAction{
			{Kind: RuleActionKindDelete},
		},
	}

	script := CompileRulesToSieve([]*Rule{rule})
	if script != "" {
		t.Errorf("CompileRulesToSieve([disabled rule]) = %q, want empty string", script)
	}
}

func TestCompileRulesToSieve_Basic(t *testing.T) {
	id1 := MustRuleId("rule-1")
	id2 := MustRuleId("rule-2")

	rules := []*Rule{
		{
			ID:       id1,
			Enabled:  true,
			Priority: 0,
			MatchAll: true,
			Conditions: []RuleCondition{
				{Kind: RuleConditionKindFrom, MatchType: RuleMatchTypeContains, Value: "newsletter@example.com"},
			},
			Actions: []RuleAction{
				{Kind: RuleActionKindMoveToFolder, Target: "News"},
			},
		},
		{
			ID:       id2,
			Enabled:  true,
			Priority: 1,
			MatchAll: false,
			Conditions: []RuleCondition{
				{Kind: RuleConditionKindSubject, MatchType: RuleMatchTypeContains, Value: "urgent"},
			},
			Actions: []RuleAction{
				{Kind: RuleActionKindMarkImportant},
				{Kind: RuleActionKindStop},
			},
		},
	}

	script := CompileRulesToSieve(rules)

	// Should contain require statement
	if !strings.Contains(script, `require`) {
		t.Error("Script should contain require statement")
	}

	// Should contain fileinto for first rule
	if !strings.Contains(script, `fileinto "News"`) {
		t.Error("Script should contain fileinto action for first rule")
	}

	// Should contain stop for second rule
	if !strings.Contains(script, `stop`) {
		t.Error("Script should contain stop action for second rule")
	}

	// Should contain header test
	if !strings.Contains(script, `header`) {
		t.Error("Script should contain header test")
	}
}

func TestCompileRulesToSieve_MatchAllVsAny(t *testing.T) {
	id1 := MustRuleId("rule-and")
	id2 := MustRuleId("rule-or")

	// Rule with MatchAll = true should use allof (AND)
	ruleAnd := &Rule{
		ID:       id1,
		Enabled:  true,
		Priority: 0,
		MatchAll: true,
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindFrom, MatchType: RuleMatchTypeContains, Value: "a"},
			{Kind: RuleConditionKindSubject, MatchType: RuleMatchTypeContains, Value: "b"},
		},
		Actions: []RuleAction{{Kind: RuleActionKindDelete}},
	}

	script := CompileRulesToSieve([]*Rule{ruleAnd})
	if !strings.Contains(script, "allof") {
		t.Error("MatchAll=true should use allof")
	}

	// Rule with MatchAll = false should use anyof (OR)
	ruleOr := &Rule{
		ID:       id2,
		Enabled:  true,
		Priority: 0,
		MatchAll: false,
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindFrom, MatchType: RuleMatchTypeContains, Value: "a"},
			{Kind: RuleConditionKindSubject, MatchType: RuleMatchTypeContains, Value: "b"},
		},
		Actions: []RuleAction{{Kind: RuleActionKindDelete}},
	}

	script = CompileRulesToSieve([]*Rule{ruleOr})
	if !strings.Contains(script, "anyof") {
		t.Error("MatchAll=false should use anyof")
	}
}

func TestCompileRulesToSieve_RedirectAction(t *testing.T) {
	id := MustRuleId("rule-redirect")
	rule := &Rule{
		ID:       id,
		Enabled:  true,
		Priority: 0,
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindFrom, MatchType: RuleMatchTypeContains, Value: "forward@example.com"},
		},
		Actions: []RuleAction{
			{Kind: RuleActionKindRedirect, ForwardTo: "other@example.com"},
		},
	}

	script := CompileRulesToSieve([]*Rule{rule})
	if !strings.Contains(script, `redirect "other@example.com"`) {
		t.Error("Script should contain redirect action with address")
	}
}

func TestCompileRulesToSieve_RejectAction(t *testing.T) {
	id := MustRuleId("rule-reject")
	rule := &Rule{
		ID:       id,
		Enabled:  true,
		Priority: 0,
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindFrom, MatchType: RuleMatchTypeContains, Value: "spam@example.com"},
		},
		Actions: []RuleAction{
			{Kind: RuleActionKindReject, Message: "Message rejected"},
		},
	}

	script := CompileRulesToSieve([]*Rule{rule})
	if !strings.Contains(script, `reject`) {
		t.Error("Script should contain reject action")
	}
}

func TestCompileRulesToSieve_StopAction(t *testing.T) {
	id := MustRuleId("rule-stop")
	rule := &Rule{
		ID:       id,
		Enabled:  true,
		Priority: 0,
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindSubject, MatchType: RuleMatchTypeContains, Value: "stop"},
		},
		Actions: []RuleAction{
			{Kind: RuleActionKindStop},
		},
	}

	script := CompileRulesToSieve([]*Rule{rule})
	if !strings.Contains(script, `stop`) {
		t.Error("Script should contain stop action")
	}
}

func TestCompileOOFConditionalVacation_Disabled(t *testing.T) {
	script := CompileOOFConditionalVacation(nil)
	if script != "" {
		t.Errorf("CompileOOFConditionalVacation(nil) = %q, want empty", script)
	}

	policy := &OOFPolicy{Enabled: false}
	script = CompileOOFConditionalVacation(policy)
	if script != "" {
		t.Errorf("CompileOOFConditionalVacation(disabled) = %q, want empty", script)
	}
}

func TestCompileOOFConditionalVacation_Basic(t *testing.T) {
	policy := &OOFPolicy{
		Enabled:             true,
		Subject:             "Out of Office",
		TextBody:            "I'm currently out of office.",
		IgnoreLists:         true,
		IgnoreBulk:          true,
		IgnoreAutoReplies:   true,
		SendIntervalSeconds: 86400, // 1 day
	}

	script := CompileOOFConditionalVacation(policy)

	if !strings.Contains(script, "vacation") {
		t.Error("Script should contain vacation action")
	}
	if !strings.Contains(script, ":subject") {
		t.Error("Script should contain subject argument")
	}
	if !strings.Contains(script, "allof") {
		t.Error("Script with suppression rules should use allof")
	}
}

func TestCompileOOFConditionalVacation_WithSchedule(t *testing.T) {
	policy := &OOFPolicy{
		Enabled:             true,
		Subject:             "Out of Office",
		TextBody:            "I'm currently out of office.",
		StartTime:           time.Now().Add(-1 * time.Hour),
		EndTime:             time.Now().Add(24 * time.Hour),
		SendIntervalSeconds: 86400,
	}

	script := CompileOOFConditionalVacation(policy)

	if !strings.Contains(script, "vacation") {
		t.Error("Script should contain vacation action")
	}
	// The schedule window is NOT encoded as a Sieve `currentdate` test: our
	// interpreter does not evaluate it, and a script compiled once cannot
	// re-check the window per delivery. The window is enforced server-side via
	// OOFPolicy.IsActiveNow() instead. The script must still guard against mail
	// loops with an X-Mail-Loop suppression test.
	if strings.Contains(script, "currentdate") {
		t.Error("Script should not contain an unevaluable currentdate test")
	}
	if !strings.Contains(script, "X-Mail-Loop") {
		t.Error("Script should suppress replies to existing mail loops")
	}
}

func TestCompilePolicyToSieve_RulesAndOOF(t *testing.T) {
	ruleID := MustRuleId("rule-1")
	oofID := MustOOFId("oof-1")
	mboxID := MustMailboxId("mbox-1")

	rules := []*Rule{
		{
			ID:        ruleID,
			MailboxID: mboxID,
			Enabled:   true,
			Priority:  0,
			Conditions: []RuleCondition{
				{Kind: RuleConditionKindSubject, MatchType: RuleMatchTypeContains, Value: "test"},
			},
			Actions: []RuleAction{
				{Kind: RuleActionKindMoveToFolder, Target: "Test"},
			},
		},
	}

	oof := &OOFPolicy{
		ID:                  oofID,
		MailboxID:           mboxID,
		Enabled:             true,
		Subject:             "OOF",
		TextBody:            "I'm out",
		SendIntervalSeconds: 86400,
	}

	script := CompilePolicyToSieve(rules, oof)

	if !strings.Contains(script, "require") {
		t.Error("Script should contain require statement")
	}
	if !strings.Contains(script, "# Inbox Rules") {
		t.Error("Script should contain rules section comment")
	}
	if !strings.Contains(script, "# Out-of-Office") {
		t.Error("Script should contain OOF section comment")
	}
	// Default keep is handled by the interpreter when no actions are produced
	if strings.Contains(script, "keep;") {
		t.Error("Script should not contain explicit keep action (interpreter adds it implicitly)")
	}
}

func TestCompilePolicyToSieve_EmptyRulesWithOOF(t *testing.T) {
	oofID := MustOOFId("oof-1")
	mboxID := MustMailboxId("mbox-1")

	oof := &OOFPolicy{
		ID:                  oofID,
		MailboxID:           mboxID,
		Enabled:             true,
		Subject:             "OOF",
		TextBody:            "I'm out",
		SendIntervalSeconds: 86400,
	}

	script := CompilePolicyToSieve(nil, oof)

	if !strings.Contains(script, "# Out-of-Office") {
		t.Error("Script should contain OOF section")
	}
	if !strings.Contains(script, "vacation") {
		t.Error("Script should contain vacation action")
	}
}

func TestValidateRuleConditions_Valid(t *testing.T) {
	rule := &Rule{
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindFrom, MatchType: RuleMatchTypeContains, Value: "test@example.com"},
		},
		Actions: []RuleAction{
			{Kind: RuleActionKindMoveToFolder, Target: "Test"},
		},
	}

	errs := ValidateRuleConditions(rule)
	if len(errs) > 0 {
		t.Errorf("ValidateRuleConditions(valid) returned errors: %v", errs)
	}
}

func TestValidateRuleConditions_MissingHeaderName(t *testing.T) {
	rule := &Rule{
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindHeader, MatchType: RuleMatchTypeContains, Value: "test"},
			// Missing HeaderName
		},
		Actions: []RuleAction{
			{Kind: RuleActionKindDelete},
		},
	}

	errs := ValidateRuleConditions(rule)
	if len(errs) == 0 {
		t.Error("ValidateRuleConditions should return error for missing headerName")
	}
}

func TestValidateRuleConditions_MissingAction(t *testing.T) {
	rule := &Rule{
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindFrom, MatchType: RuleMatchTypeContains, Value: "test@example.com"},
		},
		Actions: []RuleAction{}, // Empty actions
	}

	errs := ValidateRuleConditions(rule)
	if len(errs) == 0 {
		t.Error("ValidateRuleConditions should return error for missing actions")
	}
}

func TestValidateRuleConditions_InvalidRegex(t *testing.T) {
	rule := &Rule{
		Conditions: []RuleCondition{
			{Kind: RuleConditionKindFrom, MatchType: RuleMatchTypeMatches, Value: "[invalid(regex"},
		},
		Actions: []RuleAction{
			{Kind: RuleActionKindDelete},
		},
	}

	errs := ValidateRuleConditions(rule)
	if len(errs) == 0 {
		t.Error("ValidateRuleConditions should return error for invalid regex")
	}
}

func TestValidateAction_MissingTarget(t *testing.T) {
	action := RuleAction{Kind: RuleActionKindMoveToFolder}
	// Missing Target

	err := validateAction(action, 0)
	if err == nil {
		t.Error("validateAction should return error for missing target in moveToFolder")
	}
}

func TestValidateAction_MissingForwardTo(t *testing.T) {
	action := RuleAction{Kind: RuleActionKindForward}
	// Missing ForwardTo

	err := validateAction(action, 0)
	if err == nil {
		t.Error("validateAction should return error for missing forwardTo in forward")
	}
}

func TestValidateAction_ValidRedirect(t *testing.T) {
	action := RuleAction{Kind: RuleActionKindRedirect, ForwardTo: "test@example.com"}

	err := validateAction(action, 0)
	if err != nil {
		t.Errorf("validateAction returned error for valid redirect: %v", err)
	}
}
