// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the semantic-core snapshot and restore-continuity model.
// A snapshot captures the complete state needed to restore a mailbox to an
// exact semantic point: all canonical identities, sync-state tokens,
// subscriptions, lifecycle watermarks, and policy state.
//
// # Restore Continuity Invariant
//
// When a backup is restored, the system MUST either:
//   - Preserve all active sync tokens so clients can seamlessly resume, OR
//   - Force one explicit resync boundary by stamping the backup with a
//     ResyncRequired marker that EWS and other protocol clients can detect.
//
// Silent duplicates, gaps, or orphaned folders/items are a violation of
// this contract. The snapshot format ensures that restore can enforce
// the chosen continuity path deterministically.
//
// # Snapshot Format
//
// A snapshot is a self-describing archive keyed by semantic layer. Each layer
// is a JSON document. The snapshot format IS the semantic-core continuity
// contract. Layer keys:
//   - identity        — MailboxId, FolderId, ItemId/ChangeKey, ConversationId maps
//   - sync_state     — per-mailbox, per-folder, per-client sync watermarks
//   - tombstones     — soft/hard delete records since last consistent point
//   - lifecycle      — last N lifecycle events for watermark continuity
//   - subscriptions  — active pull/push subscription state and watermarks
//   - policy        — OOF, inbox rules, notification, and delegate state
package semcore

import (
	"encoding/json"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Continuity mode
// ---------------------------------------------------------------------------

// ContinuityMode describes what happens to clients after this snapshot is restored.
type ContinuityMode string

const (
	// ContinuityModeSeamless means the restored state is byte-identical to the
	// state at backup time. Existing sync tokens remain valid; clients resume
	// without interruption. This is the default for crash-consistent snapshots.
	ContinuityModeSeamless ContinuityMode = "seamless"

	// ContinuityModeResync means the backup captured a point-in-time that differs
	// from the current state, or a restore operation has changed the canonical
	// store in a way that invalidates existing sync tokens. All clients MUST
	// perform a full re-enumeration from the returned baseline watermark before
	// resuming incremental sync. The server stamps the snapshot with a
	// ResyncRequired marker and a baseline watermark that clients can detect.
	ContinuityModeResync ContinuityMode = "resync_required"
)

// IsResyncRequired reports whether clients must resync after restoring this snapshot.
func (m ContinuityMode) IsResyncRequired() bool { return m == ContinuityModeResync }

// ---------------------------------------------------------------------------
// Snapshot manifest
// ---------------------------------------------------------------------------

// SnapshotManifest is the header record for a semantic-core snapshot.
// It describes what the snapshot covers and which continuity contract applies.
type SnapshotManifest struct {
	// Version of the snapshot format (semantic-core contract version).
	Version string `json:"version"`

	// MailboxID this snapshot applies to (empty = all mailboxes).
	MailboxID MailboxId `json:"mailbox_id"`

	// Email of the mailbox (human-readable key for display).
	Email string `json:"email"`

	// SnapshotAt is when the snapshot was taken (UTC).
	SnapshotAt time.Time `json:"snapshot_at"`

	// ContinuityMode determines what happens to clients after restore.
	ContinuityMode ContinuityMode `json:"continuity_mode"`

	// ResyncBaselineWatermark is the baseline sequence/watermark clients must
	// resync from when ContinuityModeResync is active. For seamless restores
	// this is zero.
	ResyncBaselineWatermark uint64 `json:"resync_baseline_watermark,omitempty"`

	// ResyncReason explains why a resync is required (for diagnostics).
	ResyncReason string `json:"resync_reason,omitempty"`

	// ResyncForcedByRestore is true when the resync requirement was imposed
	// by a restore operation rather than the backup itself.
	ResyncForcedByRestore bool `json:"resync_forced_by_restore,omitempty"`

	// LayerChecksums tracks the integrity of each layer file.
	LayerChecksums map[string]string `json:"layer_checksums,omitempty"`
}

// ResyncMarker returns a diagnostic marker string explaining the resync state.
// This can be included in EWS SyncState responses when continuity is not seamless.
func (m *SnapshotManifest) ResyncMarker() string {
	if m.ContinuityMode != ContinuityModeResync {
		return ""
	}
	return fmt.Sprintf("RESYNC_REQUIRED:%s:%d:%s",
		m.MailboxID.String(), m.ResyncBaselineWatermark, m.ResyncReason)
}

// ---------------------------------------------------------------------------
// Layer structures (snapshot format)
//
// Snapshot layers use json.RawMessage for internal stored types that don't have
// public JSON marshal methods. This allows the snapshot format to evolve
// independently of the internal store representation.
// ---------------------------------------------------------------------------

// SnapshotIdentityLayer captures all canonical identities for one mailbox.
// Stored types use json.RawMessage since internal stored types are not
// directly JSON-marshallable (private fields).
type SnapshotIdentityLayer struct {
	MailboxJSON        json.RawMessage `json:"mailbox"`
	FoldersJSON       json.RawMessage `json:"folders"`
	ItemsJSON         json.RawMessage `json:"items"`
	ConversationsJSON json.RawMessage `json:"conversations"`
}

// SnapshotSyncStateLayer captures all sync-state records for one mailbox.
type SnapshotSyncStateLayer struct {
	RecordsJSON json.RawMessage `json:"records"`
}

// SnapshotTombstoneLayer captures tombstone records since the last consistent point.
type SnapshotTombstoneLayer struct {
	RecordsJSON json.RawMessage `json:"records"`
}

// SnapshotLifecycleLayer captures the tail of the lifecycle event journal.
type SnapshotLifecycleLayer struct {
	HighSeq     uint64          `json:"high_seq"`
	EventsJSON  json.RawMessage `json:"events"`
}

// SnapshotSubscriptionLayer captures active subscriptions.
type SnapshotSubscriptionLayer struct {
	SubscriptionsJSON json.RawMessage `json:"subscriptions"`
}

// SnapshotPolicyLayer captures OOF, rules, notification, and delegate state.
type SnapshotPolicyLayer struct {
	OOFSettingsJSON        json.RawMessage `json:"oof,omitempty"`
	RulesJSON              json.RawMessage `json:"rules,omitempty"`
	NotificationsJSON      json.RawMessage `json:"notifications,omitempty"`
	ResourcesJSON          json.RawMessage `json:"resources,omitempty"`
	DelegationsJSON        json.RawMessage `json:"delegations,omitempty"`
}

// ---------------------------------------------------------------------------
// Serialization helpers
// ---------------------------------------------------------------------------

// SnapshotVersion is the current snapshot format version.
// Bump this when the layer format changes incompatibly.
const SnapshotVersion = "1.0"

// ValidateSnapshotManifest checks basic invariants in a manifest.
func ValidateSnapshotManifest(m *SnapshotManifest) error {
	if m == nil {
		return fmt.Errorf("nil manifest")
	}
	if m.Version == "" {
		return fmt.Errorf("missing version")
	}
	if m.SnapshotAt.IsZero() {
		return fmt.Errorf("missing snapshot timestamp")
	}
	switch m.ContinuityMode {
	case ContinuityModeSeamless, ContinuityModeResync:
		// valid
	default:
		return fmt.Errorf("unknown continuity mode: %q", m.ContinuityMode)
	}
	return nil
}
