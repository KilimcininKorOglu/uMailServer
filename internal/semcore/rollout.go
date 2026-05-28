// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file defines rollout and feature-gating hooks. These hooks control when
// new semantic-core behavior becomes active. They are typed so that Phase 1
// and later implementations can query gate state without inferring it from
// feature flags or environment variables alone.
//
// # Feature Gates
//
// Feature gates are boolean flags keyed by semantic-capability name. Each gate
// defaults to false (feature is inactive). Gates must be set explicitly through
// configuration or server startup before the gated behavior is available.
//
// Gates are NOT roll-your-own booleans. Use FeatureGate directly.
package semcore

import "sync"

// FeatureName identifies a semantic-core feature.
type FeatureName string

const (
	// FeatureCanonicalIdentity enables the canonical identity store as the
	// authoritative source for MailboxId/FolderId/ItemId/ChangeKey. When disabled,
	// protocol adapters derive identity from their local stores (legacy mode).
	FeatureCanonicalIdentity FeatureName = "canonical_identity"

	// FeatureCanonicalSyncState enables durable sync-state, watermark, and
	// tombstone persistence in the semantic-core store. When disabled, sync
	// behavior falls back to existing JMAP/sync approaches.
	FeatureCanonicalSyncState FeatureName = "canonical_sync_state"

	// FeatureCanonicalMutation enables the shared mutation pipeline that assigns
	// semantic identity, advances ChangeKey, emits lifecycle events, and triggers
	// policy hooks. When disabled, SMTP and IMAP mutation paths remain independent.
	FeatureCanonicalMutation FeatureName = "canonical_mutation"

	// FeatureCanonicalLifecycle enables theLifecycle journal as the authoritative
	// record of object state transitions. When disabled, downstream consumers
	// infer state from current-storage side effects.
	FeatureCanonicalLifecycle FeatureName = "canonical_lifecycle"

	// FeatureEWS enables Exchange Web Services projections (Phase 4+).
	// This gate is intentionally separate from canonical_identity because EWS
	// also requires Autodiscover changes and policy gates.
	FeatureEWS FeatureName = "ews"

	// FeatureCollaboration enables calendar, contact, task, rules, OOF, and
	// resource-booking semantics backed by canonical collaboration state.
	FeatureCollaboration FeatureName = "collaboration"

	// FeatureDelegation enables shared mailbox, delegate, send-as, send-on-behalf,
	// and GAL/directory semantics.
	FeatureDelegation FeatureName = "delegation"

	// FeatureMAPIHTTP enables MAPI/HTTP and NSPI/OAB surfaces (Phase 7).
	FeatureMAPIHTTP FeatureName = "mapi_http"
)

// CompatibilityTier represents the protocol compatibility level for a mailbox.
// This determines which endpoints are advertised in Autodiscover responses.
type CompatibilityTier uint8

const (
	// TierIMAPOnly is the baseline tier: only IMAP/SMTP settings are advertised.
	// This is the implicit tier for all accounts before canonical identity migration.
	TierIMAPOnly CompatibilityTier = 0

	// TierExchange is the Exchange tier: IMAP/SMTP plus EWS/Exchange endpoints
	// are advertised. This tier is active after FeatureCanonicalIdentity is
	// enabled and the account's domain has been migrated to the semantic core.
	TierExchange CompatibilityTier = 1
)

// FeatureGate holds the global feature-gate state.
// It is safe for concurrent use.
type FeatureGate struct {
	mu    sync.RWMutex
	gates map[FeatureName]bool
}

// globalGate is the shared feature-gate instance.
var globalGate = &FeatureGate{gates: make(map[FeatureName]bool)}

// Gate returns the global FeatureGate.
func Gate() *FeatureGate { return globalGate }

// Set enables or disables a named feature gate.
// It is not concurrency-safe with Get; use only during server startup
// before any goroutines read the gate state.
func (g *FeatureGate) Set(name FeatureName, enabled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gates[name] = enabled
}

// Get reports whether the named feature gate is enabled.
func (g *FeatureGate) Get(name FeatureName) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.gates[name]
}

// IsEnabled is a convenience alias for Get.
func (g *FeatureGate) IsEnabled(name FeatureName) bool { return g.Get(name) }

// AllGates returns a snapshot of all gate states.
// The returned map is safe to read but not to modify.
func (g *FeatureGate) AllGates() map[FeatureName]bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	m := make(map[FeatureName]bool, len(g.gates))
	for k, v := range g.gates {
		m[k] = v
	}
	return m
}

// RolloutPhase documents the current rollout phase of the semantic-core program.
// Workers can query this to understand migration maturity without hardcoding
// phase assumptions in feature code.
type RolloutPhase uint8

const (
	// RolloutPhaseLegacy means the system is running on legacy stores only.
	// No semantic-core features are enabled. This is the Phase 0 starting state.
	RolloutPhaseLegacy RolloutPhase = iota

	// RolloutPhaseCanonicalIdentity means canonical identity is enabled.
	// Mail, folder, and item reads/writes use semantic-core ID types.
	RolloutPhaseCanonicalIdentity

	// RolloutPhaseCanonicalSync means canonical sync-state is enabled.
	// Sync tokens and watermarks are persisted in the semantic-core store.
	RolloutPhaseCanonicalSync

	// RolloutPhaseCanonicalMutation means the canonical mutation pipeline
	// is the sole authoritative write path for mail objects.
	RolloutPhaseCanonicalMutation

	// RolloutPhaseFull means all Phase 1–7 features have reached their
	// target compatibility bar. This is the mission completion gate.
	RolloutPhaseFull RolloutPhase = 100
)

// CurrentRolloutPhase returns the highest rollout phase currently enabled.
func CurrentRolloutPhase() RolloutPhase {
	g := Gate()
	switch {
	case g.IsEnabled(FeatureCanonicalMutation):
		return RolloutPhaseCanonicalMutation
	case g.IsEnabled(FeatureCanonicalSyncState):
		return RolloutPhaseCanonicalSync
	case g.IsEnabled(FeatureCanonicalIdentity):
		return RolloutPhaseCanonicalIdentity
	default:
		return RolloutPhaseLegacy
	}
}

// CurrentCompatibilityTier returns the effective Exchange compatibility tier.
// When canonical identity is enabled (Phase 1+ migration starting), accounts
// enter the Exchange tier and will see EWS endpoints in Autodiscover responses.
// Until that gate is passed, all accounts remain at TierIMAPOnly.
func CurrentCompatibilityTier() CompatibilityTier {
	if Gate().IsEnabled(FeatureCanonicalIdentity) {
		return TierExchange
	}
	return TierIMAPOnly
}

// String implements fmt.Stringer for RolloutPhase.
func (p RolloutPhase) String() string {
	switch p {
	case RolloutPhaseLegacy:
		return "legacy"
	case RolloutPhaseCanonicalIdentity:
		return "canonical_identity"
	case RolloutPhaseCanonicalSync:
		return "canonical_sync"
	case RolloutPhaseCanonicalMutation:
		return "canonical_mutation"
	case RolloutPhaseFull:
		return "full"
	default:
		return "unknown"
	}
}
