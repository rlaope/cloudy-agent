package dockerlog

import (
	"bytes"
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

// mockDockerAPI implements the extended dockerclient.ReadOnlyAPI with canned
// responses. logs is keyed by container ID and holds a multiplexed log buffer;
// logsErr forces a per-container ContainerLogs failure.
type mockDockerAPI struct {
	summaries []container.Summary
	logs      map[string][]byte
	logsErr   map[string]error
	tty       map[string]bool // container ID -> Config.Tty
}

func (m *mockDockerAPI) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	return m.summaries, nil
}

func (m *mockDockerAPI) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{},
		Config:            &container.Config{Tty: m.tty[id]},
	}, nil
}

func (m *mockDockerAPI) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	return nil, nil
}

func (m *mockDockerAPI) ContainerStats(_ context.Context, _ string) (container.StatsResponse, error) {
	return container.StatsResponse{}, nil
}

func (m *mockDockerAPI) ContainerLogs(_ context.Context, id string, _ container.LogsOptions) (io.ReadCloser, error) {
	if err := m.logsErr[id]; err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(m.logs[id])), nil
}

// mockHub satisfies the package's hubGetter seam without a daemon.
type mockHub struct {
	api dockerclient.ReadOnlyAPI
	err error
}

func (h mockHub) Get(_ string) (dockerclient.ReadOnlyAPI, error) { return h.api, h.err }

func newTool(hub hubGetter) tools.Tool { return &containerLogsTool{hub: hub} }

func run(t *testing.T, tool tools.Tool, args map[string]any) (tools.Observation, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Run(context.Background(), json.RawMessage(raw))
}

