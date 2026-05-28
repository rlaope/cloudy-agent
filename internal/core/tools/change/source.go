// Package change exposes a read-only "what changed recently" capability over
// the workloads cloudy can already inspect. A ChangeSource enumerates recent
// rollout / image / scale / restart events from a single backend (Kubernetes
// or Docker); MergeSorted folds several sources into one newest-first view.
//
// Nothing in this package mutates cluster or host state — sources are built
// only from list/inspect/get reads, in line with cloudy's read-only contract.
package change

import (
	"context"
	"sort"
	"time"
)

// ChangeEvent is a single observed change to a workload, normalised across
// backends. Kind is one of "rollout", "image", "scale", "event",
// "container_restart", or "image_pull". Before/After carry the human-readable
// old/new values for the change (e.g. image tags, replica counts) and may be
// empty when not applicable. Source is "k8s" or "docker".
type ChangeEvent struct {
	Time    time.Time
	Kind    string
	Target  string
	Summary string
	Before  string
	After   string
	Source  string
}

// ChangeQuery narrows a RecentChanges call. Context is the k8s context or the
// docker host name; an empty Context resolves to the backend's default. Since
// bounds how far back to look (zero = backend default), and Limit caps the
// returned events (zero = no cap).
type ChangeQuery struct {
	Workload  string
	Namespace string
	Context   string
	Since     time.Duration
	Limit     int
}

// ChangeSource is one backend that can report recent changes. Name identifies
// the backend ("k8s" / "docker") for diagnostics and logging.
type ChangeSource interface {
	Name() string
	RecentChanges(ctx context.Context, q ChangeQuery) ([]ChangeEvent, error)
}

// MergeSorted concatenates the supplied event groups, sorts them by Time
// descending (newest first), and applies limit when it is greater than zero
// (zero or negative = no cap). The input slices are not modified.
func MergeSorted(limit int, groups ...[]ChangeEvent) []ChangeEvent {
	var out []ChangeEvent
	for _, g := range groups {
		out = append(out, g...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Time.After(out[j].Time)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
