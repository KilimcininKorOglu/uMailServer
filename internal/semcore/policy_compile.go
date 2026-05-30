package semcore

import (
	"fmt"
	"regexp"
	"strings"
)

// CompileRulesToSieve compiles a sorted list of rules to a Sieve script string.
// The rules must be sorted by priority (lower = higher precedence).
// The returned string is a valid Sieve script that can be executed by the
// sieve interpreter.
func CompileRulesToSieve(rules []*Rule) string {
	if len(rules) == 0 {
		return ""
	}

	// First pass: collect enabled rules and build the if/elsif chain
	var lines []string
	firstRule := true
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}

		// Wrap in if block
		tests := compileConditions(rule)
		if tests == "" {
			// No conditions means always match, but we skip empty rules
			continue
		}

		// Only add require statement when we have actual rules
		if len(lines) == 0 {
			lines = append(lines, `require ["fileinto", "redirect", "reject", "vacation", "envelope", "body", "regex", "subaddress", "date", "index"];`)
		}

		if firstRule {
			lines = append(lines, fmt.Sprintf("if %s {", tests))
			firstRule = false
		} else {
			lines = append(lines, fmt.Sprintf("elsif %s {", tests))
		}

		// Add actions
		for _, action := range rule.Actions {
			actionLine := compileAction(action)
			if actionLine != "" {
				lines = append(lines, actionLine)
			}
		}

		lines = append(lines, "}")
	}

	return strings.Join(lines, "\n")
}

// compileConditions converts rule conditions to Sieve test syntax.
func compileConditions(rule *Rule) string {
	if len(rule.Conditions) == 0 {
		return ""
	}

	var tests []string
	for _, cond := range rule.Conditions {
		test := compileCondition(cond)
		if test != "" {
			tests = append(tests, test)
		}
	}

	if len(tests) == 0 {
		return ""
	}

	if len(tests) == 1 {
		return tests[0]
	}

	if rule.MatchAll {
		// All conditions must match (AND)
		return fmt.Sprintf("allof (%s)", strings.Join(tests, ", "))
	}
	// Any condition matches (OR)
	return fmt.Sprintf("anyof (%s)", strings.Join(tests, ", "))
}

// compileCondition converts a single RuleCondition to Sieve test syntax.
func compileCondition(cond RuleCondition) string {
	header := headerForConditionKind(cond.Kind)
	matchType := sieveMatchType(cond.MatchType)

	switch cond.Kind {
	case RuleConditionKindSize:
		return compileSizeTest(cond)
	case RuleConditionKindFlag:
		return `header :matches ["Content-Type"] "*multipart*"`
	case RuleConditionKindBody:
		return fmt.Sprintf(`body :%s %q`, matchType, cond.Value)
	default:
		if cond.HeaderName != "" {
			header = cond.HeaderName
		}
		return fmt.Sprintf(`header :%s [%q] %q`, matchType, header, cond.Value)
	}
}

// headerForConditionKind returns the Sieve header name for a condition kind.
func headerForConditionKind(kind RuleConditionKind) string {
	switch kind {
	case RuleConditionKindFrom:
		return "From"
	case RuleConditionKindTo:
		return "To"
	case RuleConditionKindSubject:
		return "Subject"
	case RuleConditionKindBody:
		return "" // handled separately
	default:
		return ""
	}
}

// sieveMatchType converts RuleMatchType to Sieve match type.
func sieveMatchType(mt RuleMatchType) string {
	switch mt {
	case RuleMatchTypeContains:
		return "contains"
	case RuleMatchTypeEquals:
		return "is"
	case RuleMatchTypeStartsWith:
		return "matches" // use * suffix
	case RuleMatchTypeEndsWith:
		return "matches" // use * prefix
	case RuleMatchTypeMatches:
		return "matches"
	default:
		return "contains"
	}
}

