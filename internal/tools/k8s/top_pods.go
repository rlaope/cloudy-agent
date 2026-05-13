package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

const (
	defaultTopPods = 20
	maxTopPods     = 200
)

// TopPodsTool implements k8s.top_pods.
type TopPodsTool struct{ hub *Hub }

// NewTopPodsTool constructs a TopPodsTool backed by the given Hub.
func NewTopPodsTool(h *Hub) *TopPodsTool { return &TopPodsTool{hub: h} }

func (t *TopPodsTool) Name() string   { return "k8s.top_pods" }
func (t *TopPodsTool) ReadOnly() bool { return true }
func (t *TopPodsTool) Description() string {
	return "Show CPU and memory usage for the top N pods (requires metrics-server). Returns ErrMetricsUnavailable if metrics-server is absent."
}
func (t *TopPodsTool) Schema() json.RawMessage {
	return schema(withContextProp(map[string]any{
		"namespace": strProp("Namespace to query (empty = all namespaces)."),
		"top":       intProp("Number of pods to return sorted by CPU descending (default 20, max 200)."),
	}), nil)
}

func (t *TopPodsTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Namespace string `json:"namespace"`
		Top       int    `json:"top"`
		Context   string `json:"context"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.top_pods: parse args: %w", err)
	}

	if err := t.hub.CheckNamespace(a.Namespace); err != nil {
		return tools.Observation{Text: err.Error()}, nil
	}

	client, err := t.hub.Get(a.Context)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.top_pods: %w", err)
	}
	ctxName := a.Context
	if ctxName == "" {
		ctxName = t.hub.Default()
	}

	top := defaultTopPods
	if a.Top > 0 {
		top = a.Top
		if top > maxTopPods {
			top = maxTopPods
		}
	}

	pods, err := client.TopPods(a.Namespace)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.top_pods: %w", err)
	}

	// Sort by CPU descending.
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].CPUMillis > pods[j].CPUMillis
	})
	if len(pods) > top {
		pods = pods[:top]
	}

	multi := t.hub.MultiContext()
	headers := []string{"NAMESPACE", "NAME", "CPU (m)", "MEMORY (Mi)"}
	aligns := []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight}
	if multi {
		headers = append([]string{"CONTEXT"}, headers...)
		aligns = append([]render.Align{render.AlignLeft}, aligns...)
	}
	tbl := &render.Table{Headers: headers, Aligns: aligns}
	for _, p := range pods {
		row := []string{
			p.Namespace,
			p.Name,
			fmt.Sprintf("%d", p.CPUMillis),
			fmt.Sprintf("%d", p.MemoryBytes/1024/1024),
		}
		if multi {
			row = append([]string{ctxName}, row...)
		}
		tbl.Rows = append(tbl.Rows, row)
	}

	return tools.Observation{
		Text:  fmt.Sprintf("Top %d pods by CPU in namespace %q", len(pods), a.Namespace),
		Table: tbl,
		Raw:   pods,
	}, nil
}
