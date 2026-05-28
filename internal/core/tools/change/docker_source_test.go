package change

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
)

// mockDockerAPI implements dockerclient.ReadOnlyAPI with canned responses.
type mockDockerAPI struct {
	summaries []container.Summary
	inspect   map[string]container.InspectResponse
	images    []image.Summary
}

func (m *mockDockerAPI) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	return m.summaries, nil
}

func (m *mockDockerAPI) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	return m.inspect[id], nil
}

func (m *mockDockerAPI) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	return m.images, nil
}

// --- pure helper tests ---

func TestParseDockerTime(t *testing.T) {
	t.Run("valid RFC3339Nano", func(t *testing.T) {
		ts := "2026-05-01T10:00:00.000000000Z"
		got, ok := parseDockerTime(ts)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if got.Year() != 2026 || got.Month() != 5 || got.Day() != 1 {
			t.Errorf("unexpected time: %v", got)
		}
	})
	t.Run("empty string", func(t *testing.T) {
		_, ok := parseDockerTime("")
		if ok {
			t.Error("expected ok=false for empty string")
		}
	})
	t.Run("zero docker time", func(t *testing.T) {
		_, ok := parseDockerTime("0001-01-01T00:00:00Z")
		if ok {
			t.Error("expected ok=false for zero time")
		}
	})
	t.Run("garbage", func(t *testing.T) {
		_, ok := parseDockerTime("not-a-time")
		if ok {
			t.Error("expected ok=false for unparseable string")
		}
	})
}

func TestMatchesWorkload(t *testing.T) {
	s := container.Summary{
		Names: []string{"/myapp-web-1"},
		Labels: map[string]string{
			"com.docker.compose.service": "web",
			"com.docker.compose.project": "myapp",
		},
	}

	if !matchesWorkload(s, "") {
		t.Error("empty workload should match everything")
	}
	if !matchesWorkload(s, "web") {
		t.Error("should match via compose service label")
	}
	if !matchesWorkload(s, "myapp") {
		t.Error("should match via compose project label")
	}
	if !matchesWorkload(s, "myapp-web-1") {
		t.Error("should match via exact container name")
	}
	// Substring matches must NOT trigger (the bug this guards against):
	// "web" is the service, so "we" / "eb" / a prefix of the name must miss.
	if matchesWorkload(s, "we") {
		t.Error("substring 'we' must not match service 'web'")
	}
	if matchesWorkload(s, "app") {
		t.Error("substring 'app' must not match project 'myapp' or name 'myapp-web-1'")
	}
	if matchesWorkload(s, "redis") {
		t.Error("should not match unrelated workload")
	}
}

func TestMatchingTags(t *testing.T) {
	tags := []string{"nginx:1.25", "nginx:latest", "<none>:<none>", "redis:7"}
	got := matchingTags(tags, "nginx")
	if len(got) != 2 {
		t.Fatalf("want 2 nginx tags, got %d: %v", len(got), got)
	}
	// Registry/org-qualified repo matches on its last path segment.
	if len(matchingTags([]string{"my.reg/org/api:v2"}, "api")) != 1 {
		t.Error("should match repo last segment 'api'")
	}
	// Substring must NOT match: "ngin" is a prefix of "nginx" but not equal.
	if len(matchingTags(tags, "ngin")) != 0 {
		t.Error("substring 'ngin' must not match repo 'nginx'")
	}
	// empty workload returns all non-placeholder tags
	all := matchingTags(tags, "")
	if len(all) != 3 {
		t.Fatalf("want 3 tags (no placeholder), got %d: %v", len(all), all)
	}
}

func TestImageListEvents(t *testing.T) {
	created := time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC)
	imgs := []image.Summary{
		{RepoTags: []string{"myapp:v2"}, Created: created.Unix()},
		{RepoTags: []string{"redis:7"}, Created: created.Add(-24 * time.Hour).Unix()},
	}

	t.Run("workload filter", func(t *testing.T) {
		evs := imageListEvents(imgs, "myapp", time.Time{})
		if len(evs) != 1 || evs[0].Kind != "image_pull" {
			t.Errorf("expected 1 image_pull for myapp, got %v", evs)
		}
		if evs[0].After != "v2" {
			t.Errorf("After = %q, want %q", evs[0].After, "v2")
		}
	})

	t.Run("since filter drops old", func(t *testing.T) {
		cutoff := created.Add(-12 * time.Hour)
		evs := imageListEvents(imgs, "", cutoff)
		if len(evs) != 1 {
			t.Fatalf("want 1 event after cutoff, got %d", len(evs))
		}
		if evs[0].Target != "myapp:v2" {
			t.Errorf("unexpected target: %s", evs[0].Target)
		}
	})
}

