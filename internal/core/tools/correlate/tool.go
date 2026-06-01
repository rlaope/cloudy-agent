package correlate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
	tlog "github.com/rlaope/cloudy/internal/core/tools/log"
	"github.com/rlaope/cloudy/internal/core/tools/trace"
)

// defaultSince is used when the caller omits `since` or supplies a value that
// time.ParseDuration cannot parse.
const defaultSince = time.Hour

// defaultLimit caps the merged timeline when the caller omits `limit`.
const defaultLimit = 50

type correlateArgs struct {
	Workload        string  `json:"workload"`
	Namespace       string  `json:"namespace"`
	Context         string  `json:"context"`
	Since           string  `json:"since"`
	Limit           int     `json:"limit"`
	MetricQuery     string  `json:"metric_query"`
	MetricThreshold float64 `json:"metric_threshold"`
	SLOQuery        string  `json:"slo_query"`
	SLOTarget       float64 `json:"slo_target"`
}

// correlateTool joins the available change sources (k8s, docker, argo) and the
// symptom sources (metric/log/trace) into one newest-first evidence timeline
// for a workload, and names the most recent state-altering event as the
// likeliest correlate of a current symptom. It is hand-written rather than
// built via Spec[Args] so it can advertise RiskLow through the RiskRated
// interface.
//
// changeSources are fixed at registration; symptom sources are built per-Run
// because the metric query/threshold arrive as call args.
type correlateTool struct {
	changeSources []change.ChangeSource
	prom          map[string]*promclient.Client
	logs          tlog.Clients
	traces        trace.Clients
	dockerHub     *dockerclient.Hub
}

// NewWorkloadTool returns the correlate.workload tool. changeSources are the
// registration-time sources (k8s/docker/argo); the remaining deps let Run build
// the metric/log/trace symptom sources from per-call args. Any dep may be
// zero/nil — the matching symptom source is simply omitted.
func NewWorkloadTool(changeSources []change.ChangeSource, prom map[string]*promclient.Client, logs tlog.Clients, traces trace.Clients, dockerHub *dockerclient.Hub) tools.Tool {
	return &correlateTool{
		changeSources: changeSources,
		prom:          prom,
		logs:          logs,
		traces:        traces,
		dockerHub:     dockerHub,
	}
}

// newCorrelateTool returns the correlate.workload tool bound only to the
// supplied change sources, with no symptom backends. Used in tests; production
// wiring goes through NewWorkloadTool.
func newCorrelateTool(sources ...change.ChangeSource) tools.Tool {
	return &correlateTool{changeSources: sources}
}

func (t *correlateTool) Name() string { return "correlate.workload" }

func (t *correlateTool) Description() string {
	return "Correlate recent changes and symptoms for a workload across signals — Kubernetes/Docker change history, Argo CD sync history, plus metric/log/trace symptoms — into one newest-first evidence timeline, and rank the state-altering changes most likely to have caused the earliest symptom by time-proximity, change weight, and entity match, each with a relative confidence. Supply metric_query (PromQL) with metric_threshold to fold a metric breach onto the timeline, or slo_query with slo_target to fold an acute SLO error-budget fast-burn as a budget_burn symptom; log and trace symptoms are added automatically when those backends are configured. Read-only."
}

func (t *correlateTool) Schema() json.RawMessage {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workload":         str("Workload to correlate (deployment/statefulset/daemonset, container/compose service, or Argo CD application name). Required."),
			"namespace":        str("Kubernetes namespace to scope the search. Honored by the k8s change source, the Loki/Elasticsearch log symptom sources, and the Tempo trace symptom source. Ignored by the Docker and Argo change sources, the Jaeger trace source (no standard namespace tag), and the metric source (scope namespace inside metric_query)."),
			"context":          str("kubeconfig context, Docker host, or Argo CD endpoint name to query; empty = each backend's default. Cloud control-plane audit events are not context-selectable — they always come from the default (first configured) account/project."),
			"since":            str("How far back to look, as a Go duration (e.g. \"1h\", \"90m\"); default \"1h\"."),
			"limit":            map[string]any{"type": "integer", "description": "Maximum number of timeline events to return; default 50."},
			"metric_query":     str("PromQL query whose breaches are folded onto the timeline as metric symptom events; empty = no metric symptom."),
			"metric_threshold": map[string]any{"type": "number", "description": "Value a metric_query sample must exceed to count as a breach; default 0 (i.e. value > 0)."},
			"slo_query":        str("PromQL returning the bad-event ratio (0..1) with the literal token $window where the range goes, e.g. \"sum(rate(http_5xx[$window]))/sum(rate(http_total[$window]))\". When set with slo_target, an acute fast-burn (≥14.4× over both 5m and 1h) is folded onto the timeline as a budget_burn symptom; empty = no SLO symptom."),
			"slo_target":       map[string]any{"type": "number", "description": "SLO target as a fraction in (0,1), e.g. 0.999, paired with slo_query. Burn rate = bad-ratio / (1 - slo_target)."},
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

	// Symptom sources are built per-call because the metric query/threshold are
	// call args. nil constructors (absent backend/inputs) are skipped, then the
	// rest join the fixed change sources on one timeline. Copy first so appends
	// never mutate the shared t.changeSources backing array across calls.
	sources := append([]change.ChangeSource(nil), t.changeSources...)
	if src := newMetricSource(t.prom, a.MetricQuery, a.MetricThreshold); src != nil {
		sources = append(sources, src)
	}
	if src := newBudgetSource(t.prom, a.SLOQuery, a.SLOTarget); src != nil {
		sources = append(sources, src)
	}
	if src := newLogSource(t.logs, t.dockerHub); src != nil {
		sources = append(sources, src)
	}
	if src := newTraceSource(t.traces); src != nil {
		sources = append(sources, src)
	}

	if len(sources) == 0 {
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
	for _, src := range sources {
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
	b.WriteString(candidateCauses(events, workload))
	b.WriteByte('\n')
	if len(failures) > 0 {
		fmt.Fprintf(&b, "note: %d source(s) failed: %s\n", len(failures), strings.Join(failures, "; "))
	}
	return strings.TrimRight(b.String(), "\n")
}
