package semcore

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// Rollout phase tests
// ---------------------------------------------------------------------------

func TestRolloutPhase_String(t *testing.T) {
	tests := []struct {
		p    RolloutPhase
		want string
	}{
		{RolloutPhaseLegacy, "legacy"},
		{RolloutPhaseCanonicalIdentity, "canonical_identity"},
		{RolloutPhaseCanonicalSync, "canonical_sync"},
		{RolloutPhaseCanonicalMutation, "canonical_mutation"},
		{RolloutPhaseFull, "full"},
		{RolloutPhase(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("RolloutPhase(%d).String() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestCurrentRolloutPhase_allLegacy(t *testing.T) {
	// Save and restore global gate
	old := globalGate
	globalGate = &FeatureGate{gates: make(map[FeatureName]bool)}
	defer func() { globalGate = old }()

	if got := CurrentRolloutPhase(); got != RolloutPhaseLegacy {
		t.Errorf("CurrentRolloutPhase() = %v, want legacy", got)
	}
}

func TestCurrentRolloutPhase_identityEnabled(t *testing.T) {
	old := globalGate
	globalGate = &FeatureGate{gates: make(map[FeatureName]bool)}
	globalGate.Set(FeatureCanonicalIdentity, true)
	defer func() { globalGate = old }()

	if got := CurrentRolloutPhase(); got != RolloutPhaseCanonicalIdentity {
		t.Errorf("CurrentRolloutPhase() = %v, want canonical_identity", got)
	}
}

func TestCurrentRolloutPhase_syncEnabled(t *testing.T) {
	old := globalGate
	globalGate = &FeatureGate{gates: make(map[FeatureName]bool)}
	globalGate.Set(FeatureCanonicalSyncState, true)
	defer func() { globalGate = old }()

	if got := CurrentRolloutPhase(); got != RolloutPhaseCanonicalSync {
		t.Errorf("CurrentRolloutPhase() = %v, want canonical_sync", got)
	}
}

func TestCurrentRolloutPhase_mutationEnabled(t *testing.T) {
	old := globalGate
	globalGate = &FeatureGate{gates: make(map[FeatureName]bool)}
	globalGate.Set(FeatureCanonicalMutation, true)
	defer func() { globalGate = old }()

	if got := CurrentRolloutPhase(); got != RolloutPhaseCanonicalMutation {
		t.Errorf("CurrentRolloutPhase() = %v, want canonical_mutation", got)
	}
}

// ---------------------------------------------------------------------------
// FeatureGate tests
// ---------------------------------------------------------------------------

func TestFeatureGate_SetGet(t *testing.T) {
	g := &FeatureGate{gates: make(map[FeatureName]bool)}

	g.Set(FeatureCanonicalIdentity, true)
	if !g.Get(FeatureCanonicalIdentity) {
		t.Error("FeatureCanonicalIdentity should be true after Set")
	}
	if g.Get(FeatureCanonicalSyncState) {
		t.Error("FeatureCanonicalSyncState should be false by default")
	}
}

func TestFeatureGate_IsEnabled(t *testing.T) {
	g := &FeatureGate{gates: make(map[FeatureName]bool)}
	g.Set(FeatureEWS, true)

	if !g.IsEnabled(FeatureEWS) {
		t.Error("IsEnabled(FeatureEWS) should be true")
	}
	if g.IsEnabled(FeatureMAPIHTTP) {
		t.Error("IsEnabled(FeatureMAPIHTTP) should be false by default")
	}
}

func TestFeatureGate_AllGates(t *testing.T) {
	g := &FeatureGate{gates: make(map[FeatureName]bool)}
	g.Set(FeatureCanonicalMutation, true)
	g.Set(FeatureEWS, true)

	snap := g.AllGates()
	if !snap[FeatureCanonicalMutation] {
		t.Error("AllGates()[FeatureCanonicalMutation] should be true")
	}
	if !snap[FeatureEWS] {
		t.Error("AllGates()[FeatureEWS] should be true")
	}
	if snap[FeatureMAPIHTTP] {
		t.Error("AllGates()[FeatureMAPIHTTP] should be false")
	}
}

// ---------------------------------------------------------------------------
// BackfillStatus and RollbackStatus string tests
// ---------------------------------------------------------------------------

func TestBackfillStatus_String(t *testing.T) {
	tests := []struct {
		s    BackfillStatus
		want string
	}{
		{BackfillStatusPending, "pending"},
		{BackfillStatusRunning, "running"},
		{BackfillStatusCompleted, "completed"},
		{BackfillStatusFailed, "failed"},
		{BackfillStatusCancelled, "canceled"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("BackfillStatus(%s).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestRollbackStatus_String(t *testing.T) {
	tests := []struct {
		s    RollbackStatus
		want string
	}{
		{RollbackStatusPending, "pending"},
		{RollbackStatusRunning, "running"},
		{RollbackStatusCompleted, "completed"},
		{RollbackStatusFailed, "failed"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("RollbackStatus(%s).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Backfill error tests
// ---------------------------------------------------------------------------

func TestBackfillError_Error(t *testing.T) {
	err := &BackfillError{msg: "identity backfill failed"}
	if got := err.Error(); got != "identity backfill failed" {
		t.Errorf("BackfillError.Error() = %q, want %q", got, "identity backfill failed")
	}
}

func TestSetBackfillExecutor(t *testing.T) {
	custom := &customBackfill{}
	SetBackfillExecutor(custom)
	// Verify the goroutine-level executor was replaced by running Run
	err := BackfillExecutor.Run(context.Background(), BackfillTargetItem, MailboxId{})
	if err != nil {
		t.Errorf("custom backfill Run returned unexpected error: %v", err)
	}
	// Reset to no-op
	SetBackfillExecutor(&noOpBackfill{})
}

func TestSetRollbackExecutor(t *testing.T) {
	custom := &customRollback{}
	SetRollbackExecutor(custom)
	// Verify the rollback executor was replaced by running Run
	err := RollbackExecutor.Run(context.Background(), RollbackTargetIdentity, MailboxId{})
	if err != nil {
		t.Errorf("custom rollback Run returned unexpected error: %v", err)
	}
	// Reset to no-op
	SetRollbackExecutor(&noOpRollback{})
}

func TestErrBackfillNotReady(t *testing.T) {
	err := ErrBackfillNotReady
	if err == nil {
		t.Fatal("ErrBackfillNotReady should not be nil")
	}
	if err.Error() == "" {
		t.Error("ErrBackfillNotReady.Error() should be non-empty")
	}
}

func TestErrRollbackNotReady(t *testing.T) {
	err := ErrRollbackNotReady
	if err == nil {
		t.Fatal("ErrRollbackNotReady should not be nil")
	}
	if err.Error() == "" {
		t.Error("ErrRollbackNotReady.Error() should be non-empty")
	}
}

type customBackfill struct{}

func (customBackfill) Run(context.Context, BackfillTarget, MailboxId) error { return nil }
func (customBackfill) Status() BackfillJob                                           { return BackfillJob{} }

type customRollback struct{}

func (customRollback) Run(context.Context, RollbackTarget, MailboxId) error { return nil }
func (customRollback) Status() RollbackJob                                        { return RollbackJob{} }
