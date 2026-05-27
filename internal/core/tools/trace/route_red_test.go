package trace_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/trace"
)

// route_red issues five queries — rate, errors, p50, p95, p99 — for one
// service. The fake server returns two routes (GET /cart, POST /checkout)
// with distinct p99 latencies so we can assert the table ordering and the
// histogram_quantile rendering path.

const rateBody = `{
  "status":"success",
  "data":{"resultType":"matrix","result":[
    {"metric":{"span_name":"GET /cart"},"values":[[1700000060,"50.0"]]},
    {"metric":{"span_name":"POST /checkout"},"values":[[1700000060,"5.0"]]}
  ]}
}`

const errBody = `{
  "status":"success",
  "data":{"resultType":"matrix","result":[
    {"metric":{"span_name":"POST /checkout"},"values":[[1700000060,"0.25"]]}
  ]}
}`

const p50Body = `{
  "status":"success",
  "data":{"resultType":"matrix","result":[
    {"metric":{"span_name":"GET /cart"},"values":[[1700000060,"0.012"]]},
    {"metric":{"span_name":"POST /checkout"},"values":[[1700000060,"0.080"]]}
  ]}
}`

const p95Body = `{
  "status":"success",
  "data":{"resultType":"matrix","result":[
    {"metric":{"span_name":"GET /cart"},"values":[[1700000060,"0.040"]]},
    {"metric":{"span_name":"POST /checkout"},"values":[[1700000060,"0.250"]]}
  ]}
}`

const p99Body = `{
  "status":"success",
  "data":{"resultType":"matrix","result":[
    {"metric":{"span_name":"GET /cart"},"values":[[1700000060,"0.090"]]},
    {"metric":{"span_name":"POST /checkout"},"values":[[1700000060,"0.500"]]}
  ]}
}`

// TestRouteRED_RendersPerRouteTable exercises URL composition, the
// histogram_quantile rendering path, error-rate join across the rate +
// error queries, and the highest-rate-first sort.
func TestRouteRED_RendersPerRouteTable(t *testing.T) {
	t.Parallel()
	srv := fakeTempoMetricsServer(t, map[string]string{
		`sum by (span_name) (rate(traces_spanmetrics_calls_total{service="checkout"}[5m]))`:                                  rateBody,
		`sum by (span_name) (rate(traces_spanmetrics_calls_total{service="checkout", status_code="STATUS_CODE_ERROR"}[5m]))`: errBody,
		`histogram_quantile(0.5, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="checkout"}[5m])))`:  p50Body,
		`histogram_quantile(0.95, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="checkout"}[5m])))`: p95Body,
		`histogram_quantile(0.99, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="checkout"}[5m])))`: p99Body,
	})
	defer srv.Close()

	cs, skips := trace.BuildClients([]config.HTTPEndpoint{
		{Name: "test", Kind: "tempo", URL: srv.URL},
	})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	reg := tools.New()
	trace.RegisterAll(reg, cs, nil)

	tool, ok := reg.Get("trace.route_red")
	if !ok {
		t.Fatal("trace.route_red not registered")
	}
	obs, err := tool.Run(context.Background(), []byte(`{"name":"test","service":"checkout","limit":10}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %+v", obs.Table)
	}
	// Sorted by req_rate desc: GET /cart (50) before POST /checkout (5).
	if obs.Table.Rows[0][0] != "GET /cart" {
		t.Errorf("expected GET /cart first, got %q", obs.Table.Rows[0][0])
	}
	if obs.Table.Rows[1][0] != "POST /checkout" {
		t.Errorf("expected POST /checkout second, got %q", obs.Table.Rows[1][0])
	}
	// POST /checkout: 0.25 err / 5 req = 5% err rate.
	if !strings.Contains(obs.Table.Rows[1][2], "5.00%") {
		t.Errorf("expected 5%% err on POST /checkout, got %q", obs.Table.Rows[1][2])
	}
	// GET /cart should have 0% err (no entry in errBody).
	if !strings.Contains(obs.Table.Rows[0][2], "0.00%") {
		t.Errorf("expected 0%% err on GET /cart, got %q", obs.Table.Rows[0][2])
	}
	// p99 column index = 5. POST /checkout p99 = 0.5000s; GET /cart = 0.0900s.
	if !strings.HasPrefix(obs.Table.Rows[0][5], "0.0900") {
		t.Errorf("expected p99 0.0900 on GET /cart, got %q", obs.Table.Rows[0][5])
	}
	if !strings.HasPrefix(obs.Table.Rows[1][5], "0.5000") {
		t.Errorf("expected p99 0.5000 on POST /checkout, got %q", obs.Table.Rows[1][5])
	}
	if !strings.Contains(obs.Text, "service=checkout") {
		t.Errorf("expected service=checkout in text, got %q", obs.Text)
	}
}

// TestRouteRED_ServiceRequired confirms the schema constraint is enforced.
func TestRouteRED_ServiceRequired(t *testing.T) {
	t.Parallel()
	srv := fakeTempoMetricsServer(t, nil)
	defer srv.Close()

	cs, _ := trace.BuildClients([]config.HTTPEndpoint{
		{Name: "test", Kind: "tempo", URL: srv.URL},
	})
	reg := tools.New()
	trace.RegisterAll(reg, cs, nil)

	tool, _ := reg.Get("trace.route_red")
	_, err := tool.Run(context.Background(), []byte(`{"name":"test"}`))
	if err == nil || !strings.Contains(err.Error(), "service is required") {
		t.Errorf("expected service-required error, got %v", err)
	}
}
