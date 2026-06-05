package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/config"
	"gopkg.in/yaml.v3"
)

func writeConfig(t *testing.T, dir string, cfg config.Config) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func findCheck(checks []Check, name string) (Check, bool) {
	for _, c := range checks {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

// TestDoctor_PassingRun verifies a fully valid setup produces all-OK checks
// (except kubeconfig which may fail in CI — we only check profile and model).
func TestDoctor_PassingRun(t *testing.T) {
	dir := t.TempDir()

	// Write valid profile.
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		GeneratedAt:   time.Now(),
		Contexts: []config.ContextProfile{
			{Name: "ctx", Reachable: true},
		},
	}
	profData, _ := yaml.Marshal(p)
	profPath := filepath.Join(dir, "profile.yaml")
	os.WriteFile(profPath, profData, 0600)

	// Write config pointing to an env var we set below.
	cfg := config.Default()
	cfg.DefaultModel = "claude-3-5-sonnet-20241022"
	cfg.Providers = map[string]config.ProviderConfig{
		"anthropic": {APIKeyEnv: "TEST_ANTHROPIC_KEY"},
	}
	cfgPath := writeConfig(t, dir, cfg)

	// Set the API key env var.
	t.Setenv("TEST_ANTHROPIC_KEY", "sk-test-1234")

	opts := Options{
		ConfigPath:  cfgPath,
		ProfilePath: profPath,
		ProfileTTL:  7 * 24 * time.Hour,
	}

	checks, err := Doctor(context.Background(), opts)
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	if len(checks) == 0 {
		t.Fatal("expected at least one check")
	}

	// Profile check must pass.
	if c, ok := findCheck(checks, "profile valid"); ok {
		if !c.OK {
			t.Errorf("profile valid check failed: %s", c.Detail)
		}
	} else {
		t.Error("missing 'profile valid' check")
	}

	// Model API key check must pass.
	if c, ok := findCheck(checks, "default model has API key in env"); ok {
		if !c.OK {
			t.Errorf("model API key check failed: %s", c.Detail)
		}
	} else {
		t.Error("missing 'default model has API key in env' check")
	}

	// Reachability check: we wrote a reachable context, should pass.
	if c, ok := findCheck(checks, "active context reachable"); ok {
		if !c.OK {
			t.Errorf("reachability check failed: %s", c.Detail)
		}
	} else {
		t.Error("missing 'active context reachable' check")
	}
}

// TestDoctor_FailingRun verifies a broken setup produces failing checks.
func TestDoctor_FailingRun(t *testing.T) {
	dir := t.TempDir()

	// No profile file, no config — everything fails.
	opts := Options{
		ConfigPath:  filepath.Join(dir, "config.yaml"),
		ProfilePath: filepath.Join(dir, "profile.yaml"),
		ProfileTTL:  7 * 24 * time.Hour,
	}

	// Ensure API key env var is unset.
	t.Setenv("ANTHROPIC_API_KEY", "")

	checks, err := Doctor(context.Background(), opts)
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}

	// Profile check must fail.
	if c, ok := findCheck(checks, "profile valid"); ok {
		if c.OK {
			t.Error("expected profile valid to fail with missing profile")
		}
	} else {
		t.Error("missing 'profile valid' check")
	}

	// Reachability check must fail (no profile).
	if c, ok := findCheck(checks, "active context reachable"); ok {
		if c.OK {
			t.Error("expected reachability check to fail without profile")
		}
	} else {
		t.Error("missing 'active context reachable' check")
	}
}

// TestDoctor_MissingAPIKey verifies API key check fails when env var absent.
func TestDoctor_MissingAPIKey(t *testing.T) {
	dir := t.TempDir()

	// Write a valid profile so other checks pass.
	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		GeneratedAt:   time.Now(),
		Contexts:      []config.ContextProfile{{Name: "ctx", Reachable: true}},
	}
	profData, _ := yaml.Marshal(p)
	profPath := filepath.Join(dir, "profile.yaml")
	os.WriteFile(profPath, profData, 0600)

	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"anthropic": {APIKeyEnv: "TEST_MISSING_KEY_12345"},
	}
	cfgPath := writeConfig(t, dir, cfg)

	// Ensure the env var is NOT set.
	os.Unsetenv("TEST_MISSING_KEY_12345")

	opts := Options{
		ConfigPath:  cfgPath,
		ProfilePath: profPath,
		ProfileTTL:  7 * 24 * time.Hour,
	}

	checks, err := Doctor(context.Background(), opts)
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}

	if c, ok := findCheck(checks, "default model has API key in env"); ok {
		if c.OK {
			t.Error("expected API key check to fail when env var is unset")
		}
	} else {
		t.Error("missing 'default model has API key in env' check")
	}
}

func TestGuessProvider_CodexAndMoonshot(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"codex/gpt-5.5", "codex"},
		{"codex/gpt-5.3-codex-spark", "codex"},
		{"kimi-k2.6", "moonshot"},
		{"moonshot-v1-128k", "moonshot"},
	}
	for _, tc := range cases {
		if got := guessProvider(tc.model); got != tc.want {
			t.Errorf("guessProvider(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestWellKnownEnvVar_CodexAndMoonshot(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"codex", "CODEX_API_KEY"},
		{"moonshot", "MOONSHOT_API_KEY"},
	}
	for _, tc := range cases {
		if got := wellKnownEnvVar(tc.provider); got != tc.want {
			t.Errorf("wellKnownEnvVar(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}
