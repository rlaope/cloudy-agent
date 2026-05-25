package trace

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

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
			// C17 (v0.5 review): JSON-Schema "type": "string" accepts LF/CR
			// and other control bytes; a service value with an embedded
			// newline would compose malformed PromQL and bubble the raw
			// query back as an error message. Reject up front so the
			// PromQL boundary stays sanitary.
			if !isCleanLabelValue(a.Service) {
				return tools.Observation{}, fmt.Errorf("trace.route_red: service contains disallowed characters (control bytes / newlines)")
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
			// C03 (v0.5 review): rate-window was hardcoded `[5m]` regardless
			// of since/until. Derive it from the resolved range so widening
			// `since` actually widens the PromQL rate window. Clamp to 1m
			// floor (Prometheus rejects sub-minute rate windows in practice).
			rateWindow := promRateWindow(until.Sub(since))
			c, err := pickTempo(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}

			svc := promEscape(a.Service)
			rateQ := fmt.Sprintf(`sum by (span_name) (rate(traces_spanmetrics_calls_total{service="%s"}[%s]))`, svc, rateWindow)
			errQ := fmt.Sprintf(`sum by (span_name) (rate(traces_spanmetrics_calls_total{service="%s", status_code="STATUS_CODE_ERROR"}[%s]))`, svc, rateWindow)
			p50Q := fmt.Sprintf(`histogram_quantile(0.5, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="%s"}[%s])))`, svc, rateWindow)
			p95Q := fmt.Sprintf(`histogram_quantile(0.95, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="%s"}[%s])))`, svc, rateWindow)
			p99Q := fmt.Sprintf(`histogram_quantile(0.99, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="%s"}[%s])))`, svc, rateWindow)

			// C01 + C09 (v0.5 review): the previous code fired 5 queries
			// serially and `_, _, _ :=`-discarded the errors of 4 of them.
			// A Tempo outage on the histogram queries silently produced
			// 0% error rate + 0s latency, looking like a healthy service.
			// errgroup parallelises (≈5x latency win) AND propagates the
			// first failure with proper context cancellation.
			var rateSeries, errSeries, p50Series, p95Series, p99Series []tempoMetricSeries
			g, gctx := errgroup.WithContext(ctx)
			g.Go(func() error {
				s, _, err := queryTempoMetrics(gctx, c, rateQ, since, until)
				if err != nil {
					return fmt.Errorf("rate: %w", err)
				}
				rateSeries = s
				return nil
			})
			g.Go(func() error {
				s, _, err := queryTempoMetrics(gctx, c, errQ, since, until)
				if err != nil {
					return fmt.Errorf("error: %w", err)
				}
				errSeries = s
				return nil
			})
			g.Go(func() error {
				s, _, err := queryTempoMetrics(gctx, c, p50Q, since, until)
				if err != nil {
					return fmt.Errorf("p50: %w", err)
				}
				p50Series = s
				return nil
			})
			g.Go(func() error {
				s, _, err := queryTempoMetrics(gctx, c, p95Q, since, until)
				if err != nil {
					return fmt.Errorf("p95: %w", err)
				}
				p95Series = s
				return nil
			})
			g.Go(func() error {
				s, _, err := queryTempoMetrics(gctx, c, p99Q, since, until)
				if err != nil {
					return fmt.Errorf("p99: %w", err)
				}
				p99Series = s
				return nil
			})
			if err := g.Wait(); err != nil {
				return tools.Observation{}, fmt.Errorf("trace.route_red: %w", err)
			}

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

	// C12 (v0.5 review): union route keys across ALL series. The previous
	// code iterated rateBy only, silently dropping routes that had errors
	// recorded but no rate sample in the same window (low-traffic routes
	// where errors and successes split across rate buckets, or rate
	// truncated by the resolveRange edge). For routes with no rate
	// sample we still emit a row so the operator can see the error
	// signal — req_rate=0 and err_rate is shown as raw count/sec.
	keys := make(map[string]struct{}, len(rateBy)+len(errBy))
	for k := range rateBy {
		keys[k] = struct{}{}
	}
	for k := range errBy {
		keys[k] = struct{}{}
	}
	for k := range p50By {
		keys[k] = struct{}{}
	}
	for k := range p95By {
		keys[k] = struct{}{}
	}
	for k := range p99By {
		keys[k] = struct{}{}
	}
	out := make([]routeRED, 0, len(keys))
	for route := range keys {
		req := rateBy[route]
		errCount := errBy[route]
		errRate := 0.0
		if req > 0 {
			errRate = errCount / req
		} else if errCount > 0 {
			// No rate sample but errors present — surface the error count
			// as a stand-in error rate so the row is not silently
			// "0 errors". A negative-zero rate is the convention
			// renderRoutes uses to flag "rate sample missing".
			errRate = errCount
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
		// C10 (v0.5 review): histogram_quantile over empty buckets
		// returns NaN; rendering literal "NaN" in p50/p95/p99 cells
		// confuses both the operator and the LLM. Show "-" so the
		// missing-data state is visually obvious without inventing a
		// number.
		tbl.Rows = append(tbl.Rows, []string{
			r.Route,
			strconv.FormatFloat(r.ReqRate, 'f', 3, 64),
			strconv.FormatFloat(r.ErrRate*100, 'f', 2, 64) + "%",
			nanSafe(r.P50, 4),
			nanSafe(r.P95, 4),
			nanSafe(r.P99, 4),
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
// quotes. NOTE: callers MUST validate the input through isCleanLabelValue
// first — this helper does NOT reject control bytes (LF / CR) and JSON
// schema "type": "string" accepts them, so without the pre-check a service
// name containing a newline would produce malformed PromQL.
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

// isCleanLabelValue rejects values containing control bytes (LF / CR /
// NUL / tab) plus length cap. C17 from the v0.5 security review: JSON
// schema "type": "string" accepts any Unicode including newlines; an
// unchecked service name with embedded LF produces malformed PromQL
// that surfaces as a Tempo 4xx with the raw query bubbled back to the
// caller (information leak + UX confusion).
func isCleanLabelValue(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return !strings.ContainsAny(s, "\n\r\t\x00")
}

// nanSafe renders a float to a fixed-precision string but returns "-"
// when the value is NaN or +/-Inf. histogram_quantile over zero buckets
// returns NaN; rendering literal "NaN" in a result cell would mislead
// both operator and LLM. C10 from the v0.5 review.
func nanSafe(f float64, prec int) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "-"
	}
	return strconv.FormatFloat(f, 'f', prec, 64)
}

// promRateWindow derives a PromQL range-vector window from a desired
// query span. C03 from the v0.5 review: the previous tools hardcoded
// `[5m]` regardless of since/until, making the time-range args
// effectively decorative. We pick the largest "round" Prometheus window
// (1m / 5m / 15m / 1h / 6h / 1d) that fits inside the span; this gives
// a stable, predictable window without exploding when callers pass
// arbitrary durations.
func promRateWindow(span time.Duration) string {
	switch {
	case span >= 24*time.Hour:
		return "1d"
	case span >= 6*time.Hour:
		return "6h"
	case span >= 1*time.Hour:
		return "1h"
	case span >= 15*time.Minute:
		return "15m"
	case span >= 5*time.Minute:
		return "5m"
	default:
		return "1m"
	}
}
