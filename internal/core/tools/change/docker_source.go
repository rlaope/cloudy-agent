package change

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
)

// DockerSource is a ChangeSource that derives recent change events from a
// Docker daemon via read-only container list/inspect and image list calls.
// It satisfies cloudy's read-only contract — no mutating Docker API call is
// reachable through this type.
type DockerSource struct {
	hub *dockerclient.Hub
}

// NewDockerSource returns a DockerSource backed by hub.
func NewDockerSource(hub *dockerclient.Hub) *DockerSource {
	return &DockerSource{hub: hub}
}

// Name implements ChangeSource.
func (s *DockerSource) Name() string { return "docker" }

// RecentChanges implements ChangeSource. It calls hub.Get(q.Context) to obtain
// a read-only Docker API handle, then derives ChangeEvents from container
// inspect results and the image list. Events older than q.Since are dropped
// (when Since > 0). The result is merged and capped at q.Limit via
// MergeSorted.
func (s *DockerSource) RecentChanges(ctx context.Context, q ChangeQuery) ([]ChangeEvent, error) {
	api, err := s.hub.Get(q.Context)
	if err != nil {
		return nil, fmt.Errorf("docker source: get host: %w", err)
	}

	cutoff := time.Time{}
	if q.Since > 0 {
		cutoff = time.Now().Add(-q.Since)
	}

	// --- containers ---
	summaries, err := api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("docker source: list containers: %w", err)
	}

	var containerEvents []ChangeEvent
	for _, s := range summaries {
		if !matchesWorkload(s, q.Workload) {
			continue
		}
		insp, err := api.ContainerInspect(ctx, s.ID)
		if err != nil {
			// Skip containers that disappear between list and inspect.
			continue
		}
		containerEvents = append(containerEvents, containerInspectEvents(insp, cutoff)...)
	}

	// --- images ---
	images, err := api.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker source: list images: %w", err)
	}
	imageEvents := imageListEvents(images, q.Workload, cutoff)

	return MergeSorted(q.Limit, containerEvents, imageEvents), nil
}

// matchesWorkload reports whether a container summary relates to the given
// workload. An empty workload matches everything. Matching is case-insensitive
// and EXACT against the compose service/project labels or a container name —
// substring matching is deliberately avoided so "api" does not match
// "api-gateway". Compose containers carry the service in a label, so querying a
// compose service by name still resolves via the label.
func matchesWorkload(s container.Summary, workload string) bool {
	if workload == "" {
		return true
	}
	wl := strings.ToLower(workload)
	for _, key := range []string{"com.docker.compose.service", "com.docker.compose.project"} {
		if v, ok := s.Labels[key]; ok && strings.ToLower(v) == wl {
			return true
		}
	}
	for _, n := range s.Names {
		if strings.ToLower(strings.TrimPrefix(n, "/")) == wl {
			return true
		}
	}
	return false
}

