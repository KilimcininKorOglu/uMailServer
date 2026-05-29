// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the semantic-core snapshot executor: the machinery that
// takes a consistent snapshot of a mailbox's semantic-core state for
// backup continuity. The executor delegates to each sub-store's export
// methods to capture the authoritative layer data.
//
// The executor also owns the restore continuity decision: after a restore,
// it stamps the target mailbox's sync state with a ResyncRequired watermark
// so that all connected clients (EWS, IMAP, JMAP) detect that they must
// perform a full re-enumeration rather than trying to continue from a
// pre-restore watermark that is now stale.
//
// Snapshot and restore operations are NOT safe to run concurrently with active
// mailbox mutations on the same mailbox. The caller is responsible for
// isolating the mailbox during snapshot/restore.
package semcore

import (
	"encoding/json"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// SnapshotExecutor
// ---------------------------------------------------------------------------

// SnapshotExecutor coordinates taking a semantic-core snapshot of a mailbox.
type SnapshotExecutor struct {
	store *Store
}

// NewSnapshotExecutor creates an executor that operates on the given Store.
func NewSnapshotExecutor(store *Store) *SnapshotExecutor {
	return &SnapshotExecutor{store: store}
}

// Snapshot captures the complete semantic state for one mailbox.
// The returned manifest describes continuity mode and resync baseline.
func (ex *SnapshotExecutor) Snapshot(mboxID MailboxId, email string) (*SnapshotManifest, map[string][]byte, error) {
	highSeq, err := ex.store.lifecycle.HighestSequence(mboxID)
	if err != nil {
		highSeq = 0
	}

	manifest := &SnapshotManifest{
		Version:        SnapshotVersion,
		MailboxID:      mboxID,
		Email:          email,
		SnapshotAt:     time.Now().UTC(),
		ContinuityMode: ContinuityModeSeamless,
	}

	// Collect each layer as raw JSON blobs.
	identityLayer, err := ex.collectIdentityLayerJSON(mboxID, email)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot identity layer: %w", err)
	}

	syncLayer, err := ex.collectSyncStateLayerJSON(mboxID)
	if err != nil {
		syncLayer = nil
	}

	tombstonesLayer, err := ex.collectTombstonesLayerJSON(mboxID)
	if err != nil {
		tombstonesLayer = nil
	}

	lifecycleLayer, err := ex.collectLifecycleLayerJSON(mboxID, highSeq)
	if err != nil {
		lifecycleLayer = nil
	}

	subsLayer, err := ex.collectSubscriptionLayerJSON(mboxID)
	if err != nil {
		subsLayer = nil
	}

	policyLayer, err := ex.collectPolicyLayerJSON(mboxID)
	if err != nil {
		policyLayer = nil
	}

	layers := map[string][]byte{
		"identity":      identityLayer,
		"sync_state":    syncLayer,
		"tombstones":    tombstonesLayer,
		"lifecycle":     lifecycleLayer,
		"subscriptions": subsLayer,
		"policy":        policyLayer,
	}

	return manifest, layers, nil
}

