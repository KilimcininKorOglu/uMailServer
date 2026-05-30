package semcore

import (
	"testing"
	"time"
)

func TestRuleId_NewAndBasic(t *testing.T) {
	id, err := NewRuleId("rule-abc123")
	if err != nil {
		t.Fatalf("NewRuleId returned error: %v", err)
	}
	if id.String() != "rule-abc123" {
		t.Errorf("String() = %q, want %q", id.String(), "rule-abc123")
	}
	if id.IsZero() {
		t.Error("IsZero() = true, want false for non-empty ID")
	}
}

func TestRuleId_NewEmpty(t *testing.T) {
	_, err := NewRuleId("")
	if err == nil {
		t.Error("NewRuleId('') did not return error")
	}
}

func TestRuleId_Equal(t *testing.T) {
	id1 := MustRuleId("rule-abc")
	id2 := MustRuleId("rule-abc")
	id3 := MustRuleId("rule-xyz")

	if !id1.Equal(id2) {
		t.Error("Equal() id1 vs id2: want true")
	}
	if id1.Equal(id3) {
		t.Error("Equal() id1 vs id3: want false")
	}
}

func TestRuleId_MarshalJSON(t *testing.T) {
	id := MustRuleId("rule-abc")
	data, err := id.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	var got RuleId
	if err := got.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON returned error: %v", err)
	}
	if !got.Equal(id) {
		t.Errorf("UnmarshalJSON round-trip: got %q, want %q", got.String(), id.String())
	}
}

func TestRuleChangeKey_NewAndBasic(t *testing.T) {
	ck, err := NewRuleChangeKey("ck-rule-abc123")
	if err != nil {
		t.Fatalf("NewRuleChangeKey returned error: %v", err)
	}
	if ck.String() != "ck-rule-abc123" {
		t.Errorf("String() = %q, want %q", ck.String(), "ck-rule-abc123")
	}
	if ck.IsZero() {
		t.Error("IsZero() = true, want false for non-empty ChangeKey")
	}
}

func TestRuleChangeKey_NewEmpty(t *testing.T) {
	_, err := NewRuleChangeKey("")
	if err == nil {
		t.Error("NewRuleChangeKey('') did not return error")
	}
}

func TestOOFId_NewAndBasic(t *testing.T) {
	id, err := NewOOFId("oof-mailbox-abc")
	if err != nil {
		t.Fatalf("NewOOFId returned error: %v", err)
	}
	if id.String() != "oof-mailbox-abc" {
		t.Errorf("String() = %q, want %q", id.String(), "oof-mailbox-abc")
	}
	if id.IsZero() {
		t.Error("IsZero() = true, want false for non-empty ID")
	}
}

func TestOOFId_NewEmpty(t *testing.T) {
	_, err := NewOOFId("")
	if err == nil {
		t.Error("NewOOFId('') did not return error")
	}
}

func TestOOFChangeKey_NewAndBasic(t *testing.T) {
	ck, err := NewOOFChangeKey("ck-oof-xyz")
	if err != nil {
		t.Fatalf("NewOOFChangeKey returned error: %v", err)
	}
	if ck.String() != "ck-oof-xyz" {
		t.Errorf("String() = %q, want %q", ck.String(), "ck-oof-xyz")
	}
}

func TestResourceId_NewAndBasic(t *testing.T) {
	id, err := NewResourceId("resource-room-abc")
	if err != nil {
		t.Fatalf("NewResourceId returned error: %v", err)
	}
	if id.String() != "resource-room-abc" {
		t.Errorf("String() = %q, want %q", id.String(), "resource-room-abc")
	}
}

func TestResourceId_NewEmpty(t *testing.T) {
	_, err := NewResourceId("")
	if err == nil {
		t.Error("NewResourceId('') did not return error")
	}
}

func TestResourceChangeKey_NewAndBasic(t *testing.T) {
	ck, err := NewResourceChangeKey("ck-resource-xyz")
	if err != nil {
		t.Fatalf("NewResourceChangeKey returned error: %v", err)
	}
	if ck.String() != "ck-resource-xyz" {
		t.Errorf("String() = %q, want %q", ck.String(), "ck-resource-xyz")
	}
}

func TestNotificationId_NewAndBasic(t *testing.T) {
	id, err := NewNotificationId("notif-abc123")
	if err != nil {
		t.Fatalf("NewNotificationId returned error: %v", err)
	}
	if id.String() != "notif-abc123" {
		t.Errorf("String() = %q, want %q", id.String(), "notif-abc123")
	}
}

func TestOOFAudience_String(t *testing.T) {
	tests := []struct {
		aud OOFAudience
		str string
	}{
		{OOFAudienceInternal, "internal"},
		{OOFAudienceExternal, "external"},
		{OOFAudienceEveryone, "everyone"},
	}
	for _, tc := range tests {
		if tc.aud.String() != tc.str {
			t.Errorf("OOFAudience(%d).String() = %q, want %q", tc.aud, tc.aud.String(), tc.str)
		}
	}
}

func TestOOFAudienceFromString(t *testing.T) {
	tests := []struct {
		input string
		aud   OOFAudience
	}{
		{"internal", OOFAudienceInternal},
		{"external", OOFAudienceExternal},
		{"everyone", OOFAudienceEveryone},
		{"unknown", OOFAudienceInternal}, // default
	}
	for _, tc := range tests {
		got := OOFAudienceFromString(tc.input)
		if got != tc.aud {
			t.Errorf("OOFAudienceFromString(%q) = %d, want %d", tc.input, got, tc.aud)
		}
	}
}

