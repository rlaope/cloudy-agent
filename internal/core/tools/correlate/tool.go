package correlate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// defaultSince is used when the caller omits `since` or supplies a value that
// time.ParseDuration cannot parse.
const defaultSince = time.Hour

// defaultLimit caps the merged timeline when the caller omits `limit`.
const defaultLimit = 50

// causeKinds is the set of state-altering change kinds that can plausibly be
// the cause behind a symptom. "event" is excluded: a Kubernetes event is a
// report of a condition, not the change that produced it.
var causeKinds = map[string]bool{
	"image":             true,
	"rollout":           true,
	"scale":             true,
	"sync":              true,
	"container_restart": true,
	"container_create":  true,
	"image_pull":        true,
}

type correlateArgs struct {
	Workload  string `json:"workload"`
	Namespace string `json:"namespace"`
	Context   string `json:"context"`
	Since     string `json:"since"`
	Limit     int    `json:"limit"`
}

// correlateTool joins the available change sources (k8s, docker, argo) into one
// newest-first evidence timeline for a workload and names the most recent
// state-altering event as the likeliest correlate of a current symptom. It is
// hand-written rather than built via Spec[Args] so it can advertise RiskLow
// through the RiskRated interface.
type correlateTool struct {
	sources []change.ChangeSource
}

// newCorrelateTool returns the correlate.workload tool bound to the supplied
// sources. At least one source is expected.
func newCorrelateTool(sources ...change.ChangeSource) tools.Tool {
	return &correlateTool{sources: sources}
}

func (t *correlateTool) Name() string { return "correlate.workload" }

func (t *correlateTool) Description() string {
	return "Correlate recent changes for a workload across signals — Kubernetes/Docker change history and Argo CD sync history — into one newest-first evidence timeline, and name the most recent state-altering event as the likeliest cause of a current symptom. Read-only."
}

func (t *correlateTool) Schema() json.RawMessage {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workload":  str("Workload to correlate (deployment/statefulset/daemonset, container/compose service, or Argo CD application name). Required."),
			"namespace": str("Kubernetes namespace to scope the search; ignored by the Docker and Argo sources."),
			"context":   str("kubeconfig context, Docker host, or Argo CD endpoint name to query; empty = each backend's default."),
			"since":     str("How far back to look, as a Go duration (e.g. \"1h\", \"90m\"); default \"1h\"."),
			"limit":     map[string]any{"type": "integer", "description": "Maximum number of timeline events to return; default 50."},
		},
		"required": []string{"workload"},
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic("correlate: schema marshal: " + err.Error())
	}
	return b
}

// Risk implements tools.RiskRated. correlate.workload only fans out to
// list/inspect reads already classified RiskLow elsewhere.
func (t *correlateTool) Risk() tools.RiskLevel { return tools.RiskLow }

func (t *correlateTool) Run(ctx context.Context, raw json.RawMessage) (tools.Observation, error) {
	var a correlateArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tools.Observation{}, fmt.Errorf("correlate.workload: parse args: %w", err)
		}
	}
	if a.Workload == "" {
		return tools.Observation{Text: "correlate.workload: workload is required"}, nil
	}

	since := defaultSince
	if a.Since != "" {
		if d, err := time.ParseDuration(a.Since); err == nil {
			since = d
		}
	}
	limit := defaultLimit
	if a.Limit > 0 {
		limit = a.Limit
	}

	if len(t.sources) == 0 {
		return tools.Observation{Text: "correlate.workload: no change sources available"}, nil
	}

	q := change.ChangeQuery{
		Workload:  a.Workload,
		Namespace: a.Namespace,
		Context:   a.Context,
		Since:     since,
		Limit:     limit,
	}

	var groups [][]change.ChangeEvent
	var failures []string
	for _, src := range t.sources {
		events, err := src.RecentChanges(ctx, q)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", src.Name(), err))
			continue
		}
		groups = append(groups, events)
	}

	// Only hard-error when every source failed; partial success still returns.
	if len(groups) == 0 {
		return tools.Observation{}, fmt.Errorf("correlate.workload: all sources failed: %s", strings.Join(failures, "; "))
	}

	merged := change.MergeSorted(limit, groups...)
	return tools.Observation{
		Text: renderCorrelation(a.Workload, since, merged, failures),
		Raw:  merged,
	}, nil
}

// candidateCause returns the newest event whose Kind is state-altering (in
// causeKinds), i.e. the likeliest correlate of a current symptom. events must
// already be newest-first (MergeSorted output). Returns nil when none qualify.
func candidateCause(events []change.ChangeEvent) *change.ChangeEvent {
	for i := range events {
		if causeKinds[events[i].Kind] {
			return &events[i]
		}
	}
	return nil
}

// renderCorrelation formats the evidence timeline newest-first followed by the
// candidate-cause line. Each timeline line is
// "RFC3339 | source | kind | target | summary | before→after"; the before→after
// segment is omitted when both are empty. Per-source failures are noted so a
// partial result is still actionable.
func renderCorrelation(workload string, since time.Duration, events []change.ChangeEvent, failures []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d event(s) for %q in the last %s (newest first)\n", len(events), workload, since)
	for _, e := range events {
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s",
			e.Time.UTC().Format(time.RFC3339), e.Source, e.Kind, e.Target, e.Summary)
		if e.Before != "" || e.After != "" {
			fmt.Fprintf(&b, " | %s→%s", e.Before, e.After)
		}
		b.WriteByte('\n')
	}
	if cause := candidateCause(events); cause != nil {
		fmt.Fprintf(&b, "candidate cause: %s %s on %s @ %s",
			cause.Source, cause.Kind, cause.Target, cause.Time.UTC().Format(time.RFC3339))
		if cause.Before != "" || cause.After != "" {
			fmt.Fprintf(&b, " (%s→%s)", cause.Before, cause.After)
		}
		b.WriteByte('\n')
	} else {
		b.WriteString("candidate cause: none — no state-altering change in the window\n")
	}
	if len(failures) > 0 {
		fmt.Fprintf(&b, "note: %d source(s) failed: %s\n", len(failures), strings.Join(failures, "; "))
	}
	return strings.TrimRight(b.String(), "\n")
}
