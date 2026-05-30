package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// cloudTraceSlowThreshold mirrors correlate's traceSource: a non-error trace
// whose duration exceeds this is a "trace_slow" symptom. Kept identical (1s) so
// cloud and Jaeger trace symptoms read consistently on one timeline.
const cloudTraceSlowThreshold = time.Second

// traceSymptomSource folds AWS X-Ray error/slow traces onto the correlate
// timeline as symptoms: ChangeEvents whose Kind is "trace_error" / "trace_slow"
// and Source is "cloud_trace", keyed by service name = workload.
//
// AWS X-Ray only. Azure Application Insights is deferred — its query needs an
// explicit app id (GUID or name+resource-group) that cannot be derived from a
// workload name alone — and GCP Cloud Trace has no read-only gcloud command
// (see docs/RFC-CLOUD-OBSERVABILITY.md §10.3). So a non-AWS cloud setup yields
// no trace symptoms here (NewTraceSymptomSource returns nil).
type traceSymptomSource struct {
	aws map[string]*awsAccount
}

// NewTraceSymptomSource builds a change.ChangeSource that surfaces AWS X-Ray
// trace symptoms for correlate.workload. It returns nil — so callers can omit
// the source — when no AWS account is configured.
func NewTraceSymptomSource(c Clients) change.ChangeSource {
	if len(c.AWS) == 0 {
		return nil
	}
	return &traceSymptomSource{aws: c.AWS}
}

func (s *traceSymptomSource) Name() string { return "cloud_trace" }

// RecentChanges runs `aws xray get-trace-summaries` scoped to the workload via
// the `service("<workload>")` filter expression over [now-Since, now] (default
// 1h), then classifies the summaries into at most one trace_error and one
// trace_slow symptom. The AWS account is chosen by deterministic default
// (PickDefaultEndpoint) since q.Context carries the k8s context, not an account
// name. Per-source errors are returned for the caller to tolerate.
func (s *traceSymptomSource) RecentChanges(ctx context.Context, q change.ChangeQuery) ([]change.ChangeEvent, error) {
	if len(s.aws) == 0 {
		return nil, nil
	}
	if err := safeArg("workload", q.Workload); err != nil {
		return nil, err
	}
	_, acct, err := tools.PickDefaultEndpoint(s.aws, "correlate", "aws account")
	if err != nil {
		return nil, err
	}

	end := time.Now()
	window := q.Since
	if window <= 0 {
		window = time.Hour
	}
	start := end.Add(-window)

	cmd := append([]string{"xray", "get-trace-summaries"}, acct.baseArgs()...)
	cmd = append(cmd,
		"--start-time", strconv.FormatInt(start.Unix(), 10),
		"--end-time", strconv.FormatInt(end.Unix(), 10),
		"--filter-expression", fmt.Sprintf("service(%q)", q.Workload),
	)
	body, err := CloudExec(ctx, "aws", cmd)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		TraceSummaries []xrayTraceSummary `json:"TraceSummaries"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return cloudTraceSymptomEvents(parsed.TraceSummaries, q.Workload), nil
}

// xrayTraceSummary is the subset of an X-Ray trace summary the symptom source
// reads. The start time is not a field — it is encoded in the trace Id.
type xrayTraceSummary struct {
	ID          string  `json:"Id"`
	Duration    float64 `json:"Duration"`
	HasError    bool    `json:"HasError"`
	HasFault    bool    `json:"HasFault"`
	HasThrottle bool    `json:"HasThrottle"`
}

// cloudTraceSymptomEvents converts X-Ray trace summaries into trace symptom
// events. It is pure (no I/O) so it can be unit-tested with literal summaries.
//
// Mirroring correlate's Jaeger traceSource it emits at most ONE "trace_error"
// (earliest error/fault/throttle trace, carrying the total count) and at most
// ONE "trace_slow" (earliest healthy trace whose Duration exceeds the
// threshold). Each event's Time is decoded from the X-Ray trace Id.
func cloudTraceSymptomEvents(summaries []xrayTraceSummary, workload string) []change.ChangeEvent {
	var (
		errCount        int
		earliestErr     xrayTraceSummary
		earliestErrTime time.Time
		haveErr         bool

		earliestSlow     xrayTraceSummary
		earliestSlowTime time.Time
		haveSlow         bool
	)

	for _, t := range summaries {
		ts := xrayTraceStartTime(t.ID)
		// A trace we cannot time-locate (malformed Id) must not anchor the
		// timeline: a zero time sorts before every real time and would hijack
		// the correlate cause engine's "earliest symptom" pick. X-Ray Ids are
		// well-formed in practice, so skipping these is safe and keeps the
		// symptom placeable.
		if ts.IsZero() {
			continue
		}
		if t.HasError || t.HasFault || t.HasThrottle {
			errCount++
			if !haveErr || ts.Before(earliestErrTime) {
				earliestErr, earliestErrTime, haveErr = t, ts, true
			}
			continue
		}
		if t.Duration > cloudTraceSlowThreshold.Seconds() {
			if !haveSlow || ts.Before(earliestSlowTime) {
				earliestSlow, earliestSlowTime, haveSlow = t, ts, true
			}
		}
	}

	var out []change.ChangeEvent
	if haveErr {
		out = append(out, change.ChangeEvent{
			Time:    earliestErrTime,
			Kind:    "trace_error",
			Target:  workload,
			Summary: fmt.Sprintf("X-Ray trace %s %s (%dms); %d failing trace(s) in window", earliestErr.ID, xrayFlags(earliestErr.HasError, earliestErr.HasFault, earliestErr.HasThrottle), int64(earliestErr.Duration*1000), errCount),
			Source:  "cloud_trace",
		})
	}
	if haveSlow {
		out = append(out, change.ChangeEvent{
			Time:    earliestSlowTime,
			Kind:    "trace_slow",
			Target:  workload,
			Summary: fmt.Sprintf("X-Ray trace %s slow (%dms > %dms threshold)", earliestSlow.ID, int64(earliestSlow.Duration*1000), cloudTraceSlowThreshold.Milliseconds()),
			Source:  "cloud_trace",
		})
	}
	return out
}

// xrayTraceStartTime decodes the request start time from an X-Ray trace Id. The
// Id format is "1-{8 hex epoch seconds}-{24 hex unique}", e.g.
// "1-581cf771-a006649127e371903a2de979". A malformed Id yields the zero time
// (it then sorts oldest), keeping the function total.
func xrayTraceStartTime(id string) time.Time {
	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(parts[1], 16, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