// containerInspectEvents derives ChangeEvents from a single container's
// InspectResponse. It emits up to three event kinds:
//   - "container_create"  — time of container creation
//   - "container_restart" — time of last start (when RestartCount > 0)
//   - "image"             — the running image at start time
//
// Events before cutoff are dropped (zero cutoff = keep all).
func containerInspectEvents(insp container.InspectResponse, cutoff time.Time) []ChangeEvent {
	if insp.ContainerJSONBase == nil {
		return nil
	}

	name := strings.TrimPrefix(insp.Name, "/")
	imageName := insp.Image // image digest from ContainerJSONBase
	if insp.Config != nil && insp.Config.Image != "" {
		imageName = insp.Config.Image
	}

	var events []ChangeEvent

	// container_create
	if t, ok := parseDockerTime(insp.Created); ok {
		ev := ChangeEvent{
			Time:    t,
			Kind:    "container_create",
			Target:  name,
			Summary: fmt.Sprintf("container %s created", name),
			After:   imageName,
			Source:  "docker",
		}
		if keep(t, cutoff) {
			events = append(events, ev)
		}
	}

	// startedAt time used for restart + image events
	var startedAt time.Time
	if insp.State != nil {
		if t, ok := parseDockerTime(insp.State.StartedAt); ok {
			startedAt = t
		}
	}

	// container_restart (only when RestartCount > 0 and we have a valid start time)
	if insp.RestartCount > 0 && !startedAt.IsZero() && keep(startedAt, cutoff) {
		events = append(events, ChangeEvent{
			Time:    startedAt,
			Kind:    "container_restart",
			Target:  name,
			Summary: fmt.Sprintf("container %s restarted (count: %d)", name, insp.RestartCount),
			After:   imageName,
			Source:  "docker",
		})
	}

	// image — record the running image at last start time. This is a current
	// baseline, not a transition, so Before is left empty (setting it to the
	// digest would render a misleading "sha256:…→tag" diff); the resolved
	// digest goes in the summary for traceability.
	if !startedAt.IsZero() && keep(startedAt, cutoff) {
		summary := fmt.Sprintf("container %s running image %s", name, imageName)
		if insp.Image != "" && insp.Image != imageName {
			summary += fmt.Sprintf(" (%s)", insp.Image)
		}
		events = append(events, ChangeEvent{
			Time:    startedAt,
			Kind:    "image",
			Target:  name,
			Summary: summary,
			After:   imageName,
			Source:  "docker",
		})
	}

	return events
}

// imageListEvents derives "image_pull" ChangeEvents from a slice of
// image.Summary values. Images whose RepoTags don't relate to workload are
// skipped (empty workload = keep all). Events before cutoff are dropped.
func imageListEvents(images []image.Summary, workload string, cutoff time.Time) []ChangeEvent {
	wl := strings.ToLower(workload)
	var events []ChangeEvent
	for _, img := range images {
		tags := matchingTags(img.RepoTags, wl)
		if len(tags) == 0 {
			continue
		}
		if img.Created <= 0 {
			// Unset/invalid creation time (Docker stores seconds since epoch);
			// skip rather than emit a 1970 timestamp.
			continue
		}
		t := time.Unix(img.Created, 0).UTC()
		if !keep(t, cutoff) {
			continue
		}
		for _, tag := range tags {
			parts := strings.SplitN(tag, ":", 2)
			after := tag
			if len(parts) == 2 {
				after = parts[1]
			}
			events = append(events, ChangeEvent{
				Time:    t,
				Kind:    "image_pull",
				Target:  tag,
				Summary: tag,
				After:   after,
				Source:  "docker",
			})
		}
	}
	return events
}

// matchingTags returns the subset of repoTags whose repository matches workload
// EXACTLY — either the full repository ("registry/org/api") or its last path
// segment ("api"). The "<none>:<none>" placeholder and empty tags are skipped.
// An empty workload returns all real tags. Substring matching is avoided so
// "api" does not match "rapid". workload is expected pre-lowercased.
func matchingTags(repoTags []string, workload string) []string {
	var out []string
	for _, tag := range repoTags {
		if tag == "<none>:<none>" || tag == "" {
			continue
		}
		if workload == "" {
			out = append(out, tag)
			continue
		}
		repo := tag
		if i := strings.LastIndex(tag, ":"); i >= 0 {
			repo = tag[:i]
		}
		repo = strings.ToLower(repo)
		seg := repo[strings.LastIndex(repo, "/")+1:]
		if repo == workload || seg == workload {
			out = append(out, tag)
		}
	}
	return out
}

// parseDockerTime parses a Docker RFC3339Nano timestamp string. It returns the
// parsed time and true on success; zero time and false on any parse error or
// empty/zero input (Docker uses "0001-01-01T00:00:00Z" for unset times).
func parseDockerTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	if t.IsZero() {
		return time.Time{}, false
	}
	return t, true
}

// keep reports whether t should be included given cutoff. A zero cutoff always
// returns true.
func keep(t time.Time, cutoff time.Time) bool {
	return cutoff.IsZero() || !t.Before(cutoff)
}
