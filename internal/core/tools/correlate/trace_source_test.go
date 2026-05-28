package correlate

import (
	"strings"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools/trace"
)

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
	if newTraceSource(trace.Clients{Tempo: map[string]*trace.TempoClient{"t": {}}}) != nil {
		t.Error("newTraceSource with only Tempo should be nil (v3 deferred)")
	}
	if newTraceSource(trace.Clients{Jaeger: map[string]*trace.JaegerClient{"j": {}}}) == nil {
		t.Error("newTraceSource with a Jaeger client should be non-nil")
	}
}
