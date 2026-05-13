package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// TopNodesTool implements k8s.top_nodes.
type TopNodesTool struct{ client *Client }

// NewTopNodesTool constructs a TopNodesTool backed by the given Client.
func NewTopNodesTool(c *Client) *TopNodesTool { return &TopNodesTool{client: c} }

func (t *TopNodesTool) Name() string      { return "k8s.top_nodes" }
func (t *TopNodesTool) ReadOnly() bool    { return true }
func (t *TopNodesTool) Description() string {
	return "Show CPU and memory usage per node (requires metrics-server)."
}
func (t *TopNodesTool) Schema() json.RawMessage {
	return schema(map[string]any{}, nil)
}

func (t *TopNodesTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	nodes, err := t.client.TopNodes()
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.top_nodes: %w", err)
	}

	// Sort by CPU descending.
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].CPUMillis > nodes[j].CPUMillis
	})

	tbl := &render.Table{
		Headers: []string{"NAME", "CPU (m)", "MEMORY (Mi)"},
		Aligns:  []render.Align{render.AlignLeft, render.AlignRight, render.AlignRight},
	}
	for _, n := range nodes {
		tbl.Rows = append(tbl.Rows, []string{
			n.Name,
			fmt.Sprintf("%d", n.CPUMillis),
			fmt.Sprintf("%d", n.MemoryBytes/1024/1024),
		})
	}

	return tools.Observation{
		Text:  fmt.Sprintf("%d node(s)", len(nodes)),
		Table: tbl,
		Raw:   nodes,
	}, nil
}
