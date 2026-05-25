package trace_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/trace"
)

// fakeTempoMetricsServer returns an httptest.Server that maps the query
// expression (?q=…) to a canned Prometheus-shaped matrix response. Tempo's
// /api/metrics/query_range endpoint wraps the same {status, data:
// {resultType: matrix, result: [...]}} envelope Prometheus uses.
func fakeTempoMetricsServer(t *testing.T, byQuery map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/query_range", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		q := r.URL.Query().Get("q")
		body, ok := byQuery[q]
		if !ok {
			// Default empty matrix.
			body = `{"status":"success","data":{"resultType":"matrix","result":[]}}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

// totalEdgesBody mirrors a Tempo metrics-generator response for the
// `sum by (server, client) (rate(traces_service_graph_request_total[5m]))`
// query: 3 edges with distinct rates. The point pair is [ts, "value"] —
// strings on the value, ints on the ts — same shape as Prometheus.
const sgTotalBody = `{
  "status": "success",
  "data": {
    "resultType": "matrix",
    "result": [
      {"metric":{"client":"frontend","server":"checkout"},"values":[[1700000000,"10.5"],[1700000060,"12.0"]]},
      {"metric":{"client":"checkout","server":"payment"},"values":[[1700000000,"4.2"],[1700000060,"5.0"]]},
      {"metric":{"client":"checkout","server":"inventory"},"values":[[1700000000,"0.05"],[1700000060,"0.02"]]}
    ]
  }
}`

const sgFailedBody = `{
  "status": "success",
  "data": {
    "resultType": "matrix",
    "result": [
      {"metric":{"client":"frontend","server":"checkout"},"values":[[1700000060,"0.6"]]},
      {"metric":{"client":"checkout","server":"payment"},"values":[[1700000060,"0"]]}
    ]
  }
}`

// TestServiceGraph_ParsesEdgesAndComputesErrRate exercises:
//   - URL composition (/api/metrics/query_range with the right q=)
//   - error-rate join across the two queries (total + failed)
//   - min_req_rate filtering (the 0.02 edge is dropped)
//   - sort by req_rate desc
func TestServiceGraph_ParsesEdgesAndComputesErrRate(t *testing.T) {
	t.Parallel()
	srv := fakeTempoMetricsServer(t, map[string]string{
		`sum by (server, client) (rate(traces_service_graph_request_total[5m]))`:        sgTotalBody,
		`sum by (server, client) (rate(traces_service_graph_request_failed_total[5m]))`: sgFailedBody,
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

	tool, ok := reg.Get("trace.service_graph")
	if !ok {
		t.Fatal("trace.service_graph not registered")
	}
	obs, err := tool.Run(context.Background(), []byte(`{"name":"test","min_req_rate":0.1,"limit":10}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Only 2 edges survive min_req_rate=0.1 (inventory at 0.02 is dropped).
	if obs.Table == nil || len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 table rows, got %+v", obs.Table)
	}
	// Sorted by req_rate desc: frontend→checkout first (12.0 > 5.0).
	if obs.Table.Rows[0][0] != "frontend" || obs.Table.Rows[0][1] != "checkout" {
		t.Errorf("expected first row frontend→checkout, got %v", obs.Table.Rows[0])
	}
	if obs.Table.Rows[1][0] != "checkout" || obs.Table.Rows[1][1] != "payment" {
		t.Errorf("expected second row checkout→payment, got %v", obs.Table.Rows[1])
	}
	// frontend→checkout has 0.6 fail / 12 req = 5% error rate.
	if !strings.Contains(obs.Table.Rows[0][3], "5.00%") {
		t.Errorf("expected ~5%% err on first row, got %q", obs.Table.Rows[0][3])
	}
	// checkout→payment has zero failures.
	if !strings.Contains(obs.Table.Rows[1][3], "0.00%") {
		t.Errorf("expected 0%% err on second row, got %q", obs.Table.Rows[1][3])
	}
	// Text summary mentions the edge count.
	if !strings.Contains(obs.Text, "2 edges") {
		t.Errorf("expected '2 edges' in text, got %q", obs.Text)
	}
	// inventory must not appear (filtered by min_req_rate).
	for _, row := range obs.Table.Rows {
		if row[1] == "inventory" {
			t.Errorf("inventory edge should be filtered out, got %v", row)
		}
	}
}

// TestServiceGraph_TolerateMissingFailedSeries ensures we still produce a
// table when the failed_total query returns no series (some Tempo deploys
// haven't populated the counter yet).
func TestServiceGraph_TolerateMissingFailedSeries(t *testing.T) {
	t.Parallel()
	srv := fakeTempoMetricsServer(t, map[string]string{
		`sum by (server, client) (rate(traces_service_graph_request_total[5m]))`: sgTotalBody,
		// no failed_total entry; default empty matrix kicks in
	})
	defer srv.Close()

	cs, _ := trace.BuildClients([]config.HTTPEndpoint{
		{Name: "test", Kind: "tempo", URL: srv.URL},
	})
	reg := tools.New()
	trace.RegisterAll(reg, cs, nil)

	tool, _ := reg.Get("trace.service_graph")
	obs, err := tool.Run(context.Background(), []byte(`{"name":"test"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil || len(obs.Table.Rows) == 0 {
		t.Fatal("expected non-empty table")
	}
	// All err_rate values should be 0.00% when failed series is empty.
	for _, row := range obs.Table.Rows {
		if !strings.Contains(row[3], "0.00%") {
			t.Errorf("expected 0%% err with no failed series, got %q", row[3])
		}
	}
}