// collectIdentityLayerJSON returns the identity layer as a JSON blob.
func (ex *SnapshotExecutor) collectIdentityLayerJSON(mboxID MailboxId, email string) ([]byte, error) {
	mboxKey := "e:" + email
	mboxIDFromStore, err := ex.store.identity.GetMailboxIDByKey(mboxKey)
	if err != nil {
		return nil, fmt.Errorf("collectIdentityLayerJSON: get mailbox id: %w", err)
	}

	// Mailbox identity
	mboxRec := struct {
		MailboxID    string `json:"mailbox_id"`
		Email       string `json:"email"`
		UIDValidity uint32 `json:"uid_validity"`
	}{
		MailboxID:    mboxIDFromStore.String(),
		Email:        email,
		UIDValidity:  1,
	}

	// Folder identities
	folders, err := ex.store.identity.ListFolderIdentitiesForMailbox(mboxKey)
	if err != nil {
		return nil, fmt.Errorf("collectIdentityLayerJSON: list folders: %w", err)
	}
	folderRecs := make([]struct {
		FolderID  string `json:"folder_id"`
		ParentID  string `json:"parent_id"`
		Role      string `json:"role"`
		SortOrder int    `json:"sort_order"`
	}, 0, len(folders))
	for _, f := range folders {
		folderRecs = append(folderRecs, struct {
			FolderID  string `json:"folder_id"`
			ParentID  string `json:"parent_id"`
			Role      string `json:"role"`
			SortOrder int    `json:"sort_order"`
		}{
			FolderID:  f.FolderID.String(),
			ParentID:  f.ParentID.String(),
			Role:      f.Role,
			SortOrder: f.SortOrder,
		})
	}

	// Item identities
	itemMap := make(map[ItemId]StoredItemIdentity)
	for _, f := range folders {
		items, err := ex.store.identity.ListItemIdentitiesByFolder(f.FolderID)
		if err != nil {
			return nil, fmt.Errorf("collectIdentityLayerJSON: list items: %w", err)
		}
		for _, it := range items {
			itemMap[it.ItemID] = it
		}
	}
	itemRecs := make([]struct {
		ItemID     string `json:"item_id"`
		ChangeKey  string `json:"change_key"`
		FolderID   string `json:"folder_id"`
		MailboxID  string `json:"mailbox_id"`
		MsgKey    string `json:"msg_key"`
		Email     string `json:"email"`
	}, 0, len(itemMap))
	for _, it := range itemMap {
		itemRecs = append(itemRecs, struct {
			ItemID    string `json:"item_id"`
			ChangeKey string `json:"change_key"`
			FolderID  string `json:"folder_id"`
			MailboxID string `json:"mailbox_id"`
			MsgKey   string `json:"msg_key"`
			Email    string `json:"email"`
		}{
			ItemID:    it.ItemID.String(),
			ChangeKey: it.ChangeKey.String(),
			FolderID:  it.FolderID.String(),
			MailboxID: it.MailboxID.String(),
			MsgKey:   it.MsgKey,
			Email:    it.Email,
		})
	}

	layer := struct {
		Mailbox  interface{} `json:"mailbox"`
		Folders interface{} `json:"folders"`
		Items   interface{} `json:"items"`
	}{
		Mailbox:  mboxRec,
		Folders:  folderRecs,
		Items:    itemRecs,
	}
	return json.Marshal(layer)
}

// collectSyncStateLayerJSON returns sync-state records as a JSON blob.
func (ex *SnapshotExecutor) collectSyncStateLayerJSON(mboxID MailboxId) ([]byte, error) {
	records, err := ex.store.syncState.ListSyncStatesByMailbox(mboxID, FolderId{})
	if err != nil || records == nil {
		return []byte(`{"records":[]}`), nil
	}
	recs := make([]struct {
		MailboxID string `json:"mailbox_id"`
		FolderID string `json:"folder_id"`
		ClientID string `json:"client_id"`
		Watermark string `json:"watermark"`
		Version  uint64 `json:"version"`
	}, 0, len(records))
	for _, r := range records {
		recs = append(recs, struct {
			MailboxID  string `json:"mailbox_id"`
			FolderID  string `json:"folder_id"`
			ClientID  string `json:"client_id"`
			Watermark string `json:"watermark"`
			Version   uint64 `json:"version"`
		}{
			MailboxID:  r.MailboxID.String(),
			FolderID:  r.FolderID.String(),
			ClientID:  r.ClientID,
			Watermark: r.Watermark,
			Version:   r.Version,
		})
	}
	return json.Marshal(struct{ Records interface{} }{Records: recs})
}

// collectTombstonesLayerJSON returns tombstone records as a JSON blob.
func (ex *SnapshotExecutor) collectTombstonesLayerJSON(mboxID MailboxId) ([]byte, error) {
	tombstones, err := ex.store.tombstones.ListTombstonesByMailbox(mboxID, FolderId{})
	if err != nil || tombstones == nil {
		return []byte(`{"records":[]}`), nil
	}
	recs := make([]struct {
		MailboxID string `json:"mailbox_id"`
		FolderID  string `json:"folder_id"`
		ItemID   string `json:"item_id"`
		Kind     string `json:"kind"`
		DeletedAt string `json:"deleted_at"`
		Actor    string `json:"actor"`
	}, 0, len(tombstones))
	for _, t := range tombstones {
		recs = append(recs, struct {
			MailboxID string `json:"mailbox_id"`
			FolderID string `json:"folder_id"`
			ItemID  string `json:"item_id"`
			Kind    string `json:"kind"`
			DeletedAt string `json:"deleted_at"`
			Actor   string `json:"actor"`
		}{
			MailboxID:  t.MailboxID.String(),
			FolderID:  t.FolderID.String(),
			ItemID:   t.ItemID.String(),
			Kind:     t.Kind.String(),
			DeletedAt: t.DeletedAt.Format(time.RFC3339),
			Actor:    t.Actor,
		})
	}
	return json.Marshal(struct{ Records interface{} }{Records: recs})
}

