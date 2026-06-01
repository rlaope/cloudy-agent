package correlate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// fakeSource is a change.ChangeSource returning canned events or an error.
type fakeSource struct {
	name   string
	events []change.ChangeEvent
	err    error
}

func (f fakeSource) Name() string { return f.name }

func (f fakeSource) RecentChanges(_ context.Context, _ change.ChangeQuery) ([]change.ChangeEvent, error) {
	return f.events, f.err
}

func runCorrelate(t *testing.T, tool tools.Tool, args map[string]any) (tools.Observation, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Run(context.Background(), json.RawMessage(raw))
}

// TestCorrelate_MergesNewestFirst: events from several sources are merged into
// one newest-first timeline.
func TestCorrelate_MergesNewestFirst(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	k8s := fakeSource{name: "k8s", events: []change.ChangeEvent{
		{Time: base.Add(-2 * time.Hour), Kind: "rollout", Target: "app", Source: "k8s"},
	}}
	docker := fakeSource{name: "docker", events: []change.ChangeEvent{
		{Time: base.Add(-1 * time.Hour), Kind: "container_restart", Target: "app", Source: "docker"},
	}}
	argo := fakeSource{name: "argo", events: []change.ChangeEvent{
		{Time: base, Kind: "sync", Target: "app", After: "abc", Source: "argo"},
	}}
	tool := newCorrelateTool(k8s, docker, argo)
	obs, err := runCorrelate(t, tool, map[string]any{"workload": "app", "since": "24h"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(obs.Text), "\n")
	// Line 0 is the header; the first event line should be the argo sync (newest).
	if len(lines) < 2 || !strings.Contains(lines[1], "argo") || !strings.Contains(lines[1], "sync") {
		t.Errorf("expected newest (argo/sync) first, got:\n%s", obs.Text)
	}
}

// TestCorrelate_SymptomRendersAndAligns: a symptom-kind event from a symptom
// source renders on the unified timeline, and candidate-cause v2 names the
// change that preceded the symptom (not merely the newest change).
func TestCorrelate_SymptomRendersAndAligns(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	changeSrc := fakeSource{name: "k8s", events: []change.ChangeEvent{
		{Time: base.Add(-30 * time.Minute), Kind: "image", Target: "app", Before: "v1", After: "v2", Source: "k8s"},
	}}
	symptomSrc := fakeSource{name: "metric", events: []change.ChangeEvent{
		{Time: base, Kind: "metric_breach", Target: "app", Summary: "error rate > 0.2", Source: "metric"},
	}}
	tool := newCorrelateTool(changeSrc, symptomSrc)
	obs, err := runCorrelate(t, tool, map[string]any{"workload": "app", "since": "24h"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Symptom line is visible on the timeline.
	if !strings.Contains(obs.Text, "metric_breach") || !strings.Contains(obs.Text, "error rate > 0.2") {
		t.Errorf("expected metric_breach symptom line on the timeline, got:\n%s", obs.Text)
	}
	// Ranked candidate causes name the image change and annotate the delta
	// before the symptom.
	if !strings.Contains(obs.Text, "candidate causes for symptom") || !strings.Contains(obs.Text, "image") || !strings.Contains(obs.Text, "before symptom") {
		t.Errorf("expected ranked candidate cause aligning the image change before the symptom, got:\n%s", obs.Text)
	}
}

// TestCorrelate_CandidateCauseSkipsEvent: the candidate cause is the newest
// state-altering event, skipping "event" kinds even when they are newer.
func TestCorrelate_CandidateCauseSkipsEvent(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	src := fakeSource{name: "k8s", events: []change.ChangeEvent{
		{Time: base, Kind: "event", Target: "app", Summary: "BackOff", Source: "k8s"},
		{Time: base.Add(-1 * time.Hour), Kind: "image", Target: "app", Before: "v1", After: "v2", Source: "k8s"},
		{Time: base.Add(-2 * time.Hour), Kind: "scale", Target: "app", Source: "k8s"},
	}}
	tool := newCorrelateTool(src)
	obs, err := runCorrelate(t, tool, map[string]any{"workload": "app", "since": "24h"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The image leads the ranking; the bare "event" kind is never a candidate.
	if !strings.Contains(obs.Text, "k8s image on app") {
		t.Errorf("expected the image change to lead the ranking (skipping the newer event), got:\n%s", obs.Text)
	}
	if strings.Contains(obs.Text, "k8s event on app") {
		t.Errorf("candidate cause must not include an 'event' kind, got:\n%s", obs.Text)
	}
}

// TestCorrelate_CandidateCausePicksSync: an Argo sync qualifies as a candidate
// cause and the newest qualifying event wins.
func TestCorrelate_CandidateCausePicksSync(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	src := fakeSource{name: "merged", events: []change.ChangeEvent{
		{Time: base, Kind: "sync", Target: "app", After: "deadbeef", Source: "argo"},
		{Time: base.Add(-3 * time.Hour), Kind: "rollout", Target: "app", Source: "k8s"},
	}}
	tool := newCorrelateTool(src)
	obs, err := runCorrelate(t, tool, map[string]any{"workload": "app", "since": "24h"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "argo sync on app") {
		t.Errorf("expected the argo sync to lead the ranking, got:\n%s", obs.Text)
	}
}

// TestCorrelate_CloudAuditIsRankedCause: a cloud control-plane audit event
// (Kind "cloud_audit", as emitted by cloud.NewAuditChangeSource) folded onto the
// timeline is ranked as a candidate cause for a preceding-aligned symptom — the
// end-to-end check that the audit source, once wired in, surfaces through Run.
func TestCorrelate_CloudAuditIsRankedCause(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	audit := fakeSource{name: "cloud_audit", events: []change.ChangeEvent{
		{Time: base.Add(-5 * time.Minute), Kind: "cloud_audit", Target: "app", Summary: "ModifyDBInstance by ops", Source: "cloud_audit_aws"},
	}}
	symptom := fakeSource{name: "metric", events: []change.ChangeEvent{
		{Time: base, Kind: "metric_breach", Target: "app", Summary: "errors up", Source: "metric"},
	}}
	tool := newCorrelateTool(audit, symptom)
	obs, err := runCorrelate(t, tool, map[string]any{"workload": "app", "since": "24h"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "candidate causes for symptom") {
		t.Fatalf("expected a ranked candidate-cause block, got:\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "cloud_audit_aws cloud_audit on app") || !strings.Contains(obs.Text, "ModifyDBInstance by ops") {
		t.Errorf("expected the cloud_audit event ranked as a candidate cause, got:\n%s", obs.Text)
	}
}

// TestCorrelate_CandidateCauseNone: with only "event" kinds in the window there
// is no state-altering change to blame.
func TestCorrelate_CandidateCauseNone(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	src := fakeSource{name: "k8s", events: []change.ChangeEvent{
		{Time: base, Kind: "event", Target: "app", Summary: "Unhealthy", Source: "k8s"},
	}}
	tool := newCorrelateTool(src)
	obs, err := runCorrelate(t, tool, map[string]any{"workload": "app"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "candidate cause: none") {
		t.Errorf("expected 'candidate cause: none', got:\n%s", obs.Text)
	}
}

// TestCorrelate_PartialFailure: one source errors, the others succeed. The tool
// must NOT error — it returns the working sources' events plus a note.
func TestCorrelate_PartialFailure(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	good := fakeSource{name: "docker", events: []change.ChangeEvent{
		{Time: base, Kind: "image", Target: "app", Source: "docker"},
	}}
	bad := fakeSource{name: "argo", err: errors.New("argo unreachable")}
	tool := newCorrelateTool(bad, good)
	obs, err := runCorrelate(t, tool, map[string]any{"workload": "app"})
	if err != nil {
		t.Fatalf("partial failure must not error: %v", err)
	}
	if !strings.Contains(obs.Text, "image") {
		t.Errorf("expected the working source's event, got:\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "note:") || !strings.Contains(obs.Text, "argo") {
		t.Errorf("expected a failure note naming argo, got:\n%s", obs.Text)
	}
}

// TestCorrelate_AllSourcesFail: every source errors → the tool returns an error
// naming each failed source.
func TestCorrelate_AllSourcesFail(t *testing.T) {
	a := fakeSource{name: "k8s", err: errors.New("boom-k8s")}
	b := fakeSource{name: "argo", err: errors.New("boom-argo")}
	tool := newCorrelateTool(a, b)
	_, err := runCorrelate(t, tool, map[string]any{"workload": "app"})
	if err == nil {
		t.Fatal("expected an error when all sources fail")
	}
	if !strings.Contains(err.Error(), "k8s") || !strings.Contains(err.Error(), "argo") {
		t.Errorf("error should name both failed sources, got: %v", err)
	}
}

// TestCorrelate_WorkloadRequired: missing workload yields a guidance
// observation, not an error.
func TestCorrelate_WorkloadRequired(t *testing.T) {
	tool := newCorrelateTool(fakeSource{name: "docker"})
	obs, err := runCorrelate(t, tool, map[string]any{"limit": 5})
	if err != nil {
		t.Fatalf("missing workload should not error: %v", err)
	}
	if !strings.Contains(obs.Text, "workload is required") {
		t.Errorf("expected 'workload is required', got: %s", obs.Text)
	}
}

// TestCorrelate_RiskLow pins the RiskRated contract.
func TestCorrelate_RiskLow(t *testing.T) {
	tool := newCorrelateTool(fakeSource{name: "k8s"})
	if got := tools.RiskOf(tool); got != tools.RiskLow {
		t.Errorf("RiskOf = %v, want RiskLow", got)
	}
}
