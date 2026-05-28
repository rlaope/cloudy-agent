package change

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// fakeSource is a ChangeSource returning canned events or an error.
type fakeSource struct {
	name   string
	events []ChangeEvent
	err    error
}

func (f fakeSource) Name() string { return f.name }

func (f fakeSource) RecentChanges(_ context.Context, _ ChangeQuery) ([]ChangeEvent, error) {
	return f.events, f.err
}

func runRecent(t *testing.T, tool tools.Tool, args map[string]any) (tools.Observation, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Run(context.Background(), json.RawMessage(raw))
}

// TestRecent_PartialFailure: one source errors, the other succeeds. The tool
// must NOT return an error — it returns the working source's events plus a note
// naming the failed source.
func TestRecent_PartialFailure(t *testing.T) {
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	good := fakeSource{name: "docker", events: []ChangeEvent{
		{Time: base, Kind: "image", Target: "app", Summary: "running", Source: "docker"},
	}}
	bad := fakeSource{name: "k8s", err: errors.New("no kubeconfig")}

	tool := NewRecentTool(bad, good)
	obs, err := runRecent(t, tool, map[string]any{"workload": "app"})
	if err != nil {
		t.Fatalf("partial failure must not error: %v", err)
	}
	if !strings.Contains(obs.Text, "app") || !strings.Contains(obs.Text, "image") {
		t.Errorf("expected the working source's event in output, got:\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "note:") || !strings.Contains(obs.Text, "k8s") {
		t.Errorf("expected a failure note naming k8s, got:\n%s", obs.Text)
	}
}

// TestRecent_AllSourcesFail: every source errors → the tool returns an error.
func TestRecent_AllSourcesFail(t *testing.T) {
	a := fakeSource{name: "k8s", err: errors.New("boom-k8s")}
	b := fakeSource{name: "docker", err: errors.New("boom-docker")}
	tool := NewRecentTool(a, b)
	_, err := runRecent(t, tool, map[string]any{"workload": "app"})
	if err == nil {
		t.Fatal("expected an error when all sources fail")
	}
	if !strings.Contains(err.Error(), "k8s") || !strings.Contains(err.Error(), "docker") {
		t.Errorf("error should name both failed sources, got: %v", err)
	}
}

// TestRecent_WorkloadRequired: missing workload yields a guidance observation,
// not an error.
func TestRecent_WorkloadRequired(t *testing.T) {
	tool := NewRecentTool(fakeSource{name: "docker"})
	obs, err := runRecent(t, tool, map[string]any{"limit": 5})
	if err != nil {
		t.Fatalf("missing workload should not error: %v", err)
	}
	if !strings.Contains(obs.Text, "workload is required") {
		t.Errorf("expected 'workload is required', got: %s", obs.Text)
	}
}

// TestRecent_NoSources: with no sources configured the tool reports that
// rather than erroring.
func TestRecent_NoSources(t *testing.T) {
	tool := NewRecentTool()
	obs, err := runRecent(t, tool, map[string]any{"workload": "app"})
	if err != nil {
		t.Fatalf("no sources should not error: %v", err)
	}
	if !strings.Contains(obs.Text, "no change sources") {
		t.Errorf("expected 'no change sources' note, got: %s", obs.Text)
	}
}

// TestRecent_MergesNewestFirst: events from two sources are merged newest-first.
func TestRecent_MergesNewestFirst(t *testing.T) {
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	k8s := fakeSource{name: "k8s", events: []ChangeEvent{
		{Time: base.Add(-2 * time.Hour), Kind: "rollout", Target: "app", Source: "k8s"},
	}}
	docker := fakeSource{name: "docker", events: []ChangeEvent{
		{Time: base, Kind: "image", Target: "app", Source: "docker"},
	}}
	tool := NewRecentTool(k8s, docker)
	obs, err := runRecent(t, tool, map[string]any{"workload": "app"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(obs.Text), "\n")
	// First line is the header; the first event line should be the docker
	// "image" event (newest).
	if len(lines) < 2 || !strings.Contains(lines[1], "docker") || !strings.Contains(lines[1], "image") {
		t.Errorf("expected newest (docker/image) first, got:\n%s", obs.Text)
	}
}
