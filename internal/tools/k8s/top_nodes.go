package k8s

import (
	k8sclient "github.com/rlaope/cloudy/internal/clients/k8s"

	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// NewTopNodesTool returns the k8s.top_nodes tool.
func NewTopNodesTool(hub *k8sclient.Hub) tools.Tool {
	return ListResourceSpec[k8sclient.MetricsNode]{
		Name:        "k8s.top_nodes",
		Description: "Show CPU and memory usage per node (requires metrics-server).",
		Schema:      schema(withContextProp(map[string]any{}), nil),
		Headers:     []string{"NAME", "CPU (m)", "MEMORY (Mi)"},
		Aligns:      []render.Align{render.AlignLeft, render.AlignRight, render.AlignRight},
		Items: func(_ context.Context, client *k8sclient.Client, _ listArgs, _ metav1.ListOptions) ([]k8sclient.MetricsNode, any, error) {
			nodes, err := client.TopNodes()
			if err != nil {
				return nil, nil, err
			}
			sort.Slice(nodes, func(i, j int) bool { return nodes[i].CPUMillis > nodes[j].CPUMillis })
			return nodes, nodes, nil
		},
		ProjectRow: func(n k8sclient.MetricsNode) []string {
			return []string{
				n.Name,
				fmt.Sprintf("%d", n.CPUMillis),
				fmt.Sprintf("%d", n.MemoryBytes/1024/1024),
			}
		},
		Summary: func(items []k8sclient.MetricsNode, _ listArgs) string {
			return fmt.Sprintf("%d node(s)", len(items))
		},
	}.Build(hub)
}
