package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// newTempoServiceGraphTool calls Tempo's metrics-generator service-graph
// counters via /api/metrics/query_range, which is the PromQL-compatible
// metrics endpoint exposed by Tempo (>= 2.0). The metric names produced by
// the metrics-generator service_graphs processor are:
//
//	traces_service_graph_request_total{client,server,...}
//	traces_service_graph_request_failed_total{client,server,...}
//
// We issue two queries (total + failed), compute err_rate per edge as
// failed/total, drop edges below min_req_rate, and return one row per
// (client → server) pair.
func newTempoServiceGraphTool(clients map[string]*TempoClient) tools.Tool {
	type args struct {
		Name       string  `json:"name"`
		Since      string  `json:"since"`
		Until      string  `json:"until"`
		MinReqRate float64 `json:"min_req_rate"`
		Limit      int     `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":         tempoEndpointSchema,
			"since":        map[string]any{"type": "string", "description": "Range start in RFC3339. Default = until - 5m."},
			"until":        map[string]any{"type": "string", "description": "Range end in RFC3339. Default = now."},
			"min_req_rate": map[string]any{"type": "number", "description": "Drop edges with req_rate (req/s) below this threshold. Default 0.1.", "default": 0.1},
			"limit":        map[string]any{"type": "integer", "description": "Max edges to render (default 50, max 500).", "default": 50, "minimum": 1, "maximum": 500},
		},
	})
	return tools.Spec[args]{
		Name:        "trace.service_graph",
		Description: "Derive the service call graph from Tempo's metrics-generator (traces_service_graph_request_total / _failed_total). Returns edges (caller → callee, req/s, error_rate).",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Limit <= 0 {
				a.Limit = 50
			}
			if a.Limit > 500 {
				a.Limit = 500
			}
			if a.MinReqRate < 0 {
				a.MinReqRate = 0
			}
			if a.MinReqRate == 0 {
				a.MinReqRate = 0.1
			}
			until, since, err := resolveRange(a.Until, a.Since, 5*time.Minute)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("trace.service_graph: %w", err)
			}
			c, err := pickTempo(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}

			totalQ := `sum by (server, client) (rate(traces_service_graph_request_total[5m]))`
			failedQ := `sum by (server, client) (rate(traces_service_graph_request_failed_total[5m]))`

			totals, rawTotal, err := queryTempoMetrics(ctx, c, totalQ, since, until)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("trace.service_graph: total: %w", err)
			}
			failures, _, err := queryTempoMetrics(ctx, c, failedQ, since, until)
			if err != nil {
				// Some Tempo deployments may not have the failed counter populated yet;
				// surface the edges without error rate rather than failing the whole call.
				failures = nil
			}

			edges := buildEdges(totals, failures, a.MinReqRate)
			tbl, text := renderEdges(edges, a.Limit)

			return tools.Observation{
				Text:  text,
				Table: tbl,
				Raw: map[string]any{
					"total_query":  totalQ,
					"failed_query": failedQ,
					"total_body":   rawTotal,
					"edges":        edges,
				},
			}, nil
		},
	}.Build()
}

// serviceGraphEdge is a single (client → server) edge in the derived graph.
type serviceGraphEdge struct {
	Client   string  `json:"client"`
	Server   string  `json:"server"`
	ReqRate  float64 `json:"req_rate"`
	FailRate float64 `json:"fail_rate"`
	ErrRate  float64 `json:"err_rate"`
}

func buildEdges(totals, failures []tempoMetricSeries, minReqRate float64) []serviceGraphEdge {
	// Index failures by (client,server) for fast lookup.
	failIdx := map[string]float64{}
	for _, s := range failures {
		key := s.Labels["client"] + "→" + s.Labels["server"]
		failIdx[key] = s.LastValue
	}
	out := make([]serviceGraphEdge, 0, len(totals))
	for _, s := range totals {
		req := s.LastValue
		if req < minReqRate {
			continue
		}
		client := s.Labels["client"]
		server := s.Labels["server"]
		key := client + "→" + server
		fail := failIdx[key]
		errRate := 0.0
		if req > 0 {
			errRate = fail / req
		}
		out = append(out, serviceGraphEdge{
			Client:   client,
			Server:   server,
			ReqRate:  req,
			FailRate: fail,
			ErrRate:  errRate,
		})
	}
	// Stable sort: highest req rate first.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ReqRate != out[j].ReqRate {
			return out[i].ReqRate > out[j].ReqRate
		}
		return out[i].Client+out[i].Server < out[j].Client+out[j].Server
	})
	return out
}

func renderEdges(edges []serviceGraphEdge, limit int) (*render.Table, string) {
	tbl := &render.Table{
		Headers: []string{"CALLER", "CALLEE", "REQ_PER_SEC", "ERR_RATE"},
		Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignRight, render.AlignRight},
	}
	shown := len(edges)
	if shown > limit {
		shown = limit
	}
	for i := 0; i < shown; i++ {
		e := edges[i]
		tbl.Rows = append(tbl.Rows, []string{
			e.Client,
			e.Server,
			strconv.FormatFloat(e.ReqRate, 'f', 3, 64),
			strconv.FormatFloat(e.ErrRate*100, 'f', 2, 64) + "%",
		})
	}
	if len(edges) == 0 {
		return tbl, "(no service-graph edges above threshold)"
	}
	text := fmt.Sprintf("%d edges (showing %d)", len(edges), shown)
	return tbl, text
}

// resolveRange normalises optional RFC3339 since/until into a (until, since)
// pair with the supplied default window.
func resolveRange(untilRaw, sinceRaw string, defaultWindow time.Duration) (time.Time, time.Time, error) {
	var (
		until time.Time
		since time.Time
		err   error
	)
	if untilRaw != "" {
		until, err = time.Parse(time.RFC3339, untilRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parse until: %w", err)
		}
	} else {
		until = time.Now()
	}
	if sinceRaw != "" {
		since, err = time.Parse(time.RFC3339, sinceRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parse since: %w", err)
		}
	} else {
		since = until.Add(-defaultWindow)
	}
	if !until.After(since) {
		return time.Time{}, time.Time{}, fmt.Errorf("until (%s) must be after since (%s)", until.Format(time.RFC3339), since.Format(time.RFC3339))
	}
	return until, since, nil
}

// tempoMetricSeries is one matrix series returned by Tempo's
// /api/metrics/query_range endpoint. LastValue is the most recent point's
// value, which is the per-edge / per-route summary we want to render.
type tempoMetricSeries struct {
	Labels    map[string]string `json:"labels"`
	LastValue float64           `json:"last_value"`
}

// queryTempoMetrics issues a GET /api/metrics/query_range and returns the
// flattened (labels, lastValue) series list. The endpoint is the metrics-
// generator's PromQL-compatible query API; Tempo wraps the same {status,
// data: {resultType: matrix, result: [{metric, values: [[ts, "v"], ...]}]}}
// envelope that Prometheus uses.
func queryTempoMetrics(ctx context.Context, c *TempoClient, q string, since, until time.Time) ([]tempoMetricSeries, []byte, error) {
	params := url.Values{
		"q":     {q},
		"start": {strconv.FormatInt(since.Unix(), 10)},
		"end":   {strconv.FormatInt(until.Unix(), 10)},
		"step":  {"60s"},
	}
	body, err := c.RawGet(ctx, "/api/metrics/query_range", params)
	if err != nil {
		return nil, nil, err
	}
	series, perr := parseTempoMatrix(body)
	if perr != nil {
		return nil, body, fmt.Errorf("decode: %w", perr)
	}
	return series, body, nil
}

// parseTempoMatrix decodes the Prometheus-shaped matrix envelope that
// Tempo's metrics endpoint returns and reduces each series to its last
// observed value.
func parseTempoMatrix(body []byte) ([]tempoMetricSeries, error) {
	var env struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]json.Number  `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	if env.Status != "" && env.Status != "success" {
		return nil, fmt.Errorf("tempo metrics status=%s", env.Status)
	}
	out := make([]tempoMetricSeries, 0, len(env.Data.Result))
	for _, s := range env.Data.Result {
		if len(s.Values) == 0 {
			continue
		}
		last := s.Values[len(s.Values)-1]
		v, err := last[1].Float64()
		if err != nil {
			continue
		}
		out = append(out, tempoMetricSeries{
			Labels:    s.Metric,
			LastValue: v,
		})
	}
	return out, nil
}