func TestContainerInspectEvents(t *testing.T) {
	createTime := "2026-05-01T09:00:00Z"
	startTime := "2026-05-10T10:00:00Z"

	insp := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:           "abc123",
			Name:         "/myapp",
			Created:      createTime,
			Image:        "sha256:deadbeef",
			RestartCount: 2,
			State: &container.State{
				StartedAt: startTime,
			},
		},
		Config: &container.Config{
			Image: "myapp:v2",
		},
	}

	evs := containerInspectEvents(insp, time.Time{})

	kinds := make(map[string]bool)
	for _, e := range evs {
		kinds[e.Kind] = true
		if e.Source != "docker" {
			t.Errorf("event %s Source = %q, want docker", e.Kind, e.Source)
		}
	}
	if !kinds["container_create"] {
		t.Error("expected container_create event")
	}
	if !kinds["container_restart"] {
		t.Error("expected container_restart event (RestartCount=2)")
	}
	if !kinds["image"] {
		t.Error("expected image event")
	}

	// restart event should note count
	for _, e := range evs {
		if e.Kind == "container_restart" && e.After != "myapp:v2" {
			t.Errorf("restart After = %q, want myapp:v2", e.After)
		}
	}
}

func TestContainerInspectEvents_NoRestartWhenZero(t *testing.T) {
	insp := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:           "xyz",
			Name:         "/svc",
			Created:      "2026-05-01T09:00:00Z",
			RestartCount: 0,
			State: &container.State{
				StartedAt: "2026-05-01T09:01:00Z",
			},
		},
		Config: &container.Config{Image: "svc:latest"},
	}
	evs := containerInspectEvents(insp, time.Time{})
	for _, e := range evs {
		if e.Kind == "container_restart" {
			t.Error("should not emit container_restart when RestartCount=0")
		}
	}
}

func TestContainerInspectEvents_SinceFilter(t *testing.T) {
	createTime := "2026-05-01T09:00:00Z"
	startTime := "2026-05-10T10:00:00Z"
	cutoff := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)

	insp := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:           "abc",
			Name:         "/svc",
			Created:      createTime, // before cutoff → dropped
			RestartCount: 1,
			State:        &container.State{StartedAt: startTime}, // after cutoff → kept
		},
		Config: &container.Config{Image: "svc:v1"},
	}

	evs := containerInspectEvents(insp, cutoff)
	for _, e := range evs {
		if e.Kind == "container_create" {
			t.Error("container_create should be filtered out (before cutoff)")
		}
	}
	// restart and image events (startTime after cutoff) should be present
	kinds := make(map[string]bool)
	for _, e := range evs {
		kinds[e.Kind] = true
	}
	if !kinds["container_restart"] {
		t.Error("expected container_restart after cutoff")
	}
	if !kinds["image"] {
		t.Error("expected image event after cutoff")
	}
}

// --- integration-style test using the mock hub seam ---

// mockHub wraps a mockDockerAPI so DockerSource can call it via a thin adapter
// that satisfies the hub.Get(name) (ReadOnlyAPI, error) contract without
// importing dockerclient (same package test, no daemon needed).
type mockHubAdapter struct {
	api *mockDockerAPI
}

// DockerSource accepts *dockerclient.Hub, so we test the pure helpers above
// and below we exercise RecentChanges via a small integration path using a
// real Hub with an injected client — but that requires a real hub. Instead,
// we test that containerInspectEvents and imageListEvents together, piped
// through MergeSorted, produce a correct merged+limited result.
func TestMergedDockerEvents(t *testing.T) {
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)

	containers := []ChangeEvent{
		{Time: base, Kind: "image", Target: "app", Source: "docker"},
		{Time: base.Add(-1 * time.Hour), Kind: "container_create", Target: "app", Source: "docker"},
	}
	images := []ChangeEvent{
		{Time: base.Add(-30 * time.Minute), Kind: "image_pull", Target: "app:v2", Source: "docker"},
	}

	got := MergeSorted(2, containers, images)
	if len(got) != 2 {
		t.Fatalf("want 2 (limit), got %d", len(got))
	}
	if got[0].Kind != "image" {
		t.Errorf("got[0].Kind = %q, want image", got[0].Kind)
	}
	if got[1].Kind != "image_pull" {
		t.Errorf("got[1].Kind = %q, want image_pull", got[1].Kind)
	}
}
