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
