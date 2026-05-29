package semcore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ContinuityMode
// ---------------------------------------------------------------------------

func TestContinuityMode_IsResyncRequired(t *testing.T) {
	tests := []struct {
		mode ContinuityMode
		want bool
	}{
		{ContinuityModeSeamless, false},
		{ContinuityModeResync,   true},
		{ContinuityMode("invalid_mode"), false},
	}

	for _, tt := range tests {
		if got := tt.mode.IsResyncRequired(); got != tt.want {
			t.Errorf("ContinuityMode(%q).IsResyncRequired() = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// SnapshotManifest
// ---------------------------------------------------------------------------

func TestSnapshotManifest_JSON(t *testing.T) {
	manifest := SnapshotManifest{
		Version:                    "1.0",
		Email:                     "user@example.com",
		SnapshotAt:                time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ContinuityMode:             ContinuityModeResync,
		ResyncBaselineWatermark:   42,
		ResyncReason:              "forced by restore operation",
		ResyncForcedByRestore:     true,
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal(manifest) error: %v", err)
	}

	var got SnapshotManifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(manifest) error: %v", err)
	}

	if got.Version != manifest.Version {
		t.Errorf("Version: got %q, want %q", got.Version, manifest.Version)
	}
	if got.Email != manifest.Email {
		t.Errorf("Email: got %q, want %q", got.Email, manifest.Email)
	}
	if got.ContinuityMode != manifest.ContinuityMode {
		t.Errorf("ContinuityMode: got %v, want %v", got.ContinuityMode, manifest.ContinuityMode)
	}
	if got.ResyncBaselineWatermark != manifest.ResyncBaselineWatermark {
		t.Errorf("ResyncBaselineWatermark: got %d, want %d", got.ResyncBaselineWatermark, manifest.ResyncBaselineWatermark)
	}
	if got.ResyncForcedByRestore != manifest.ResyncForcedByRestore {
		t.Errorf("ResyncForcedByRestore: got %v, want %v", got.ResyncForcedByRestore, manifest.ResyncForcedByRestore)
	}
}

func TestSnapshotManifest_ResyncMarker(t *testing.T) {
	mboxID := MustMailboxId("mbox-test-001")

	tests := []struct {
		name     string
		manifest SnapshotManifest
		wantEmpty bool
	}{
		{
			name: "seamless returns empty marker",
			manifest: SnapshotManifest{
				Version:        "1.0",
				ContinuityMode: ContinuityModeSeamless,
			},
			wantEmpty: true,
		},
		{
			name: "resync returns non-empty marker",
			manifest: SnapshotManifest{
				Version:                  "1.0",
				MailboxID:               mboxID,
				ContinuityMode:          ContinuityModeResync,
				ResyncBaselineWatermark: 99,
				ResyncReason:           "restore",
			},
			wantEmpty: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.manifest.ResyncMarker()
			if (got == "") != tt.wantEmpty {
				t.Errorf("ResyncMarker() = %q, wantEmpty %v", got, tt.wantEmpty)
			}
			if !tt.wantEmpty && !strings.Contains(got, "RESYNC_REQUIRED") {
				t.Errorf("ResyncMarker() should contain RESYNC_REQUIRED, got %q", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateSnapshotManifest
// ---------------------------------------------------------------------------

func TestValidateSnapshotManifest(t *testing.T) {
	tests := []struct {
		name    string
		manifest *SnapshotManifest
		wantErr bool
	}{
		{
			name: "valid seamless manifest",
			manifest: &SnapshotManifest{
				Version:       "1.0",
				SnapshotAt:    time.Now(),
				ContinuityMode: ContinuityModeSeamless,
			},
			wantErr: false,
		},
		{
			name: "valid resync manifest",
			manifest: &SnapshotManifest{
				Version:                    "1.0",
				SnapshotAt:                time.Now(),
				ContinuityMode:             ContinuityModeResync,
				ResyncBaselineWatermark:   42,
				ResyncReason:              "forced by user",
			},
			wantErr: false,
		},
		{
			name:    "nil manifest",
			manifest: nil,
			wantErr: true,
		},
		{
			name: "missing version",
			manifest: &SnapshotManifest{
				SnapshotAt:    time.Now(),
				ContinuityMode: ContinuityModeSeamless,
			},
			wantErr: true,
		},
		{
			name: "zero snapshot time",
			manifest: &SnapshotManifest{
				Version:       "1.0",
				SnapshotAt:    time.Time{},
				ContinuityMode: ContinuityModeSeamless,
			},
			wantErr: true,
		},
		{
			name: "unknown continuity mode",
			manifest: &SnapshotManifest{
				Version:       "1.0",
				SnapshotAt:    time.Now(),
				ContinuityMode: ContinuityMode("unknown"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSnapshotManifest(tt.manifest)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSnapshotManifest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RestoreContinuityExecutor
// ---------------------------------------------------------------------------

func TestRestoreContinuityExecutor_RestoreContinuityMode(t *testing.T) {
	exec := NewRestoreContinuityExecutor(nil)

	tests := []struct {
		name        string
		manifest    *SnapshotManifest
		forceResync bool
		want        ContinuityMode
	}{
		{
			name: "seamless manifest, no force",
			manifest: &SnapshotManifest{
				ContinuityMode: ContinuityModeSeamless,
			},
			forceResync: false,
			want:        ContinuityModeSeamless,
		},
		{
			name: "seamless manifest, force resync",
			manifest: &SnapshotManifest{
				ContinuityMode: ContinuityModeSeamless,
			},
			forceResync: true,
			want:        ContinuityModeResync,
		},
		{
			name: "resync manifest",
			manifest: &SnapshotManifest{
				ContinuityMode: ContinuityModeResync,
			},
			forceResync: false,
			want:        ContinuityModeResync,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exec.RestoreContinuityMode(tt.manifest, tt.forceResync)
			if got != tt.want {
				t.Errorf("RestoreContinuityMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// hasResyncSuffix
// ---------------------------------------------------------------------------

func TestHasResyncSuffix(t *testing.T) {
	tests := []struct {
		clientID string
		want     bool
	}{
		{"sync_v1",                false},
		{"desktop_outlook",        false},
		{"desktop_outlook_resync", true},
		{"mobile_resync",          true},
		{"abc_resync",             true},
		{"outlook_resync",         true},
		{"resync",                 false}, // too short (< 8 chars)
		{"",                       false},
	}

	for _, tt := range tests {
		if got := hasResyncSuffix(tt.clientID); got != tt.want {
			t.Errorf("hasResyncSuffix(%q) = %v, want %v", tt.clientID, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// resync suffix via hasResyncSuffix
// ---------------------------------------------------------------------------

func TestResyncSuffix_Value(t *testing.T) {
	// The suffix is "_resync" (7 chars) as checked by hasResyncSuffix.
	// We verify it indirectly: the suffix must collide with real client IDs
	// rarely (no underscore in middle of a real client ID should cause false positives).
	if !hasResyncSuffix("sync_resync") {
		t.Error("hasResyncSuffix should return true for client IDs ending with _resync")
	}
	if hasResyncSuffix("desktop") {
		t.Error("hasResyncSuffix should return false for normal client IDs")
	}
}

// ---------------------------------------------------------------------------
// SnapshotIdentityLayer
// ---------------------------------------------------------------------------

func TestSnapshotIdentityLayer_JSON(t *testing.T) {
	// Verify that SnapshotIdentityLayer can be serialized and deserialized
	// as a JSON blob without errors, regardless of whether internal data is nil.
	layer := SnapshotIdentityLayer{
		MailboxJSON:        nil,
		FoldersJSON:       nil,
		ItemsJSON:         nil,
		ConversationsJSON: nil,
	}

	data, err := json.Marshal(layer)
	if err != nil {
		t.Fatalf("json.Marshal(SnapshotIdentityLayer) error: %v", err)
	}

	// Verify the layer was serialized to valid JSON.
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(SnapshotIdentityLayer) error: %v", err)
	}

	// All nil fields should serialize to "null" JSON tokens.
	if len(got) != 4 {
		t.Errorf("expected 4 fields in serialized layer, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// SnapshotVersion constant
// ---------------------------------------------------------------------------

func TestSnapshotVersion(t *testing.T) {
	// SnapshotVersion must be non-empty so that snapshots carry a format label.
	if SnapshotVersion == "" {
		t.Error("SnapshotVersion must not be empty")
	}
	// Version should be parseable as a version string (major.minor or similar).
	if !strings.Contains(SnapshotVersion, ".") {
		t.Logf("Note: SnapshotVersion %q does not contain a dot", SnapshotVersion)
	}
}
