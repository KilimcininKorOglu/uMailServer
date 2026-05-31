package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SaveAtomic marshals the configuration to YAML and writes it to path
// atomically: it writes to a temporary file in the same directory, fsyncs it,
// then renames it over the destination. This guarantees a reader never observes
// a truncated or partially written config file. The file is written with 0o600
// permissions because it may contain secrets.
//
// Note: marshaling the struct discards comments and key ordering from the
// original file. This is acceptable for the live runtime config file.
func SaveAtomic(cfg *Config, path string) error {
	if path == "" {
		return fmt.Errorf("SaveAtomic: empty path")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("SaveAtomic: marshal: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".umailserver-config-*.tmp")
	if err != nil {
		return fmt.Errorf("SaveAtomic: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail out before the rename.
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName) //nolint:errcheck // best-effort cleanup
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close() //nolint:errcheck
		return fmt.Errorf("SaveAtomic: chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() //nolint:errcheck
		return fmt.Errorf("SaveAtomic: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close() //nolint:errcheck
		return fmt.Errorf("SaveAtomic: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("SaveAtomic: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("SaveAtomic: rename: %w", err)
	}
	return nil
}
