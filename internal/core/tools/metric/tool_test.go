package metric

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	"github.com/rlaope/cloudy/internal/core/tools"
)

// mockDockerAPI implements dockerclient.ReadOnlyAPI with canned responses.
// statsErr keyed by container ID forces a per-container stats failure.
type mockDockerAPI struct {
	summaries []container.Summary
	stats     map[string]container.StatsResponse
	statsErr  map[string]error
}

func (m *mockDockerAPI) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	return m.summaries, nil
}

func (m *mockDockerAPI) ContainerInspect(_ context.Context, _ string) (container.InspectResponse, error) {
	return container.InspectResponse{}, nil
}

func (m *mockDockerAPI) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	return nil, nil
}

func (m *mockDockerAPI) ContainerStats(_ context.Context, id string) (container.StatsResponse, error) {
	if err := m.statsErr[id]; err != nil {
		return container.StatsResponse{}, err
	}
	return m.stats[id], nil
}

func (m *mockDockerAPI) ContainerLogs(_ context.Context, _ string, _ container.LogsOptions) (io.ReadCloser, error) {
	return nil, nil
}

// mockHub satisfies the metric package's hubGetter seam without a daemon.
type mockHub struct {
	api dockerclient.ReadOnlyAPI
	err error
}

func (h mockHub) Get(_ string) (dockerclient.ReadOnlyAPI, error) {
	return h.api, h.err
}

func newTool(hub hubGetter) tools.Tool { return &containerStatsTool{hub: hub} }

func run(t *testing.T, tool tools.Tool, args map[string]any) (tools.Observation, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Run(context.Background(), json.RawMessage(raw))
}

func cannedStats(total, preTotal, system, preSystem uint64, online uint32, mem, limit uint64) container.StatsResponse {
	s := statsWithCPU(total, preTotal, system, preSystem, online)
	s.MemoryStats.Usage = mem
	s.MemoryStats.Limit = limit
	return s
}

func TestContainerStats_RendersMatched(t *testing.T) {
	api := &mockDockerAPI{
		summaries: []container.Summary{
			{ID: "id-web", Names: []string{"/web"}, Labels: map[string]string{"com.docker.compose.service": "web"}},
			{ID: "id-db", Names: []string{"/db"}, Labels: map[string]string{"com.docker.compose.service": "db"}},
		},
		stats: map[string]container.StatsResponse{
			"id-web": cannedStats(300, 100, 2000, 1000, 4, 150, 1000), // CPU 80%, mem 15%
		},
	}
	tool := newTool(mockHub{api: api})

	obs, err := run(t, tool, map[string]any{"workload": "web"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "1 container(s) for \"web\"") {
		t.Errorf("header missing: %s", obs.Text)
	}
	if !strings.Contains(obs.Text, "web | CPU 80.00%") {
		t.Errorf("expected web CPU line, got:\n%s", obs.Text)
	}
	if strings.Contains(obs.Text, "db") {
		t.Errorf("db must not appear (only web requested):\n%s", obs.Text)
	}
}

// TestContainerStats_ExactMatchNoSubstring is the headline regression: "api"
// must not match "api-gateway" (no substring false positive).
func TestContainerStats_ExactMatchNoSubstring(t *testing.T) {
	api := &mockDockerAPI{
		summaries: []container.Summary{
			{ID: "id-gw", Names: []string{"/api-gateway"}, Labels: map[string]string{"com.docker.compose.service": "api-gateway"}},
		},
		stats: map[string]container.StatsResponse{
			"id-gw": cannedStats(300, 100, 2000, 1000, 1, 10, 100),
		},
	}
	tool := newTool(mockHub{api: api})

	obs, err := run(t, tool, map[string]any{"workload": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "0 container(s) for \"api\"") {
		t.Errorf("substring 'api' must NOT match 'api-gateway'; got:\n%s", obs.Text)
	}
}

// TestContainerStats_TolerantOfPerContainerError: one matched container's stats
// call fails; the tool still renders the working one and notes the failure.
func TestContainerStats_TolerantOfPerContainerError(t *testing.T) {
	api := &mockDockerAPI{
		summaries: []container.Summary{
			{ID: "id-1", Names: []string{"/web-1"}, Labels: map[string]string{"com.docker.compose.service": "web"}},
			{ID: "id-2", Names: []string{"/web-2"}, Labels: map[string]string{"com.docker.compose.service": "web"}},
		},
		stats: map[string]container.StatsResponse{
			"id-1": cannedStats(300, 100, 2000, 1000, 2, 10, 100),
		},
		statsErr: map[string]error{
			"id-2": errors.New("container gone"),
		},
	}
	tool := newTool(mockHub{api: api})

	obs, err := run(t, tool, map[string]any{"workload": "web"})
	if err != nil {
		t.Fatalf("per-container stats error must not fail the tool: %v", err)
	}
	if !strings.Contains(obs.Text, "web-1 | CPU") {
		t.Errorf("expected working container web-1 rendered:\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "note:") || !strings.Contains(obs.Text, "web-2") {
		t.Errorf("expected failure note naming web-2:\n%s", obs.Text)
	}
}

func TestContainerStats_WorkloadRequired(t *testing.T) {
	tool := newTool(mockHub{api: &mockDockerAPI{}})
	obs, err := run(t, tool, map[string]any{})
	if err != nil {
		t.Fatalf("missing workload should not error: %v", err)
	}
	if !strings.Contains(obs.Text, "workload is required") {
		t.Errorf("expected guidance, got: %s", obs.Text)
	}
}

func TestContainerStats_HubGetError(t *testing.T) {
	tool := newTool(mockHub{err: errors.New("no docker hosts configured")})
	_, err := run(t, tool, map[string]any{"workload": "web"})
	if err == nil {
		t.Fatal("expected error when hub.Get fails")
	}
	if !strings.Contains(err.Error(), "no docker hosts configured") {
		t.Errorf("error should surface the hub failure, got: %v", err)
	}
}

func TestContainerStats_Risk(t *testing.T) {
	tool := newTool(mockHub{api: &mockDockerAPI{}})
	rr, ok := tool.(tools.RiskRated)
	if !ok {
		t.Fatal("metric.container_stats must implement RiskRated")
	}
	if rr.Risk() != tools.RiskLow {
		t.Errorf("Risk = %v, want RiskLow", rr.Risk())
	}
}

func TestContainerStats_NameAndSchema(t *testing.T) {
	tool := newTool(mockHub{api: &mockDockerAPI{}})
	if tool.Name() != "metric.container_stats" {
		t.Errorf("Name = %q", tool.Name())
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	req, _ := schema["required"].([]any)
	if len(req) != 1 || req[0] != "workload" {
		t.Errorf("required = %v, want [workload]", req)
	}
}
