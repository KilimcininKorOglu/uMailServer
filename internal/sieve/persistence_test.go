package sieve

import "testing"

// TestManager_PersistenceRoundTrip verifies that stored scripts, the active
// selection, deactivation, and deletion all survive a simulated restart (a
// fresh Manager loading the same storage directory). Without persistence these
// would be lost when the process exits.
func TestManager_PersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	user := "qa@example.test"

	m1 := NewManager()
	if err := m1.SetStorageDir(dir); err != nil {
		t.Fatalf("SetStorageDir: %v", err)
	}
	if err := m1.SetActiveScript(user, "main", "keep;"); err != nil {
		t.Fatalf("SetActiveScript: %v", err)
	}
	if err := m1.StoreScript(user, "extra", "discard;"); err != nil {
		t.Fatalf("StoreScript: %v", err)
	}

	// Restart 1: scripts + active selection must be restored.
	m2 := NewManager()
	if err := m2.SetStorageDir(dir); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := m2.GetActiveScriptName(user); got != "main" {
		t.Fatalf("active script not restored: got %q", got)
	}
	if src := m2.GetScriptSource(user, "extra"); src != "discard;" {
		t.Fatalf("script source not restored: got %q", src)
	}
	if names := m2.ListScripts(user); len(names) != 2 {
		t.Fatalf("expected 2 scripts after reload, got %v", names)
	}

	// Restart 2: deletion persists.
	m2.DeleteScript(user, "extra")
	m3 := NewManager()
	if err := m3.SetStorageDir(dir); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if src := m3.GetScriptSource(user, "extra"); src != "" {
		t.Fatalf("deleted script still present after reload: %q", src)
	}
}

// TestManager_ReloadUserCrossNode verifies the multi-node coherence fix: two
// managers share one storage directory (as two cluster nodes share the Sieve
// dir). A script written by node A is invisible to node B's in-memory cache
// until B reloads that user from the shared file, after which it is live. A
// later removal on A propagates to B the same way.
func TestManager_ReloadUserCrossNode(t *testing.T) {
	dir := t.TempDir()
	user := "qa.bob@example.test"

	nodeA := NewManager()
	if err := nodeA.SetStorageDir(dir); err != nil {
		t.Fatalf("nodeA SetStorageDir: %v", err)
	}
	nodeB := NewManager()
	if err := nodeB.SetStorageDir(dir); err != nil {
		t.Fatalf("nodeB SetStorageDir: %v", err)
	}

	// Node A creates the user's active script (writes the shared file).
	if err := nodeA.SetActiveScript(user, ManagedScriptName, "fileinto \"Archive\"; stop;"); err != nil {
		t.Fatalf("nodeA SetActiveScript: %v", err)
	}

	// Node B's cache predates the write, so it does not see it yet.
	if nodeB.HasActiveScript(user) {
		t.Fatalf("nodeB should not yet see the script created on nodeA")
	}
	// After reloading the user from the shared dir, node B sees it live.
	if err := nodeB.ReloadUser(user); err != nil {
		t.Fatalf("nodeB ReloadUser: %v", err)
	}
	if !nodeB.HasActiveScript(user) {
		t.Fatalf("nodeB should see the script after ReloadUser")
	}
	if got := nodeB.GetActiveScriptName(user); got != ManagedScriptName {
		t.Fatalf("nodeB active script = %q, want %q", got, ManagedScriptName)
	}

	// Node A removes the user's scripts; node B converges on reload.
	nodeA.DeleteScript(user, ManagedScriptName)
	if err := nodeB.ReloadUser(user); err != nil {
		t.Fatalf("nodeB ReloadUser after delete: %v", err)
	}
	if nodeB.HasActiveScript(user) {
		t.Fatalf("nodeB should no longer see the script after nodeA deleted it")
	}
}
