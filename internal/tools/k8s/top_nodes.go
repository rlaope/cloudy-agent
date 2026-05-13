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
type TopNodesTool struct{ hub *Hub }

// NewTopNodesTool constructs a TopNodesTool backed by the given Hub.
func NewTopNodesTool(h *Hub) *TopNodesTool { return &TopNodesTool{hub: h} }

func (t *TopNodesTool) Name() string   { return "k8s.top_nodes" }
func (t *TopNodesTool) ReadOnly() bool { return true }
func (t *TopNodesTool) Description() string {
	return "Show CPU and memory usage per node (requires metrics-server)."
}
func (t *TopNodesTool) Schema() json.RawMessage {
	return schema(withContextProp(map[string]any{}), nil)
}

func (t *TopNodesTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Context string `json:"context"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return tools.Observation{}, fmt.Errorf("k8s.top_nodes: parse args: %w", err)
		}
	}

	client, err := t.hub.Get(a.Context)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.top_nodes: %w", err)
	}
	ctxName := a.Context
	if ctxName == "" {
		ctxName = t.hub.Default()
	}

	nodes, err := client.TopNodes()
	if err != nil {
		return tools.Observation{}, fmt.Errorf("k8s.top_nodes: %w", err)
	}

	// Sort by CPU descending.
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].CPUMillis > nodes[j].CPUMillis
	})

	multi := t.hub.MultiContext()
	headers := []string{"NAME", "CPU (m)", "MEMORY (Mi)"}
	aligns := []render.Align{render.AlignLeft, render.AlignRight, render.AlignRight}
	if multi {
		headers = append([]string{"CONTEXT"}, headers...)
		aligns = append([]render.Align{render.AlignLeft}, aligns...)
	}
	tbl := &render.Table{Headers: headers, Aligns: aligns}
	for _, n := range nodes {
		row := []string{
			n.Name,
			fmt.Sprintf("%d", n.CPUMillis),
			fmt.Sprintf("%d", n.MemoryBytes/1024/1024),
		}
		if multi {
			row = append([]string{ctxName}, row...)
		}
		tbl.Rows = append(tbl.Rows, row)
	}

	return tools.Observation{
		Text:  fmt.Sprintf("%d node(s)", len(nodes)),
		Table: tbl,
		Raw:   nodes,
	}, nil
}
