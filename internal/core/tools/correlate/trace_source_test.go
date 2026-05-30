package correlate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools/change"
	"github.com/rlaope/cloudy/internal/core/tools/trace"
)

// TestTraceSource_MultiEndpointPicksDefault verifies that with more than one
// Jaeger endpoint configured, RecentChanges no longer errors (it used to pass
// q.Context as the endpoint name) and queries the deterministic default — the
// sorted-first key.
func TestTraceSource_MultiEndpointPicksDefault(t *testing.T) {
	emptyData := `{"data":[]}`

	hit := ""
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "jaeger-1"
		_, _ = w.Write([]byte(emptyData))
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "jaeger-2"
		_, _ = w.Write([]byte(emptyData))
	}))
	defer srv2.Close()

	c1, err := httpapi.NewClient("jaeger-1", srv1.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient jaeger-1: %v", err)
	}
	c2, err := httpapi.NewClient("jaeger-2", srv2.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient jaeger-2: %v", err)
	}

	traces := trace.Clients{Jaeger: map[string]*trace.JaegerClient{
		"jaeger-2": {Client: c2},
		"jaeger-1": {Client: c1},
	}}
	src := newTraceSource(traces)
	if src == nil {
		t.Fatal("newTraceSource returned nil")
	}

	// q.Context is the k8s context, NOT an endpoint name — this used to error.
	_, err = src.RecentChanges(context.Background(), change.ChangeQuery{Context: "kind-cloudy-test", Workload: "api"})
	if err != nil {
		t.Fatalf("RecentChanges errored on multi-endpoint map: %v", err)
	}
	if hit != "jaeger-1" {
		t.Errorf("queried endpoint = %q, want sorted-first %q", hit, "jaeger-1")
	}
}

func TestTraceSymptomEvents(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)

	t.Run("two error spans emit one trace_error at the earliest start", func(t *testing.T) {
		spans := []trace.JaegerSpan{
			{StartTime: base.Add(30 * time.Second), Duration: 200 * time.Millisecond, Error: true, Operation: "POST /pay"},
			{StartTime: base.Add(5 * time.Second), Duration: 150 * time.Millisecond, Error: true, Operation: "POST /pay"},
		}
		got := traceSymptomEvents(spans, "checkout")
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		e := got[0]
		if e.Kind != "trace_error" {
			t.Errorf("Kind = %q, want trace_error", e.Kind)
		}
		if !e.Time.Equal(base.Add(5 * time.Second)) {
			t.Errorf("Time = %v, want earliest error span start %v", e.Time, base.Add(5*time.Second))
		}
		if e.Target != "checkout" {
			t.Errorf("Target = %q, want checkout", e.Target)
		}
		if e.Source != "trace" {
			t.Errorf("Source = %q, want trace", e.Source)
		}
		if !strings.Contains(e.Summary, "2 error span") {
			t.Errorf("Summary = %q, want it to report count=2", e.Summary)
		}
	})

	t.Run("no error spans emit zero events", func(t *testing.T) {
		spans := []trace.JaegerSpan{
			{StartTime: base, Duration: 50 * time.Millisecond, Error: false, Operation: "GET /health"},
		}
		if got := traceSymptomEvents(spans, "checkout"); len(got) != 0 {
			t.Fatalf("len = %d, want 0 (no error or slow spans)", len(got))
		}
	})

	t.Run("slow non-error span emits one trace_slow event", func(t *testing.T) {
		spans := []trace.JaegerSpan{
			{StartTime: base, Duration: 2 * time.Second, Error: false, Operation: "GET /report"},
		}
		got := traceSymptomEvents(spans, "checkout")
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		e := got[0]
		if e.Kind != "trace_slow" {
			t.Errorf("Kind = %q, want trace_slow", e.Kind)
		}
		if !e.Time.Equal(base) {
			t.Errorf("Time = %v, want %v", e.Time, base)
		}
		if e.Source != "trace" {
			t.Errorf("Source = %q, want trace", e.Source)
		}
	})
}

