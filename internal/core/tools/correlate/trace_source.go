package correlate

import (
	"context"
	"fmt"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
	"github.com/rlaope/cloudy/internal/core/tools/trace"
)

// traceSlowThreshold is the hardcoded latency above which a non-error span is
// treated as a "trace_slow" symptom. 1s is a sane default for a request span;
// it is intentionally not configurable in v2.
const traceSlowThreshold = time.Second

// traceSource folds trace errors and latency outliers onto the change timeline
// as symptoms: ChangeEvents whose Kind is "trace_error" / "trace_slow" and
// Source is "trace". It draws from the Jaeger backends.
//
// v2 covers Jaeger only. Tempo is deferred to v3: its search response is opaque
// (raw, schema-light) and would need its own span extraction, so a Tempo-only
// deployment yields no trace symptoms here (newTraceSource returns nil).
type traceSource struct {
	traces trace.Clients
}

// newTraceSource builds a traceSource over the configured tracing backends. It
// returns nil — so callers can omit the source — when no Jaeger client is wired
// (Tempo-only deployments are deferred to v3; see the type doc).
func newTraceSource(traces trace.Clients) change.ChangeSource {
	if len(traces.Jaeger) == 0 {
		return nil
	}
	return &traceSource{traces: traces}
}

func (s *traceSource) Name() string { return "trace" }

// RecentChanges searches recent Jaeger traces for q.Workload (used as the
// service name) over the window [now-Since, now] (default 1h), filtered to
// error spans via the `error=true` tag, and emits trace symptom events. The
// Jaeger backend is chosen by deterministic default (PickDefaultEndpoint)
// rather than from q.Context, which carries the k8s context, not an endpoint
// name. Per-source errors are returned for the caller to tolerate.
func (s *traceSource) RecentChanges(ctx context.Context, q change.ChangeQuery) ([]change.ChangeEvent, error) {
	if len(s.traces.Jaeger) == 0 {
		return nil, nil
	}
	_, client, err := tools.PickDefaultEndpoint(s.traces.Jaeger, "correlate", "jaeger endpoint")
	if err != nil {
		return nil, err
	}

	end := time.Now()
	window := q.Since
	if window <= 0 {
		window = time.Hour
	}
	start := end.Add(-window)

	spans, err := client.SearchErrorSpans(ctx, q.Workload, `{"error":"true"}`, start, end, 100)
	if err != nil {
		return nil, err
	}
	return traceSymptomEvents(spans, q.Workload), nil
}

// traceSymptomEvents converts Jaeger spans into trace symptom events. It is
// pure (no I/O) so it can be unit-tested with literal span slices.
//
// To avoid flooding the timeline it mirrors the metric/log "earliest onset"
// approach: it emits at most ONE "trace_error" event, at the earliest error
// span's start, carrying the total error-span count in its Summary. It also
// emits at most ONE "trace_slow" event for the earliest non-error span whose
// Duration exceeds traceSlowThreshold. All events use Source "trace" and Target
// workload.
func traceSymptomEvents(spans []trace.JaegerSpan, workload string) []change.ChangeEvent {
	var (
		errCount     int
		earliestErr  trace.JaegerSpan
		haveErr      bool
		earliestSlow trace.JaegerSpan
		haveSlow     bool
	)

	for _, sp := range spans {
		if sp.Error {
			errCount++
			if !haveErr || sp.StartTime.Before(earliestErr.StartTime) {
				earliestErr = sp
				haveErr = true
			}
			continue
		}
		if sp.Duration > traceSlowThreshold {
			if !haveSlow || sp.StartTime.Before(earliestSlow.StartTime) {
				earliestSlow = sp
				haveSlow = true
			}
		}
	}

	var out []change.ChangeEvent
	if haveErr {
		out = append(out, change.ChangeEvent{
			Time:    earliestErr.StartTime,
			Kind:    "trace_error",
			Target:  workload,
			Summary: fmt.Sprintf("%s failed (%dms); %d error span(s) in window", earliestErr.Operation, earliestErr.Duration.Milliseconds(), errCount),
			Source:  "trace",
		})
	}
	if haveSlow {
		out = append(out, change.ChangeEvent{
			Time:    earliestSlow.StartTime,
			Kind:    "trace_slow",
			Target:  workload,
			Summary: fmt.Sprintf("%s slow (%dms > %dms threshold)", earliestSlow.Operation, earliestSlow.Duration.Milliseconds(), traceSlowThreshold.Milliseconds()),
			Source:  "trace",
		})
	}
	return out
}
