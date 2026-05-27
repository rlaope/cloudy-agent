package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// LabelValuesTool implements prom.label_values.
type LabelValuesTool struct {
	clients    map[string]*promclient.Client
	defaultKey string
}

// NewLabelValuesTool constructs a LabelValuesTool.
func NewLabelValuesTool(clients map[string]*promclient.Client) *LabelValuesTool {
	return &LabelValuesTool{clients: clients, defaultKey: firstKey(clients)}
}

func (t *LabelValuesTool) Name() string { return "prom.label_values" }
func (t *LabelValuesTool) Description() string {
	return "List all values for a Prometheus label, optionally filtered by series selectors."
}
func (t *LabelValuesTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"endpoint": strProp("Named Prometheus endpoint (empty = default)."),
		"label":    strProp("Label name to retrieve values for."),
		"match":    strArrayProp("Optional series selectors to restrict the search."),
		"start":    strProp("Start time in RFC3339 (optional)."),
		"end":      strProp("End time in RFC3339 (optional)."),
	}, []string{"label"})
}

func (t *LabelValuesTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Endpoint string   `json:"endpoint"`
		Label    string   `json:"label"`
		Match    []string `json:"match"`
		Start    string   `json:"start"`
		End      string   `json:"end"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("prom.label_values: parse args: %w", err)
	}
	if a.Label == "" {
		return tools.Observation{}, fmt.Errorf("prom.label_values: label is required")
	}

	key := a.Endpoint
	if key == "" {
		key = t.defaultKey
	}
	c, ok := t.clients[key]
	if !ok {
		return tools.Observation{}, fmt.Errorf("prom.label_values: unknown endpoint %q", a.Endpoint)
	}

	var start, end time.Time
	var err error
	if a.Start != "" {
		start, err = time.Parse(time.RFC3339, a.Start)
		if err != nil {
			return tools.Observation{}, fmt.Errorf("prom.label_values: parse start: %w", err)
		}
	}
	if a.End != "" {
		end, err = time.Parse(time.RFC3339, a.End)
		if err != nil {
			return tools.Observation{}, fmt.Errorf("prom.label_values: parse end: %w", err)
		}
	}

	vals, err := c.LabelValues(ctx, a.Label, a.Match, start, end)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("prom.label_values: %w", err)
	}

	tbl := &render.Table{
		Headers: []string{"VALUE"},
	}
	for _, v := range vals {
		tbl.Rows = append(tbl.Rows, []string{v})
	}

	text := fmt.Sprintf("label=%q values=%d: %s", a.Label, len(vals), strings.Join(vals, ", "))
	return tools.Observation{Text: text, Table: tbl, Raw: vals}, nil
}