// collectLifecycleLayerJSON returns the lifecycle tail as a JSON blob.
func (ex *SnapshotExecutor) collectLifecycleLayerJSON(mboxID MailboxId, highSeq uint64) ([]byte, error) {
	events, _, err := ex.store.lifecycle.PollEvents(mboxID, 0, 1000)
	if err != nil || events == nil {
		return json.Marshal(struct {
			HighSeq uint64        `json:"high_seq"`
			Events  []LifecycleRef `json:"events"`
		}{HighSeq: highSeq, Events: nil})
	}
	refs := make([]LifecycleRef, len(events))
	for i, e := range events {
		refs[i] = LifecycleRef{
			MailboxID:  e.MailboxID.String(),
			FolderID:   e.FolderID.String(),
			ItemID:    e.ItemID.String(),
			Kind:      e.Kind.String(),
			At:        e.At.Format(time.RFC3339),
			Actor:     e.Actor,
			ChangeKey: e.ChangeKey.String(),
		}
	}
	return json.Marshal(struct {
		HighSeq uint64        `json:"high_seq"`
		Events  []LifecycleRef `json:"events"`
	}{HighSeq: highSeq, Events: refs})
}

// LifecycleRef is a JSON-serializable reference to a lifecycle event.
type LifecycleRef struct {
	MailboxID  string `json:"mailbox_id"`
	FolderID   string `json:"folder_id"`
	ItemID    string `json:"item_id"`
	Kind      string `json:"kind"`
	At        string `json:"at"`
	Actor     string `json:"actor"`
	ChangeKey string `json:"change_key"`
}

// collectSubscriptionLayerJSON returns active subscriptions as a JSON blob.
func (ex *SnapshotExecutor) collectSubscriptionLayerJSON(mboxID MailboxId) ([]byte, error) {
	subs, err := ex.store.subscriptions.ListSubscriptionsByMailbox(mboxID)
	if err != nil || subs == nil {
		return []byte(`{"subscriptions":[]}`), nil
	}
	recs := make([]struct {
		ID        string   `json:"id"`
		MailboxID string   `json:"mailbox_id"`
		Kind     string   `json:"kind"`
		FolderIDs []string `json:"folder_ids"`
		LastSeq  uint64   `json:"last_seq"`
		ExpiresAt string  `json:"expires_at"`
	}, 0, len(subs))
	for _, s := range subs {
		fids := make([]string, len(s.FolderIDs))
		for i, f := range s.FolderIDs {
			fids[i] = f.String()
		}
		recs = append(recs, struct {
			ID         string   `json:"id"`
			MailboxID  string   `json:"mailbox_id"`
			Kind      string   `json:"kind"`
			FolderIDs []string `json:"folder_ids"`
			LastSeq   uint64   `json:"last_seq"`
			ExpiresAt string   `json:"expires_at"`
		}{
			ID:         s.ID.ID,
			MailboxID:  s.MailboxID.String(),
			Kind:      s.Kind.String(),
			FolderIDs: fids,
			LastSeq:   s.LastSeq,
			ExpiresAt: s.ExpiresAt.Format(time.RFC3339),
		})
	}
	return json.Marshal(struct{ Subscriptions interface{} }{Subscriptions: recs})
}

// collectPolicyLayerJSON returns OOF, rules, and delegations as a JSON blob.
func (ex *SnapshotExecutor) collectPolicyLayerJSON(mboxID MailboxId) ([]byte, error) {
	layer := struct {
		OOF       interface{} `json:"oof,omitempty"`
		Rules     interface{} `json:"rules,omitempty"`
		Delegations interface{} `json:"delegations,omitempty"`
	}{}

	oofID, err := NewOOFId(mboxID.String())
	if err == nil {
		if oof, err := ex.store.policy.GetOOF(oofID); err == nil && oof != nil {
			layer.OOF = oof
		}
	}

	if rules, err := ex.store.policy.ListRules(mboxID); err == nil && rules != nil {
		layer.Rules = rules
	}

	if delegations, err := ex.store.delegation.ListDelegates(mboxID); err == nil && delegations != nil {
		layer.Delegations = delegations
	}

	return json.Marshal(layer)
}

// ---------------------------------------------------------------------------
// RestoreContinuityExecutor
// ---------------------------------------------------------------------------

// RestoreContinuityExecutor coordinates restore operations and enforces the
// continuity contract.
type RestoreContinuityExecutor struct {
	store *Store
}

