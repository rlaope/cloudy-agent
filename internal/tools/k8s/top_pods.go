package k8s

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

const (
	defaultTopPods = 20
	maxTopPods     = 200
)

// NewTopPodsTool returns the k8s.top_pods tool.
func NewTopPodsTool(hub *Hub) tools.Tool {
	return ListResourceSpec[MetricsPod]{
		Name:        "k8s.top_pods",
		Description: "Show CPU and memory usage for the top N pods (requires metrics-server). Returns ErrMetricsUnavailable if metrics-server is absent.",
		Schema: schema(withContextProp(map[string]any{
			"namespace": strProp("Namespace to query (empty = all namespaces)."),
			"top":       intProp("Number of pods to return sorted by CPU descending (default 20, max 200)."),
		}), nil),
		Headers:        []string{"NAMESPACE", "NAME", "CPU (m)", "MEMORY (Mi)"},
		Aligns:         []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight},
		NeedsNamespace: true,
		Items: func(_ context.Context, client *Client, a listArgs, _ metav1.ListOptions) ([]MetricsPod, any, error) {
			pods, err := client.TopPods(a.Namespace)
			if err != nil {
				return nil, nil, err
			}
			sort.Slice(pods, func(i, j int) bool { return pods[i].CPUMillis > pods[j].CPUMillis })
			top := defaultTopPods
			if a.Limit > 0 {
				top = int(a.Limit)
			}
			if top > maxTopPods {
				top = maxTopPods
			}
			if len(pods) > top {
				pods = pods[:top]
			}
			return pods, pods, nil
		},
		ProjectRow: func(p MetricsPod) []string {
			return []string{
				p.Namespace, p.Name,
				fmt.Sprintf("%d", p.CPUMillis),
				fmt.Sprintf("%d", p.MemoryBytes/1024/1024),
			}
		},
		Summary: func(items []MetricsPod, a listArgs) string {
			return fmt.Sprintf("Top %d pods by CPU in namespace %q", len(items), a.Namespace)
		},
	}.Build(hub)
}