// compileSizeTest generates a size test.
func compileSizeTest(cond RuleCondition) string {
	// cond.Value should be a number with optional K, M, G suffix
	size := cond.Value
	// Convert to bytes for Sieve
	if strings.HasSuffix(strings.ToUpper(size), "K") {
		n := strings.TrimSuffix(size, "K")
		size = fmt.Sprintf("%dK", parseSize(n, 1024))
	} else if strings.HasSuffix(strings.ToUpper(size), "M") {
		n := strings.TrimSuffix(size, "M")
		size = fmt.Sprintf("%dK", parseSize(n, 1024*1024))
	}

	rel := ":over"
	if strings.HasPrefix(cond.Value, "-") {
		rel = ":under"
		size = strings.TrimPrefix(size, "-")
	}
	return fmt.Sprintf("size %s %s", rel, size)
}

// parseSize parses a numeric string with optional suffix.
func parseSize(s string, multiplier int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0
	}
	return n * multiplier
}

// compileAction converts a single RuleAction to Sieve action syntax.
func compileAction(action RuleAction) string {
	switch action.Kind {
	case RuleActionKindMoveToFolder:
		if action.Target == "" {
			return ""
		}
		return fmt.Sprintf(`fileinto %q;`, action.Target)

	case RuleActionKindCopyToFolder:
		// Sieve doesn't have native copy; use fileinto with keep
		if action.Target == "" {
			return ""
		}
		return fmt.Sprintf(`fileinto %q;`, action.Target)

	case RuleActionKindDelete:
		return "discard;"

	case RuleActionKindMarkRead:
		// Sieve doesn't have native read flag; this is informational
		return "" // handled by keep + flag extension

	case RuleActionKindMarkImportant:
		return "" // handled by flag extension

	case RuleActionKindForward:
		if action.ForwardTo == "" {
			return ""
		}
		return fmt.Sprintf(`redirect %q;`, action.ForwardTo)

	case RuleActionKindForwardAsAttachment:
		// Not directly supported in basic Sieve; redirect is closest
		if action.ForwardTo == "" {
			return ""
		}
		return fmt.Sprintf(`redirect %q;`, action.ForwardTo)

	case RuleActionKindRedirect:
		if action.ForwardTo == "" {
			return ""
		}
		return fmt.Sprintf(`redirect %q;`, action.ForwardTo)

	case RuleActionKindReject:
		if action.Message == "" {
			return `reject "Message rejected";`
		}
		return fmt.Sprintf(`reject %q;`, action.Message)

	case RuleActionKindVacation:
		// Vacation is handled separately by the OOF compiler
		return ""

	case RuleActionKindAddHeader:
		if action.HeaderName == "" || action.HeaderValue == "" {
			return ""
		}
		return fmt.Sprintf(`addheader %q %q;`, action.HeaderName, action.HeaderValue)

	case RuleActionKindDeleteHeader:
		if action.HeaderName == "" {
			return ""
		}
		return fmt.Sprintf(`deleteheader %q;`, action.HeaderName)

	case RuleActionKindFlag:
		// Not natively supported; requires flag extension
		return ""

	case RuleActionKindStop:
		return "stop;"

	default:
		return ""
	}
}

// CompileOOFToSieve compiles an OOF policy to a Sieve vacation action.
// Returns a partial Sieve script containing only the vacation action.
func CompileOOFToSieve(policy *OOFPolicy) string {
	if policy == nil || !policy.Enabled {
		return ""
	}

	var lines []string

	// Check if we're in the active window
	if !policy.StartTime.IsZero() || !policy.EndTime.IsZero() {
		// Add date test if we have a schedule
		var dateTests []string
		if !policy.StartTime.IsZero() {
			dateTests = append(dateTests, fmt.Sprintf(`currentdate :value "GE" "ZONK" %q`, policy.StartTime.Format("20060102T150400")))
		}
		if !policy.EndTime.IsZero() {
			dateTests = append(dateTests, fmt.Sprintf(`currentdate :value "LE" "ZONK" %q`, policy.EndTime.Format("20060102T150400")))
		}
		if len(dateTests) > 0 {
			lines = append(lines, fmt.Sprintf("if allof(%s) {", strings.Join(dateTests, ",\n")))
		}
	}

	// Build vacation action
	vacationLine := compileVacationAction(policy)
	lines = append(lines, vacationLine)

	// Close date test if opened
	if !policy.StartTime.IsZero() || !policy.EndTime.IsZero() {
		lines = append(lines, "}")
	}

	return strings.Join(lines, "\n")
}

