package trace

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// newTempoRouteREDTool surfaces per-route RED metrics (Rate / Error /
// Duration) for one service via Tempo's metrics-generator span-metrics. The
// metric family produced by the span_metrics processor is:
//
//	traces_spanmetrics_latency_bucket{service, span_name, le, ...}
//	traces_spanmetrics_calls_total{service, span_name, status_code, ...}
//
// We issue four queries (p50/p95/p99 from histogram_quantile, total call
// rate, error rate via status_code="STATUS_CODE_ERROR") and join them per
// span_name (= route) for one (service, route) table.
func newTempoRouteREDTool(clients map[string]*TempoClient) tools.Tool {
	type args struct {
		Name    string `json:"name"`
		Service string `json:"service"`
		Since   string `json:"since"`
		Until   string `json:"until"`
		Limit   int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    tempoEndpointSchema,
			"service": map[string]any{"type": "string", "description": "Service name (required); matched against the service label on span-metrics."},
			"since":   map[string]any{"type": "string", "description": "Range start in RFC3339. Default = until - 5m."},
			"until":   map[string]any{"type": "string", "description": "Range end in RFC3339. Default = now."},
			"limit":   map[string]any{"type": "integer", "description": "Max routes to render (default 30, max 500).", "default": 30, "minimum": 1, "maximum": 500},
		},
		"required": []string{"service"},
	})
	return tools.Spec[args]{
		Name:        "trace.route_red",
		Description: "Per-route RED metrics (Rate, Error rate, p50/p95/p99 Duration) for one service via Tempo's metrics-generator span-metrics. Returns a table keyed by span_name.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Service == "" {
				return tools.Observation{}, fmt.Errorf("trace.route_red: service is required")
			}
			if a.Limit <= 0 {
				a.Limit = 30
			}
			if a.Limit > 500 {
				a.Limit = 500
			}
			until, since, err := resolveRange(a.Until, a.Since, 5*time.Minute)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("trace.route_red: %w", err)
			}
			c, err := pickTempo(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}

			svc := promEscape(a.Service)
			rateQ := fmt.Sprintf(`sum by (span_name) (rate(traces_spanmetrics_calls_total{service="%s"}[5m]))`, svc)
			errQ := fmt.Sprintf(`sum by (span_name) (rate(traces_spanmetrics_calls_total{service="%s", status_code="STATUS_CODE_ERROR"}[5m]))`, svc)
			p50Q := fmt.Sprintf(`histogram_quantile(0.5, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="%s"}[5m])))`, svc)
			p95Q := fmt.Sprintf(`histogram_quantile(0.95, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="%s"}[5m])))`, svc)
			p99Q := fmt.Sprintf(`histogram_quantile(0.99, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="%s"}[5m])))`, svc)

			rateSeries, _, err := queryTempoMetrics(ctx, c, rateQ, since, until)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("trace.route_red: rate: %w", err)
			}
			errSeries, _, _ := queryTempoMetrics(ctx, c, errQ, since, until)
			p50Series, _, _ := queryTempoMetrics(ctx, c, p50Q, since, until)
			p95Series, _, _ := queryTempoMetrics(ctx, c, p95Q, since, until)
			p99Series, _, _ := queryTempoMetrics(ctx, c, p99Q, since, until)

			routes := buildRoutes(rateSeries, errSeries, p50Series, p95Series, p99Series)
			tbl, text := renderRoutes(a.Service, routes, a.Limit)

			return tools.Observation{
				Text:  text,
				Table: tbl,
				Raw: map[string]any{
					"service":     a.Service,
					"rate_query":  rateQ,
					"error_query": errQ,
					"p50_query":   p50Q,
					"p95_query":   p95Q,
					"p99_query":   p99Q,
					"routes":      routes,
				},
			}, nil
		},
	}.Build()
}

// routeRED is one (service, route) row.
type routeRED struct {
	Route   string  `json:"route"`
	ReqRate float64 `json:"req_rate"`
	ErrRate float64 `json:"err_rate"`
	P50     float64 `json:"p50"`
	P95     float64 `json:"p95"`
	P99     float64 `json:"p99"`
}

func buildRoutes(rateSeries, errSeries, p50, p95, p99 []tempoMetricSeries) []routeRED {
	rateBy := indexBySpanName(rateSeries)
	errBy := indexBySpanName(errSeries)
	p50By := indexBySpanName(p50)
	p95By := indexBySpanName(p95)
	p99By := indexBySpanName(p99)

	// Union of route names across all queries; rate is the canonical key
	// (no rate ⇒ nothing useful to show).
	out := make([]routeRED, 0, len(rateBy))
	for route, req := range rateBy {
		errCount := errBy[route]
		errRate := 0.0
		if req > 0 {
			errRate = errCount / req
		}
		out = append(out, routeRED{
			Route:   route,
			ReqRate: req,
			ErrRate: errRate,
			P50:     p50By[route],
			P95:     p95By[route],
			P99:     p99By[route],
		})
	}
	// Highest req rate first; ties broken by route name for deterministic output.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ReqRate != out[j].ReqRate {
			return out[i].ReqRate > out[j].ReqRate
		}
		return out[i].Route < out[j].Route
	})
	return out
}

func indexBySpanName(series []tempoMetricSeries) map[string]float64 {
	out := make(map[string]float64, len(series))
	for _, s := range series {
		name := s.Labels["span_name"]
		if name == "" {
			continue
		}
		out[name] = s.LastValue
	}
	return out
}

func renderRoutes(service string, routes []routeRED, limit int) (*render.Table, string) {
	tbl := &render.Table{
		Headers: []string{"ROUTE", "REQ_PER_SEC", "ERR_RATE", "P50_S", "P95_S", "P99_S"},
		Aligns: []render.Align{
			render.AlignLeft, render.AlignRight, render.AlignRight,
			render.AlignRight, render.AlignRight, render.AlignRight,
		},
	}
	shown := len(routes)
	if shown > limit {
		shown = limit
	}
	for i := 0; i < shown; i++ {
		r := routes[i]
		tbl.Rows = append(tbl.Rows, []string{
			r.Route,
			strconv.FormatFloat(r.ReqRate, 'f', 3, 64),
			strconv.FormatFloat(r.ErrRate*100, 'f', 2, 64) + "%",
			strconv.FormatFloat(r.P50, 'f', 4, 64),
			strconv.FormatFloat(r.P95, 'f', 4, 64),
			strconv.FormatFloat(r.P99, 'f', 4, 64),
		})
	}
	if len(routes) == 0 {
		return tbl, fmt.Sprintf("(no routes for service=%s)", service)
	}
	text := fmt.Sprintf("service=%s: %d routes (showing %d)", service, len(routes), shown)
	return tbl, text
}

// promEscape escapes a value for inclusion as a PromQL string literal.
// We only need to handle '\' and '"' since the caller is wrapped in double
// quotes; newlines etc. are rejected upstream by the schema (string field).
func promEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' || c == '"' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(out)
}
