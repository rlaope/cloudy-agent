package correlate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// TestMetricSource_MultiEndpointPicksDefault verifies that with more than one
// Prometheus endpoint configured, RecentChanges no longer errors (it used to
// pass q.Context as the endpoint name) and queries the deterministic default —
// the sorted-first key.
func TestMetricSource_MultiEndpointPicksDefault(t *testing.T) {
	emptyMatrix := `{"status":"success","data":{"resultType":"matrix","result":[]}}`

	hit := ""
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "prom-1"
		_, _ = w.Write([]byte(emptyMatrix))
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "prom-2"
		_, _ = w.Write([]byte(emptyMatrix))
	}))
	defer srv2.Close()

	c1, err := promclient.NewClient(srv1.URL, nil, "", "", "")
	if err != nil {
		t.Fatalf("NewClient prom-1: %v", err)
	}
	c2, err := promclient.NewClient(srv2.URL, nil, "", "", "")
	if err != nil {
		t.Fatalf("NewClient prom-2: %v", err)
	}

	src := newMetricSource(map[string]*promclient.Client{"prom-2": c2, "prom-1": c1}, "up", 1.0)
	if src == nil {
		t.Fatal("newMetricSource returned nil")
	}

	// q.Context is the k8s context, NOT an endpoint name — this used to error.
	_, err = src.RecentChanges(context.Background(), change.ChangeQuery{Context: "kind-cloudy-test", Workload: "api"})
	if err != nil {
		t.Fatalf("RecentChanges errored on multi-endpoint map: %v", err)
	}
	if hit != "prom-1" {
		t.Errorf("queried endpoint = %q, want sorted-first %q", hit, "prom-1")
	}
}

func TestMetricBreachEvents_SingleBreach(t *testing.T) {
	// Series crosses threshold at T=1000 (value 5.0 > threshold 4.0).
	res := &promclient.Result{
		ResultType: "matrix",
		Matrix: []promclient.Series{
			{
				Labels: map[string]string{"job": "test"},
				Values: [][2]float64{
					{900, 1.0},
					{950, 3.9},
					{1000, 5.0},
					{1050, 6.0},
				},
			},
		},
	}

	events := metricBreachEvents(res, 4.0, "test_query")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Kind != "metric_breach" {
		t.Errorf("kind: got %q, want %q", e.Kind, "metric_breach")
	}
	if e.Source != "metric" {
		t.Errorf("source: got %q, want %q", e.Source, "metric")
	}
	want := time.Unix(1000, 0)
	if !e.Time.Equal(want) {
		t.Errorf("time: got %v, want %v", e.Time, want)
	}
}

func TestMetricBreachEvents_EarliestAcrossMultipleSeries(t *testing.T) {
	// Series A breaches at T=2000; Series B breaches earlier at T=1500.
	// Expect event time == 1500.
	res := &promclient.Result{
		ResultType: "matrix",
		Matrix: []promclient.Series{
			{
				Labels: map[string]string{"instance": "a"},
				Values: [][2]float64{
					{1000, 0.5},
					{2000, 10.0},
				},
			},
			{
				Labels: map[string]string{"instance": "b"},
				Values: [][2]float64{
					{1000, 0.1},
					{1500, 7.0},
					{2000, 8.0},
				},
			},
		},
	}

	events := metricBreachEvents(res, 5.0, "multi_series")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	want := time.Unix(1500, 0)
	if !events[0].Time.Equal(want) {
		t.Errorf("time: got %v, want %v", events[0].Time, want)
	}
}

func TestMetricBreachEvents_NoBreach(t *testing.T) {
	res := &promclient.Result{
		ResultType: "matrix",
		Matrix: []promclient.Series{
			{
				Labels: map[string]string{"job": "test"},
				Values: [][2]float64{
					{1000, 1.0},
					{2000, 2.0},
					{3000, 3.0},
				},
			},
		},
	}

	events := metricBreachEvents(res, 5.0, "no_breach")
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestMetricBreachEvents_EmptyResult(t *testing.T) {
	if events := metricBreachEvents(nil, 1.0, "q"); len(events) != 0 {
		t.Errorf("nil result: expected 0 events, got %d", len(events))
	}
	if events := metricBreachEvents(&promclient.Result{}, 1.0, "q"); len(events) != 0 {
		t.Errorf("empty matrix: expected 0 events, got %d", len(events))
	}
}

func TestMetricBreachEvents_ThresholdZeroSemantics(t *testing.T) {
	// value == 0 must NOT breach (strictly >); value > 0 must breach.
	res := &promclient.Result{
		ResultType: "matrix",
		Matrix: []promclient.Series{
			{
				Labels: map[string]string{},
				Values: [][2]float64{
					{100, 0.0},
					{200, 0.001},
				},
			},
		},
	}

	events := metricBreachEvents(res, 0.0, "zero_threshold")
	if len(events) != 1 {
		t.Fatalf("expected 1 event (value 0.001 > 0), got %d", len(events))
	}
	want := time.Unix(200, 0)
	if !events[0].Time.Equal(want) {
		t.Errorf("time: got %v, want %v", events[0].Time, want)
	}

	// Confirm value exactly == 0 yields no breach.
	resZero := &promclient.Result{
		ResultType: "matrix",
		Matrix: []promclient.Series{
			{Labels: map[string]string{}, Values: [][2]float64{{100, 0.0}}},
		},
	}
	if ev := metricBreachEvents(resZero, 0.0, "zero_value"); len(ev) != 0 {
		t.Errorf("value==0 with threshold==0: expected no breach, got %d events", len(ev))
	}
}