// compileVacationAction generates a Sieve vacation action from an OOF policy.
func compileVacationAction(policy *OOFPolicy) string {
	var args []string

	// Subject
	if policy.Subject != "" {
		args = append(args, fmt.Sprintf(`:subject %q`, policy.Subject))
	}

	// Days (send interval)
	days := int(policy.SendInterval().Hours() / 24)
	if days < 1 {
		days = 7
	}
	args = append(args, fmt.Sprintf(":days %d", days))

	// Body
	if policy.TextBody != "" {
		args = append(args, fmt.Sprintf("%q", policy.TextBody))
	} else if policy.HTMLBody != "" {
		args = append(args, fmt.Sprintf("%q", policy.HTMLBody))
	}

	// Addresses to exclude
	if len(policy.ExcludeAddresses) > 0 {
		addrs := "[" + strings.Join(policy.ExcludeAddresses, ", ") + "]"
		args = append(args, fmt.Sprintf(":addresses %s", addrs))
	}

	// MIME flag if we have HTML
	if policy.ReplyStyle == OOFAutoReplyStyleHTML || policy.ReplyStyle == OOFAutoReplyStyleBoth {
		args = append(args, ":mime")
	}

	// Handle (X-Mail-Loop marker)
	args = append(args, `:handle "oof"`)

	if len(args) == 0 {
		return ""
	}

	return fmt.Sprintf("vacation %s;", strings.Join(args, " "))
}

// CompileOOFConditionalVacation generates a complete if block for OOF
// that respects suppression rules (ignore lists, bulk, auto-replies).
func CompileOOFConditionalVacation(policy *OOFPolicy) string {
	if policy == nil || !policy.Enabled {
		return ""
	}

	var lines []string

	// Start with suppression tests
	var suppressionTests []string

	// Don't reply to mailing lists
	if policy.IgnoreLists {
		suppressionTests = append(suppressionTests, `not header :matches "List-Id" "*"`)
		suppressionTests = append(suppressionTests, `not header :matches "List-Unsubscribe" "*"`)
	}

	// Don't reply to bulk mail
	if policy.IgnoreBulk {
		suppressionTests = append(suppressionTests, `not header :matches "Precedence" "bulk"`)
	}

	// Don't reply to auto-generated messages
	if policy.IgnoreAutoReplies {
		suppressionTests = append(suppressionTests, `not header :is "Auto-Submitted" "no"`)
	}

	// Don't reply if already in the loop
	suppressionTests = append(suppressionTests, `not header :matches "X-Mail-Loop" "*"`)

	// Combine suppression tests
	if len(suppressionTests) > 0 {
		lines = append(lines, fmt.Sprintf("if allof(%s) {", strings.Join(suppressionTests, ",\n")))
	} else {
		lines = append(lines, "if true {")
	}

	// Add schedule tests if we have a schedule
	if !policy.StartTime.IsZero() || !policy.EndTime.IsZero() {
		var scheduleTests []string
		if !policy.StartTime.IsZero() {
			scheduleTests = append(scheduleTests, fmt.Sprintf(`currentdate :value "GE" "ZONK" %q`, policy.StartTime.Format("20060102T150400")))
		}
		if !policy.EndTime.IsZero() {
			scheduleTests = append(scheduleTests, fmt.Sprintf(`currentdate :value "LE" "ZONK" %q`, policy.EndTime.Format("20060102T150400")))
		}
		if len(scheduleTests) > 0 {
			lines = append(lines, fmt.Sprintf("  if allof(%s) {", strings.Join(scheduleTests, ",\n")))
		}
	}

	// Vacation action
	vacationLine := "  " + compileVacationAction(policy)
	lines = append(lines, vacationLine)

	// Close schedule test
	if !policy.StartTime.IsZero() || !policy.EndTime.IsZero() {
		lines = append(lines, "  }")
	}

	// Close suppression test
	lines = append(lines, "}")

	return strings.Join(lines, "\n")
}

