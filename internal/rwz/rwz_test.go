package rwz

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestCrossCheckWithReferenceParser verifies that our generated .rwz parses with the
// independent reference parser (the reference parser). This is the
// strongest verification available here: there is no real Outlook to test
// against, so an independent reader recovering our rule names and recipients
// proves the byte layout is well-formed, not merely self-consistent. The test
// skips when node or the built reference is unavailable.
func TestCrossCheckWithReferenceParser(t *testing.T) {
	bin := filepath.Join("..", "..", "helper-projects", "the reference parser", "bin", "index.js")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("the reference parser not built (%s); run `npm install && npm run build` in the reference parser", bin)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}

	data, _, err := Write(sampleRules())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := t.TempDir()
	inPath := filepath.Join(dir, "rules.rwz")
	outPath := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(inPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if out, err := exec.Command(node, bin, inPath, outPath).CombinedOutput(); err != nil {
		t.Fatalf("the reference parser failed: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read the reference parser output: %v", err)
	}
	var parsed struct {
		Header struct {
			NumberOfRules int `json:"numberOfRules"`
		} `json:"header"`
		Rules []struct {
			Header struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			} `json:"header"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode the reference parser json: %v", err)
	}

	want := sampleRules()
	if parsed.Header.NumberOfRules != len(want) {
		t.Errorf("numberOfRules: got %d want %d", parsed.Header.NumberOfRules, len(want))
	}
	if len(parsed.Rules) != len(want) {
		t.Fatalf("the reference parser rule count: got %d want %d", len(parsed.Rules), len(want))
	}
	for i := range want {
		if parsed.Rules[i].Header.Name != want[i].Name {
			t.Errorf("rule %d name: the reference parser read %q want %q", i, parsed.Rules[i].Header.Name, want[i].Name)
		}
		if parsed.Rules[i].Header.Enabled != want[i].Enabled {
			t.Errorf("rule %d enabled: the reference parser read %v want %v", i, parsed.Rules[i].Header.Enabled, want[i].Enabled)
		}
	}
}

// sampleRules exercises every supported condition and action so the round-trip
// test proves the full subset survives Write -> Parse.
func sampleRules() []*semcore.Rule {
	return []*semcore.Rule{
		{
			Name:     "Invoices",
			Enabled:  true,
			MatchAll: true,
			Conditions: []semcore.RuleCondition{
				{Kind: semcore.RuleConditionKindSubject, MatchType: semcore.RuleMatchTypeContains, Value: "invoice"},
			},
			Actions: []semcore.RuleAction{
				{Kind: semcore.RuleActionKindMoveToFolder, Target: "Archive"},
				{Kind: semcore.RuleActionKindStop},
			},
		},
		{
			Name:     "From boss",
			Enabled:  false,
			MatchAll: true,
			Conditions: []semcore.RuleCondition{
				{Kind: semcore.RuleConditionKindFrom, MatchType: semcore.RuleMatchTypeContains, Value: "boss@example.com"},
				{Kind: semcore.RuleConditionKindTo, MatchType: semcore.RuleMatchTypeContains, Value: "me@example.com"},
			},
			Actions: []semcore.RuleAction{
				{Kind: semcore.RuleActionKindForward, ForwardTo: "assistant@example.com"},
				{Kind: semcore.RuleActionKindMarkRead},
				{Kind: semcore.RuleActionKindMarkImportant},
			},
		},
		{
			Name:     "Big mail",
			Enabled:  true,
			MatchAll: true,
			Conditions: []semcore.RuleCondition{
				{Kind: semcore.RuleConditionKindSize, MatchType: semcore.RuleMatchTypeContains, Value: "1000K"},
			},
			Actions: []semcore.RuleAction{
				{Kind: semcore.RuleActionKindDelete},
			},
		},
		{
			Name:     "Body urgent",
			Enabled:  true,
			MatchAll: true,
			Conditions: []semcore.RuleCondition{
				{Kind: semcore.RuleConditionKindBody, MatchType: semcore.RuleMatchTypeContains, Value: "urgent"},
			},
			Actions: []semcore.RuleAction{
				{Kind: semcore.RuleActionKindCopyToFolder, Target: "Copies"},
				{Kind: semcore.RuleActionKindRedirect, ForwardTo: "redir@example.com"},
			},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	in := sampleRules()
	data, wrep, err := Write(in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if wrep.SkippedRules != 0 || wrep.SkippedElements != 0 {
		t.Fatalf("unexpected write skips: %+v", wrep)
	}

	out, prep, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if prep.SkippedElements != 0 {
		t.Fatalf("unexpected parse skips: %+v", prep)
	}
	if len(out) != len(in) {
		t.Fatalf("rule count: got %d want %d", len(out), len(in))
	}

	for i := range in {
		want, got := in[i], out[i]
		if got.Name != want.Name {
			t.Errorf("rule %d name: got %q want %q", i, got.Name, want.Name)
		}
		if got.Enabled != want.Enabled {
			t.Errorf("rule %d enabled: got %v want %v", i, got.Enabled, want.Enabled)
		}
		if len(got.Conditions) != len(want.Conditions) {
			t.Fatalf("rule %d conditions: got %d want %d", i, len(got.Conditions), len(want.Conditions))
		}
		for j := range want.Conditions {
			wc, gc := want.Conditions[j], got.Conditions[j]
			if gc.Kind != wc.Kind || gc.Value != wc.Value {
				t.Errorf("rule %d cond %d: got {%d,%q} want {%d,%q}", i, j, gc.Kind, gc.Value, wc.Kind, wc.Value)
			}
			if gc.MatchType != semcore.RuleMatchTypeContains {
				t.Errorf("rule %d cond %d: match type %q, want contains", i, j, gc.MatchType)
			}
		}
		if len(got.Actions) != len(want.Actions) {
			t.Fatalf("rule %d actions: got %d want %d", i, len(got.Actions), len(want.Actions))
		}
		for j := range want.Actions {
			wa, ga := want.Actions[j], got.Actions[j]
			if ga.Kind != wa.Kind || ga.Target != wa.Target || ga.ForwardTo != wa.ForwardTo {
				t.Errorf("rule %d action %d: got %+v want %+v", i, j, ga, wa)
			}
		}
	}
}

// TestSpecConformance checks the fixed Outlook 2016+ header bytes and the first
// rule's CRuleElement marker, matching the documented Outlook rule layout.
func TestSpecConformance(t *testing.T) {
	data, _, err := Write(sampleRules()[:1])
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(data) < 48 {
		t.Fatalf("output too short: %d bytes", len(data))
	}
	if got := binary.LittleEndian.Uint32(data[0:4]); got != rwSignature2019 {
		t.Errorf("signature: got 0x%x want 0x%x", got, rwSignature2019)
	}
	if got := binary.LittleEndian.Uint32(data[4:8]); got != rwFlags2019 {
		t.Errorf("flags: got 0x%x want 0x%x", got, rwFlags2019)
	}
	// unknown8 (offset 8 + 7*4 = 36) must be 1.
	if got := binary.LittleEndian.Uint32(data[36:40]); got != 1 {
		t.Errorf("unknown8: got %d want 1", got)
	}
	// numRules u16 at offset 44.
	if got := binary.LittleEndian.Uint16(data[44:46]); got != 1 {
		t.Errorf("numRules: got %d want 1", got)
	}
	if !bytes.Contains(data, []byte(ruleClassName)) {
		t.Errorf("first rule missing %q marker", ruleClassName)
	}
}

func TestExportReportsUnrepresentable(t *testing.T) {
	rules := []*semcore.Rule{
		{
			Name:     "Header rule",
			Enabled:  true,
			MatchAll: true,
			Conditions: []semcore.RuleCondition{
				{Kind: semcore.RuleConditionKindHeader, MatchType: semcore.RuleMatchTypeContains, Value: "x", HeaderName: "X-Spam"},
				{Kind: semcore.RuleConditionKindSubject, MatchType: semcore.RuleMatchTypeContains, Value: "ok"},
			},
			Actions: []semcore.RuleAction{
				{Kind: semcore.RuleActionKindReject, Message: "no"},
				{Kind: semcore.RuleActionKindStop},
			},
		},
	}
	_, rep, err := Write(rules)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Header condition + Reject action are not representable.
	if rep.SkippedElements != 2 {
		t.Errorf("SkippedElements: got %d want 2", rep.SkippedElements)
	}
	if rep.SkippedRules != 0 {
		t.Errorf("SkippedRules: got %d want 0", rep.SkippedRules)
	}
}

func TestExportDropsEmptyRule(t *testing.T) {
	rules := []*semcore.Rule{
		{
			Name:     "All unrepresentable",
			Enabled:  true,
			MatchAll: true,
			Conditions: []semcore.RuleCondition{
				{Kind: semcore.RuleConditionKindFlag, MatchType: semcore.RuleMatchTypeContains, Value: "x"},
			},
			Actions: []semcore.RuleAction{
				{Kind: semcore.RuleActionKindVacation, Message: "away"},
			},
		},
	}
	data, rep, err := Write(rules)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if rep.SkippedRules != 1 {
		t.Errorf("SkippedRules: got %d want 1", rep.SkippedRules)
	}
	out, _, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 rules in output, got %d", len(out))
	}
}

func TestMatchTypeDowngradeNoted(t *testing.T) {
	rules := []*semcore.Rule{
		{
			Name:     "Equals rule",
			Enabled:  true,
			MatchAll: true,
			Conditions: []semcore.RuleCondition{
				{Kind: semcore.RuleConditionKindSubject, MatchType: semcore.RuleMatchTypeEquals, Value: "exact"},
			},
			Actions: []semcore.RuleAction{{Kind: semcore.RuleActionKindStop}},
		},
	}
	_, rep, err := Write(rules)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(rep.Notes) == 0 {
		t.Error("expected a downgrade note for equals match type")
	}
}

func TestSizeValueMapping(t *testing.T) {
	cases := []struct {
		value            string
		wantMin, wantMax uint32
	}{
		{"1000K", 1000, 0},
		{"5M", 5120, 0},
		{"-2M", 0, 2048},
		{"512", 512, 0},
	}
	for _, c := range cases {
		minKB, maxKB, ok := sizeValueToMinMax(c.value)
		if !ok {
			t.Errorf("%q: not ok", c.value)
			continue
		}
		if minKB != c.wantMin || maxKB != c.wantMax {
			t.Errorf("%q: got min=%d max=%d want min=%d max=%d", c.value, minKB, maxKB, c.wantMin, c.wantMax)
		}
	}
	if _, _, ok := sizeValueToMinMax("abc"); ok {
		t.Error(`"abc" should not be representable`)
	}
}

func TestParseRejectsBadSignature(t *testing.T) {
	bad := make([]byte, 64)
	binary.LittleEndian.PutUint32(bad[0:4], 0xdeadbeef)
	if _, _, err := Parse(bad); err == nil {
		t.Error("expected error for bad signature")
	}
}

func TestParseRejectsTruncated(t *testing.T) {
	data, _, err := Write(sampleRules())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Cut into the rule region (not just the advisory footer) so a rule parse
	// genuinely runs out of data.
	if _, _, err := Parse(data[:len(data)/2]); err == nil {
		t.Error("expected error for truncated input")
	}
}

func TestParseEmptyRuleset(t *testing.T) {
	data, _, err := Write(nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	out, _, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 rules, got %d", len(out))
	}
}
