package sieve

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// persistedUser is the on-disk representation of a single user's Sieve state:
// the script sources keyed by name plus the active script name. Compiled
// scripts are not persisted — sources are recompiled on load.
type persistedUser struct {
	Scripts map[string]string `json:"scripts"`
	Active  string            `json:"active"`
}

// SetStorageDir enables on-disk persistence of Sieve scripts under dir and
// loads any previously stored scripts into memory. Without it the manager keeps
// scripts in memory only (the default, used by unit tests), so they would be
// lost on restart.
func (m *Manager) SetStorageDir(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sieve storage dir: %w", err)
	}
	m.scriptsMu.Lock()
	m.storageDir = dir
	m.scriptsMu.Unlock()
	return m.load()
}

// userFile returns the JSON path for a user. The user ID (an email address) is
// base64url-encoded so it is always a safe, collision-free filename.
func (m *Manager) userFile(userID string) string {
	name := base64.RawURLEncoding.EncodeToString([]byte(userID)) + ".json"
	return filepath.Join(m.storageDir, name)
}

// persistUserLocked writes the user's scripts to disk atomically. The caller
// must hold scriptsMu. It is a no-op when persistence is disabled.
func (m *Manager) persistUserLocked(userID string) {
	if m.storageDir == "" {
		return
	}
	pu := persistedUser{Scripts: make(map[string]string), Active: m.activeScripts[userID]}
	for name, s := range m.scripts[userID] {
		pu.Scripts[name] = s.Source
	}
	data, err := json.Marshal(pu)
	if err != nil {
		return
	}
	target := m.userFile(userID)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp) //nolint:errcheck // best-effort cleanup of the temp file
	}
}

// ReloadUser re-reads a single user's persisted Sieve state from disk and
// refreshes the in-memory cache, so a script compiled on another node (which
// wrote the shared file) becomes visible here. It exists for the multi-node
// deployment: the compiled-script cache is per-process, and the shared Sieve
// directory is the cross-node source of truth. A no-op when persistence is
// disabled. When the user's file is absent (e.g. all their rules were removed
// on another node) their in-memory scripts are cleared.
func (m *Manager) ReloadUser(userID string) error {
	m.scriptsMu.Lock()
	defer m.scriptsMu.Unlock()
	if m.storageDir == "" {
		return nil
	}
	data, err := os.ReadFile(m.userFile(userID))
	if err != nil {
		if os.IsNotExist(err) {
			delete(m.scripts, userID)
			delete(m.activeScripts, userID)
			return nil
		}
		return fmt.Errorf("sieve reload %q: %w", userID, err)
	}
	var pu persistedUser
	if json.Unmarshal(data, &pu) != nil {
		return nil // a half-written file: keep what we have rather than wipe it
	}
	m.applyPersistedUserLocked(userID, pu)
	return nil
}

// applyPersistedUserLocked refreshes one user's in-memory scripts from a parsed
// on-disk record, recompiling each source. The caller must hold scriptsMu.
func (m *Manager) applyPersistedUserLocked(userID string, pu persistedUser) {
	m.scripts[userID] = make(map[string]*StoredScript)
	for name, src := range pu.Scripts {
		script, cerr := m.CompileScript(src)
		if cerr != nil {
			continue // skip a script that no longer compiles
		}
		m.scripts[userID][name] = &StoredScript{Name: name, Source: src, Script: script}
	}
	delete(m.activeScripts, userID)
	if pu.Active != "" {
		if _, ok := m.scripts[userID][pu.Active]; ok {
			m.activeScripts[userID] = pu.Active
		}
	}
}

// load reads every persisted user file into memory, recompiling each source.
// Corrupt scripts or files are skipped rather than aborting startup.
func (m *Manager) load() error {
	entries, err := os.ReadDir(m.storageDir)
	if err != nil {
		return err
	}

	m.scriptsMu.Lock()
	defer m.scriptsMu.Unlock()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		userID := string(raw)

		data, err := os.ReadFile(filepath.Join(m.storageDir, e.Name()))
		if err != nil {
			continue
		}
		var pu persistedUser
		if json.Unmarshal(data, &pu) != nil {
			continue
		}

		if m.scripts[userID] == nil {
			m.scripts[userID] = make(map[string]*StoredScript)
		}
		for name, src := range pu.Scripts {
			script, cerr := m.CompileScript(src)
			if cerr != nil {
				continue // skip a script that no longer compiles
			}
			m.scripts[userID][name] = &StoredScript{Name: name, Source: src, Script: script}
		}
		if pu.Active != "" {
			if _, ok := m.scripts[userID][pu.Active]; ok {
				m.activeScripts[userID] = pu.Active
			}
		}
	}
	return nil
}
