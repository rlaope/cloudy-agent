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

// QueryTool implements prom.query.
type QueryTool struct {
	clients    map[string]*promclient.Client
	defaultKey string
}

// NewQueryTool constructs a QueryTool. clients maps endpoint names to Clients;
// the first key is used as the default when endpoint is empty.
func NewQueryTool(clients map[string]*promclient.Client) *QueryTool {
	def := firstKey(clients)
	return &QueryTool{clients: clients, defaultKey: def}
}

func (t *QueryTool) Name() string { return "prom.query" }
func (t *QueryTool) Description() string {
	return "Execute an instant PromQL query against a Prometheus endpoint."
}
func (t *QueryTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"endpoint": strProp("Named Prometheus endpoint (empty = default)."),
		"query":    strProp("PromQL expression."),
		"time":     strProp("Evaluation timestamp in RFC3339 (default: now)."),
	}, []string{"query"})
}

func (t *QueryTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Endpoint string `json:"endpoint"`
		Query    string `json:"query"`
		Time     string `json:"time"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("prom.query: parse args: %w", err)
	}

	c, err := t.resolve(a.Endpoint)
	if err != nil {
		return tools.Observation{}, err
	}

	var ts time.Time
	if a.Time != "" {
		ts, err = time.Parse(time.RFC3339, a.Time)
		if err != nil {
			return tools.Observation{}, fmt.Errorf("prom.query: parse time: %w", err)
		}
	}

	res, err := c.Query(ctx, a.Query, ts)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("prom.query: %w", err)
	}

	tbl, text := resultToObservation(res, a.Query)
	return tools.Observation{Text: text, Table: tbl, Raw: res}, nil
}

func (t *QueryTool) resolve(endpoint string) (*promclient.Client, error) {
	key := endpoint
	if key == "" {
		key = t.defaultKey
	}
	c, ok := t.clients[key]
	if !ok {
		return nil, fmt.Errorf("prom: unknown endpoint %q", endpoint)
	}
	return c, nil
}

// resultToObservation converts a Result into a Table and a Text summary.
func resultToObservation(res *promclient.Result, query string) (*render.Table, string) {
	switch res.ResultType {
	case "vector":
		tbl := &render.Table{
			Headers: []string{"LABELS", "VALUE", "TIMESTAMP"},
			Aligns:  []render.Align{render.AlignLeft, render.AlignRight, render.AlignRight},
		}
		for _, s := range res.Vector {
			tbl.Rows = append(tbl.Rows, []string{
				labelsString(s.Labels),
				fmt.Sprintf("%g", s.Value),
				time.Unix(int64(s.Timestamp), 0).UTC().Format(time.RFC3339),
			})
		}
		text := fmt.Sprintf("query=%q resultType=vector samples=%d", query, len(res.Vector))
		// Sparkline for single-series vector: single value, nothing useful to spark.
		return tbl, text

	case "matrix":
		tbl := &render.Table{
			Headers: []string{"LABELS", "POINTS", "SPARKLINE"},
		}
		var sb strings.Builder
		for _, s := range res.Matrix {
			spark := seriesSparkline(s.Values, 20)
			tbl.Rows = append(tbl.Rows, []string{
				labelsString(s.Labels),
				fmt.Sprintf("%d", len(s.Values)),
				spark,
			})
			fmt.Fprintf(&sb, "%s → %s\n", labelsString(s.Labels), spark)
		}
		text := fmt.Sprintf("query=%q resultType=matrix series=%d\n%s", query, len(res.Matrix), sb.String())
		return tbl, text

	case "scalar":
		if res.Scalar != nil {
			text := fmt.Sprintf("scalar: %g", res.Scalar.Value)
			return nil, text
		}
		return nil, "scalar: (empty)"

	default:
		return nil, fmt.Sprintf("resultType=%s (unhandled)", res.ResultType)
	}
}

func labelsString(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func seriesSparkline(values [][2]float64, width int) string {
	floats := make([]float64, len(values))
	for i, v := range values {
		floats[i] = v[1]
	}
	return render.Render(floats, width)
}

func firstKey(m map[string]*promclient.Client) string {
	for k := range m {
		return k
	}
	return ""
}
