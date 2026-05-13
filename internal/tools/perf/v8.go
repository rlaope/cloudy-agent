package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/httpapi"
)

// NodeInspectorClient wraps an httpapi.Client with the V8 Inspector
// discovery layout (/json, /json/list, /json/version).
type NodeInspectorClient struct {
	*httpapi.Client
}

func pickNode(m map[string]*NodeInspectorClient, name string) (*NodeInspectorClient, error) {
	if name == "" {
		if len(m) == 1 {
			for _, c := range m {
				return c, nil
			}
		}
		return nil, fmt.Errorf("perf: node inspector endpoint name required (configured: %s)", strings.Join(keys(m), ", "))
	}
	c, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("perf: unknown node inspector endpoint %q (configured: %s)", name, strings.Join(keys(m), ", "))
	}
	return c, nil
}

var nodeEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the node_inspectors endpoint. Optional if exactly one is configured.",
}

// newNodeInspectorTargetsTool wraps GET /json/list — the V8 Inspector's
// debug-target discovery endpoint. Each target exposes its title, URL,
// and a webSocketDebuggerUrl that the agent (or operator) can attach to
// for a deeper CPU/heap session. The deeper attach flow is intentionally
// out of scope for this release; the discovery surface is read-only and
// stands on its own as a "what is debuggable here" tool.
func newNodeInspectorTargetsTool(clients map[string]*NodeInspectorClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
	}
	return tools.Spec[args]{
		Name:        "perf.v8_inspector_targets",
		Description: "Enumerate Node.js V8 Inspector debug targets (GET /json/list). Returns title, type, URL, and WebSocket debugger URL per target.",
		Schema: mustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": nodeEndpointSchema},
		}),
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickNode(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/json/list", nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("perf.v8_inspector_targets: %w", err)
			}
			var arr []map[string]any
			if err := json.Unmarshal(body, &arr); err != nil {
				return tools.Observation{}, fmt.Errorf("perf.v8_inspector_targets: decode: %w", err)
			}
			tbl := &render.Table{Headers: []string{"TYPE", "TITLE", "URL", "WS_DEBUGGER_URL"}}
			for _, m := range arr {
				tbl.Rows = append(tbl.Rows, []string{
					asString(m["type"]),
					asString(m["title"]),
					asString(m["url"]),
					asString(m["webSocketDebuggerUrl"]),
				})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d targets", len(arr)),
				Table: tbl,
				Raw:   arr,
			}, nil
		},
	}.Build()
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
