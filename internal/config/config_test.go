package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
)

func TestDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.Safety.AllowSecrets {
		t.Error("Default: AllowSecrets should be false")
	}
	if cfg.Safety.MaxLogLines != 5000 {
		t.Errorf("Default: MaxLogLines = %d, want 5000", cfg.Safety.MaxLogLines)
	}
	if cfg.Safety.MaxProfileSeconds != 60 {
		t.Errorf("Default: MaxProfileSeconds = %d, want 60", cfg.Safety.MaxProfileSeconds)
	}
	// DefaultModel is intentionally empty — see the package comment on
	// Default(). The /login conversation owns model selection now;
	// hard-coding a specific id here was a deprecation-time-bomb.
	if cfg.DefaultModel != "" {
		t.Errorf("Default: DefaultModel must be empty (model picked via /login), got %q", cfg.DefaultModel)
	}
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := config.Default()
	original.DefaultModel = "my-test-model"
	original.Safety.MaxLogLines = 1234
	original.Safety.AllowSecrets = true
	original.Routing.CheapToolStrongSummary = true

	if err := config.Save(path, original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file permissions.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file perm = %o, want 0600", fi.Mode().Perm())
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.DefaultModel != original.DefaultModel {
		t.Errorf("DefaultModel: got %q, want %q", loaded.DefaultModel, original.DefaultModel)
	}
	if loaded.Safety.MaxLogLines != original.Safety.MaxLogLines {
		t.Errorf("MaxLogLines: got %d, want %d", loaded.Safety.MaxLogLines, original.Safety.MaxLogLines)
	}
	if loaded.Safety.AllowSecrets != original.Safety.AllowSecrets {
		t.Errorf("AllowSecrets: got %v, want %v", loaded.Safety.AllowSecrets, original.Safety.AllowSecrets)
	}
	if loaded.Routing.CheapToolStrongSummary != original.Routing.CheapToolStrongSummary {
		t.Errorf("CheapToolStrongSummary: got %v, want %v", loaded.Routing.CheapToolStrongSummary, original.Routing.CheapToolStrongSummary)
	}
}

func TestLoad_MissingFile_ReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load of missing file returned error: %v", err)
	}
	def := config.Default()
	if cfg.DefaultModel != def.DefaultModel {
		t.Errorf("DefaultModel: got %q, want %q", cfg.DefaultModel, def.DefaultModel)
	}
	if cfg.Safety.MaxLogLines != def.Safety.MaxLogLines {
		t.Errorf("MaxLogLines: got %d, want %d", cfg.Safety.MaxLogLines, def.Safety.MaxLogLines)
	}
}

func TestLoad_PartialYAML_OverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.yaml")

	// Only override one field; the rest should stay at defaults.
	partial := "default_model: partial-override-model\n"
	if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DefaultModel != "partial-override-model" {
		t.Errorf("DefaultModel: got %q, want %q", cfg.DefaultModel, "partial-override-model")
	}
	// Safety defaults must be preserved.
	if cfg.Safety.MaxLogLines != config.Default().Safety.MaxLogLines {
		t.Errorf("MaxLogLines defaulted to %d, want %d", cfg.Safety.MaxLogLines, config.Default().Safety.MaxLogLines)
	}
}

func TestPath_XDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	p := config.Path()
	want := filepath.Join(dir, "cloudy", "config.yaml")
	if p != want {
		t.Errorf("Path with XDG: got %q, want %q", p, want)
	}
}

func TestPath_Default(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	p := config.Path()
	if filepath.Base(p) != "config.yaml" {
		t.Errorf("Path base: got %q, want config.yaml", filepath.Base(p))
	}
}
