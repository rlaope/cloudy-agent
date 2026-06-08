package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/config"
)

func TestProfile_SaveAndLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yaml")

	p := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		GeneratedAt:   time.Now().UTC().Truncate(time.Second),
		Contexts: []config.ContextProfile{
			{
				Name:                "prod",
				Reachable:           true,
				K8sVersion:          "v1.28.5",
				NodeCount:           10,
				GPUNodeCount:        2,
				Namespaces:          []string{"default", "kube-system"},
				PodSampleCount:      2000,
				PodSampleIncomplete: true,
				RuntimePodCounts:    map[string]int{"go": 4, "node": 3, "ruby": 1},
				HasPrometheus:       true,
				FrontendPodCount:    3,
				IngressHostCount:    2,
				HasFrontendSurface:  true,
			},
		},
		RecommendedSkills: []string{"k8s-incident", "gpu-saturation"},
	}

	if err := config.SaveProfile(path, p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	loaded, err := config.LoadProfile(path)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}

	if loaded.SchemaVersion != p.SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", loaded.SchemaVersion, p.SchemaVersion)
	}
	if !loaded.GeneratedAt.Equal(p.GeneratedAt) {
		t.Errorf("GeneratedAt: got %v, want %v", loaded.GeneratedAt, p.GeneratedAt)
	}
	if len(loaded.Contexts) != 1 {
		t.Fatalf("Contexts len: got %d, want 1", len(loaded.Contexts))
	}
	if loaded.Contexts[0].Name != "prod" {
		t.Errorf("Context.Name: got %q, want prod", loaded.Contexts[0].Name)
	}
	if loaded.Contexts[0].K8sVersion != "v1.28.5" {
		t.Errorf("K8sVersion: got %q, want v1.28.5", loaded.Contexts[0].K8sVersion)
	}
	if loaded.Contexts[0].PodSampleCount != 2000 {
		t.Errorf("PodSampleCount: got %d, want 2000", loaded.Contexts[0].PodSampleCount)
	}
	if !loaded.Contexts[0].PodSampleIncomplete {
		t.Error("PodSampleIncomplete: got false, want true")
	}
	if loaded.Contexts[0].FrontendPodCount != 3 {
		t.Errorf("FrontendPodCount: got %d, want 3", loaded.Contexts[0].FrontendPodCount)
	}
	if got := loaded.Contexts[0].RuntimePodCounts["go"]; got != 4 {
		t.Errorf("RuntimePodCounts[go]: got %d, want 4", got)
	}
	if got := loaded.Contexts[0].RuntimePodCounts["node"]; got != 3 {
		t.Errorf("RuntimePodCounts[node]: got %d, want 3", got)
	}
	if loaded.Contexts[0].IngressHostCount != 2 {
		t.Errorf("IngressHostCount: got %d, want 2", loaded.Contexts[0].IngressHostCount)
	}
	if !loaded.Contexts[0].HasFrontendSurface {
		t.Error("HasFrontendSurface: got false, want true")
	}
	if len(loaded.RecommendedSkills) != 2 {
		t.Errorf("RecommendedSkills len: got %d, want 2", len(loaded.RecommendedSkills))
	}
}

func TestProfile_LoadMissing_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-profile.yaml")

	p, err := config.LoadProfile(path)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if p.SchemaVersion != 0 {
		t.Errorf("expected zero SchemaVersion, got %d", p.SchemaVersion)
	}
}

func TestProfile_IsValid(t *testing.T) {
	good := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion,
		Contexts:      []config.ContextProfile{{Name: "ctx1"}},
	}
	if !good.IsValid() {
		t.Error("expected IsValid() = true for valid profile")
	}

	noCtx := config.Profile{SchemaVersion: config.CurrentSchemaVersion}
	if noCtx.IsValid() {
		t.Error("expected IsValid() = false when no contexts")
	}

	wrongVersion := config.Profile{
		SchemaVersion: config.CurrentSchemaVersion + 1,
		Contexts:      []config.ContextProfile{{Name: "ctx1"}},
	}
	if wrongVersion.IsValid() {
		t.Error("expected IsValid() = false for wrong schema version")
	}
}

func TestProfile_Expired(t *testing.T) {
	ttl := time.Hour

	fresh := config.Profile{GeneratedAt: time.Now().Add(-30 * time.Minute)}
	if fresh.Expired(ttl) {
		t.Error("fresh profile should not be expired")
	}

	old := config.Profile{GeneratedAt: time.Now().Add(-2 * time.Hour)}
	if !old.Expired(ttl) {
		t.Error("old profile should be expired")
	}

	zero := config.Profile{}
	if !zero.Expired(ttl) {
		t.Error("zero GeneratedAt should always be expired")
	}
}

func TestProfilePath_XDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	p := config.ProfilePath()
	want := filepath.Join(dir, "cloudy", "profile.yaml")
	if p != want {
		t.Errorf("ProfilePath with XDG: got %q, want %q", p, want)
	}
}
