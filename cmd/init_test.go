package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/angerops/autoshelf/internal/config"
)

// Locks in the duplication contract: the YAML at the repo root must be
// character-identical to the constant the init command writes. If you edit
// one, edit the other. Tests run from the cmd/ directory, so ../ points at
// the repo root.
func TestSampleConfigMatchesRepoExample(t *testing.T) {
	onDisk, err := os.ReadFile(filepath.Join("..", "autoshelf.example.yaml"))
	if err != nil {
		t.Fatalf("reading repo example: %v", err)
	}
	if string(onDisk) != sampleConfig {
		t.Errorf("autoshelf.example.yaml is out of sync with cmd/init.go sampleConfig.\n" +
			"Update both so the file users see in the repo matches what `autoshelf init` writes.")
	}
}

// The embedded sample must itself be a valid config so `autoshelf init &&
// autoshelf validate` always succeeds out of the box.
func TestSampleConfigParsesAndValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoshelf.yaml")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("sample config failed to load: %v", err)
	}
	if len(cfg.Watches) == 0 {
		t.Errorf("sample config should declare at least one watch")
	}
}
