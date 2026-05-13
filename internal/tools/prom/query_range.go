package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rlaope/cloudy/internal/tools"
)

// QueryRangeTool implements prom.query_range.
type QueryRangeTool struct {
	clients    map[string]*Client
	defaultKey string
}

// NewQueryRangeTool constructs a QueryRangeTool.
func NewQueryRangeTool(clients map[string]*Client) *QueryRangeTool {
	return &QueryRangeTool{clients: clients, defaultKey: firstKey(clients)}
}

func (t *QueryRangeTool) Name() string { return "prom.query_range" }
func (t *QueryRangeTool) Description() string {
	return "Execute a range PromQL query over a time window and return matrix results."
}
func (t *QueryRangeTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"endpoint": strProp("Named Prometheus endpoint (empty = default)."),
		"query":    strProp("PromQL expression."),
		"start":    strProp("Start time in RFC3339."),
		"end":      strProp("End time in RFC3339."),
		"step":     strProp("Resolution step duration, e.g. \"30s\", \"1m\", \"5m\"."),
	}, []string{"query", "start", "end", "step"})
}

func (t *QueryRangeTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Endpoint string `json:"endpoint"`
		Query    string `json:"query"`
		Start    string `json:"start"`
		End      string `json:"end"`
		Step     string `json:"step"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("prom.query_range: parse args: %w", err)
	}

	key := a.Endpoint
	if key == "" {
		key = t.defaultKey
	}
	c, ok := t.clients[key]
	if !ok {
		return tools.Observation{}, fmt.Errorf("prom.query_range: unknown endpoint %q", a.Endpoint)
	}

	start, err := time.Parse(time.RFC3339, a.Start)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("prom.query_range: parse start: %w", err)
	}
	end, err := time.Parse(time.RFC3339, a.End)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("prom.query_range: parse end: %w", err)
	}
	step, err := time.ParseDuration(a.Step)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("prom.query_range: parse step: %w", err)
	}
	if step <= 0 {
		return tools.Observation{}, fmt.Errorf("prom.query_range: step must be positive")
	}

	res, err := c.QueryRange(ctx, a.Query, start, end, step)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("prom.query_range: %w", err)
	}

	tbl, text := resultToObservation(res, a.Query)
	return tools.Observation{Text: text, Table: tbl, Raw: res}, nil
}