func TestNewTraceSource(t *testing.T) {
	if newTraceSource(trace.Clients{}) != nil {
		t.Error("newTraceSource with no clients should be nil")
	}
	if newTraceSource(trace.Clients{Tempo: map[string]*trace.TempoClient{"t": {}}}) == nil {
		t.Error("newTraceSource with a Tempo client should be non-nil (v3)")
	}
	if newTraceSource(trace.Clients{Jaeger: map[string]*trace.JaegerClient{"j": {}}}) == nil {
		t.Error("newTraceSource with a Jaeger client should be non-nil")
	}
}

func TestTempoTraceQL(t *testing.T) {
	if got := tempoTraceQL("api", "", "status = error"); got != `{ resource.service.name="api" && status = error }` {
		t.Errorf("no namespace: got %q", got)
	}
	got := tempoTraceQL("api", "prod", "status = error")
	want := `{ resource.service.name="api" && resource.k8s.namespace.name="prod" && status = error }`
	if got != want {
		t.Errorf("with namespace:\n got %q\nwant %q", got, want)
	}
}

func TestTempoTraceEvents(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)

	t.Run("two error traces emit one trace_error at the earliest start", func(t *testing.T) {
		errTraces := []trace.TempoTraceSummary{
			{StartTime: base.Add(30 * time.Second), Duration: 200 * time.Millisecond, RootName: "POST /pay"},
			{StartTime: base.Add(5 * time.Second), Duration: 150 * time.Millisecond, RootName: "POST /pay"},
		}
		got := tempoTraceEvents(errTraces, nil, "checkout")
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		e := got[0]
		if e.Kind != "trace_error" {
			t.Errorf("Kind = %q, want trace_error", e.Kind)
		}
		if !e.Time.Equal(base.Add(5 * time.Second)) {
			t.Errorf("Time = %v, want earliest error trace start %v", e.Time, base.Add(5*time.Second))
		}
		if e.Target != "checkout" || e.Source != "trace" {
			t.Errorf("Target/Source = %q/%q, want checkout/trace", e.Target, e.Source)
		}
		if !strings.Contains(e.Summary, "2 error trace") {
			t.Errorf("Summary = %q, want it to report count=2", e.Summary)
		}
	})

	t.Run("slow traces emit one trace_slow event", func(t *testing.T) {
		slowTraces := []trace.TempoTraceSummary{
			{StartTime: base, Duration: 2 * time.Second, RootService: "checkout"},
		}
		got := tempoTraceEvents(nil, slowTraces, "checkout")
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		if got[0].Kind != "trace_slow" {
			t.Errorf("Kind = %q, want trace_slow", got[0].Kind)
		}
		if !strings.Contains(got[0].Summary, "checkout slow") {
			t.Errorf("Summary = %q, want root-service label fallback", got[0].Summary)
		}
	})

	t.Run("no traces emit zero events", func(t *testing.T) {
		if got := tempoTraceEvents(nil, nil, "checkout"); len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})
}

// TestTraceSource_TempoOnly drives the full Tempo path against a canned
// /api/search response, verifying a Tempo-only deployment now yields symptoms.
func TestTraceSource_TempoOnly(t *testing.T) {
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	// One error trace; the slow query returns nothing.
	errBody := fmt.Sprintf(`{"traces":[{"rootServiceName":"checkout","rootTraceName":"POST /pay","startTimeUnixNano":"%d","durationMs":420}]}`, base.UnixNano())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("q"), "status = error") {
			_, _ = w.Write([]byte(errBody))
			return
		}
		_, _ = w.Write([]byte(`{"traces":[]}`))
	}))
	defer srv.Close()

	c, err := httpapi.NewClient("tempo-1", srv.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	traces := trace.Clients{Tempo: map[string]*trace.TempoClient{"tempo-1": {Client: c}}}
	src := newTraceSource(traces)
	if src == nil {
		t.Fatal("newTraceSource returned nil for Tempo-only clients")
	}

	events, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "checkout"})
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "trace_error" {
		t.Fatalf("events = %+v, want one trace_error", events)
	}
	if !events[0].Time.Equal(base) {
		t.Errorf("Time = %v, want %v", events[0].Time, base)
	}
}
