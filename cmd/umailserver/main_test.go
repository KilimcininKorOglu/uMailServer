package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCLIConfig(t *testing.T, content string) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "umailserver.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	return configPath
}

func TestGetDatabasePathUsesConfiguredPath(t *testing.T) {
	configPath := writeCLIConfig(t, `server:
  data_dir: /tmp/ignored

database:
  path: /tmp/configured.db
`)
	t.Setenv("UMAILSERVER_CONFIG", configPath)

	if path := getDatabasePath(); path != "/tmp/configured.db" {
		t.Fatalf("expected configured database path, got %s", path)
	}
}

func TestGetDatabasePathFallsBackToDataDir(t *testing.T) {
	configPath := writeCLIConfig(t, `server:
  data_dir: /tmp/umail-cli
`)
	t.Setenv("UMAILSERVER_CONFIG", configPath)

	if path := getDatabasePath(); path != "/tmp/umail-cli/umailserver.db" {
		t.Fatalf("expected fallback database path, got %s", path)
	}
}