func TestOOFPolicy_IsActiveNow(t *testing.T) {
	now := time.Now()

	// Disabled policy
	policy := &OOFPolicy{Enabled: false}
	if policy.IsActiveNow() {
		t.Error("Disabled policy should not be active")
	}

	// Enabled without schedule
	policy = &OOFPolicy{Enabled: true}
	if !policy.IsActiveNow() {
		t.Error("Enabled policy without schedule should be active")
	}

	// Exchange semantics: an Enabled policy is active now even if it carries a
	// (future) duration — the duration is informational for the Enabled state.
	policy = &OOFPolicy{Enabled: true, State: "Enabled", StartTime: now.Add(1 * time.Hour)}
	if !policy.IsActiveNow() {
		t.Error("Enabled policy should be active regardless of its duration window")
	}

	// Scheduled with future start time
	policy = &OOFPolicy{Enabled: true, State: "Scheduled", StartTime: now.Add(1 * time.Hour)}
	if policy.IsActiveNow() {
		t.Error("Scheduled policy with future start should not be active")
	}

	// Scheduled with past end time
	policy = &OOFPolicy{Enabled: true, State: "Scheduled", EndTime: now.Add(-1 * time.Hour)}
	if policy.IsActiveNow() {
		t.Error("Scheduled policy with past end should not be active")
	}

	// Scheduled within schedule window
	policy = &OOFPolicy{Enabled: true, State: "Scheduled", StartTime: now.Add(-1 * time.Hour), EndTime: now.Add(1 * time.Hour)}
	if !policy.IsActiveNow() {
		t.Error("Scheduled policy within schedule window should be active")
	}
}

func TestOOFPolicy_SendInterval(t *testing.T) {
	// Default interval
	policy := &OOFPolicy{}
	if d := policy.SendInterval(); d != 7*24*time.Hour {
		t.Errorf("Default SendInterval = %v, want 7 days", d)
	}

	// Custom interval
	policy = &OOFPolicy{SendIntervalSeconds: 86400} // 1 day in seconds
	if d := policy.SendInterval(); d != 24*time.Hour {
		t.Errorf("SendInterval = %v, want 1 day", d)
	}
}

func TestRule_IsZero(t *testing.T) {
	rule := &Rule{}
	if !rule.IsZero() {
		t.Error("Empty Rule should be zero")
	}

	id := MustRuleId("rule-abc")
	rule = &Rule{ID: id}
	if rule.IsZero() {
		t.Error("Rule with ID should not be zero")
	}
}

func TestRuleCondition_IsZero(t *testing.T) {
	cond := &RuleCondition{}
	if !cond.IsZero() {
		t.Error("Empty RuleCondition should be zero")
	}

	cond = &RuleCondition{Kind: RuleConditionKindFrom, Value: "test@example.com"}
	if cond.IsZero() {
		t.Error("RuleCondition with data should not be zero")
	}
}

func TestRuleAction_IsZero(t *testing.T) {
	action := &RuleAction{}
	if !action.IsZero() {
		t.Error("Empty RuleAction should be zero")
	}

	action = &RuleAction{Kind: RuleActionKindDelete}
	if action.IsZero() {
		t.Error("RuleAction with kind should not be zero")
	}
}

func TestRuleAction_Kind_String(t *testing.T) {
	tests := []struct {
		kind   RuleActionKind
		expect string
	}{
		{RuleActionKindMoveToFolder, "moveToFolder"},
		{RuleActionKindCopyToFolder, "copyToFolder"},
		{RuleActionKindDelete, "delete"},
		{RuleActionKindMarkRead, "markRead"},
		{RuleActionKindMarkImportant, "markImportant"},
		{RuleActionKindForward, "forward"},
		{RuleActionKindForwardAsAttachment, "forwardAsAttachment"},
		{RuleActionKindRedirect, "redirect"},
		{RuleActionKindReject, "reject"},
		{RuleActionKindVacation, "vacation"},
		{RuleActionKindAddHeader, "addHeader"},
		{RuleActionKindDeleteHeader, "deleteHeader"},
		{RuleActionKindFlag, "flag"},
		{RuleActionKindStop, "stop"},
	}
	for _, tc := range tests {
		if tc.kind.String() != tc.expect {
			t.Errorf("RuleActionKind(%d).String() = %q, want %q", tc.kind, tc.kind.String(), tc.expect)
		}
	}
}

func TestResourceKind_String(t *testing.T) {
	if ResourceKindRoom.String() != "room" {
		t.Errorf("ResourceKindRoom.String() = %q, want %q", ResourceKindRoom.String(), "room")
	}
	if ResourceKindEquipment.String() != "equipment" {
		t.Errorf("ResourceKindEquipment.String() = %q, want %q", ResourceKindEquipment.String(), "equipment")
	}
}

func TestResourcePolicy_IsZero(t *testing.T) {
	policy := &ResourcePolicy{}
	if !policy.IsZero() {
		t.Error("Empty ResourcePolicy should be zero")
	}

	id := MustResourceId("resource-abc")
	policy = &ResourcePolicy{ID: id}
	if policy.IsZero() {
		t.Error("ResourcePolicy with ID should not be zero")
	}
}

func TestNotificationPolicy_IsZero(t *testing.T) {
	policy := &NotificationPolicy{}
	if !policy.IsZero() {
		t.Error("Empty NotificationPolicy should be zero")
	}

	id := MustNotificationId("notif-abc")
	policy = &NotificationPolicy{ID: id}
	if policy.IsZero() {
		t.Error("NotificationPolicy with ID should not be zero")
	}
}
