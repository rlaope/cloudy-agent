package correlate

import (
	"context"
	"fmt"
	"strings"
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
// Source is "trace". It draws from both Jaeger and Tempo backends.
//
// Jaeger symptoms come from /api/traces error-span scans; Tempo symptoms (v3)
// come from TraceQL /api/search summaries (status=error and duration>threshold)
// — the search summary already carries trace start + duration, so no full-trace
// OTLP fetch is needed. A deployment usually wires only one trace backend; when
// both are wired the source emits from each.
type traceSource struct {
	traces trace.Clients
}

// newTraceSource builds a traceSource over the configured tracing backends. It
// returns nil — so callers can omit the source — only when neither a Jaeger nor
// a Tempo client is wired.
func newTraceSource(traces trace.Clients) change.ChangeSource {
	if len(traces.Jaeger) == 0 && len(traces.Tempo) == 0 {
		return nil
	}
	return &traceSource{traces: traces}
}

func (s *traceSource) Name() string { return "trace" }

// RecentChanges searches recent traces for q.Workload (used as the service
// name) over the window [now-Since, now] (default 1h) and emits trace symptom
// events from whichever trace backends are wired. The backend client is chosen
// by deterministic default (PickDefaultEndpoint) rather than from q.Context,
// which carries the k8s context, not an endpoint name. When q.Namespace is set
// the Tempo TraceQL is scoped by resource.k8s.namespace.name; Jaeger has no
// standard namespace tag and stays namespace-agnostic. Per-source errors are
// returned for the caller to tolerate.
func (s *traceSource) RecentChanges(ctx context.Context, q change.ChangeQuery) ([]change.ChangeEvent, error) {
	end := time.Now()
	window := q.Since
	if window <= 0 {
		window = time.Hour
	}
	start := end.Add(-window)

	var out []change.ChangeEvent

	if len(s.traces.Jaeger) > 0 {
		_, client, err := tools.PickDefaultEndpoint(s.traces.Jaeger, "correlate", "jaeger endpoint")
		if err != nil {
			return nil, err
		}
		spans, err := client.SearchErrorSpans(ctx, q.Workload, `{"error":"true"}`, start, end, 100)
		if err != nil {
			return nil, err
		}
		out = append(out, traceSymptomEvents(spans, q.Workload)...)
	}

	if len(s.traces.Tempo) > 0 {
		events, err := tempoSymptomEvents(ctx, s.traces.Tempo, q, start, end)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}

	return out, nil
}

// tempoSymptomEvents queries Tempo for error and slow traces of q.Workload and
// folds them onto the timeline. It runs two TraceQL searches — one for failed
// traces (status=error) and one for slow non-error traces (duration over the
// threshold) — mirroring the Jaeger source's error/slow split.
func tempoSymptomEvents(ctx context.Context, clients map[string]*trace.TempoClient, q change.ChangeQuery, start, end time.Time) ([]change.ChangeEvent, error) {
	_, client, err := tools.PickDefaultEndpoint(clients, "correlate", "tempo endpoint")
	if err != nil {
		return nil, err
	}

	errTraces, err := client.SearchTraces(ctx, tempoTraceQL(q.Workload, q.Namespace, "status = error"), start, end, 100)
	if err != nil {
		return nil, err
	}
	slowExtra := fmt.Sprintf("duration > %dms && status != error", traceSlowThreshold.Milliseconds())
	slowTraces, err := client.SearchTraces(ctx, tempoTraceQL(q.Workload, q.Namespace, slowExtra), start, end, 100)
	if err != nil {
		return nil, err
	}
	return tempoTraceEvents(errTraces, slowTraces, q.Workload), nil
}

// tempoTraceQL composes a single-spanset TraceQL filter scoped to a service,
// optionally a k8s namespace, plus an extra condition (status / duration).
// Values are rendered with %q so a quote/newline cannot break out of the
// string literal.
func tempoTraceQL(workload, namespace, extra string) string {
	conds := []string{fmt.Sprintf("resource.service.name=%q", workload)}
	if namespace != "" {
		conds = append(conds, fmt.Sprintf("resource.k8s.namespace.name=%q", namespace))
	}
	if extra != "" {
		conds = append(conds, extra)
	}
	return "{ " + strings.Join(conds, " && ") + " }"
}

// tempoTraceEvents converts Tempo error/slow trace summaries into symptom
// events. It is pure (no I/O) so it can be unit-tested with literal summaries.
// Like the Jaeger path it emits at most ONE "trace_error" event (at the
// earliest failed trace's start, count in Summary) and at most ONE "trace_slow"
// event (earliest slow trace). All events use Source "trace" and Target
// workload.
func tempoTraceEvents(errTraces, slowTraces []trace.TempoTraceSummary, workload string) []change.ChangeEvent {
	var out []change.ChangeEvent
	if earliest, ok := earliestTrace(errTraces); ok {
		out = append(out, change.ChangeEvent{
			Time:    earliest.StartTime,
			Kind:    "trace_error",
			Target:  workload,
			Summary: fmt.Sprintf("%s failed (%dms); %d error trace(s) in window", tempoTraceLabel(earliest), earliest.Duration.Milliseconds(), len(errTraces)),
			Source:  "trace",
		})
	}
	if earliest, ok := earliestTrace(slowTraces); ok {
		out = append(out, change.ChangeEvent{
			Time:    earliest.StartTime,
			Kind:    "trace_slow",
			Target:  workload,
			Summary: fmt.Sprintf("%s slow (%dms > %dms threshold)", tempoTraceLabel(earliest), earliest.Duration.Milliseconds(), traceSlowThreshold.Milliseconds()),
			Source:  "trace",
		})
	}
	return out
}

// earliestTrace returns the trace with the earliest StartTime, and false when
// the slice is empty.
func earliestTrace(traces []trace.TempoTraceSummary) (trace.TempoTraceSummary, bool) {
	if len(traces) == 0 {
		return trace.TempoTraceSummary{}, false
	}
	earliest := traces[0]
	for _, t := range traces[1:] {
		if t.StartTime.Before(earliest.StartTime) {
			earliest = t
		}
	}
	return earliest, true
}

// tempoTraceLabel names a trace for its symptom Summary, preferring the root
// operation, then the root service, then a generic fallback.
func tempoTraceLabel(t trace.TempoTraceSummary) string {
	if t.RootName != "" {
		return t.RootName
	}
	if t.RootService != "" {
		return t.RootService
	}
	return "trace"
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