// NewRestoreContinuityExecutor creates an executor for restore operations.
func NewRestoreContinuityExecutor(store *Store) *RestoreContinuityExecutor {
	return &RestoreContinuityExecutor{store: store}
}

// EnforceResyncBoundary marks all active sync-state records and subscriptions
// for the given mailbox as requiring a full resync. This is called when a
// restore has changed the canonical state in a way that invalidates existing
// continuation tokens.
//
// After this method returns, any client that presents a pre-restore sync token
// will receive a ResyncRequired error from the appropriate handler.
//
// The baseline watermark is the highest lifecycle sequence from the snapshot
// that was restored.
func (ex *RestoreContinuityExecutor) EnforceResyncBoundary(mboxID MailboxId, baselineSeq uint64) error {
	// 1. Invalidate all sync-state records by writing a resync marker watermark.
	// Clients that decode "RESYNC:<folderID>:<baseline>" detect the marker
	// and restart from scratch.
	allSyncStates, err := ex.store.syncState.ListSyncStatesByMailbox(mboxID, FolderId{})
	if err != nil {
		return fmt.Errorf("EnforceResyncBoundary: list sync states: %w", err)
	}
	for _, st := range allSyncStates {
		resyncWM := fmt.Sprintf("RESYNC:%s:%d", st.FolderID.String(), baselineSeq)
		if err := ex.store.syncState.PutSyncState(st.MailboxID, st.FolderID, st.ClientID+"_resync", resyncWM); err != nil {
			return fmt.Errorf("EnforceResyncBoundary: put resync watermark: %w", err)
		}
	}

	// 2. Remove all active subscriptions so clients must re-subscribe fresh.
	// This prevents stale subscription watermarks from surviving a restore.
	subs, err := ex.store.subscriptions.ListSubscriptionsByMailbox(mboxID)
	if err != nil {
		return fmt.Errorf("EnforceResyncBoundary: list subscriptions: %w", err)
	}
	for _, sub := range subs {
		if err := ex.store.subscriptions.RemoveSubscription(sub.ID); err != nil {
			return fmt.Errorf("EnforceResyncBoundary: remove subscription: %w", err)
		}
	}

	return nil
}

// RemoveResyncBoundary clears any resync markers inserted by EnforceResyncBoundary.
// This is called when a Seamless restore completes.
func (ex *RestoreContinuityExecutor) RemoveResyncBoundary(mboxID MailboxId) error {
	allSyncStates, err := ex.store.syncState.ListSyncStatesByMailbox(mboxID, FolderId{})
	if err != nil {
		return fmt.Errorf("RemoveResyncBoundary: list sync states: %w", err)
	}
	for _, st := range allSyncStates {
		if hasResyncSuffix(st.ClientID) {
			if err := ex.store.syncState.PutSyncState(st.MailboxID, st.FolderID, st.ClientID, st.Watermark); err != nil {
				return fmt.Errorf("RemoveResyncBoundary: put sync state: %w", err)
			}
		}
	}
	return nil
}

// RestoreContinuityMode determines which continuity mode to apply based on
// the snapshot manifest and any restore options.
func (ex *RestoreContinuityExecutor) RestoreContinuityMode(manifest *SnapshotManifest, forceResync bool) ContinuityMode {
	if forceResync || manifest.ContinuityMode == ContinuityModeResync {
		return ContinuityModeResync
	}
	return ContinuityModeSeamless
}

// IsSnapshotStale returns true if the given manifest was created before the
// current highest lifecycle sequence for the mailbox — meaning the snapshot
// does not reflect the latest state and a restore would skip recent mutations.
func (ex *RestoreContinuityExecutor) IsSnapshotStale(mboxID MailboxId, manifest *SnapshotManifest) (bool, uint64, error) {
	currentHigh, err := ex.store.lifecycle.HighestSequence(mboxID)
	if err != nil {
		return false, 0, err
	}
	return currentHigh > manifest.ResyncBaselineWatermark, currentHigh, nil
}

// ParseSnapshotManifest decodes a JSON snapshot manifest from raw bytes.
func ParseSnapshotManifest(data []byte) (*SnapshotManifest, error) {
	var m SnapshotManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse snapshot manifest: %w", err)
	}
	return &m, nil
}

// hasResyncSuffix returns true when a ClientID is a resync-scoped token.
func hasResyncSuffix(clientID string) bool {
	return len(clientID) > 7 && clientID[len(clientID)-7:] == "_resync"
}
