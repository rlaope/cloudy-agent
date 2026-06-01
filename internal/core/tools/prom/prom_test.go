package prom_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools/prom"
)

// fakePromServer returns an httptest.Server that responds to Prometheus HTTP
// API paths with canned JSON.
func fakePromServer(t *testing.T, handlers map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range handlers {
		body := body // capture
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		})
	}
	return httptest.NewServer(mux)
}

const vectorResponse = `{
  "status": "success",
  "data": {
    "resultType": "vector",
    "result": [
      {
        "metric": {"__name__": "up", "job": "prometheus", "instance": "localhost:9090"},
        "value": [1609459200.123, "1"]
      }
    ]
  }
}`

const matrixResponse = `{
  "status": "success",
  "data": {
    "resultType": "matrix",
    "result": [
      {
        "metric": {"__name__": "http_requests_total", "job": "api"},
        "values": [
          [1609459200.0, "42"],
          [1609459260.0, "45"],
          [1609459320.0, "50"]
        ]
      }
    ]
  }
}`

const labelValuesResponse = `{
  "status": "success",
  "data": ["prometheus", "node-exporter", "grafana"]
}`

const seriesResponse = `{
  "status": "success",
  "data": [
    {"__name__": "up", "job": "prometheus", "instance": "localhost:9090"},
    {"__name__": "up", "job": "node-exporter", "instance": "localhost:9100"}
  ]
}`

// TestQuery_SendsCorrectPath verifies that prom.query issues GET /api/v1/query
// and parses the vector result into Observation.Table.
func TestQuery_SendsCorrectPath(t *testing.T) {
	srv := fakePromServer(t, map[string]string{
		"/api/v1/query": vectorResponse,
	})
	defer srv.Close()

	c, err := promclient.NewClient(srv.URL, nil, "", "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	clients := map[string]*promclient.Client{"default": c}
	tool := prom.NewQueryTool(clients)

	args, _ := json.Marshal(map[string]any{
		"query": "up",
	})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table in Observation")
	}
	if len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(obs.Table.Rows))
	}
	// Value column should be "1".
	if obs.Table.Rows[0][1] != "1" {
		t.Errorf("expected value=1, got %s", obs.Table.Rows[0][1])
	}
}

// TestQueryRange_ParsesMatrix verifies that prom.query_range parses matrix results.
func TestQueryRange_ParsesMatrix(t *testing.T) {
	srv := fakePromServer(t, map[string]string{
		"/api/v1/query_range": matrixResponse,
	})
	defer srv.Close()

	c, err := promclient.NewClient(srv.URL, nil, "", "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	clients := map[string]*promclient.Client{"default": c}
	tool := prom.NewQueryRangeTool(clients)

	start := time.Now().Add(-5 * time.Minute)
	end := time.Now()
	args, _ := json.Marshal(map[string]any{
		"query": "http_requests_total",
		"start": start.UTC().Format(time.RFC3339),
		"end":   end.UTC().Format(time.RFC3339),
		"step":  "1m",
	})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table in Observation")
	}
	// Matrix table: one row per series.
	if len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 series row, got %d", len(obs.Table.Rows))
	}
	// POINTS column should be "3".
	if obs.Table.Rows[0][1] != "3" {
		t.Errorf("expected POINTS=3, got %s", obs.Table.Rows[0][1])
	}
}

// TestLabelValues_ParsesValues verifies /api/v1/label/{label}/values path and parsing.
func TestLabelValues_ParsesValues(t *testing.T) {
	srv := fakePromServer(t, map[string]string{
		"/api/v1/label/job/values": labelValuesResponse,
	})
	defer srv.Close()

	c, err := promclient.NewClient(srv.URL, nil, "", "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	clients := map[string]*promclient.Client{"default": c}
	tool := prom.NewLabelValuesTool(clients)

	args, _ := json.Marshal(map[string]any{
		"label": "job",
	})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table")
	}
	if len(obs.Table.Rows) != 3 {
		t.Fatalf("expected 3 label values, got %d", len(obs.Table.Rows))
	}
}

// TestSeries_ParsesSeries verifies /api/v1/series path and parsing.
func TestSeries_ParsesSeries(t *testing.T) {
	srv := fakePromServer(t, map[string]string{
		"/api/v1/series": seriesResponse,
	})
	defer srv.Close()

	c, err := promclient.NewClient(srv.URL, nil, "", "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	clients := map[string]*promclient.Client{"default": c}
	tool := prom.NewSeriesTool(clients)

	args, _ := json.Marshal(map[string]any{
		"matchers": []string{"up"},
	})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table")
	}
	if len(obs.Table.Rows) != 2 {
		t.Fatalf("expected 2 series, got %d", len(obs.Table.Rows))
	}
}

// TestPromQLValidation verifies that bad PromQL is rejected before hitting the wire.
func TestPromQLValidation(t *testing.T) {
	// Use a server that should never be reached.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("HTTP request should not have been made for invalid PromQL")
	}))
	defer srv.Close()

	c, _ := promclient.NewClient(srv.URL, nil, "", "", "")
	clients := map[string]*promclient.Client{"default": c}
	tool := prom.NewQueryTool(clients)

	args, _ := json.Marshal(map[string]any{"query": "rate(http_requests_total[5m)"}) // unbalanced
	_, err := tool.Run(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for unbalanced PromQL, got nil")
	}
}