func TestContainerLogs_RendersMatchedWithErrorSummary(t *testing.T) {
	api := &mockDockerAPI{
		summaries: []container.Summary{
			{ID: "id-web", Names: []string{"/web"}, Labels: map[string]string{"com.docker.compose.service": "web"}},
			{ID: "id-db", Names: []string{"/db"}, Labels: map[string]string{"com.docker.compose.service": "db"}},
		},
		logs: map[string][]byte{
			"id-web": muxFrame(t, "started ok\nserving requests\n", "ERROR connect refused\n"),
		},
	}
	tool := newTool(mockHub{api: api})

	obs, err := run(t, tool, map[string]any{"workload": "web"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "1 container(s) for \"web\"") {
		t.Errorf("header missing:\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "--- web (3 line(s), 1 error line(s)) ---") {
		t.Errorf("expected web block with 3 lines / 1 error, got:\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "ERROR connect refused") {
		t.Errorf("expected stderr line rendered:\n%s", obs.Text)
	}
	if strings.Contains(obs.Text, "db") {
		t.Errorf("db must not appear (only web requested):\n%s", obs.Text)
	}
}

// TestContainerLogs_ExactMatchNoSubstring is the headline regression: "api"
// must not match "api-gateway".
func TestContainerLogs_ExactMatchNoSubstring(t *testing.T) {
	api := &mockDockerAPI{
		summaries: []container.Summary{
			{ID: "id-gw", Names: []string{"/api-gateway"}, Labels: map[string]string{"com.docker.compose.service": "api-gateway"}},
		},
		logs: map[string][]byte{
			"id-gw": muxFrame(t, "hello\n", ""),
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

// TestContainerLogs_TolerantOfPerContainerError: one matched container's log
// fetch fails; the tool still renders the working one and notes the failure.
func TestContainerLogs_TolerantOfPerContainerError(t *testing.T) {
	api := &mockDockerAPI{
		summaries: []container.Summary{
			{ID: "id-1", Names: []string{"/web-1"}, Labels: map[string]string{"com.docker.compose.service": "web"}},
			{ID: "id-2", Names: []string{"/web-2"}, Labels: map[string]string{"com.docker.compose.service": "web"}},
		},
		logs: map[string][]byte{
			"id-1": muxFrame(t, "web-1 up\n", ""),
		},
		logsErr: map[string]error{
			"id-2": errors.New("container gone"),
		},
	}
	tool := newTool(mockHub{api: api})

	obs, err := run(t, tool, map[string]any{"workload": "web"})
	if err != nil {
		t.Fatalf("per-container log error must not fail the tool: %v", err)
	}
	if !strings.Contains(obs.Text, "--- web-1 ") || !strings.Contains(obs.Text, "web-1 up") {
		t.Errorf("expected working container web-1 rendered:\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "note:") || !strings.Contains(obs.Text, "web-2") {
		t.Errorf("expected failure note naming web-2:\n%s", obs.Text)
	}
}

func TestContainerLogs_TailLimitsLines(t *testing.T) {
	api := &mockDockerAPI{
		summaries: []container.Summary{
			{ID: "id-web", Names: []string{"/web"}, Labels: map[string]string{"com.docker.compose.service": "web"}},
		},
		logs: map[string][]byte{
			"id-web": muxFrame(t, "l1\nl2\nl3\nl4\nl5\n", ""),
		},
	}
	tool := newTool(mockHub{api: api})

	obs, err := run(t, tool, map[string]any{"workload": "web", "tail": 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "(2 line(s)") {
		t.Errorf("expected tail=2 to limit to 2 lines:\n%s", obs.Text)
	}
	if strings.Contains(obs.Text, "l1") || strings.Contains(obs.Text, "l3") {
		t.Errorf("tail=2 should keep only the last two lines (l4,l5):\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "l4") || !strings.Contains(obs.Text, "l5") {
		t.Errorf("tail=2 should retain l4 and l5:\n%s", obs.Text)
	}
}

// TestContainerLogs_TTYRawStream pins the TTY fix: a container created with a
// TTY emits a RAW log stream (no stdcopy 8-byte framing). The tool must inspect
// Config.Tty and read it verbatim rather than running it through StdCopy (which
// would misread the first bytes as a frame header and corrupt/empty the output).
func TestContainerLogs_TTYRawStream(t *testing.T) {
	raw := "raw tty line 1\nERROR raw tty failure\n"
	api := &mockDockerAPI{
		summaries: []container.Summary{
			{ID: "id-tty", Names: []string{"/web"}, Labels: map[string]string{"com.docker.compose.service": "web"}},
		},
		logs: map[string][]byte{
			"id-tty": []byte(raw), // raw, NOT muxFrame
		},
		tty: map[string]bool{"id-tty": true},
	}
	tool := newTool(mockHub{api: api})

	obs, err := run(t, tool, map[string]any{"workload": "web"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(obs.Text, "raw tty line 1") || !strings.Contains(obs.Text, "ERROR raw tty failure") {
		t.Errorf("TTY raw stream must be read verbatim, got:\n%s", obs.Text)
	}
	if !strings.Contains(obs.Text, "(2 line(s), 1 error line(s))") {
		t.Errorf("expected 2 lines / 1 error from raw stream, got:\n%s", obs.Text)
	}
}

func TestContainerLogs_WorkloadRequired(t *testing.T) {
	tool := newTool(mockHub{api: &mockDockerAPI{}})
	obs, err := run(t, tool, map[string]any{})
	if err != nil {
		t.Fatalf("missing workload should not error: %v", err)
	}
	if !strings.Contains(obs.Text, "workload is required") {
		t.Errorf("expected guidance, got: %s", obs.Text)
	}
}

func TestContainerLogs_InvalidSince(t *testing.T) {
	tool := newTool(mockHub{api: &mockDockerAPI{}})
	obs, err := run(t, tool, map[string]any{"workload": "web", "since": "soon"})
	if err != nil {
		t.Fatalf("invalid since should not error the tool: %v", err)
	}
	if !strings.Contains(obs.Text, "invalid since duration") {
		t.Errorf("expected since guidance, got: %s", obs.Text)
	}
}

func TestContainerLogs_HubGetError(t *testing.T) {
	tool := newTool(mockHub{err: errors.New("no docker hosts configured")})
	_, err := run(t, tool, map[string]any{"workload": "web"})
	if err == nil {
		t.Fatal("expected error when hub.Get fails")
	}
	if !strings.Contains(err.Error(), "no docker hosts configured") {
		t.Errorf("error should surface the hub failure, got: %v", err)
	}
}

func TestContainerLogs_Risk(t *testing.T) {
	tool := newTool(mockHub{api: &mockDockerAPI{}})
	rr, ok := tool.(tools.RiskRated)
	if !ok {
		t.Fatal("log.container must implement RiskRated")
	}
	if rr.Risk() != tools.RiskLow {
		t.Errorf("Risk = %v, want RiskLow", rr.Risk())
	}
}

func TestContainerLogs_NameAndSchema(t *testing.T) {
	tool := newTool(mockHub{api: &mockDockerAPI{}})
	if tool.Name() != "log.container" {
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
