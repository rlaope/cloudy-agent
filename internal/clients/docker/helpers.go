package dockerclient

import (
	"strings"

	"github.com/docker/docker/api/types/container"
)

// MatchesWorkload reports whether a container summary relates to workload.
// Matching is case-insensitive and EXACT against the compose service/project
// labels or a container name — substring matching is deliberately avoided so
// "api" does not match "api-gateway". Compose containers carry the service in
// a label, so querying a compose service by name still resolves via the label.
// An empty workload never matches (callers that want match-all must short-circuit
// before calling this function).
func MatchesWorkload(s container.Summary, workload string) bool {
	if workload == "" {
		return false
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

// DisplayName returns a stable human-facing name for a container summary: the
// first name (slash-trimmed) when present, else the short container ID.
func DisplayName(s container.Summary) string {
	if len(s.Names) > 0 {
		return strings.TrimPrefix(s.Names[0], "/")
	}
	if len(s.ID) > 12 {
		return s.ID[:12]
	}
	return s.ID
}