// CompilePolicyToSieve compiles rules and OOF policy to a complete Sieve script.
// This is the main entry point for generating runtime Sieve scripts from
// canonical policy state.
func CompilePolicyToSieve(rules []*Rule, oof *OOFPolicy) string {
	var lines []string

	// Require statements
	lines = append(lines, `require ["fileinto", "redirect", "reject", "vacation", "envelope", "body", "regex", "subaddress", "date", "index"];`)
	lines = append(lines, "")

	// Compile rules first
	if len(rules) > 0 {
		ruleScript := CompileRulesToSieve(rules)
		if ruleScript != "" {
			lines = append(lines, "# Inbox Rules")
			lines = append(lines, ruleScript)
			lines = append(lines, "")
		}
	}

	// Compile OOF vacation
	if oof != nil && oof.Enabled {
		lines = append(lines, "# Out-of-Office")
		oofScript := CompileOOFConditionalVacation(oof)
		if oofScript != "" {
			lines = append(lines, oofScript)
		} else {
			// Simple vacation without schedule/suppression
			vacationLine := compileVacationAction(oof)
			if vacationLine != "" {
				lines = append(lines, vacationLine)
			}
		}
	}

	// Default: keep
	lines = append(lines, "")
	lines = append(lines, "# Default: keep in inbox")
	lines = append(lines, "keep;")

	return strings.Join(lines, "\n")
}

// ValidateRuleConditions validates rule conditions for policy correctness.
// Returns a list of validation errors, or nil if all conditions are valid.
func ValidateRuleConditions(rule *Rule) []error {
	var errs []error

	for i, cond := range rule.Conditions {
		if cond.Kind == RuleConditionKindHeader && cond.HeaderName == "" {
			errs = append(errs, fmt.Errorf("condition %d: header kind requires headerName", i))
		}
		if cond.Value == "" {
			errs = append(errs, fmt.Errorf("condition %d: value is required", i))
		}
		if cond.MatchType == RuleMatchTypeMatches {
			// Validate regex
			pattern := cond.Value
			pattern = strings.ReplaceAll(pattern, "*", ".*")
			pattern = strings.ReplaceAll(pattern, "?", ".")
			if _, err := regexp.Compile(pattern); err != nil {
				errs = append(errs, fmt.Errorf("condition %d: invalid regex pattern: %w", i, err))
			}
		}
	}

	if len(rule.Actions) == 0 {
		errs = append(errs, fmt.Errorf("rule must have at least one action"))
	}

	for i, action := range rule.Actions {
		if err := validateAction(action, i); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// validateAction validates a single rule action.
func validateAction(action RuleAction, index int) error {
	switch action.Kind {
	case RuleActionKindMoveToFolder, RuleActionKindCopyToFolder:
		if action.Target == "" {
			return fmt.Errorf("action %d: %s requires target folder", index, action.Kind)
		}
	case RuleActionKindForward, RuleActionKindForwardAsAttachment, RuleActionKindRedirect:
		if action.ForwardTo == "" {
			return fmt.Errorf("action %d: %s requires forwardTo address", index, action.Kind)
		}
	case RuleActionKindReject:
		// Message is optional
	case RuleActionKindAddHeader:
		if action.HeaderName == "" {
			return fmt.Errorf("action %d: addHeader requires headerName", index)
		}
	case RuleActionKindDeleteHeader:
		if action.HeaderName == "" {
			return fmt.Errorf("action %d: deleteHeader requires headerName", index)
		}
	}
	return nil
}
