package change

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// defaultSince is used when the caller omits `since` or supplies a value that
// time.ParseDuration cannot parse.
const defaultSince = 24 * time.Hour

// defaultLimit caps the merged event list when the caller omits `limit`.
const defaultLimit = 50

type recentArgs struct {
	Workload  string `json:"workload"`
	Namespace string `json:"namespace"`
	Context   string `json:"context"`
	Since     string `json:"since"`
	Limit     int    `json:"limit"`
}

// recentTool is the imperative implementation behind change.recent. It is not
// built with Spec[Args] because it must advertise RiskLow via the RiskRated
// interface, which Spec's generated tool does not expose.
type recentTool struct {
	sources []ChangeSource
}

// NewRecentTool returns the change.recent tool bound to the supplied sources.
// At least one source is expected; with none, the tool reports that no change
// sources are available.
func NewRecentTool(sources ...ChangeSource) tools.Tool {
	return &recentTool{sources: sources}
}

func (t *recentTool) Name() string { return "change.recent" }

func (t *recentTool) Description() string {
	return "List recent changes (rollouts, image updates, scale events, restarts) for a workload across the available Kubernetes contexts and Docker hosts. Read-only; results are newest-first."
}

func (t *recentTool) Schema() json.RawMessage {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workload":  str("Workload to inspect (deployment/statefulset/daemonset name, or container/compose service name). Required."),
			"namespace": str("Kubernetes namespace to scope the search; ignored by the Docker source."),
			"context":   str("kubeconfig context OR Docker host name to query; empty = each backend's default."),
			"since":     str("How far back to look, as a Go duration (e.g. \"24h\", \"90m\"); default \"24h\"."),
			"limit":     map[string]any{"type": "integer", "description": "Maximum number of events to return; default 50."},
		},
		"required": []string{"workload"},
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic("change: schema marshal: " + err.Error())
	}
	return b
}

// Risk implements tools.RiskRated. change.recent only performs list/inspect
// reads already classified RiskLow elsewhere.
func (t *recentTool) Risk() tools.RiskLevel { return tools.RiskLow }

func (t *recentTool) Run(ctx context.Context, raw json.RawMessage) (tools.Observation, error) {
	var a recentArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return tools.Observation{}, fmt.Errorf("change.recent: parse args: %w", err)
		}
	}
	if a.Workload == "" {
		return tools.Observation{Text: "change.recent: workload is required"}, nil
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

	q := ChangeQuery{
		Workload:  a.Workload,
		Namespace: a.Namespace,
		Context:   a.Context,
		Since:     since,
		Limit:     limit,
	}

	if len(t.sources) == 0 {
		return tools.Observation{Text: "change.recent: no change sources available"}, nil
	}

	var groups [][]ChangeEvent
	var failures []string
	for _, src := range t.sources {
		events, err := src.RecentChanges(ctx, q)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", src.Name(), err))
			continue
		}
		groups = append(groups, events)
	}

	// Only error when every source failed; partial success still returns.
	if len(groups) == 0 {
		return tools.Observation{}, fmt.Errorf("change.recent: all sources failed: %s", strings.Join(failures, "; "))
	}

	merged := MergeSorted(limit, groups...)
	return tools.Observation{
		Text: renderRecent(a.Workload, merged, failures),
		Raw:  merged,
	}, nil
}

// renderRecent formats the merged events as newest-first lines. Each line is
// "RFC3339 | source | kind | target | summary | before→after"; the
// before→after segment is omitted when both are empty. Per-source failures are
// appended as a short note so a partial result is still actionable.
func renderRecent(workload string, events []ChangeEvent, failures []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d change(s) for %q\n", len(events), workload)
	for _, e := range events {
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s",
			e.Time.UTC().Format(time.RFC3339), e.Source, e.Kind, e.Target, e.Summary)
		if e.Before != "" || e.After != "" {
			fmt.Fprintf(&b, " | %s→%s", e.Before, e.After)
		}
		b.WriteByte('\n')
	}
	if len(failures) > 0 {
		fmt.Fprintf(&b, "note: %d source(s) failed: %s\n", len(failures), strings.Join(failures, "; "))
	}
	return strings.TrimRight(b.String(), "\n")
}
