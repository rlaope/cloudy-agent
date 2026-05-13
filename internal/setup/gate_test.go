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

func writeProfile(t *testing.T, dir string, p config.Profile) string {
	t.Helper()
	path := filepath.Join(dir, "profile.yaml")
	data, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return path
}

func validProfile() config.Profile {
	return config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		GeneratedAt:   time.Now(),
		Contexts: []config.ContextProfile{
			{Name: "prod", Reachable: true},
		},
	}
}

// TestGate_MissingProfile: no profile file → StateNeedsSetup.
func TestGate_MissingProfile(t *testing.T) {
	dir := t.TempDir()
	opts := Options{ProfilePath: filepath.Join(dir, "profile.yaml")}

	res, err := EnsureReady(context.Background(), ModeTUI, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.State != StateNeedsSetup {
		t.Errorf("expected StateNeedsSetup, got %v", res.State)
	}
	if len(res.Reasons) == 0 {
		t.Error("expected at least one reason")
	}
}

// TestGate_ExpiredProfile: stale profile → StateNeedsSetup.
func TestGate_ExpiredProfile(t *testing.T) {
	dir := t.TempDir()
	p := validProfile()
	p.GeneratedAt = time.Now().Add(-30 * 24 * time.Hour) // 30 days old
	path := writeProfile(t, dir, p)

	opts := Options{ProfilePath: path, ProfileTTL: 7 * 24 * time.Hour}
	res, err := EnsureReady(context.Background(), ModeTUI, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.State != StateNeedsSetup {
		t.Errorf("expected StateNeedsSetup for expired profile, got %v", res.State)
	}
}

// TestGate_WrongSchemaVersion: old schema → StateNeedsSetup.
func TestGate_WrongSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	p := validProfile()
	p.SchemaVersion = 0 // outdated
	path := writeProfile(t, dir, p)

	opts := Options{ProfilePath: path}
	res, err := EnsureReady(context.Background(), ModeTUI, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.State != StateNeedsSetup {
		t.Errorf("expected StateNeedsSetup for wrong schema, got %v", res.State)
	}
}

// TestGate_ValidProfile: fresh valid profile → StateReady.
func TestGate_ValidProfile(t *testing.T) {
	dir := t.TempDir()
	path := writeProfile(t, dir, validProfile())

	opts := Options{ProfilePath: path, ProfileTTL: 7 * 24 * time.Hour}
	res, err := EnsureReady(context.Background(), ModeTUI, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.State != StateReady {
		t.Errorf("expected StateReady, got %v (reasons: %v)", res.State, res.Reasons)
	}
}

// TestGate_MissingPrometheus: valid profile but prometheus required and absent → StatePartial.
func TestGate_MissingPrometheus(t *testing.T) {
	dir := t.TempDir()
	p := validProfile()
	p.Contexts[0].HasPrometheus = false
	path := writeProfile(t, dir, p)

	opts := Options{
		ProfilePath:        path,
		ProfileTTL:         7 * 24 * time.Hour,
		RequiredComponents: []string{"prometheus"},
	}
	res, err := EnsureReady(context.Background(), ModeTUI, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.State != StatePartial {
		t.Errorf("expected StatePartial, got %v", res.State)
	}
	if len(res.Disabled) == 0 {
		t.Error("expected at least one disabled tool")
	}
	found := false
	for _, d := range res.Disabled {
		if d == "prom-explorer" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected prom-explorer in disabled list, got %v", res.Disabled)
	}
}

// TestGate_PrometheusPresent: valid profile with prometheus and required → StateReady.
func TestGate_PrometheusPresent(t *testing.T) {
	dir := t.TempDir()
	p := validProfile()
	p.Contexts[0].HasPrometheus = true
	path := writeProfile(t, dir, p)

	opts := Options{
		ProfilePath:        path,
		ProfileTTL:         7 * 24 * time.Hour,
		RequiredComponents: []string{"prometheus"},
	}
	res, err := EnsureReady(context.Background(), ModeTUI, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.State != StateReady {
		t.Errorf("expected StateReady, got %v", res.State)
	}
}
